package pipeline

import (
	"errors"
	"fmt"

	"github.com/shaansundar/doppio/internal/models"
)

// binding wires a stage input to the source image (from == "") or to an
// upstream stage's artifact.
type binding struct {
	as       string
	from     string
	artifact string
	optional bool
}

type stageSpec struct {
	name     string
	version  string
	model    *models.Model
	bindings []binding
	paramsFn func(outputs map[string]stageOutput) (map[string]any, error)
}

func (s stageSpec) bind(in Input, outputs map[string]stageOutput) (paths, keys map[string]string, err error) {
	paths = make(map[string]string, len(s.bindings))
	keys = make(map[string]string, len(s.bindings))
	for _, b := range s.bindings {
		if b.from == "" {
			paths[b.as] = in.Path
			keys[b.as] = "blob:" + in.SHA256
			continue
		}
		up, ok := outputs[b.from]
		if !ok {
			return nil, nil, fmt.Errorf("stage %s depends on %s, which has not run", s.name, b.from)
		}
		path, ok := up.artifacts[b.artifact]
		if !ok {
			if b.optional {
				continue
			}
			return nil, nil, fmt.Errorf("stage %s needs artifact %s/%s, which was not produced", s.name, b.from, b.artifact)
		}
		paths[b.as] = path
		keys[b.as] = up.key + "/" + b.artifact
	}
	return paths, keys, nil
}

func (s stageSpec) params(outputs map[string]stageOutput) (map[string]any, error) {
	if s.paramsFn == nil {
		return map[string]any{}, nil
	}
	return s.paramsFn(outputs)
}

type executionPlan struct {
	preset Preset
	policy models.Policy
	stages []stageSpec
}

// Stage versions: bump when a stage's outputs change meaning, which
// invalidates its cache entries and everything downstream.
const (
	ingestVersion   = "1"
	geometryVersion = "1"
	mapsVersion     = "1"
)

// supportedOverrideTasks lists tasks whose model can be overridden per request.
var supportedOverrideTasks = map[string]bool{"geometry": true}

func (e *Engine) plan(opts Options) (executionPlan, error) {
	preset := opts.Preset
	if preset == "" {
		preset = PresetStandard
	}
	def, ok := presets[preset]
	if !ok {
		return executionPlan{}, fmt.Errorf("%w: unknown preset %q", ErrInvalidInput, preset)
	}
	policy := opts.Policy
	if policy == "" {
		policy = models.PolicyCommercial
	}
	for task := range opts.ModelOverrides {
		if !supportedOverrideTasks[task] {
			return executionPlan{}, fmt.Errorf("%w: model override for task %q is not supported", ErrInvalidInput, task)
		}
	}

	geometryID := def.geometryModel
	if id, ok := opts.ModelOverrides["geometry"]; ok {
		geometryID = id
	}
	geometry, err := e.registry.Resolve(geometryID, policy)
	if err != nil {
		return executionPlan{}, fmt.Errorf("%w: %w", ErrInvalidInput, err)
	}
	if geometry.Task != "geometry" {
		return executionPlan{}, fmt.Errorf("%w: model %s is a %s model, not geometry", ErrInvalidInput, geometry.ID, geometry.Task)
	}

	ingest := stageSpec{
		name: "ingest", version: ingestVersion,
		bindings: []binding{{as: "source"}},
		paramsFn: func(map[string]stageOutput) (map[string]any, error) {
			return map[string]any{"max_side": def.maxSide}, nil
		},
	}
	geom := stageSpec{
		name: "geometry", version: geometryVersion, model: &geometry,
		bindings: []binding{{as: "image", from: "ingest", artifact: "image"}},
		paramsFn: func(outputs map[string]stageOutput) (map[string]any, error) {
			params := map[string]any{"resolution_level": def.resolutionLevel}
			if opts.DisableEXIFPrior {
				return params, nil
			}
			var meta ingestMeta
			if err := outputs["ingest"].metadata(&meta); err != nil {
				return nil, err
			}
			if meta.FOVPrior != nil && meta.FOVPrior.FovXDeg > 0 && meta.FOVPrior.FovXDeg < 180 {
				params["fov_x_deg"] = meta.FOVPrior.FovXDeg
			}
			return params, nil
		},
	}
	maps := stageSpec{
		name: "maps", version: mapsVersion,
		bindings: []binding{
			{as: "depth", from: "geometry", artifact: "depth"},
			{as: "mask", from: "geometry", artifact: "mask"},
			{as: "normal", from: "geometry", artifact: "normal", optional: true},
		},
	}
	return executionPlan{preset: preset, policy: policy, stages: []stageSpec{ingest, geom, maps}}, nil
}

// IsInvalidInput reports whether err was caused by the request.
func IsInvalidInput(err error) bool { return errors.Is(err, ErrInvalidInput) }

// Stage metadata as emitted by the worker (see worker/src/doppio_worker/stages).

type ingestMeta struct {
	Format         string  `json:"format"`
	OriginalWidth  int     `json:"original_width"`
	OriginalHeight int     `json:"original_height"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	Scale          float64 `json:"scale"`
	HadAlpha       bool    `json:"had_alpha"`
	FOVPrior       *struct {
		Source  string  `json:"source"`
		FovXDeg float64 `json:"fov_x_deg"`
	} `json:"fov_prior"`
}

type geometryMeta struct {
	Width  int `json:"width"`
	Height int `json:"height"`
	Camera struct {
		Fx      float64 `json:"fx"`
		Fy      float64 `json:"fy"`
		Cx      float64 `json:"cx"`
		Cy      float64 `json:"cy"`
		FovXDeg float64 `json:"fov_x_deg"`
		FovYDeg float64 `json:"fov_y_deg"`
	} `json:"camera"`
	FOVSource   string `json:"fov_source"`
	MetricScale bool   `json:"metric_scale"`
	DepthStats  struct {
		ValidFraction float64  `json:"valid_fraction"`
		NearM         *float64 `json:"near_m"`
		MedianM       *float64 `json:"median_m"`
		FarM          *float64 `json:"far_m"`
		LikelySky     bool     `json:"likely_sky"`
	} `json:"depth_stats"`
	FP16 bool `json:"fp16"`
}
