package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/models"
	"github.com/shaansundar/doppio/internal/scp"
)

const testRegistry = `version: 1
models:
  - {id: moge2-vits-normal, task: geometry, family: moge2, status: available, hf_repo: r/s, revision: aaa, memory_gb: 0.5, license: {id: MIT, commercial: true}}
  - {id: moge2-vitl-normal, task: geometry, family: moge2, status: available, hf_repo: r/l, revision: bbb, memory_gb: 2, license: {id: MIT, commercial: true}}
  - {id: nc-geometry, task: geometry, family: moge2, status: available, hf_repo: r/nc, revision: ccc, memory_gb: 1, license: {id: CC-BY-NC-4.0, commercial: false}}
  - {id: seg, task: segmentation, family: sam2, status: available, hf_repo: r/seg, revision: ddd, memory_gb: 1, license: {id: Apache-2.0, commercial: true}}
`

// fakeRunner mimics the worker: writes files and returns metadata.
type fakeRunner struct {
	mu       sync.Mutex
	calls    map[string]int
	params   map[string]map[string]any
	exifFOV  bool
	delay    time.Duration
	failOn   string
	badPath  bool
	noNormal bool
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{calls: map[string]int{}, params: map[string]map[string]any{}, exifFOV: true}
}

func (f *fakeRunner) count(stage string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[stage]
}

func (f *fakeRunner) RunStage(ctx context.Context, req StageRequest, progress func(float64, string)) (StageResult, error) {
	f.mu.Lock()
	f.calls[req.Stage]++
	f.params[req.Stage] = req.Params
	f.mu.Unlock()
	if f.delay > 0 {
		select {
		case <-time.After(f.delay):
		case <-ctx.Done():
			return StageResult{}, ctx.Err()
		}
	}
	if req.Stage == f.failOn {
		return StageResult{}, errors.New("worker exploded")
	}
	for name, path := range req.Inputs {
		if _, err := os.Stat(path); err != nil {
			return StageResult{}, errors.New("input " + name + " missing: " + path)
		}
	}
	progress(0.5, "working")
	write := func(rel string) ArtifactRef {
		_ = os.WriteFile(filepath.Join(req.OutDir, rel), []byte(req.Stage+":"+rel), 0o600)
		return ArtifactRef{Name: rel[:len(rel)-len(filepath.Ext(rel))], RelPath: rel, MediaType: "application/octet-stream"}
	}
	var res StageResult
	var meta any
	switch req.Stage {
	case "ingest":
		res.Artifacts = []ArtifactRef{write("image.png")}
		m := map[string]any{"format": "JPEG", "original_width": 4000, "original_height": 3000, "width": 1024, "height": 768, "scale": 0.256}
		if f.exifFOV {
			m["fov_prior"] = map[string]any{"source": "exif_35mm", "fov_x_deg": 70.0}
		}
		meta = m
	case "geometry":
		res.Artifacts = []ArtifactRef{write("points.npy"), write("depth.npy"), write("mask.npy")}
		if !f.noNormal {
			res.Artifacts = append(res.Artifacts, write("normal.npy"))
		}
		if f.badPath {
			res.Artifacts = append(res.Artifacts, ArtifactRef{Name: "evil", RelPath: "../evil.npy"})
		}
		src := "model"
		if _, ok := req.Params["fov_x_deg"]; ok {
			src = "prior"
		}
		meta = map[string]any{
			"width": 1024, "height": 768, "fov_source": src, "metric_scale": true, "fp16": true,
			"camera":      map[string]any{"fx": 731.0, "fy": 731.0, "cx": 512.0, "cy": 384.0, "fov_x_deg": 70.0, "fov_y_deg": 55.4},
			"depth_stats": map[string]any{"valid_fraction": 0.9, "near_m": 0.8, "median_m": 3.0, "far_m": 11.0, "likely_sky": false},
		}
	case "maps":
		res.Artifacts = []ArtifactRef{write("depth_inv16.png"), write("depth_inv8.png"), write("valid_mask.png")}
		if _, ok := req.Inputs["normal"]; ok {
			res.Artifacts = append(res.Artifacts, write("normal.png"))
		}
		meta = map[string]any{}
	default:
		return StageResult{}, errors.New("unknown stage")
	}
	res.Metadata, _ = json.Marshal(meta)
	res.ElapsedSeconds = 0.01
	return res, nil
}

