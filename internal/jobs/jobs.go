// Package jobs runs analyses asynchronously with bounded concurrency.
package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	"github.com/shaansundar/doppio/internal/pipeline"
)

// Status is a job's lifecycle state.
type Status string

const (
	StatusQueued    Status = "queued"
	StatusRunning   Status = "running"
	StatusSucceeded Status = "succeeded"
	StatusFailed    Status = "failed"
	StatusCanceled  Status = "canceled"
)

// Terminal reports whether the status is final.
func (s Status) Terminal() bool {
	return s == StatusSucceeded || s == StatusFailed || s == StatusCanceled
}

// ErrorKind classifies failures for API clients.
type ErrorKind string

const (
	ErrorInvalidInput ErrorKind = "invalid_input"
	ErrorInternal     ErrorKind = "internal"
	ErrorCanceled     ErrorKind = "canceled"
)

// JobError describes why a job did not succeed.
type JobError struct {
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
}

// Job is an immutable snapshot of a job's state.
type Job struct {
	ID          string            `json:"id"`
	Status      Status            `json:"status"`
	Preset      string            `json:"preset"`
	InputSHA256 string            `json:"input_sha256"`
	Filename    string            `json:"filename,omitempty"`
	Progress    pipeline.Progress `json:"progress"`
	PackID      string            `json:"pack_id,omitempty"`
	Error       *JobError         `json:"error,omitempty"`
	CreatedAt   time.Time         `json:"created_at"`
	StartedAt   *time.Time        `json:"started_at,omitempty"`
	FinishedAt  *time.Time        `json:"finished_at,omitempty"`
}

// Runner executes one analysis; *pipeline.Engine satisfies it.
type Runner interface {
	Run(ctx context.Context, in pipeline.Input, opts pipeline.Options, progress func(pipeline.Progress)) (pipeline.Result, error)
}

// Manager queues and runs jobs. Safe for concurrent use.
type Manager struct {
	runner    Runner
	sem       chan struct{}
	retention time.Duration
	now       func() time.Time

	baseCtx  context.Context
	stopAll  context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.Mutex
	jobs     map[string]*entry
	shutdown bool
}

type entry struct {
	job    Job
	cancel context.CancelFunc
	done   chan struct{}
}

// Option configures a Manager.
type Option func(*Manager)

// WithConcurrency bounds simultaneously running jobs (default 1: one GPU).
func WithConcurrency(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.sem = make(chan struct{}, n)
		}
	}
}

// WithRetention controls how long finished jobs stay queryable (default 1h).
func WithRetention(d time.Duration) Option { return func(m *Manager) { m.retention = d } }

// WithClock overrides the time source (tests).
func WithClock(now func() time.Time) Option { return func(m *Manager) { m.now = now } }

// NewManager returns a running manager.
func NewManager(runner Runner, opts ...Option) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		runner: runner, sem: make(chan struct{}, 1), retention: time.Hour, now: time.Now,
		baseCtx: ctx, stopAll: cancel, jobs: map[string]*entry{},
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// ErrShuttingDown is returned by Submit after Shutdown.
var ErrShuttingDown = errors.New("job manager is shutting down")

// Submit enqueues an analysis and returns its initial snapshot.
func (m *Manager) Submit(in pipeline.Input, opts pipeline.Options) (Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.shutdown {
		return Job{}, ErrShuttingDown
	}
	m.pruneLocked()
	ctx, cancel := context.WithCancel(m.baseCtx)
	e := &entry{
		job: Job{
			ID: newID(), Status: StatusQueued, Preset: string(opts.Preset), InputSHA256: in.SHA256,
			Filename: in.Filename, CreatedAt: m.now().UTC(),
		},
		cancel: cancel, done: make(chan struct{}),
	}
	m.jobs[e.job.ID] = e
	m.wg.Add(1)
	go m.run(ctx, e, in, opts)
	return e.job, nil
}

func (m *Manager) run(ctx context.Context, e *entry, in pipeline.Input, opts pipeline.Options) {
	defer m.wg.Done()
	defer close(e.done)
	defer e.cancel()

	select {
	case m.sem <- struct{}{}:
		defer func() { <-m.sem }()
	case <-ctx.Done():
		m.finish(e, "", ctx.Err())
		return
	}
	m.update(e, func(j *Job) {
		j.Status = StatusRunning
		t := m.now().UTC()
		j.StartedAt = &t
	})
	res, err := m.runner.Run(ctx, in, opts, func(p pipeline.Progress) {
		m.update(e, func(j *Job) { j.Progress = p })
	})
	m.finish(e, res.PackID, err)
}

func (m *Manager) finish(e *entry, packID string, err error) {
	m.update(e, func(j *Job) {
		t := m.now().UTC()
		j.FinishedAt = &t
		switch {
		case err == nil:
			j.Status, j.PackID = StatusSucceeded, packID
		case errors.Is(err, context.Canceled):
			j.Status = StatusCanceled
			j.Error = &JobError{Kind: ErrorCanceled, Message: "job canceled"}
		case pipeline.IsInvalidInput(err):
			j.Status = StatusFailed
			j.Error = &JobError{Kind: ErrorInvalidInput, Message: err.Error()}
		default:
			j.Status = StatusFailed
			j.Error = &JobError{Kind: ErrorInternal, Message: err.Error()}
		}
	})
}

func (m *Manager) update(e *entry, fn func(*Job)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(&e.job)
}

// Get returns a job snapshot.
func (m *Manager) Get(id string) (Job, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.jobs[id]
	if !ok {
		return Job{}, false
	}
	return e.job, true
}

// Wait blocks until the job finishes or ctx is done, then returns the latest snapshot.
func (m *Manager) Wait(ctx context.Context, id string) (Job, bool) {
	m.mu.Lock()
	e, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Job{}, false
	}
	select {
	case <-e.done:
	case <-ctx.Done():
	}
	return m.Get(id)
}

// Cancel requests cancellation; the returned snapshot may not be terminal yet.
func (m *Manager) Cancel(id string) (Job, bool) {
	m.mu.Lock()
	e, ok := m.jobs[id]
	m.mu.Unlock()
	if !ok {
		return Job{}, false
	}
	e.cancel()
	return m.Get(id)
}

// Shutdown cancels all jobs and waits for them, bounded by ctx.
func (m *Manager) Shutdown(ctx context.Context) error {
	m.mu.Lock()
	m.shutdown = true
	m.mu.Unlock()
	m.stopAll()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (m *Manager) pruneLocked() {
	cutoff := m.now().Add(-m.retention)
	for id, e := range m.jobs {
		if e.job.Status.Terminal() && e.job.FinishedAt != nil && e.job.FinishedAt.Before(cutoff) {
			delete(m.jobs, id)
		}
	}
}

func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("crypto/rand failed: " + err.Error())
	}
	return "job_" + hex.EncodeToString(b[:])
}
