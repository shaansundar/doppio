package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	workerv1 "github.com/shaansundar/doppio/gen/go/doppio/worker/v1"
	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/jobs"
	"github.com/shaansundar/doppio/internal/models"
	"github.com/shaansundar/doppio/internal/pipeline"
	"github.com/shaansundar/doppio/internal/scp"
)

const (
	testToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testHost  = "127.0.0.1:7341"
	packID    = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"
)

var jpegHeader = []byte{0xFF, 0xD8, 0xFF, 0xE0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1}

type fakePacks struct{ dir string }

func (p fakePacks) OpenPack(id string) (string, scp.Manifest, error) {
	if id != packID {
		return "", scp.Manifest{}, pipeline.ErrPackNotFound
	}
	m, err := scp.ReadManifest(p.dir)
	return p.dir, m, err
}

type fakeRunner struct {
	delay time.Duration
	got   chan pipeline.Options
}

func (f *fakeRunner) Run(ctx context.Context, _ pipeline.Input, opts pipeline.Options, _ func(pipeline.Progress)) (pipeline.Result, error) {
	if f.got != nil {
		f.got <- opts
	}
	select {
	case <-time.After(f.delay):
		return pipeline.Result{PackID: packID}, nil
	case <-ctx.Done():
		return pipeline.Result{}, ctx.Err()
	}
}

type fakeWorker struct{ err error }

func (f fakeWorker) Health(context.Context) (*workerv1.HealthResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &workerv1.HealthResponse{Device: "mps", WorkerVersion: "0.1.0", Stages: []string{"ingest"}}, nil
}

type env struct {
	srv     *httptest.Server
	handler http.Handler
	runner  *fakeRunner
	manager *jobs.Manager
}

