package worker

import (
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	workerv1 "github.com/shaansundar/doppio/gen/go/doppio/worker/v1"
	"github.com/shaansundar/doppio/internal/models"
	"github.com/shaansundar/doppio/internal/pipeline"
)

type fakeServer struct {
	workerv1.UnimplementedWorkerServiceServer
	lastReq  *workerv1.RunStageRequest
	err      error
	noResult bool
}

func (f *fakeServer) RunStage(req *workerv1.RunStageRequest, stream grpc.ServerStreamingServer[workerv1.RunStageResponse]) error {
	f.lastReq = req
	if f.err != nil {
		return f.err
	}
	_ = stream.Send(&workerv1.RunStageResponse{Event: &workerv1.RunStageResponse_Progress{
		Progress: &workerv1.StageProgress{Fraction: 0.5, Message: "half"}}})
	if f.noResult {
		return nil
	}
	return stream.Send(&workerv1.RunStageResponse{Event: &workerv1.RunStageResponse_Result{Result: &workerv1.StageResult{
		Artifacts:      []*workerv1.Artifact{{Name: "image", RelPath: "image.png", MediaType: "image/png", Shape: []int64{2, 3, 3}, Dtype: "uint8"}},
		MetadataJson:   `{"width":3}`,
		ElapsedSeconds: 1.5,
	}}})
}