type harness struct {
	engine *Engine
	runner *fakeRunner
	input  Input
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	root := t.TempDir()
	store, err := cache.Open(filepath.Join(root, "cache"))
	if err != nil {
		t.Fatal(err)
	}
	reg, err := models.Parse([]byte(testRegistry))
	if err != nil {
		t.Fatal(err)
	}
	runner := newFakeRunner()
	engine, err := NewEngine(runner, store, reg, filepath.Join(root, "packs"),
		WithVersion("test"), WithClock(func() time.Time { return time.Unix(1700000000, 0) }))
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(root, "room.jpg")
	if err := os.WriteFile(src, []byte("fake jpeg"), 0o600); err != nil {
		t.Fatal(err)
	}
	sha, _ := cache.Key(cache.KeyParts{Stage: "input"})
	return &harness{engine: engine, runner: runner, input: Input{SHA256: sha, Path: src, Filename: "/some/dir/room.jpg"}}
}

func TestRunProducesValidPack(t *testing.T) {
	h := newHarness(t)
	var progress []Progress
	res, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, func(p Progress) { progress = append(progress, p) })
	if err != nil {
		t.Fatal(err)
	}
	dir, manifest, err := h.engine.OpenPack(res.PackID)
	if err != nil {
		t.Fatal(err)
	}
	if dir != res.Dir {
		t.Fatalf("OpenPack dir = %s, want %s", dir, res.Dir)
	}
	if err := scp.ValidateManifest(manifest); err != nil {
		t.Fatalf("manifest invalid: %v", err)
	}
	var scene scp.Scene
	data, _ := os.ReadFile(filepath.Join(dir, scp.SceneFile))
	if err := json.Unmarshal(data, &scene); err != nil {
		t.Fatal(err)
	}
	if err := scp.ValidateScene(scene); err != nil {
		t.Fatalf("scene invalid: %v", err)
	}

	if manifest.Input.Filename != "room.jpg" || manifest.Preset != "fast" || !manifest.CommercialUseOK {
		t.Errorf("manifest = %+v", manifest)
	}
	if len(manifest.Models) != 1 || manifest.Models[0].ID != "moge2-vits-normal" {
		t.Errorf("models = %+v", manifest.Models)
	}
	if scene.Camera.Source != "exif+model" {
		t.Errorf("camera source = %s, want exif+model", scene.Camera.Source)
	}
	if len(scene.Quality.Warnings) == 0 {
		t.Error("want downscale warning")
	}
	wantPaths := []string{"scene.json", "image.png", "arrays/points.npy", "arrays/depth_metric.npy",
		"arrays/normal.npy", "arrays/valid_mask.npy", "maps/depth_inv16.png", "maps/depth_inv8.png", "maps/normal.png", "maps/valid_mask.png"}
	got := map[string]bool{}
	for _, a := range manifest.Artifacts {
		got[a.Path] = true
		if _, err := os.Stat(filepath.Join(dir, a.Path)); err != nil {
			t.Errorf("artifact %s missing on disk", a.Path)
		}
	}
	for _, p := range wantPaths {
		if !got[p] {
			t.Errorf("manifest missing artifact %s", p)
		}
	}
	if params := h.runner.params["geometry"]; params["fov_x_deg"] != 70.0 || params["resolution_level"] != 5 {
		t.Errorf("geometry params = %v", params)
	}
	if last := progress[len(progress)-1]; last.Fraction != 1 || last.Stage != "maps" {
		t.Errorf("last progress = %+v", last)
	}
}

