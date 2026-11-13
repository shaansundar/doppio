// Package client is a Go client for the doppio REST API.
package client

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/shaansundar/doppio/internal/api"
	"github.com/shaansundar/doppio/internal/jobs"
	"github.com/shaansundar/doppio/internal/scp"
)

// Client talks to a local doppio daemon.
type Client struct {
	base  string
	token string
	http  *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient overrides the HTTP client (tests).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// New returns a client for addr (host:port) authenticating with token.
func New(addr, token string, opts ...Option) *Client {
	c := &Client{base: "http://" + addr, token: token, http: &http.Client{}}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// APIError is a non-2xx response carrying RFC 9457 problem details.
type APIError struct {
	StatusCode int
	Problem    api.Problem
}

func (e *APIError) Error() string {
	if e.Problem.Detail != "" {
		return fmt.Sprintf("doppio API %d: %s", e.StatusCode, e.Problem.Detail)
	}
	return fmt.Sprintf("doppio API %d: %s", e.StatusCode, http.StatusText(e.StatusCode))
}

// Job is a job snapshot plus hypermedia links.
type Job struct {
	jobs.Job
	Links map[string]string `json:"links"`
}

// AnalyzeRequest holds optional analysis settings.
type AnalyzeRequest struct {
	Preset             string
	AllowNonCommercial bool
	GeometryModel      string
	DisableEXIFPrior   bool
	// Wait asks the server to hold the response until the job finishes (max 10m).
	Wait time.Duration
}

func (c *Client) newRequest(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	return req, nil
}

func (c *Client) do(req *http.Request, out any) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach doppio daemon: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&apiErr.Problem)
		return apiErr
	}
	if out == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("failed to decode response: %w", err)
	}
	return nil
}

// Health checks that the daemon is up (no auth needed).
func (c *Client) Health(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/healthz", nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// Shutdown asks the daemon to exit gracefully.
func (c *Client) Shutdown(ctx context.Context) error {
	req, err := c.newRequest(ctx, http.MethodPost, "/v1/admin/shutdown", nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// Info returns daemon and worker information.
func (c *Client) Info(ctx context.Context) (map[string]any, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/info", nil)
	if err != nil {
		return nil, err
	}
	var info map[string]any
	return info, c.do(req, &info)
}

// Analyze uploads imagePath and starts an analysis. The body is streamed.
func (c *Client) Analyze(ctx context.Context, imagePath string, ar AnalyzeRequest) (Job, error) {
	f, err := os.Open(imagePath)
	if err != nil {
		return Job{}, fmt.Errorf("failed to open image: %w", err)
	}
	defer f.Close()

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := writeAnalyzeForm(mw, f, filepath.Base(imagePath), ar)
		if err == nil {
			err = mw.Close()
		}
		pw.CloseWithError(err)
	}()

	path := "/v1/analyze"
	if ar.Wait > 0 {
		path += "?wait=" + url.QueryEscape(ar.Wait.String())
	}
	req, err := c.newRequest(ctx, http.MethodPost, path, pr)
	if err != nil {
		return Job{}, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	var job Job
	return job, c.do(req, &job)
}

func writeAnalyzeForm(mw *multipart.Writer, image io.Reader, name string, ar AnalyzeRequest) error {
	fields := map[string]string{}
	if ar.Preset != "" {
		fields["preset"] = ar.Preset
	}
	if ar.AllowNonCommercial {
		fields["allow_noncommercial"] = "true"
	}
	if ar.GeometryModel != "" {
		fields["geometry_model"] = ar.GeometryModel
	}
	if ar.DisableEXIFPrior {
		fields["disable_exif_prior"] = strconv.FormatBool(true)
	}
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			return fmt.Errorf("failed to write form field: %w", err)
		}
	}
	part, err := mw.CreateFormFile("image", name)
	if err != nil {
		return fmt.Errorf("failed to create image part: %w", err)
	}
	if _, err := io.Copy(part, image); err != nil {
		return fmt.Errorf("failed to stream image: %w", err)
	}
	return nil
}

// Job fetches a job snapshot.
func (c *Client) Job(ctx context.Context, id string) (Job, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/jobs/"+url.PathEscape(id), nil)
	if err != nil {
		return Job{}, err
	}
	var job Job
	return job, c.do(req, &job)
}

// CancelJob requests cancellation.
func (c *Client) CancelJob(ctx context.Context, id string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, "/v1/jobs/"+url.PathEscape(id), nil)
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

// WaitJob polls until the job is terminal, calling onUpdate on each snapshot.
func (c *Client) WaitJob(ctx context.Context, id string, every time.Duration, onUpdate func(Job)) (Job, error) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		job, err := c.Job(ctx, id)
		if err != nil {
			return Job{}, err
		}
		if onUpdate != nil {
			onUpdate(job)
		}
		if job.Status.Terminal() {
			return job, nil
		}
		select {
		case <-ctx.Done():
			return job, ctx.Err()
		case <-ticker.C:
		}
	}
}

// Manifest fetches a pack's manifest.
func (c *Client) Manifest(ctx context.Context, packID string) (scp.Manifest, error) {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/packs/"+url.PathEscape(packID), nil)
	if err != nil {
		return scp.Manifest{}, err
	}
	var m scp.Manifest
	return m, c.do(req, &m)
}

// ErrDigestMismatch means a downloaded artifact does not match its manifest digest.
var ErrDigestMismatch = errors.New("artifact digest mismatch")

// DownloadPack writes the manifest and every artifact into outDir,
// verifying each artifact's SHA-256 against the manifest.
func (c *Client) DownloadPack(ctx context.Context, packID, outDir string) (scp.Manifest, error) {
	m, err := c.Manifest(ctx, packID)
	if err != nil {
		return scp.Manifest{}, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return scp.Manifest{}, fmt.Errorf("failed to create output directory: %w", err)
	}
	for _, a := range m.Artifacts {
		if !filepath.IsLocal(filepath.FromSlash(a.Path)) {
			return scp.Manifest{}, fmt.Errorf("server listed unsafe artifact path %q", a.Path)
		}
		if err := c.downloadArtifact(ctx, packID, a, filepath.Join(outDir, filepath.FromSlash(a.Path))); err != nil {
			return scp.Manifest{}, err
		}
	}
	if err := scp.WriteJSON(outDir, scp.ManifestFile, m); err != nil {
		return scp.Manifest{}, err
	}
	return m, nil
}

func (c *Client) downloadArtifact(ctx context.Context, packID string, a scp.Artifact, dst string) error {
	req, err := c.newRequest(ctx, http.MethodGet, "/v1/packs/"+url.PathEscape(packID)+"/artifacts/"+a.Path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("failed to download %s: %w", a.Path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&apiErr.Problem)
		return apiErr
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("failed to create directory for %s: %w", a.Path, err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), resp.Body)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("failed to write %s: %w", a.Path, err)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("%w: %s", ErrDigestMismatch, a.Path)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		return fmt.Errorf("failed to place %s: %w", a.Path, err)
	}
	return nil
}
