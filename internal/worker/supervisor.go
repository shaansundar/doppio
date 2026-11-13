// Package worker supervises the Python inference worker process and exposes
// it as a pipeline.StageRunner over gRPC on a Unix socket.
package worker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	workerv1 "github.com/shaansundar/doppio/gen/go/doppio/worker/v1"
	"github.com/shaansundar/doppio/internal/pipeline"
)

// ErrUnavailable is returned when the worker cannot be started or reached.
var ErrUnavailable = errors.New("inference worker unavailable")

// ErrDependencyMissing is returned when a stage's optional backend is not installed.
var ErrDependencyMissing = errors.New("worker dependency missing")

// Config locates and tunes the worker process.
type Config struct {
	// Dir is the worker project directory (contains pyproject.toml and .venv).
	Dir string
	// Command overrides the worker executable (default Dir/.venv/bin/doppio-worker).
	Command string
	// SocketPath is the Unix socket the worker listens on.
	SocketPath    string
	MaxResidentGB float64
	// Device forces "cuda", "mps" or "cpu"; empty auto-selects.
	Device       string
	StartTimeout time.Duration
	// Log receives the worker's stderr.
	Log io.Writer
}

// Supervisor owns the worker process lifecycle. Safe for concurrent use.
type Supervisor struct {
	cfg Config

	mu     sync.Mutex
	proc   *process
	conn   *grpc.ClientConn
	client workerv1.WorkerServiceClient
	health *workerv1.HealthResponse
}

type process struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	exited chan struct{}
	tail   *ringBuffer
	err    error
}

// DefaultSocketPath is a short, per-user path (AF_UNIX paths are limited to ~104 bytes on macOS).
func DefaultSocketPath() string {
	return filepath.Join(os.TempDir(), "doppio-"+strconv.Itoa(os.Getuid()), "worker.sock")
}

// DefaultCommand returns the worker entrypoint inside the project venv.
func DefaultCommand(dir string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(dir, ".venv", "Scripts", "doppio-worker.exe")
	}
	return filepath.Join(dir, ".venv", "bin", "doppio-worker")
}

// New validates cfg and returns an unstarted supervisor.
func New(cfg Config) (*Supervisor, error) {
	if cfg.Command == "" {
		if cfg.Dir == "" {
			return nil, errors.New("worker directory or command is required")
		}
		cfg.Command = DefaultCommand(cfg.Dir)
	}
	if cfg.SocketPath == "" {
		cfg.SocketPath = DefaultSocketPath()
	}
	if cfg.MaxResidentGB <= 0 {
		cfg.MaxResidentGB = 6
	}
	if cfg.StartTimeout <= 0 {
		cfg.StartTimeout = 90 * time.Second
	}
	if cfg.Log == nil {
		cfg.Log = io.Discard
	}
	return &Supervisor{cfg: cfg}, nil
}

// Start launches the worker if it is not running and waits until healthy.
func (s *Supervisor) Start(ctx context.Context) error {
	_, err := s.ensure(ctx)
	return err
}

// Health returns the worker's last health response, starting it if needed.
func (s *Supervisor) Health(ctx context.Context) (*workerv1.HealthResponse, error) {
	client, err := s.ensure(ctx)
	if err != nil {
		return nil, err
	}
	resp, err := client.Health(ctx, &workerv1.HealthRequest{})
	if err != nil {
		return nil, fmt.Errorf("%w: health check failed: %w", ErrUnavailable, err)
	}
	return resp, nil
}

func (s *Supervisor) ensure(ctx context.Context) (workerv1.WorkerServiceClient, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.proc != nil {
		select {
		case <-s.proc.exited:
			s.teardownLocked()
		default:
			return s.client, nil
		}
	}
	if err := s.spawnLocked(ctx); err != nil {
		s.teardownLocked()
		return nil, err
	}
	return s.client, nil
}

func (s *Supervisor) spawnLocked(ctx context.Context) error {
	if _, err := os.Stat(s.cfg.Command); err != nil {
		return fmt.Errorf("%w: worker executable %s not found; run `doppio setup`", ErrUnavailable, s.cfg.Command)
	}
	sockDir := filepath.Dir(s.cfg.SocketPath)
	if err := os.MkdirAll(sockDir, 0o700); err != nil {
		return fmt.Errorf("failed to create socket directory: %w", err)
	}
	if err := os.Chmod(sockDir, 0o700); err != nil {
		return fmt.Errorf("failed to secure socket directory: %w", err)
	}

	args := []string{
		"--socket", s.cfg.SocketPath,
		"--max-resident-gb", strconv.FormatFloat(s.cfg.MaxResidentGB, 'f', -1, 64),
		"--exit-on-stdin-eof",
	}
	if s.cfg.Device != "" {
		args = append(args, "--device", s.cfg.Device)
	}
	cmd := exec.Command(s.cfg.Command, args...)
	cmd.Dir = s.cfg.Dir
	cmd.Env = append(os.Environ(), "PYTHONUNBUFFERED=1")
	configureProcess(cmd)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to open worker stdin: %w", err)
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to open worker stderr: %w", err)
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("%w: failed to start worker: %w", ErrUnavailable, err)
	}
	p := &process{cmd: cmd, stdin: stdin, exited: make(chan struct{}), tail: newRingBuffer(40)}
	go p.pump(stderr, s.cfg.Log)
	go func() {
		p.err = cmd.Wait()
		close(p.exited)
	}()
	s.proc = p

	conn, err := grpc.NewClient("passthrough:///doppio-worker",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", s.cfg.SocketPath)
		}),
	)
	if err != nil {
		return fmt.Errorf("failed to create worker client: %w", err)
	}
	s.conn = conn
	s.client = workerv1.NewWorkerServiceClient(conn)

	deadline := time.Now().Add(s.cfg.StartTimeout)
	for {
		hctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		resp, err := s.client.Health(hctx, &workerv1.HealthRequest{})
		cancel()
		if err == nil {
			s.health = resp
			return nil
		}
		select {
		case <-p.exited:
			return fmt.Errorf("%w: worker exited during startup: %v\n%s", ErrUnavailable, p.err, p.tail.String())
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: worker not healthy after %s\n%s", ErrUnavailable, s.cfg.StartTimeout, p.tail.String())
		}
	}
}