func TestSecondRunHitsCacheAndReusesPack(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	first, err := h.engine.Run(ctx, h.input, Options{Preset: PresetFast}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := h.engine.Run(ctx, h.input, Options{Preset: PresetFast}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.PackID != second.PackID {
		t.Fatalf("pack ids differ: %s vs %s", first.PackID, second.PackID)
	}
	for _, stage := range []string{"ingest", "geometry", "maps"} {
		if n := h.runner.count(stage); n != 1 {
			t.Errorf("%s ran %d times, want 1", stage, n)
		}
	}
}

func TestPresetChangeReusesIngestOnlyWhenParamsMatch(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if _, err := h.engine.Run(ctx, h.input, Options{Preset: PresetFast}, nil); err != nil {
		t.Fatal(err)
	}
	// Same preset, different geometry model: ingest cached, geometry + maps recomputed.
	opts := Options{Preset: PresetFast, ModelOverrides: map[string]string{"geometry": "moge2-vitl-normal"}}
	if _, err := h.engine.Run(ctx, h.input, opts, nil); err != nil {
		t.Fatal(err)
	}
	if h.runner.count("ingest") != 1 || h.runner.count("geometry") != 2 || h.runner.count("maps") != 2 {
		t.Errorf("calls = %v", h.runner.calls)
	}
}

func TestDisableEXIFPrior(t *testing.T) {
	h := newHarness(t)
	res, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast, DisableEXIFPrior: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := h.runner.params["geometry"]["fov_x_deg"]; ok {
		t.Error("fov prior passed despite DisableEXIFPrior")
	}
	data, _ := os.ReadFile(filepath.Join(res.Dir, scp.SceneFile))
	var scene scp.Scene
	_ = json.Unmarshal(data, &scene)
	if scene.Camera.Source != "model" {
		t.Errorf("camera source = %s", scene.Camera.Source)
	}
}

func TestLicensePolicy(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	opts := Options{Preset: PresetFast, ModelOverrides: map[string]string{"geometry": "nc-geometry"}}
	_, err := h.engine.Run(ctx, h.input, opts, nil)
	if !errors.Is(err, models.ErrLicenseNotAllowed) || !IsInvalidInput(err) {
		t.Fatalf("err = %v, want license + invalid input", err)
	}
	opts.Policy = models.PolicyAny
	res, err := h.engine.Run(ctx, h.input, opts, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, manifest, _ := h.engine.OpenPack(res.PackID)
	if manifest.CommercialUseOK || manifest.LicensePolicy != "any" || len(manifest.Warnings) == 0 {
		t.Errorf("manifest = %+v", manifest)
	}
}

func TestPlanErrors(t *testing.T) {
	h := newHarness(t)
	tests := map[string]Options{
		"bad preset":       {Preset: "turbo"},
		"unknown override": {ModelOverrides: map[string]string{"lighting": "x"}},
		"wrong task model": {ModelOverrides: map[string]string{"geometry": "seg"}},
		"unknown model":    {ModelOverrides: map[string]string{"geometry": "nope"}},
	}
	for name, opts := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := h.engine.Run(context.Background(), h.input, opts, nil); !IsInvalidInput(err) {
				t.Fatalf("err = %v, want invalid input", err)
			}
		})
	}
	bad := h.input
	bad.SHA256 = "../x"
	if _, err := h.engine.Run(context.Background(), bad, Options{}, nil); !IsInvalidInput(err) {
		t.Fatalf("bad sha err = %v", err)
	}
}

func TestRunnerFailuresAreNotCached(t *testing.T) {
	h := newHarness(t)
	h.runner.failOn = "geometry"
	if _, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, nil); err == nil {
		t.Fatal("want error")
	}
	h.runner.failOn = ""
	if _, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, nil); err != nil {
		t.Fatal(err)
	}
	if h.runner.count("geometry") != 2 {
		t.Errorf("geometry calls = %d, want 2 (failure must not be cached)", h.runner.count("geometry"))
	}
}

func TestUnsafeArtifactPathRejected(t *testing.T) {
	h := newHarness(t)
	h.runner.badPath = true
	_, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, nil)
	if err == nil {
		t.Fatal("want error for traversal artifact")
	}
}

func TestOptionalNormalMissing(t *testing.T) {
	h := newHarness(t)
	h.runner.noNormal = true
	res, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(res.Dir, "maps", "normal.png")); !os.IsNotExist(err) {
		t.Error("normal map should be absent")
	}
}

