package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/shaansundar/doppio/internal/client"
	"github.com/shaansundar/doppio/internal/config"
)

// connect returns a client for a running daemon, starting one in the
// background when autostart is set and none is reachable.
func connect(ctx context.Context, g *globalFlags, autostart bool) (*client.Client, error) {
	if err := config.ValidateLoopbackAddr(g.addr); err != nil {
		return nil, err
	}
	paths, err := config.DefaultPaths()
	if err != nil {
		return nil, err
	}
	if err := paths.Ensure(); err != nil {
		return nil, err
	}
	token, err := config.LoadOrCreateToken(paths.TokenFile())
	if err != nil {
		return nil, err
	}
	c := client.New(g.addr, token)
	pingCtx, cancel := context.WithTimeout(ctx, time.Second)
	err = c.Health(pingCtx)
	cancel()
	if err == nil {
		return c, nil
	}
	if !autostart {
		return nil, errors.New("doppio daemon is not running; start it with `doppio serve`")
	}
	if err := spawnDaemon(g.addr, paths.LogFile()); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, time.Second)
		err := c.Health(pingCtx)
		cancel()
		if err == nil {
			return c, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, fmt.Errorf("daemon did not become ready; see %s", paths.LogFile())
}

// spawnDaemon starts `doppio serve` detached from this terminal session.
func spawnDaemon(addr, logFile string) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("failed to locate doppio executable: %w", err)
	}
	log, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("failed to open daemon log: %w", err)
	}
	defer log.Close()
	cmd := exec.Command(exe, "serve", "--addr", addr, "--log-file", logFile)
	cmd.Stdout, cmd.Stderr = log, log
	detach(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start daemon: %w", err)
	}
	fmt.Fprintf(os.Stderr, "started doppio daemon (pid %d, logs: %s)\n", cmd.Process.Pid, logFile)
	return cmd.Process.Release()
}