func dialFake(t *testing.T, srv *fakeServer) workerv1.WorkerServiceClient {
	t.Helper()
	lis := bufconn.Listen(1 << 20)
	gs := grpc.NewServer()
	workerv1.RegisterWorkerServiceServer(gs, srv)
	go func() { _ = gs.Serve(lis) }()
	t.Cleanup(gs.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	return workerv1.NewWorkerServiceClient(conn)
}

func TestRunStageConvertsRequestAndResult(t *testing.T) {
	srv := &fakeServer{}
	client := dialFake(t, srv)
	model := &models.Model{ID: "m", Family: "moge2", HFRepo: "r/m", Revision: "abc", MemoryGB: 0.5}
	var fractions []float64
	res, err := runStage(context.Background(), client, pipeline.StageRequest{
		Stage: "geometry", Inputs: map[string]string{"image": "/x/image.png"},
		Params: map[string]any{"resolution_level": 5}, Model: model, OutDir: "/tmp/out",
	}, func(f float64, _ string) { fractions = append(fractions, f) })
	if err != nil {
		t.Fatal(err)
	}
	if srv.lastReq.GetModel().GetHfRepo() != "r/m" || srv.lastReq.GetParamsJson() != `{"resolution_level":5}` {
		t.Errorf("request = %v", srv.lastReq)
	}
	if len(res.Artifacts) != 1 || res.Artifacts[0].RelPath != "image.png" || string(res.Metadata) != `{"width":3}` || res.ElapsedSeconds != 1.5 {
		t.Errorf("result = %+v", res)
	}
	if len(fractions) != 1 || fractions[0] != 0.5 {
		t.Errorf("progress = %v", fractions)
	}
}

func TestRunStageErrorMapping(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		check func(error) bool
	}{
		{"invalid argument", status.Error(codes.InvalidArgument, "bad image"), func(e error) bool { return pipeline.IsInvalidInput(e) && strings.Contains(e.Error(), "bad image") }},
		{"cancelled", status.Error(codes.Canceled, "x"), func(e error) bool { return errors.Is(e, context.Canceled) }},
		{"deadline", status.Error(codes.DeadlineExceeded, "x"), func(e error) bool { return errors.Is(e, context.DeadlineExceeded) }},
		{"unavailable", status.Error(codes.Unavailable, "gone"), func(e error) bool { return errors.Is(e, ErrUnavailable) }},
		{"dependency", status.Error(codes.FailedPrecondition, "run doppio setup"), func(e error) bool {
			return errors.Is(e, ErrDependencyMissing) && strings.Contains(e.Error(), "doppio setup")
		}},
		{"internal", status.Error(codes.Internal, "boom"), func(e error) bool { return strings.Contains(e.Error(), "boom") && !pipeline.IsInvalidInput(e) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := dialFake(t, &fakeServer{err: tt.err})
			_, err := runStage(context.Background(), client, pipeline.StageRequest{Stage: "x"}, nil)
			if err == nil || !tt.check(err) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestRunStageWithoutResultFails(t *testing.T) {
	client := dialFake(t, &fakeServer{noResult: true})
	if _, err := runStage(context.Background(), client, pipeline.StageRequest{Stage: "x"}, nil); err == nil {
		t.Fatal("want error when stream ends without result")
	}
}

func TestNewDefaultsAndMissingExecutable(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("want error without dir or command")
	}
	s, err := New(Config{Dir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(s.cfg.Command, filepath.Join(".venv", "bin", "doppio-worker")) && runtime.GOOS != "windows" {
		t.Errorf("command = %s", s.cfg.Command)
	}
	if err := s.Start(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Start err = %v, want ErrUnavailable", err)
	}
}

func TestStartupFailureReportsStderr(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses a shell script")
	}
	script := filepath.Join(t.TempDir(), "fake-worker")
	if err := os.WriteFile(script, []byte("#!/bin/sh\necho 'ImportError: no module named torch' >&2\nexit 3\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	s, _ := New(Config{Command: script, SocketPath: filepath.Join(shortTempDir(t), "w.sock"), StartTimeout: 5 * time.Second})
	err := s.Start(context.Background())
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "no module named torch") {
		t.Fatalf("err = %v", err)
	}
}

func TestRingBuffer(t *testing.T) {
	b := newRingBuffer(2)
	for _, l := range []string{"a", "b", "c"} {
		b.Add(l)
	}
	if got := b.String(); got != "b\nc" {
		t.Fatalf("ring = %q", got)
	}
}

func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "dpw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// TestRealWorkerIngest spawns the real Python worker. Opt in with
// DOPPIO_WORKER_IT=1 after `make worker-setup`.
func TestRealWorkerIngest(t *testing.T) {
	if os.Getenv("DOPPIO_WORKER_IT") != "1" {
		t.Skip("set DOPPIO_WORKER_IT=1 to run against the real worker")
	}
	dir, _ := filepath.Abs("../../worker")
	s, err := New(Config{Dir: dir, SocketPath: filepath.Join(shortTempDir(t), "w.sock"), Device: "cpu"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	health, err := s.Health(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(health.GetStages(), ","), "ingest") {
		t.Fatalf("stages = %v", health.GetStages())
	}

	src := filepath.Join(t.TempDir(), "in.png")
	img := image.NewRGBA(image.Rect(0, 0, 300, 200))
	for x := 0; x < 300; x++ {
		for y := 0; y < 200; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 100, 255})
		}
	}
	f, _ := os.Create(src)
	_ = png.Encode(f, img)
	_ = f.Close()
	out := t.TempDir()
	res, err := s.RunStage(ctx, pipeline.StageRequest{
		Stage: "ingest", Inputs: map[string]string{"source": src}, Params: map[string]any{"max_side": 150}, OutDir: out,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Artifacts) != 1 || !strings.Contains(string(res.Metadata), `"width": 150`) {
		t.Fatalf("result = %+v metadata=%s", res, res.Metadata)
	}
	if _, err := os.Stat(filepath.Join(out, "image.png")); err != nil {
		t.Fatal(err)
	}

	// Invalid input maps to ErrInvalidInput.
	bad := filepath.Join(t.TempDir(), "bad.png")
	_ = os.WriteFile(bad, []byte("nope"), 0o600)
	_, err = s.RunStage(ctx, pipeline.StageRequest{Stage: "ingest", Inputs: map[string]string{"source": bad}, OutDir: t.TempDir()}, nil)
	if !pipeline.IsInvalidInput(err) {
		t.Fatalf("bad image err = %v", err)
	}

	// The supervisor restarts a crashed worker on the next call.
	s.mu.Lock()
	_ = s.proc.cmd.Process.Kill()
	exited := s.proc.exited
	s.mu.Unlock()
	<-exited
	if _, err := s.Health(ctx); err != nil {
		t.Fatalf("restart after crash failed: %v", err)
	}
}