func TestConcurrentRunsDeduplicateStages(t *testing.T) {
	h := newHarness(t)
	h.runner.delay = 50 * time.Millisecond
	var wg sync.WaitGroup
	var failures atomic.Int32
	ids := make([]string, 4)
	for i := range ids {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			res, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, nil)
			if err != nil {
				failures.Add(1)
				return
			}
			ids[i] = res.PackID
		}(i)
	}
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d concurrent runs failed", failures.Load())
	}
	for _, id := range ids[1:] {
		if id != ids[0] {
			t.Fatal("concurrent runs produced different packs")
		}
	}
	if n := h.runner.count("geometry"); n != 1 {
		t.Errorf("geometry ran %d times under concurrency, want 1", n)
	}
}

func TestCancellation(t *testing.T) {
	h := newHarness(t)
	h.runner.delay = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := h.engine.Run(ctx, h.input, Options{Preset: PresetFast}, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
}

func TestOpenPackRejectsUnknownAndInvalidIDs(t *testing.T) {
	h := newHarness(t)
	for _, id := range []string{"", "../../etc", "zz"} {
		if _, _, err := h.engine.OpenPack(id); !errors.Is(err, ErrPackNotFound) {
			t.Errorf("OpenPack(%q) err = %v", id, err)
		}
	}
	missing, _ := cache.Key(cache.KeyParts{Stage: "missing"})
	if _, _, err := h.engine.OpenPack(missing); !errors.Is(err, ErrPackNotFound) {
		t.Errorf("missing pack err = %v", err)
	}
}

func TestParsePreset(t *testing.T) {
	if p, err := ParsePreset(""); err != nil || p != PresetStandard {
		t.Errorf("ParsePreset(\"\") = %v, %v", p, err)
	}
	if _, err := ParsePreset("nope"); !IsInvalidInput(err) {
		t.Errorf("ParsePreset(nope) err = %v", err)
	}
	if got := PresetNames(); len(got) != 3 || got[0] != "fast" {
		t.Errorf("PresetNames = %v", got)
	}
}

func TestFollowerSurvivesLeaderCancellation(t *testing.T) {
	h := newHarness(t)
	h.runner.delay = 100 * time.Millisecond
	leaderCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	leaderErr := make(chan error, 1)
	go func() {
		_, err := h.engine.Run(leaderCtx, h.input, Options{Preset: PresetFast}, nil)
		leaderErr <- err
	}()
	time.Sleep(10 * time.Millisecond) // let the leader start ingest first
	if _, err := h.engine.Run(context.Background(), h.input, Options{Preset: PresetFast}, nil); err != nil {
		t.Fatalf("follower failed after leader cancellation: %v", err)
	}
	if err := <-leaderErr; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("leader err = %v, want deadline exceeded", err)
	}
}

func TestPrunePacksEvictsLeastRecentlyUsed(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	old, err := h.engine.Run(ctx, h.input, Options{Preset: PresetFast}, nil)
	if err != nil {
		t.Fatal(err)
	}
	past := time.Now().Add(-time.Hour)
	_ = os.Chtimes(filepath.Join(old.Dir, scp.ManifestFile), past, past)
	recent, err := h.engine.Run(ctx, h.input, Options{Preset: PresetStandard}, nil)
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(h.engine.PacksDir(), ".tmp-abandoned")
	_ = os.MkdirAll(stale, 0o700)
	_ = os.Chtimes(stale, past.Add(-time.Hour), past.Add(-time.Hour))

	size, _ := treeSize(recent.Dir)
	freed, err := h.engine.PrunePacks(size) // room for exactly one pack
	if err != nil {
		t.Fatal(err)
	}
	if freed == 0 {
		t.Fatal("nothing freed")
	}
	if _, _, err := h.engine.OpenPack(old.PackID); !errors.Is(err, ErrPackNotFound) {
		t.Errorf("old pack survived: %v", err)
	}
	if _, _, err := h.engine.OpenPack(recent.PackID); err != nil {
		t.Errorf("recent pack pruned: %v", err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Error("abandoned temp pack dir not removed")
	}
}