func newEnv(t *testing.T, mutate ...func(*Config)) *env {
	t.Helper()
	root := t.TempDir()
	store, err := cache.Open(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := models.Parse([]byte(`version: 1
models:
  - {id: free, task: geometry, family: moge2, status: available, hf_repo: r/a, revision: a, memory_gb: 1, license: {id: MIT, commercial: true}}
  - {id: nc, task: geometry, family: moge2, status: available, hf_repo: r/b, revision: b, memory_gb: 1, license: {id: CC-BY-NC-4.0, commercial: false}}
  - {id: later, task: geometry, family: x, status: planned, hf_repo: r/c, revision: c, memory_gb: 1, license: {id: MIT, commercial: true}}
`))
	if err != nil {
		t.Fatal(err)
	}
	packDir := filepath.Join(root, "pack")
	_ = os.MkdirAll(filepath.Join(packDir, "maps"), 0o700)
	_ = os.WriteFile(filepath.Join(packDir, "maps", "depth_inv8.png"), []byte("PNGDATA"), 0o600)
	_ = os.WriteFile(filepath.Join(packDir, "secret.txt"), []byte("do not serve"), 0o600)
	_ = scp.WriteJSON(packDir, scp.ManifestFile, scp.Manifest{
		Schema: scp.SchemaManifest, PackID: packID,
		Artifacts: []scp.Artifact{{Name: "maps.depth_inv8", Path: "maps/depth_inv8.png", MediaType: "image/png", SHA256: packID, Bytes: 7}},
	})

	runner := &fakeRunner{delay: 10 * time.Millisecond}
	manager := jobs.NewManager(runner)
	t.Cleanup(func() { _ = manager.Shutdown(context.Background()) })
	cfg := Config{
		Queue: manager, Status: manager, Packs: fakePacks{packDir}, Blobs: store, Worker: fakeWorker{},
		Registry: reg, Token: testToken, AllowedHosts: map[string]bool{testHost: true},
		CORSOrigins: map[string]bool{"uxp://doppio": true}, MaxUploadBytes: 1024, Version: "test",
	}
	for _, m := range mutate {
		m(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return &env{handler: s.Handler(), runner: runner, manager: manager}
}

func (e *env) do(t *testing.T, method, path string, body io.Reader, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, "http://"+testHost+path, body)
	req.Host = testHost
	req.Header.Set("Authorization", "Bearer "+testToken)
	for k, v := range headers {
		if v == "" {
			req.Header.Del(k)
		} else {
			req.Header.Set(k, v)
		}
	}
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	return rec
}

type part struct {
	name, filename string
	data           []byte
}

func multipartBody(t *testing.T, parts ...part) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, p := range parts {
		var w io.Writer
		var err error
		if p.filename != "" {
			w, err = mw.CreateFormFile(p.name, p.filename)
		} else {
			w, err = mw.CreateFormField(p.name)
		}
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write(p.data)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return v
}

func TestHealthzIsPublicButHostGuarded(t *testing.T) {
	e := newEnv(t)
	if rec := e.do(t, "GET", "/healthz", nil, map[string]string{"Authorization": ""}); rec.Code != 200 {
		t.Fatalf("healthz = %d", rec.Code)
	}
	req := httptest.NewRequest("GET", "http://evil.example/healthz", nil)
	req.Host = "evil.example:7341"
	rec := httptest.NewRecorder()
	e.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusMisdirectedRequest {
		t.Fatalf("rebinding host = %d, want 421", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("content-type = %s", ct)
	}
}

func TestAuthRequired(t *testing.T) {
	e := newEnv(t)
	for _, h := range []string{"", "Bearer wrong", "Basic " + testToken} {
		rec := e.do(t, "GET", "/v1/info", nil, map[string]string{"Authorization": h})
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("auth %q = %d, want 401", h, rec.Code)
		}
	}
	rec := e.do(t, "GET", "/v1/info", nil, nil)
	if rec.Code != 200 {
		t.Fatalf("info = %d %s", rec.Code, rec.Body)
	}
	info := decode[map[string]any](t, rec)
	if info["worker"].(map[string]any)["device"] != "mps" {
		t.Errorf("info = %v", info)
	}
}

func TestInfoReportsWorkerFailure(t *testing.T) {
	e := newEnv(t, func(c *Config) { c.Worker = fakeWorker{err: errors.New("no venv")} })
	info := decode[map[string]any](t, e.do(t, "GET", "/v1/info", nil, nil))
	if w := info["worker"].(map[string]any); w["ok"] != false || !strings.Contains(w["error"].(string), "no venv") {
		t.Errorf("worker = %v", w)
	}
}

func TestModelsListing(t *testing.T) {
	e := newEnv(t)
	type resp struct {
		Models []struct {
			ID     string `json:"id"`
			Usable bool   `json:"usable"`
		} `json:"models"`
	}
	usable := func(policy string) map[string]bool {
		r := decode[resp](t, e.do(t, "GET", "/v1/models?policy="+policy, nil, nil))
		out := map[string]bool{}
		for _, m := range r.Models {
			out[m.ID] = m.Usable
		}
		return out
	}
	if got := usable(""); !got["free"] || got["nc"] || got["later"] {
		t.Errorf("commercial policy = %v", got)
	}
	if got := usable("any"); !got["nc"] || got["later"] {
		t.Errorf("any policy = %v", got)
	}
	if rec := e.do(t, "GET", "/v1/models?policy=bogus", nil, nil); rec.Code != 400 {
		t.Errorf("bad policy = %d", rec.Code)
	}
}

func TestAnalyzeWaitReturnsFinishedJob(t *testing.T) {
	e := newEnv(t)
	body, ct := multipartBody(t, part{"image", "C:\\photos\\room.jpg", jpegHeader}, part{"preset", "", []byte("fast")})
	rec := e.do(t, "POST", "/v1/analyze?wait=5s", body, map[string]string{"Content-Type": ct})
	if rec.Code != 200 {
		t.Fatalf("analyze = %d %s", rec.Code, rec.Body)
	}
	job := decode[jobView](t, rec)
	if job.Status != jobs.StatusSucceeded || job.PackID != packID || job.Links["pack"] != "/v1/packs/"+packID {
		t.Fatalf("job = %+v", job)
	}
	if job.Filename != "room.jpg" || job.Preset != "fast" {
		t.Errorf("filename/preset = %q %q", job.Filename, job.Preset)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/jobs/"+job.ID {
		t.Errorf("Location = %s", loc)
	}
	got := decode[jobView](t, e.do(t, "GET", "/v1/jobs/"+job.ID, nil, nil))
	if got.ID != job.ID {
		t.Errorf("GET job = %+v", got)
	}
}

func TestAnalyzeAsyncAndCancel(t *testing.T) {
	e := newEnv(t)
	e.runner.delay = time.Minute
	body, ct := multipartBody(t, part{"image", "a.png", []byte("\x89PNG\r\n\x1a\nrest")})
	rec := e.do(t, "POST", "/v1/analyze", body, map[string]string{"Content-Type": ct})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("analyze = %d %s", rec.Code, rec.Body)
	}
	job := decode[jobView](t, rec)
	if rec := e.do(t, "DELETE", "/v1/jobs/"+job.ID, nil, nil); rec.Code != http.StatusAccepted {
		t.Fatalf("cancel = %d", rec.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if j, _ := e.manager.Wait(ctx, job.ID); j.Status != jobs.StatusCanceled {
		t.Fatalf("job after cancel = %+v", j)
	}
	for _, method := range []string{"GET", "DELETE"} {
		if rec := e.do(t, method, "/v1/jobs/job_nope", nil, nil); rec.Code != 404 {
			t.Errorf("%s unknown job = %d", method, rec.Code)
		}
	}
}

func TestAnalyzeOptionsForwarded(t *testing.T) {
	e := newEnv(t)
	e.runner.got = make(chan pipeline.Options, 1)
	body, ct := multipartBody(t, part{"image", "a.jpg", jpegHeader},
		part{"allow_noncommercial", "", []byte("true")}, part{"geometry_model", "", []byte("nc")},
		part{"disable_exif_prior", "", []byte("1")})
	rec := e.do(t, "POST", "/v1/analyze", body, map[string]string{"Content-Type": ct})
	if rec.Code != http.StatusAccepted {
		t.Fatalf("analyze = %d %s", rec.Code, rec.Body)
	}
	opts := <-e.runner.got
	if opts.Policy != models.PolicyAny || opts.ModelOverrides["geometry"] != "nc" || !opts.DisableEXIFPrior || opts.Preset != pipeline.PresetStandard {
		t.Errorf("opts = %+v", opts)
	}
}

func TestAnalyzeRejections(t *testing.T) {
	big := append(append([]byte{}, jpegHeader...), bytes.Repeat([]byte{0}, 2048)...)
	tests := []struct {
		name   string
		parts  []part
		ct     string
		query  string
		status int
		slug   string
	}{
		{"not multipart", nil, "application/json", "", 415, "not-multipart"},
		{"missing image", []part{{"preset", "", []byte("fast")}}, "", "", 400, "missing-image"},
		{"unknown field", []part{{"colour", "", []byte("red")}}, "", "", 400, "unknown-field"},
		{"not an image", []part{{"image", "a.jpg", []byte("hello world, not an image")}}, "", "", 415, "unsupported-image"},
		{"too large", []part{{"image", "a.jpg", big}}, "", "", 413, "too-large"},
		{"two images", []part{{"image", "a.jpg", jpegHeader}, {"image", "b.jpg", jpegHeader}}, "", "", 400, "multiple-images"},
		{"bad preset", []part{{"image", "a.jpg", jpegHeader}, {"preset", "", []byte("turbo")}}, "", "", 400, "bad-preset"},
		{"bad policy", []part{{"image", "a.jpg", jpegHeader}, {"license_policy", "", []byte("free")}}, "", "", 400, "bad-policy"},
		{"nc model blocked", []part{{"image", "a.jpg", jpegHeader}, {"geometry_model", "", []byte("nc")}}, "", "", 403, "license-not-allowed"},
		{"planned model", []part{{"image", "a.jpg", jpegHeader}, {"geometry_model", "", []byte("later")}}, "", "", 422, "model-unavailable"},
		{"long field", []part{{"image", "a.jpg", jpegHeader}, {"preset", "", bytes.Repeat([]byte("x"), 300)}}, "", "", 400, "field-too-long"},
		{"bad wait", []part{{"image", "a.jpg", jpegHeader}}, "", "?wait=forever", 400, "bad-wait"},
		{"wait too long", []part{{"image", "a.jpg", jpegHeader}}, "", "?wait=1h", 400, "bad-wait"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEnv(t)
			body, ct := multipartBody(t, tt.parts...)
			if tt.ct != "" {
				ct = tt.ct
			}
			rec := e.do(t, "POST", "/v1/analyze"+tt.query, body, map[string]string{"Content-Type": ct})
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.status, rec.Body)
			}
			p := decode[Problem](t, rec)
			if p.Type != problemBase+tt.slug {
				t.Errorf("problem type = %s, want %s", p.Type, tt.slug)
			}
		})
	}
}

