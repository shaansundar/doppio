// Package api serves doppio's local REST API (/v1).
package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	workerv1 "github.com/shaansundar/doppio/gen/go/doppio/worker/v1"
	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/jobs"
	"github.com/shaansundar/doppio/internal/models"
	"github.com/shaansundar/doppio/internal/pipeline"
	"github.com/shaansundar/doppio/internal/scp"
)

// JobQueue accepts and cancels analyses.
type JobQueue interface {
	Submit(in pipeline.Input, opts pipeline.Options) (jobs.Job, error)
	Cancel(id string) (jobs.Job, bool)
}

// JobStatus reads job state.
type JobStatus interface {
	Get(id string) (jobs.Job, bool)
	Wait(ctx context.Context, id string) (jobs.Job, bool)
}

// PackStore resolves pack ids.
type PackStore interface {
	OpenPack(id string) (string, scp.Manifest, error)
}

// BlobStore persists uploads by content hash.
type BlobStore interface {
	PutBlob(r io.Reader, maxBytes int64, ext string) (string, string, error)
}

// WorkerHealth reports inference worker state.
type WorkerHealth interface {
	Health(ctx context.Context) (*workerv1.HealthResponse, error)
}

// Config wires the server's dependencies.
type Config struct {
	Queue    JobQueue
	Status   JobStatus
	Packs    PackStore
	Blobs    BlobStore
	Worker   WorkerHealth
	Registry *models.Registry
	Token    string
	// AllowedHosts are accepted Host header values (see config.AllowedHosts).
	AllowedHosts map[string]bool
	// CORSOrigins are browser origins allowed to call the API.
	CORSOrigins    map[string]bool
	MaxUploadBytes int64
	Version        string
	Logger         *slog.Logger
	// Shutdown, if set, is invoked by POST /v1/admin/shutdown (`doppio stop`).
	Shutdown func()
}

// Server is the HTTP API.
type Server struct {
	cfg Config
}

const maxWait = 10 * time.Minute

// New validates cfg.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Queue == nil || cfg.Status == nil || cfg.Packs == nil || cfg.Blobs == nil || cfg.Registry == nil:
		return nil, errors.New("api: queue, status, packs, blobs and registry are required")
	case len(cfg.Token) < 32:
		return nil, errors.New("api: token must be at least 32 characters")
	case len(cfg.AllowedHosts) == 0:
		return nil, errors.New("api: allowed hosts are required")
	}
	if cfg.MaxUploadBytes <= 0 {
		cfg.MaxUploadBytes = 200 << 20
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if cfg.CORSOrigins == nil {
		cfg.CORSOrigins = map[string]bool{}
	}
	return &Server{cfg: cfg}, nil
}

// Handler returns the fully wrapped HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.handleHealthz)
	mux.HandleFunc("GET /v1/info", s.handleInfo)
	mux.HandleFunc("GET /v1/models", s.handleModels)
	mux.HandleFunc("POST /v1/analyze", s.handleAnalyze)
	mux.HandleFunc("GET /v1/jobs/{id}", s.handleGetJob)
	mux.HandleFunc("DELETE /v1/jobs/{id}", s.handleCancelJob)
	mux.HandleFunc("GET /v1/packs/{id}", s.handleGetPack)
	mux.HandleFunc("GET /v1/packs/{id}/artifacts/{path...}", s.handleArtifact)
	if s.cfg.Shutdown != nil {
		mux.HandleFunc("POST /v1/admin/shutdown", func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, http.StatusAccepted, map[string]string{"status": "shutting down"})
			go s.cfg.Shutdown()
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeProblem(w, r, http.StatusNotFound, "not-found", "no such endpoint")
	})

	public := map[string]bool{"/healthz": true}
	var h http.Handler = mux
	h = auth(s.cfg.Token, public, h)
	h = cors(s.cfg.CORSOrigins, h)
	h = hostGuard(s.cfg.AllowedHosts, h)
	return recoverAndLog(s.cfg.Logger, h)
}

func (s *Server) handleHealthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": s.cfg.Version})
}

func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	info := map[string]any{
		"version":                s.cfg.Version,
		"presets":                pipeline.PresetNames(),
		"default_license_policy": models.PolicyCommercial,
		"max_upload_bytes":       s.cfg.MaxUploadBytes,
	}
	if s.cfg.Worker != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if h, err := s.cfg.Worker.Health(ctx); err != nil {
			info["worker"] = map[string]any{"ok": false, "error": err.Error()}
		} else {
			info["worker"] = map[string]any{
				"ok": true, "version": h.GetWorkerVersion(), "device": h.GetDevice(),
				"torch_version": h.GetTorchVersion(), "stages": h.GetStages(),
			}
		}
	}
	writeJSON(w, http.StatusOK, info)
}

