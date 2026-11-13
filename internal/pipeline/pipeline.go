// Package pipeline plans and executes analysis stages with stage-level
// caching, then assembles a Spatial Context Pack.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/models"
)

// ErrInvalidInput marks failures caused by the request (bad image, bad
// options) rather than by the service.
var ErrInvalidInput = errors.New("invalid input")

// StageRunner executes one stage, usually in the Python worker.
type StageRunner interface {
	RunStage(ctx context.Context, req StageRequest, progress func(fraction float64, message string)) (StageResult, error)
}

// StageRequest is everything a runner needs to execute a stage.
type StageRequest struct {
	Stage  string
	Inputs map[string]string
	Params map[string]any
	Model  *models.Model
	OutDir string
}

// ArtifactRef is a file a stage wrote, relative to its output directory.
type ArtifactRef struct {
	Name      string  `json:"name"`
	RelPath   string  `json:"rel_path"`
	MediaType string  `json:"media_type"`
	Shape     []int64 `json:"shape,omitempty"`
	DType     string  `json:"dtype,omitempty"`
}

// StageResult is a runner's output for one stage.
type StageResult struct {
	Artifacts      []ArtifactRef   `json:"artifacts"`
	Metadata       json.RawMessage `json:"metadata"`
	ElapsedSeconds float64         `json:"elapsed_seconds"`
}

// Progress reports pipeline advancement across all stages.
type Progress struct {
	Stage    string  `json:"stage"`
	Index    int     `json:"index"`
	Total    int     `json:"total"`
	Fraction float64 `json:"fraction"`
	Message  string  `json:"message,omitempty"`
}

// Input is a content-addressed source image.
type Input struct {
	SHA256   string
	Path     string
	Filename string
}

// Options configure one analysis.
type Options struct {
	Preset Preset
	Policy models.Policy
	// ModelOverrides maps a task (e.g. "geometry") to a registry model id.
	ModelOverrides map[string]string
	// DisableEXIFPrior ignores EXIF focal length when estimating the camera.
	DisableEXIFPrior bool
}

// Result locates a finished pack.
type Result struct {
	PackID string
	Dir    string
}

// Engine runs pipelines. It is safe for concurrent use.
type Engine struct {
	runner   StageRunner
	store    *cache.Store
	registry *models.Registry
	packsDir string
	version  string
	now      func() time.Time

	mu     sync.Mutex
	flight map[string]*call
}

type call struct {
	done chan struct{}
	out  stageOutput
	err  error
}

// Option configures an Engine.
type Option func(*Engine)

// WithVersion sets the doppio version recorded in manifests.
func WithVersion(v string) Option { return func(e *Engine) { e.version = v } }

// WithClock overrides the time source (tests).
func WithClock(now func() time.Time) Option { return func(e *Engine) { e.now = now } }