func TestPackAndArtifacts(t *testing.T) {
	e := newEnv(t)
	rec := e.do(t, "GET", "/v1/packs/"+packID, nil, nil)
	if rec.Code != 200 || decode[scp.Manifest](t, rec).PackID != packID {
		t.Fatalf("pack = %d %s", rec.Code, rec.Body)
	}
	rec = e.do(t, "GET", "/v1/packs/"+packID+"/artifacts/maps/depth_inv8.png", nil, nil)
	if rec.Code != 200 || rec.Body.String() != "PNGDATA" || rec.Header().Get("Content-Type") != "image/png" {
		t.Fatalf("artifact = %d %q %v", rec.Code, rec.Body, rec.Header())
	}
	if rec.Header().Get("ETag") != `"`+packID+`"` {
		t.Errorf("etag = %s", rec.Header().Get("ETag"))
	}
	rec = e.do(t, "GET", "/v1/packs/"+packID+"/artifacts/manifest.json", nil, nil)
	if rec.Code != 200 {
		t.Errorf("manifest artifact = %d", rec.Code)
	}
	cases := map[string]int{
		"/v1/packs/" + packID + "/artifacts/secret.txt":         404, // on disk but not listed
		"/v1/packs/" + packID + "/artifacts/maps/../secret.txt": 301, // mux cleans and redirects
		"/v1/packs/deadbeef": 404,
		"/v1/packs/deadbeef/artifacts/maps/depth_inv8.png": 404,
		"/v2/nothing": 404,
	}
	for path, want := range cases {
		if rec := e.do(t, "GET", path, nil, nil); rec.Code != want {
			t.Errorf("GET %s = %d, want %d", path, rec.Code, want)
		}
	}
}