func (s *Supervisor) teardownLocked() {
	if s.conn != nil {
		_ = s.conn.Close()
	}
	if s.proc != nil {
		_ = s.proc.stdin.Close()
		select {
		case <-s.proc.exited:
		case <-time.After(5 * time.Second):
			_ = s.proc.cmd.Process.Kill()
			<-s.proc.exited
		}
	}
	s.proc, s.conn, s.client, s.health = nil, nil, nil, nil
}

// Close stops the worker: closing stdin asks it to exit; it is killed after 5 s.
func (s *Supervisor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.teardownLocked()
	return nil
}

// RunStage implements pipeline.StageRunner.
func (s *Supervisor) RunStage(ctx context.Context, req pipeline.StageRequest, progress func(float64, string)) (pipeline.StageResult, error) {
	client, err := s.ensure(ctx)
	if err != nil {
		return pipeline.StageResult{}, err
	}
	return runStage(ctx, client, req, progress)
}

func runStage(ctx context.Context, client workerv1.WorkerServiceClient, req pipeline.StageRequest, progress func(float64, string)) (pipeline.StageResult, error) {
	params, err := json.Marshal(req.Params)
	if err != nil {
		return pipeline.StageResult{}, fmt.Errorf("failed to encode stage params: %w", err)
	}
	pbReq := &workerv1.RunStageRequest{
		Stage: req.Stage, Inputs: req.Inputs, ParamsJson: string(params), OutDir: req.OutDir,
	}
	if m := req.Model; m != nil {
		pbReq.Model = &workerv1.ModelSpec{
			Id: m.ID, Family: m.Family, HfRepo: m.HFRepo, Revision: m.Revision, Filename: m.Filename, MemoryGb: m.MemoryGB,
		}
	}
	stream, err := client.RunStage(ctx, pbReq)
	if err != nil {
		return pipeline.StageResult{}, mapError(ctx, err)
	}
	for {
		resp, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return pipeline.StageResult{}, fmt.Errorf("worker closed stage %s without a result", req.Stage)
		}
		if err != nil {
			return pipeline.StageResult{}, mapError(ctx, err)
		}
		switch ev := resp.GetEvent().(type) {
		case *workerv1.RunStageResponse_Progress:
			if progress != nil {
				progress(float64(ev.Progress.GetFraction()), ev.Progress.GetMessage())
			}
		case *workerv1.RunStageResponse_Result:
			return convertResult(ev.Result)
		}
	}
}

func convertResult(r *workerv1.StageResult) (pipeline.StageResult, error) {
	out := pipeline.StageResult{ElapsedSeconds: r.GetElapsedSeconds()}
	meta := r.GetMetadataJson()
	if meta == "" {
		meta = "{}"
	}
	if !json.Valid([]byte(meta)) {
		return out, errors.New("worker returned invalid metadata JSON")
	}
	out.Metadata = json.RawMessage(meta)
	for _, a := range r.GetArtifacts() {
		out.Artifacts = append(out.Artifacts, pipeline.ArtifactRef{
			Name: a.GetName(), RelPath: a.GetRelPath(), MediaType: a.GetMediaType(), Shape: a.GetShape(), DType: a.GetDtype(),
		})
	}
	return out, nil
}

func mapError(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr
	}
	st, ok := status.FromError(err)
	if !ok {
		return fmt.Errorf("worker call failed: %w", err)
	}
	switch st.Code() {
	case codes.InvalidArgument:
		return fmt.Errorf("%w: %s", pipeline.ErrInvalidInput, st.Message())
	case codes.Canceled:
		return context.Canceled
	case codes.DeadlineExceeded:
		return context.DeadlineExceeded
	case codes.Unavailable:
		return fmt.Errorf("%w: %s", ErrUnavailable, st.Message())
	case codes.FailedPrecondition:
		return fmt.Errorf("%w: %s", ErrDependencyMissing, st.Message())
	default:
		return fmt.Errorf("worker error (%s): %s", st.Code(), st.Message())
	}
}

func (p *process) pump(r io.Reader, log io.Writer) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := sc.Text()
		p.tail.Add(line)
		fmt.Fprintln(log, "[worker] "+line)
	}
}

// ringBuffer keeps the last n lines of worker stderr for error reports.
type ringBuffer struct {
	mu    sync.Mutex
	lines []string
	n     int
}

func newRingBuffer(n int) *ringBuffer { return &ringBuffer{n: n} }

func (b *ringBuffer) Add(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lines = append(b.lines, line)
	if len(b.lines) > b.n {
		b.lines = b.lines[len(b.lines)-b.n:]
	}
}

func (b *ringBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}