// NewEngine wires an engine. packsDir holds assembled packs.
func NewEngine(runner StageRunner, store *cache.Store, registry *models.Registry, packsDir string, opts ...Option) (*Engine, error) {
	if err := os.MkdirAll(packsDir, 0o700); err != nil {
		return nil, fmt.Errorf("failed to create packs directory: %w", err)
	}
	e := &Engine{
		runner: runner, store: store, registry: registry, packsDir: packsDir,
		version: "dev", now: time.Now, flight: map[string]*call{},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e, nil
}

// PacksDir returns the directory holding assembled packs.
func (e *Engine) PacksDir() string { return e.packsDir }

// stageOutput is a resolved (computed or cached) stage.
type stageOutput struct {
	name      string
	version   string
	key       string
	dir       string
	cached    bool
	model     *models.Model
	result    StageResult
	artifacts map[string]string // name -> absolute path
}

func (s stageOutput) metadata(v any) error {
	if len(s.result.Metadata) == 0 {
		return nil
	}
	if err := json.Unmarshal(s.result.Metadata, v); err != nil {
		return fmt.Errorf("failed to decode %s metadata: %w", s.name, err)
	}
	return nil
}

// Run executes the pipeline for in and returns the assembled pack.
func (e *Engine) Run(ctx context.Context, in Input, opts Options, progress func(Progress)) (Result, error) {
	if progress == nil {
		progress = func(Progress) {}
	}
	if !cache.ValidKey(in.SHA256) {
		return Result{}, fmt.Errorf("%w: input sha256 is not a valid digest", ErrInvalidInput)
	}
	plan, err := e.plan(opts)
	if err != nil {
		return Result{}, err
	}

	outputs := make(map[string]stageOutput, len(plan.stages))
	ordered := make([]stageOutput, 0, len(plan.stages))
	for i, st := range plan.stages {
		if err := ctx.Err(); err != nil {
			return Result{}, err
		}
		inputs, inputKeys, err := st.bind(in, outputs)
		if err != nil {
			return Result{}, err
		}
		params, err := st.params(outputs)
		if err != nil {
			return Result{}, err
		}
		parts := cache.KeyParts{Stage: st.name, StageVersion: st.version, Params: params, Inputs: inputKeys}
		if st.model != nil {
			parts.ModelID, parts.ModelRevision = st.model.ID, st.model.Revision
		}
		key, err := cache.Key(parts)
		if err != nil {
			return Result{}, err
		}
		total := len(plan.stages)
		report := func(fraction float64, message string) {
			progress(Progress{Stage: st.name, Index: i, Total: total,
				Fraction: (float64(i) + clamp01(fraction)) / float64(total), Message: message})
		}
		report(0, "starting")
		out, err := e.resolveStage(ctx, key, st, StageRequest{Stage: st.name, Inputs: inputs, Params: params, Model: st.model}, report)
		if err != nil {
			return Result{}, fmt.Errorf("stage %s: %w", st.name, err)
		}
		report(1, map[bool]string{true: "cached", false: "done"}[out.cached])
		outputs[st.name] = out
		ordered = append(ordered, out)
	}
	return e.assemble(in, opts, plan, ordered)
}

// resolveStage returns a cached stage, joins an identical in-flight
// computation, or runs the stage and commits it to the cache.
func (e *Engine) resolveStage(ctx context.Context, key string, st stageSpec, req StageRequest, report func(float64, string)) (stageOutput, error) {
	for {
		out, retry, err := e.resolveOnce(ctx, key, st, req, report)
		if !retry {
			return out, err
		}
	}
}

// resolveOnce returns retry=true when it joined another caller's computation
// that was cancelled while this caller's context is still live.
func (e *Engine) resolveOnce(ctx context.Context, key string, st stageSpec, req StageRequest, report func(float64, string)) (stageOutput, bool, error) {
	if out, ok, err := e.lookup(key, st); err != nil || ok {
		return out, false, err
	}

	e.mu.Lock()
	if c, ok := e.flight[key]; ok {
		e.mu.Unlock()
		select {
		case <-c.done:
			leaderCancelled := errors.Is(c.err, context.Canceled) || errors.Is(c.err, context.DeadlineExceeded)
			if leaderCancelled && ctx.Err() == nil {
				return stageOutput{}, true, nil
			}
			return c.out, false, c.err
		case <-ctx.Done():
			return stageOutput{}, false, ctx.Err()
		}
	}
	c := &call{done: make(chan struct{})}
	e.flight[key] = c
	e.mu.Unlock()

	c.out, c.err = e.compute(ctx, key, st, req, report)
	e.mu.Lock()
	delete(e.flight, key)
	e.mu.Unlock()
	close(c.done)
	return c.out, false, c.err
}

func (e *Engine) lookup(key string, st stageSpec) (stageOutput, bool, error) {
	entry, ok, err := e.store.Get(key)
	if err != nil || !ok {
		return stageOutput{}, false, err
	}
	var res StageResult
	if err := json.Unmarshal(entry.Meta, &res); err != nil {
		return stageOutput{}, false, fmt.Errorf("corrupt cache entry %s: %w", key[:12], err)
	}
	out, err := newStageOutput(st, key, entry.Dir, res, true)
	return out, err == nil, err
}

func (e *Engine) compute(ctx context.Context, key string, st stageSpec, req StageRequest, report func(float64, string)) (stageOutput, error) {
	txn, err := e.store.Begin(key)
	if err != nil {
		return stageOutput{}, err
	}
	defer txn.Abort()
	req.OutDir = txn.Dir()
	res, err := e.runner.RunStage(ctx, req, report)
	if err != nil {
		return stageOutput{}, err
	}
	if _, err := newStageOutput(st, key, txn.Dir(), res, false); err != nil {
		return stageOutput{}, fmt.Errorf("worker returned invalid artifacts: %w", err)
	}
	meta, err := json.Marshal(res)
	if err != nil {
		return stageOutput{}, fmt.Errorf("failed to encode stage result: %w", err)
	}
	entry, err := txn.Commit(meta)
	if err != nil {
		return stageOutput{}, err
	}
	return newStageOutput(st, key, entry.Dir, res, false)
}

func newStageOutput(st stageSpec, key, dir string, res StageResult, cached bool) (stageOutput, error) {
	out := stageOutput{
		name: st.name, version: st.version, key: key, dir: dir, cached: cached,
		model: st.model, result: res, artifacts: make(map[string]string, len(res.Artifacts)),
	}
	for _, a := range res.Artifacts {
		if a.Name == "" || !filepath.IsLocal(a.RelPath) {
			return stageOutput{}, fmt.Errorf("artifact %q has unsafe path %q", a.Name, a.RelPath)
		}
		if _, dup := out.artifacts[a.Name]; dup {
			return stageOutput{}, fmt.Errorf("duplicate artifact name %q", a.Name)
		}
		path := filepath.Join(dir, a.RelPath)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() {
			return stageOutput{}, fmt.Errorf("artifact %q is missing or not a regular file", a.Name)
		}
		out.artifacts[a.Name] = path
	}
	return out, nil
}

func clamp01(v float64) float64 {
	switch {
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}