type modelView struct {
	models.Model
	Usable bool `json:"usable"`
}

func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	policy, err := models.ParsePolicy(r.URL.Query().Get("policy"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad-policy", err.Error())
		return
	}
	all := s.cfg.Registry.All()
	out := make([]modelView, 0, len(all))
	for _, m := range all {
		out = append(out, modelView{Model: m, Usable: m.Status == models.StatusAvailable && policy.Allows(m)})
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy, "models": out})
}

type jobView struct {
	jobs.Job
	Links map[string]string `json:"links"`
}

func viewJob(j jobs.Job) jobView {
	links := map[string]string{"self": "/v1/jobs/" + j.ID}
	if j.PackID != "" {
		links["pack"] = "/v1/packs/" + j.PackID
	}
	return jobView{Job: j, Links: links}
}

// analyzeForm holds the non-file multipart fields.
type analyzeForm struct {
	preset, policy, geometryModel string
	allowNonCommercial            bool
	disableEXIFPrior              bool
}

var formFields = map[string]bool{
	"preset": true, "license_policy": true, "allow_noncommercial": true,
	"geometry_model": true, "disable_exif_prior": true,
}

func (s *Server) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	wait, err := parseWait(r.URL.Query().Get("wait"))
	if err != nil {
		writeProblem(w, r, http.StatusBadRequest, "bad-wait", err.Error())
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadBytes+(1<<20))
	mr, err := r.MultipartReader()
	if err != nil {
		writeProblem(w, r, http.StatusUnsupportedMediaType, "not-multipart", "send multipart/form-data with an 'image' file part")
		return
	}

	var input *pipeline.Input
	var form analyzeForm
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			s.uploadError(w, r, err)
			return
		}
		name := part.FormName()
		switch {
		case name == "image":
			if input != nil {
				writeProblem(w, r, http.StatusBadRequest, "multiple-images", "send exactly one image (multi-view arrives in a later version)")
				return
			}
			body, ext, err := peekImage(part)
			if err != nil {
				s.uploadError(w, r, err)
				return
			}
			sha, path, err := s.cfg.Blobs.PutBlob(body, s.cfg.MaxUploadBytes, ext)
			if err != nil {
				s.uploadError(w, r, err)
				return
			}
			input = &pipeline.Input{SHA256: sha, Path: path, Filename: sanitizeFilename(part.FileName())}
		case formFields[name]:
			value, err := readField(part)
			if err != nil {
				s.uploadError(w, r, err)
				return
			}
			form.set(name, value)
		default:
			writeProblem(w, r, http.StatusBadRequest, "unknown-field", fmt.Sprintf("unknown form field %q", name))
			return
		}
	}
	if input == nil {
		writeProblem(w, r, http.StatusBadRequest, "missing-image", "multipart body has no 'image' part")
		return
	}

	opts, status, slug, err := s.options(form)
	if err != nil {
		writeProblem(w, r, status, slug, err.Error())
		return
	}
	job, err := s.cfg.Queue.Submit(*input, opts)
	if err != nil {
		writeProblem(w, r, http.StatusServiceUnavailable, "shutting-down", err.Error())
		return
	}
	if wait > 0 {
		ctx, cancel := context.WithTimeout(r.Context(), wait)
		job, _ = s.cfg.Status.Wait(ctx, job.ID)
		cancel()
	}
	w.Header().Set("Location", "/v1/jobs/"+job.ID)
	code := http.StatusAccepted
	if job.Status.Terminal() {
		code = http.StatusOK
	}
	writeJSON(w, code, viewJob(job))
}

func (f *analyzeForm) set(name, value string) {
	switch name {
	case "preset":
		f.preset = value
	case "license_policy":
		f.policy = value
	case "allow_noncommercial":
		f.allowNonCommercial = parseBool(value)
	case "geometry_model":
		f.geometryModel = value
	case "disable_exif_prior":
		f.disableEXIFPrior = parseBool(value)
	}
}

// options validates the form synchronously so license and preset errors
// surface as HTTP errors instead of failed jobs.
func (s *Server) options(form analyzeForm) (pipeline.Options, int, string, error) {
	preset, err := pipeline.ParsePreset(form.preset)
	if err != nil {
		return pipeline.Options{}, http.StatusBadRequest, "bad-preset", err
	}
	policy, err := models.ParsePolicy(form.policy)
	if err != nil {
		return pipeline.Options{}, http.StatusBadRequest, "bad-policy", err
	}
	if form.allowNonCommercial {
		policy = models.PolicyAny
	}
	opts := pipeline.Options{Preset: preset, Policy: policy, DisableEXIFPrior: form.disableEXIFPrior}
	if form.geometryModel != "" {
		if _, err := s.cfg.Registry.Resolve(form.geometryModel, policy); err != nil {
			status, slug := http.StatusUnprocessableEntity, "model-unavailable"
			if errors.Is(err, models.ErrLicenseNotAllowed) {
				status, slug = http.StatusForbidden, "license-not-allowed"
			}
			return pipeline.Options{}, status, slug, err
		}
		opts.ModelOverrides = map[string]string{"geometry": form.geometryModel}
	}
	return opts, 0, "", nil
}

