package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaansundar/doppio/internal/api"
	"github.com/shaansundar/doppio/internal/jobs"
	"github.com/shaansundar/doppio/internal/scp"
)

const token = "tok"

func digest(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func newTestServer(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" && r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(401)
			return
		}
		h(w, r)
	}))
	t.Cleanup(srv.Close)
	return New(strings.TrimPrefix(srv.URL, "http://"), token)
}

func TestAnalyzeStreamsMultipart(t *testing.T) {
	img := filepath.Join(t.TempDir(), "room.jpg")
	_ = os.WriteFile(img, []byte("JPEGBYTES"), 0o600)
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/analyze" || r.URL.Query().Get("wait") != "30s" {
			t.Errorf("path = %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		f, hdr, err := r.FormFile("image")
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(f)
		if string(data) != "JPEGBYTES" || hdr.Filename != "room.jpg" {
			t.Errorf("image = %q %q", data, hdr.Filename)
		}
		if r.FormValue("preset") != "fast" || r.FormValue("allow_noncommercial") != "true" ||
			r.FormValue("geometry_model") != "m" || r.FormValue("disable_exif_prior") != "true" {
			t.Errorf("form = %v", r.MultipartForm.Value)
		}
		w.WriteHeader(202)
		_ = json.NewEncoder(w).Encode(Job{Job: jobs.Job{ID: "job_1", Status: jobs.StatusQueued}})
	})
	job, err := c.Analyze(context.Background(), img, AnalyzeRequest{
		Preset: "fast", AllowNonCommercial: true, GeometryModel: "m", DisableEXIFPrior: true, Wait: 30 * time.Second,
	})
	if err != nil || job.ID != "job_1" {
		t.Fatalf("Analyze = %+v, %v", job, err)
	}
	if _, err := c.Analyze(context.Background(), "/no/such/file.jpg", AnalyzeRequest{}); err == nil {
		t.Error("missing file accepted")
	}
}

func TestAPIErrorCarriesProblem(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(403)
		_ = json.NewEncoder(w).Encode(api.Problem{Status: 403, Detail: "license not allowed"})
	})
	_, err := c.Job(context.Background(), "job_x")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != 403 || !strings.Contains(err.Error(), "license not allowed") {
		t.Fatalf("err = %v", err)
	}
}

func TestWaitJobPollsUntilTerminal(t *testing.T) {
	var calls atomic.Int32
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		status := jobs.StatusRunning
		if calls.Add(1) >= 3 {
			status = jobs.StatusSucceeded
		}
		_ = json.NewEncoder(w).Encode(Job{Job: jobs.Job{ID: "job_1", Status: status}})
	})
	var updates int
	job, err := c.WaitJob(context.Background(), "job_1", time.Millisecond, func(Job) { updates++ })
	if err != nil || job.Status != jobs.StatusSucceeded || updates != 3 {
		t.Fatalf("WaitJob = %+v, %v, updates=%d", job, err, updates)
	}
}

func TestDownloadPackVerifiesDigests(t *testing.T) {
	content := map[string][]byte{"scene.json": []byte(`{"a":1}`), "maps/depth_inv8.png": []byte("PNG")}
	tamper := false
	manifest := scp.Manifest{Schema: scp.SchemaManifest, PackID: "p"}
	for path, data := range content {
		manifest.Artifacts = append(manifest.Artifacts, scp.Artifact{Path: path, SHA256: digest(data)})
	}
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/packs/p":
			_ = json.NewEncoder(w).Encode(manifest)
		case strings.HasPrefix(r.URL.Path, "/v1/packs/p/artifacts/"):
			rel := strings.TrimPrefix(r.URL.Path, "/v1/packs/p/artifacts/")
			data := content[rel]
			if tamper {
				data = []byte("evil")
			}
			_, _ = w.Write(data)
		default:
			w.WriteHeader(404)
		}
	})
	out := filepath.Join(t.TempDir(), "room.doppio")
	m, err := c.DownloadPack(context.Background(), "p", out)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Artifacts) != 2 {
		t.Fatalf("manifest = %+v", m)
	}
	for path, data := range content {
		got, err := os.ReadFile(filepath.Join(out, filepath.FromSlash(path)))
		if err != nil || string(got) != string(data) {
			t.Errorf("%s = %q, %v", path, got, err)
		}
	}
	if _, err := os.Stat(filepath.Join(out, scp.ManifestFile)); err != nil {
		t.Error("manifest.json not written")
	}

	tamper = true
	if _, err := c.DownloadPack(context.Background(), "p", t.TempDir()); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("tampered download err = %v", err)
	}

	tamper = false
	manifest.Artifacts = []scp.Artifact{{Path: "../escape.txt", SHA256: digest(nil)}}
	if _, err := c.DownloadPack(context.Background(), "p", t.TempDir()); err == nil || !strings.Contains(err.Error(), "unsafe") {
		t.Fatalf("traversal err = %v", err)
	}
}

func TestHealthAndInfo(t *testing.T) {
	c := newTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"version": "x"})
	})
	if err := c.Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	info, err := c.Info(context.Background())
	if err != nil || info["version"] != "x" {
		t.Fatalf("Info = %v, %v", info, err)
	}
	if err := c.CancelJob(context.Background(), "job_1"); err != nil {
		t.Fatal(err)
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	dead := New("127.0.0.1:1", token)
	if err := dead.Health(context.Background()); err == nil {
		t.Fatal("unreachable daemon reported healthy")
	}
}
