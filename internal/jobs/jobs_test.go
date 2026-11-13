package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shaansundar/doppio/internal/pipeline"
)

type fakeRunner struct {
	delay   time.Duration
	err     error
	running atomic.Int32
	peak    atomic.Int32
}

func (f *fakeRunner) Run(ctx context.Context, in pipeline.Input, _ pipeline.Options, progress func(pipeline.Progress)) (pipeline.Result, error) {
	n := f.running.Add(1)
	defer f.running.Add(-1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	progress(pipeline.Progress{Stage: "geometry", Fraction: 0.5})
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return pipeline.Result{}, ctx.Err()
	}
	if f.err != nil {
		return pipeline.Result{}, f.err
	}
	return pipeline.Result{PackID: "pack-" + in.SHA256}, nil
}

func waitTerminal(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	j, ok := m.Wait(ctx, id)
	if !ok || !j.Status.Terminal() {
		t.Fatalf("job %s not terminal: %+v", id, j)
	}
	return j
}

func TestJobSucceeds(t *testing.T) {
	m := NewManager(&fakeRunner{delay: 10 * time.Millisecond})
	j, err := m.Submit(pipeline.Input{SHA256: "abc", Filename: "a.jpg"}, pipeline.Options{Preset: pipeline.PresetFast})
	if err != nil {
		t.Fatal(err)
	}
	if j.Status != StatusQueued || !strings.HasPrefix(j.ID, "job_") || j.Preset != "fast" {
		t.Fatalf("initial job = %+v", j)
	}
	done := waitTerminal(t, m, j.ID)
	if done.Status != StatusSucceeded || done.PackID != "pack-abc" || done.StartedAt == nil || done.FinishedAt == nil {
		t.Fatalf("finished job = %+v", done)
	}
	if done.Progress.Stage != "geometry" {
		t.Errorf("progress not recorded: %+v", done.Progress)
	}
}

func TestErrorClassification(t *testing.T) {
	tests := []struct {
		err  error
		kind ErrorKind
	}{
		{fmt.Errorf("%w: bad image", pipeline.ErrInvalidInput), ErrorInvalidInput},
		{errors.New("worker crashed"), ErrorInternal},
	}
	for _, tt := range tests {
		m := NewManager(&fakeRunner{err: tt.err})
		j, _ := m.Submit(pipeline.Input{SHA256: "x"}, pipeline.Options{})
		done := waitTerminal(t, m, j.ID)
		if done.Status != StatusFailed || done.Error == nil || done.Error.Kind != tt.kind {
			t.Errorf("err %v -> job %+v", tt.err, done)
		}
	}
}

func TestConcurrencyIsBounded(t *testing.T) {
	r := &fakeRunner{delay: 30 * time.Millisecond}
	m := NewManager(r, WithConcurrency(2))
	var ids []string
	for i := 0; i < 6; i++ {
		j, _ := m.Submit(pipeline.Input{SHA256: fmt.Sprint(i)}, pipeline.Options{})
		ids = append(ids, j.ID)
	}
	for _, id := range ids {
		waitTerminal(t, m, id)
	}
	if p := r.peak.Load(); p != 2 {
		t.Fatalf("peak concurrency = %d, want 2", p)
	}
}

func TestCancelRunningAndQueuedJobs(t *testing.T) {
	m := NewManager(&fakeRunner{delay: time.Minute})
	running, _ := m.Submit(pipeline.Input{SHA256: "1"}, pipeline.Options{})
	queued, _ := m.Submit(pipeline.Input{SHA256: "2"}, pipeline.Options{})
	time.Sleep(20 * time.Millisecond)
	for _, id := range []string{queued.ID, running.ID} {
		if _, ok := m.Cancel(id); !ok {
			t.Fatalf("Cancel(%s) not found", id)
		}
		if j := waitTerminal(t, m, id); j.Status != StatusCanceled || j.Error.Kind != ErrorCanceled {
			t.Errorf("job %s = %+v", id, j)
		}
	}
	if _, ok := m.Cancel("job_missing"); ok {
		t.Error("Cancel of unknown job reported found")
	}
}

func TestWaitHonorsContext(t *testing.T) {
	m := NewManager(&fakeRunner{delay: time.Minute})
	j, _ := m.Submit(pipeline.Input{SHA256: "1"}, pipeline.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, ok := m.Wait(ctx, j.ID)
	if !ok || got.Status.Terminal() {
		t.Fatalf("Wait = %+v, %v; want non-terminal snapshot", got, ok)
	}
	if _, ok := m.Wait(ctx, "job_missing"); ok {
		t.Error("Wait of unknown job reported found")
	}
	_ = m.Shutdown(context.Background())
}

func TestShutdownCancelsAndRejects(t *testing.T) {
	m := NewManager(&fakeRunner{delay: time.Minute})
	j, _ := m.Submit(pipeline.Input{SHA256: "1"}, pipeline.Options{})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.Get(j.ID); got.Status != StatusCanceled {
		t.Errorf("job after shutdown = %+v", got)
	}
	if _, err := m.Submit(pipeline.Input{}, pipeline.Options{}); !errors.Is(err, ErrShuttingDown) {
		t.Errorf("Submit after shutdown err = %v", err)
	}
}

func TestRetentionPrunesFinishedJobs(t *testing.T) {
	var mu sync.Mutex
	now := time.Unix(1700000000, 0)
	clock := func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		return now
	}
	m := NewManager(&fakeRunner{}, WithRetention(time.Minute), WithClock(clock))
	old, _ := m.Submit(pipeline.Input{SHA256: "old"}, pipeline.Options{})
	waitTerminal(t, m, old.ID)
	mu.Lock()
	now = now.Add(2 * time.Minute)
	mu.Unlock()
	if _, err := m.Submit(pipeline.Input{SHA256: "new"}, pipeline.Options{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Get(old.ID); ok {
		t.Error("expired job was not pruned")
	}
}