func TestCORS(t *testing.T) {
	e := newEnv(t)
	pre := map[string]string{"Origin": "uxp://doppio", "Access-Control-Request-Method": "POST", "Authorization": ""}
	rec := e.do(t, "OPTIONS", "/v1/analyze", nil, pre)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != "uxp://doppio" {
		t.Fatalf("allowed preflight = %d %v", rec.Code, rec.Header())
	}
	pre["Origin"] = "https://evil.example"
	rec = e.do(t, "OPTIONS", "/v1/analyze", nil, pre)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("foreign preflight = %d %v", rec.Code, rec.Header())
	}
	rec = e.do(t, "GET", "/v1/info", nil, map[string]string{"Origin": "https://evil.example"})
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("CORS header leaked to foreign origin")
	}
}

func TestNewValidatesConfig(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Error("empty config accepted")
	}
	m := jobs.NewManager(&fakeRunner{})
	defer m.Shutdown(context.Background())
	reg, _ := models.Default()
	store, _ := cache.Open(t.TempDir())
	base := Config{Queue: m, Status: m, Packs: fakePacks{}, Blobs: store, Registry: reg, AllowedHosts: map[string]bool{testHost: true}}
	base.Token = "short"
	if _, err := New(base); err == nil {
		t.Error("short token accepted")
	}
	base.Token, base.AllowedHosts = testToken, nil
	if _, err := New(base); err == nil {
		t.Error("missing hosts accepted")
	}
}

func TestSniffImage(t *testing.T) {
	ftyp := func(brand string) []byte {
		return append([]byte{0, 0, 0, 24, 'f', 't', 'y', 'p'}, []byte(brand+"\x00\x00\x00\x00")...)
	}
	tests := []struct {
		head []byte
		ext  string
	}{
		{jpegHeader, ".jpg"},
		{[]byte("\x89PNG\r\n\x1a\n...."), ".png"},
		{[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ".webp"},
		{[]byte("II*\x00...."), ".tiff"},
		{[]byte("MM\x00*...."), ".tiff"},
		{ftyp("heic"), ".heic"},
		{ftyp("mif1"), ".heic"},
		{ftyp("isom"), ""},
		{[]byte("GIF89a"), ""},
		{nil, ""},
	}
	for _, tt := range tests {
		ext, err := sniffImage(tt.head)
		if ext != tt.ext || (tt.ext == "") != (err != nil) {
			t.Errorf("sniffImage(%q) = %q, %v; want %q", tt.head, ext, err, tt.ext)
		}
	}
}

func TestSanitizeFilenameAndParseWait(t *testing.T) {
	names := map[string]string{
		"room.jpg":               "room.jpg",
		"C:\\Users\\x\\a.jpg":    "a.jpg",
		"../../etc/passwd":       "passwd",
		"bad\x00\nname.png":      "badname.png",
		"":                       "",
		strings.Repeat("a", 300): strings.Repeat("a", 255),
	}
	for in, want := range names {
		if got := sanitizeFilename(in); got != want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", in, got, want)
		}
	}
	waits := map[string]time.Duration{"": 0, "30s": 30 * time.Second, "45": 45 * time.Second, "2m": 2 * time.Minute}
	for in, want := range waits {
		if got, err := parseWait(in); err != nil || got != want {
			t.Errorf("parseWait(%q) = %v, %v", in, got, err)
		}
	}
	for _, bad := range []string{"-1s", "11m", "soon"} {
		if _, err := parseWait(bad); err == nil {
			t.Errorf("parseWait(%q) accepted", bad)
		}
	}
}

func TestShutdownEndpoint(t *testing.T) {
	called := make(chan struct{})
	e := newEnv(t, func(c *Config) { c.Shutdown = func() { close(called) } })
	if rec := e.do(t, "POST", "/v1/admin/shutdown", nil, map[string]string{"Authorization": ""}); rec.Code != 401 {
		t.Fatalf("unauthenticated shutdown = %d", rec.Code)
	}
	if rec := e.do(t, "POST", "/v1/admin/shutdown", nil, nil); rec.Code != 202 {
		t.Fatalf("shutdown = %d", rec.Code)
	}
	select {
	case <-called:
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown callback not invoked")
	}
	plain := newEnv(t)
	if rec := plain.do(t, "POST", "/v1/admin/shutdown", nil, nil); rec.Code != 404 {
		t.Fatalf("shutdown without callback = %d, want 404", rec.Code)
	}
}
