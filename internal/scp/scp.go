// Package scp defines the Spatial Context Pack: the versioned bundle doppio
// returns (manifest.json + scene.json + maps/ + overlays/ + 3d/).
package scp

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/shaansundar/doppio/internal/models"
)

// Schema identifiers. Bump the major version on breaking changes.
const (
	SchemaManifest = "doppio.manifest/v1"
	SchemaScene    = "doppio.scene/v1"

	ManifestFile = "manifest.json"
	SceneFile    = "scene.json"
)

// FullFrameDiagonalMM is the 36x24 mm film diagonal used for 35 mm equivalents.
var FullFrameDiagonalMM = math.Hypot(36, 24)

// Manifest describes how a pack was produced and lists its artifacts.
type Manifest struct {
	Schema          string     `json:"schema"`
	PackID          string     `json:"pack_id"`
	CreatedAt       time.Time  `json:"created_at"`
	DoppioVersion   string     `json:"doppio_version"`
	Preset          string     `json:"preset"`
	LicensePolicy   string     `json:"license_policy"`
	CommercialUseOK bool       `json:"commercial_use_ok"`
	Input           Input      `json:"input"`
	Models          []ModelUse `json:"models"`
	Stages          []StageRun `json:"stages"`
	Artifacts       []Artifact `json:"artifacts"`
	Warnings        []string   `json:"warnings"`
}

// Input identifies the analyzed image.
type Input struct {
	SHA256         string `json:"sha256"`
	Filename       string `json:"filename,omitempty"`
	Format         string `json:"format,omitempty"`
	OriginalWidth  int    `json:"original_width"`
	OriginalHeight int    `json:"original_height"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
}

// ModelUse records a model that contributed to the pack and its license.
type ModelUse struct {
	ID       string         `json:"id"`
	Task     string         `json:"task"`
	HFRepo   string         `json:"hf_repo,omitempty"`
	Revision string         `json:"revision,omitempty"`
	License  models.License `json:"license"`
}

// StageRun records one executed (or cache-hit) pipeline stage.
type StageRun struct {
	Name           string  `json:"name"`
	Version        string  `json:"version"`
	Key            string  `json:"key"`
	Cached         bool    `json:"cached"`
	ElapsedSeconds float64 `json:"elapsed_seconds"`
	ModelID        string  `json:"model_id,omitempty"`
}

// Artifact is one file in the pack, addressed relative to the pack root.
type Artifact struct {
	Name        string  `json:"name"`
	Path        string  `json:"path"`
	MediaType   string  `json:"media_type"`
	Shape       []int64 `json:"shape,omitempty"`
	DType       string  `json:"dtype,omitempty"`
	SHA256      string  `json:"sha256"`
	Bytes       int64   `json:"bytes"`
	Description string  `json:"description,omitempty"`
}

// Scene is the structured spatial description of the image.
type Scene struct {
	Schema      string      `json:"schema"`
	Image       ImageInfo   `json:"image"`
	Conventions Conventions `json:"conventions"`
	Camera      Camera      `json:"camera"`
	DepthStats  DepthStats  `json:"depth_stats"`
	Quality     Quality     `json:"quality"`
}

// ImageInfo describes the working image all pixel coordinates refer to.
type ImageInfo struct {
	Width  int    `json:"width"`
	Height int    `json:"height"`
	SHA256 string `json:"sha256"`
}

// Conventions makes coordinate frames and encodings explicit for consumers.
type Conventions struct {
	CameraFrame  string `json:"camera_frame"`
	Units        string `json:"units"`
	ImageOrigin  string `json:"image_origin"`
	DepthMap     string `json:"depth_map"`
	NormalMap    string `json:"normal_map"`
	InvalidValue string `json:"invalid_value"`
}

// DefaultConventions are the conventions every v1 pack uses.
func DefaultConventions() Conventions {
	return Conventions{
		CameraFrame:  "opencv: x right, y down, z forward",
		Units:        "meters",
		ImageOrigin:  "top-left, pixels",
		DepthMap:     "inverse depth, robust-percentile normalized, near=bright, invalid=0",
		NormalMap:    "opengl: R=+x right, G=+y up, B=+z toward viewer, invalid=0",
		InvalidValue: "NaN in .npy arrays",
	}
}

// Camera holds pinhole intrinsics for the working image.
type Camera struct {
	Model          string        `json:"model"`
	Fx             float64       `json:"fx"`
	Fy             float64       `json:"fy"`
	Cx             float64       `json:"cx"`
	Cy             float64       `json:"cy"`
	FovXDeg        float64       `json:"fov_x_deg"`
	FovYDeg        float64       `json:"fov_y_deg"`
	Focal35mmEquiv float64       `json:"focal_35mm_equiv"`
	Source         string        `json:"source"`
	K              [3][3]float64 `json:"k"`
}

// NewPinholeCamera builds a camera and derives K and the 35 mm equivalent.
func NewPinholeCamera(fx, fy, cx, cy, fovX, fovY float64, width, height int, source string) Camera {
	c := Camera{
		Model: "pinhole", Fx: fx, Fy: fy, Cx: cx, Cy: cy,
		FovXDeg: fovX, FovYDeg: fovY, Source: source,
		K: [3][3]float64{{fx, 0, cx}, {0, fy, cy}, {0, 0, 1}},
	}
	if diag := math.Hypot(float64(width), float64(height)); diag > 0 {
		c.Focal35mmEquiv = (fx + fy) / 2 * FullFrameDiagonalMM / diag
	}
	return c
}

// DepthStats summarizes metric depth over valid pixels.
type DepthStats struct {
	NearM         *float64 `json:"near_m,omitempty"`
	MedianM       *float64 `json:"median_m,omitempty"`
	FarM          *float64 `json:"far_m,omitempty"`
	ValidFraction float64  `json:"valid_fraction"`
	LikelySky     bool     `json:"likely_sky"`
}

// Quality carries confidence signals and warnings for consumers.
type Quality struct {
	MetricScale bool     `json:"metric_scale"`
	Warnings    []string `json:"warnings"`
}

// WriteJSON writes v as indented JSON to dir/name with owner-only permissions.
func WriteJSON(dir, name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to encode %s: %w", name, err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
		return fmt.Errorf("failed to write %s: %w", name, err)
	}
	return nil
}

// ReadManifest loads dir/manifest.json.
func ReadManifest(dir string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(filepath.Join(dir, ManifestFile))
	if err != nil {
		return m, fmt.Errorf("failed to read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("failed to decode manifest: %w", err)
	}
	if m.Schema != SchemaManifest {
		return m, fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	return m, nil
}
