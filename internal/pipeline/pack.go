package pipeline

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/scp"
)

// packFormatVersion changes when pack layout changes without stage changes.
const packFormatVersion = "1"

type placement struct {
	stage, artifact, path, description string
	optional                           bool
}

// placements maps stage artifacts to their location inside a pack.
var placements = []placement{
	{"ingest", "image", "image.png", "working image; all pixel coordinates refer to it", false},
	{"geometry", "points", "arrays/points.npy", "HxWx3 float32 camera-frame points (m), NaN = invalid", false},
	{"geometry", "depth", "arrays/depth_metric.npy", "HxW float32 metric depth (m), NaN = invalid", false},
	{"geometry", "normal", "arrays/normal.npy", "HxWx3 float32 unit normals, OpenCV camera frame", true},
	{"geometry", "mask", "arrays/valid_mask.npy", "HxW bool valid-geometry mask", false},
	{"maps", "depth_inv16", "maps/depth_inv16.png", "16-bit inverse depth, near=bright (ControlNet depth)", false},
	{"maps", "depth_inv8", "maps/depth_inv8.png", "8-bit inverse depth, near=bright (ControlNet depth)", false},
	{"maps", "normal", "maps/normal.png", "OpenGL-convention normal map (ControlNet normal)", true},
	{"maps", "valid_mask", "maps/valid_mask.png", "255 = valid geometry, 0 = sky / no data", false},
}

func (e *Engine) assemble(in Input, opts Options, plan executionPlan, stages []stageOutput) (Result, error) {
	byName := make(map[string]stageOutput, len(stages))
	keys := make([]string, 0, len(stages))
	for _, s := range stages {
		byName[s.name] = s
		keys = append(keys, s.name+"="+s.key)
	}
	idSource, err := json.Marshal(map[string]any{
		"format": packFormatVersion, "preset": plan.preset, "policy": plan.policy, "stages": keys,
	})
	if err != nil {
		return Result{}, fmt.Errorf("failed to derive pack id: %w", err)
	}
	sum := sha256.Sum256(idSource)
	packID := hex.EncodeToString(sum[:])
	final := filepath.Join(e.packsDir, packID)
	if _, err := os.Stat(filepath.Join(final, scp.ManifestFile)); err == nil {
		touch(filepath.Join(final, scp.ManifestFile))
		return Result{PackID: packID, Dir: final}, nil
	}

	tmp, err := os.MkdirTemp(e.packsDir, ".tmp-"+packID[:12]+"-")
	if err != nil {
		return Result{}, fmt.Errorf("failed to create pack directory: %w", err)
	}
	renamed := false
	defer func() {
		if !renamed {
			_ = os.RemoveAll(tmp)
		}
	}()

	manifest, scene, err := e.describe(in, plan, byName, packID)
	if err != nil {
		return Result{}, err
	}

	for _, p := range placements {
		st, ok := byName[p.stage]
		if !ok {
			continue
		}
		src, ok := st.artifacts[p.artifact]
		if !ok {
			if p.optional {
				continue
			}
			return Result{}, fmt.Errorf("stage %s did not produce required artifact %s", p.stage, p.artifact)
		}
		art, err := placeArtifact(src, tmp, p.path)
		if err != nil {
			return Result{}, err
		}
		ref := findArtifact(st.result.Artifacts, p.artifact)
		art.Name, art.MediaType, art.Shape, art.DType, art.Description =
			p.stage+"."+p.artifact, ref.MediaType, ref.Shape, ref.DType, p.description
		manifest.Artifacts = append(manifest.Artifacts, art)
	}

	if err := scp.ValidateScene(scene); err != nil {
		return Result{}, fmt.Errorf("refusing to write invalid scene: %w", err)
	}
	if err := scp.WriteJSON(tmp, scp.SceneFile, scene); err != nil {
		return Result{}, err
	}
	sceneArt, err := describeFile(filepath.Join(tmp, scp.SceneFile), scp.SceneFile)
	if err != nil {
		return Result{}, err
	}
	sceneArt.Name, sceneArt.MediaType, sceneArt.Description = "scene", "application/json", "structured scene description"
	manifest.Artifacts = append([]scp.Artifact{sceneArt}, manifest.Artifacts...)

	if err := scp.ValidateManifest(manifest); err != nil {
		return Result{}, fmt.Errorf("refusing to write invalid manifest: %w", err)
	}
	if err := scp.WriteJSON(tmp, scp.ManifestFile, manifest); err != nil {
		return Result{}, err
	}
	if err := os.Rename(tmp, final); err != nil {
		if _, statErr := os.Stat(filepath.Join(final, scp.ManifestFile)); statErr == nil {
			return Result{PackID: packID, Dir: final}, nil // a concurrent run won
		}
		return Result{}, fmt.Errorf("failed to publish pack: %w", err)
	}
	renamed = true
	return Result{PackID: packID, Dir: final}, nil
}