func (s *Server) uploadError(w http.ResponseWriter, r *http.Request, err error) {
	var maxErr *http.MaxBytesError
	switch {
	case errors.As(err, &maxErr), errors.Is(err, cache.ErrTooLarge):
		writeProblem(w, r, http.StatusRequestEntityTooLarge, "too-large",
			fmt.Sprintf("upload exceeds %d bytes", s.cfg.MaxUploadBytes))
	case errors.Is(err, errUnsupportedImage):
		writeProblem(w, r, http.StatusUnsupportedMediaType, "unsupported-image", err.Error())
	case errors.Is(err, errFieldTooLong):
		writeProblem(w, r, http.StatusBadRequest, "field-too-long", err.Error())
	default:
		s.cfg.Logger.Error("upload failed", "err", err)
		writeProblem(w, r, http.StatusBadRequest, "bad-upload", "could not read the upload")
	}
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.cfg.Status.Get(r.PathValue("id"))
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "job-not-found", "no such job")
		return
	}
	writeJSON(w, http.StatusOK, viewJob(job))
}

func (s *Server) handleCancelJob(w http.ResponseWriter, r *http.Request) {
	job, ok := s.cfg.Queue.Cancel(r.PathValue("id"))
	if !ok {
		writeProblem(w, r, http.StatusNotFound, "job-not-found", "no such job")
		return
	}
	writeJSON(w, http.StatusAccepted, viewJob(job))
}

func (s *Server) handleGetPack(w http.ResponseWriter, r *http.Request) {
	_, manifest, err := s.cfg.Packs.OpenPack(r.PathValue("id"))
	if err != nil {
		s.packError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, manifest)
}

func (s *Server) handleArtifact(w http.ResponseWriter, r *http.Request) {
	dir, manifest, err := s.cfg.Packs.OpenPack(r.PathValue("id"))
	if err != nil {
		s.packError(w, r, err)
		return
	}
	rel := r.PathValue("path")
	mediaType, etag := "", ""
	if rel == scp.ManifestFile {
		mediaType = "application/json"
	} else {
		for _, a := range manifest.Artifacts {
			if a.Path == rel {
				mediaType, etag = a.MediaType, `"`+a.SHA256+`"`
				break
			}
		}
	}
	if mediaType == "" {
		writeProblem(w, r, http.StatusNotFound, "artifact-not-found", "no such artifact in this pack")
		return
	}
	// os.Root confines the open to the pack directory even if a path slipped through.
	root, err := os.OpenRoot(dir)
	if err != nil {
		s.packError(w, r, err)
		return
	}
	defer root.Close()
	f, err := root.Open(filepath.FromSlash(rel))
	if err != nil {
		writeProblem(w, r, http.StatusNotFound, "artifact-not-found", "artifact missing on disk")
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		s.packError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", mediaType)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	if etag != "" {
		w.Header().Set("ETag", etag)
	}
	http.ServeContent(w, r, filepath.Base(rel), info.ModTime(), f)
}

func (s *Server) packError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, pipeline.ErrPackNotFound) {
		writeProblem(w, r, http.StatusNotFound, "pack-not-found", "no such pack")
		return
	}
	s.cfg.Logger.Error("pack read failed", "err", err)
	writeProblem(w, r, http.StatusInternalServerError, "internal", "failed to read pack")
}

func parseWait(v string) (time.Duration, error) {
	if v == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		secs, convErr := strconv.Atoi(v)
		if convErr != nil {
			return 0, fmt.Errorf("wait must be a duration like 30s: %w", err)
		}
		d = time.Duration(secs) * time.Second
	}
	if d < 0 || d > maxWait {
		return 0, fmt.Errorf("wait must be between 0 and %s", maxWait)
	}
	return d, nil
}

var errFieldTooLong = errors.New("form field too long")

func readField(r io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(r, 257))
	if err != nil {
		return "", fmt.Errorf("failed to read form field: %w", err)
	}
	if len(data) > 256 {
		return "", errFieldTooLong
	}
	return strings.TrimSpace(string(data)), nil
}

func parseBool(v string) bool {
	b, err := strconv.ParseBool(v)
	return err == nil && b
}

func sanitizeFilename(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" {
		return ""
	}
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, name)
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}
