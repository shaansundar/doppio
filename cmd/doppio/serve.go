package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/shaansundar/doppio/internal/api"
	"github.com/shaansundar/doppio/internal/cache"
	"github.com/shaansundar/doppio/internal/config"
	"github.com/shaansundar/doppio/internal/jobs"
	"github.com/shaansundar/doppio/internal/models"
	"github.com/shaansundar/doppio/internal/pipeline"
	"github.com/shaansundar/doppio/internal/worker"
)

type serveFlags struct {
	workerDir     string
	device        string
	maxResidentGB float64
	maxUploadMB   int64
	cacheMaxGB    float64
	packsMaxGB    float64
	logFile       string
	noPrewarm     bool
}

func newServeCmd(g *globalFlags) *cobra.Command {
	f := &serveFlags{}
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the doppio daemon in the foreground",
		Long: `Run the local API server and inference worker. Other commands start it
automatically in the background when needed.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error { return runServe(cmd.Context(), g, f) },
	}
	cmd.Flags().StringVar(&f.workerDir, "worker-dir", "", "Python worker project directory (env DOPPIO_WORKER_DIR)")
	cmd.Flags().StringVar(&f.device, "device", os.Getenv("DOPPIO_DEVICE"), "force cuda, mps or cpu (default: auto)")
	cmd.Flags().Float64Var(&f.maxResidentGB, "max-resident-gb", 6, "resident model memory budget in GB")
	cmd.Flags().Int64Var(&f.maxUploadMB, "max-upload-mb", 200, "maximum upload size in MB")
	cmd.Flags().Float64Var(&f.cacheMaxGB, "cache-max-gb", 20, "stage cache and upload size limit in GB (pruned at startup)")
	cmd.Flags().Float64Var(&f.packsMaxGB, "packs-max-gb", 5, "assembled pack size limit in GB (pruned at startup)")
	cmd.Flags().StringVar(&f.logFile, "log-file", "", "append logs to this file instead of stderr")
	cmd.Flags().BoolVar(&f.noPrewarm, "no-prewarm", false, "do not start the worker until the first request")
	return cmd
}

func runServe(parent context.Context, g *globalFlags, f *serveFlags) error {
	ctx, stop := context.WithCancel(parent)
	defer stop()
	if err := config.ValidateLoopbackAddr(g.addr); err != nil {
		return err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return err
	}
	if err := paths.Ensure(); err != nil {
		return err
	}
	var logOut io.Writer = os.Stderr
	if f.logFile != "" {
		lf, err := os.OpenFile(f.logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("failed to open log file: %w", err)
		}
		defer lf.Close()
		logOut = lf
	}
	logger := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: slog.LevelInfo}))

	token, err := config.LoadOrCreateToken(paths.TokenFile())
	if err != nil {
		return err
	}
	workerDir, err := config.ResolveWorkerDir(f.workerDir)
	if err != nil {
		return err
	}
	store, err := cache.Open(paths.StageCacheDir())
	if err != nil {
		return err
	}
	go func() {
		if freed, err := store.Prune(int64(f.cacheMaxGB * (1 << 30))); err != nil {
			logger.Warn("cache prune failed", "err", err)
		} else if freed > 0 {
			logger.Info("pruned stage cache", "freed_mb", freed>>20)
		}
	}()
	registry, err := models.Default()
	if err != nil {
		return err
	}
	sup, err := worker.New(worker.Config{
		Dir: workerDir, MaxResidentGB: f.maxResidentGB, Device: f.device, Log: logOut,
	})
	if err != nil {
		return err
	}
	defer sup.Close()
	engine, err := pipeline.NewEngine(sup, store, registry, paths.PacksDir(), pipeline.WithVersion(version))
	if err != nil {
		return err
	}
	go func() {
		if freed, err := engine.PrunePacks(int64(f.packsMaxGB * (1 << 30))); err != nil {
			logger.Warn("pack prune failed", "err", err)
		} else if freed > 0 {
			logger.Info("pruned packs", "freed_mb", freed>>20)
		}
	}()
	manager := jobs.NewManager(engine, jobs.WithConcurrency(1))
	server, err := api.New(api.Config{
		Queue: manager, Status: manager, Packs: engine, Blobs: store, Worker: sup, Registry: registry,
		Token: token, AllowedHosts: config.AllowedHosts(g.addr), MaxUploadBytes: f.maxUploadMB << 20,
		Version: version, Logger: logger, Shutdown: stop,
	})
	if err != nil {
		return err
	}

	ln, err := net.Listen("tcp", g.addr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s (is doppio already running?): %w", g.addr, err)
	}
	httpSrv := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()
	logger.Info("doppio daemon listening", "addr", g.addr, "version", version, "worker_dir", workerDir, "cache", paths.CacheDir)

	if !f.noPrewarm {
		go func() {
			if err := sup.Start(ctx); err != nil && !errors.Is(err, context.Canceled) {
				logger.Warn("worker prewarm failed; it will be retried on the first request", "err", err)
			}
		}()
	}

	select {
	case <-ctx.Done():
	case err := <-serveErr:
		if !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http server failed: %w", err)
		}
	}
	logger.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	if err := manager.Shutdown(shutdownCtx); err != nil {
		logger.Warn("jobs did not stop in time", "err", err)
	}
	return nil
}