func (e *Engine) describe(in Input, plan executionPlan, byName map[string]stageOutput, packID string) (scp.Manifest, scp.Scene, error) {
	var ing ingestMeta
	if err := byName["ingest"].metadata(&ing); err != nil {
		return scp.Manifest{}, scp.Scene{}, err
	}
	var geo geometryMeta
	if err := byName["geometry"].metadata(&geo); err != nil {
		return scp.Manifest{}, scp.Scene{}, err
	}
	if geo.Width != ing.Width || geo.Height != ing.Height {
		return scp.Manifest{}, scp.Scene{}, fmt.Errorf("geometry size %dx%d does not match working image %dx%d",
			geo.Width, geo.Height, ing.Width, ing.Height)
	}

	sceneWarnings := []string{}
	if ing.Scale < 1 {
		sceneWarnings = append(sceneWarnings, fmt.Sprintf(
			"analyzed a downscaled %dx%d working image (original %dx%d); pixel coordinates refer to the working image",
			ing.Width, ing.Height, ing.OriginalWidth, ing.OriginalHeight))
	}
	if !geo.MetricScale {
		sceneWarnings = append(sceneWarnings, "depth is relative, not metric")
	}
	if geo.DepthStats.ValidFraction < 0.5 {
		sceneWarnings = append(sceneWarnings, "less than half of the image has valid geometry")
	}
	source := "model"
	if geo.FOVSource == "prior" {
		source = "exif+model"
	}
	scene := scp.Scene{
		Schema:      scp.SchemaScene,
		Image:       scp.ImageInfo{Width: ing.Width, Height: ing.Height, SHA256: in.SHA256},
		Conventions: scp.DefaultConventions(),
		Camera: scp.NewPinholeCamera(geo.Camera.Fx, geo.Camera.Fy, geo.Camera.Cx, geo.Camera.Cy,
			geo.Camera.FovXDeg, geo.Camera.FovYDeg, ing.Width, ing.Height, source),
		DepthStats: scp.DepthStats{
			NearM: geo.DepthStats.NearM, MedianM: geo.DepthStats.MedianM, FarM: geo.DepthStats.FarM,
			ValidFraction: geo.DepthStats.ValidFraction, LikelySky: geo.DepthStats.LikelySky,
		},
		Quality: scp.Quality{MetricScale: geo.MetricScale, Warnings: sceneWarnings},
	}

	filename := ""
	if in.Filename != "" {
		filename = filepath.Base(in.Filename)
	}
	manifest := scp.Manifest{
		Schema: scp.SchemaManifest, PackID: packID, CreatedAt: e.now().UTC(), DoppioVersion: e.version,
		Preset: string(plan.preset), LicensePolicy: string(plan.policy), CommercialUseOK: true,
		Input: scp.Input{
			SHA256: in.SHA256, Filename: filename, Format: ing.Format,
			OriginalWidth: ing.OriginalWidth, OriginalHeight: ing.OriginalHeight, Width: ing.Width, Height: ing.Height,
		},
		Models: []scp.ModelUse{}, Stages: []scp.StageRun{}, Artifacts: []scp.Artifact{}, Warnings: []string{},
	}
	for _, st := range orderedStages(plan, byName) {
		run := scp.StageRun{Name: st.name, Version: st.version, Key: st.key, Cached: st.cached, ElapsedSeconds: st.result.ElapsedSeconds}
		if st.model != nil {
			run.ModelID = st.model.ID
			manifest.Models = append(manifest.Models, scp.ModelUse{
				ID: st.model.ID, Task: st.model.Task, HFRepo: st.model.HFRepo, Revision: st.model.Revision, License: st.model.License,
			})
			if !st.model.License.Commercial {
				manifest.CommercialUseOK = false
			}
		}
		manifest.Stages = append(manifest.Stages, run)
	}
	if !geo.FP16 {
		manifest.Warnings = append(manifest.Warnings, "geometry ran in fp32 (fp16 unavailable or disabled); slower but not less accurate")
	}
	if !manifest.CommercialUseOK {
		manifest.Warnings = append(manifest.Warnings, "contains output from non-commercial models; not cleared for commercial use")
	}
	return manifest, scene, nil
}

func orderedStages(plan executionPlan, byName map[string]stageOutput) []stageOutput {
	out := make([]stageOutput, 0, len(plan.stages))
	for _, s := range plan.stages {
		if st, ok := byName[s.name]; ok {
			out = append(out, st)
		}
	}
	return out
}

func findArtifact(refs []ArtifactRef, name string) ArtifactRef {
	for _, r := range refs {
		if r.Name == name {
			return r
		}
	}
	return ArtifactRef{}
}

// placeArtifact hard-links src into the pack (cache files are immutable),
// falling back to a copy across filesystems, and returns its digest.
func placeArtifact(src, packDir, rel string) (scp.Artifact, error) {
	dst := filepath.Join(packDir, rel)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return scp.Artifact{}, fmt.Errorf("failed to create pack subdirectory: %w", err)
	}
	if err := os.Link(src, dst); err != nil {
		if err := copyFile(src, dst); err != nil {
			return scp.Artifact{}, err
		}
	}
	return describeFile(dst, rel)
}

func describeFile(path, rel string) (scp.Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return scp.Artifact{}, fmt.Errorf("failed to open artifact: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return scp.Artifact{}, fmt.Errorf("failed to hash artifact: %w", err)
	}
	return scp.Artifact{Path: filepath.ToSlash(rel), SHA256: hex.EncodeToString(h.Sum(nil)), Bytes: n}, nil
}

func copyFile(src, dst string) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("failed to open artifact for copy: %w", err)
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("failed to create artifact copy: %w", err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil {
			err = closeErr
		}
	}()
	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("failed to copy artifact: %w", err)
	}
	return nil
}

// OpenPack resolves a pack id to its directory and manifest.
func (e *Engine) OpenPack(packID string) (string, scp.Manifest, error) {
	if !cache.ValidKey(packID) {
		return "", scp.Manifest{}, ErrPackNotFound
	}
	dir := filepath.Join(e.packsDir, packID)
	m, err := scp.ReadManifest(dir)
	if errors.Is(err, os.ErrNotExist) {
		return "", scp.Manifest{}, ErrPackNotFound
	}
	if err != nil {
		return "", scp.Manifest{}, err
	}
	touch(filepath.Join(dir, scp.ManifestFile))
	return dir, m, nil
}

// touch records an access for LRU pruning; failures are harmless.
func touch(path string) {
	now := time.Now()
	_ = os.Chtimes(path, now, now)
}

// ErrPackNotFound is returned for unknown pack ids.
var ErrPackNotFound = errors.New("pack not found")
