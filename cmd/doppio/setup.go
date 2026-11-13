package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"github.com/spf13/cobra"

	"github.com/shaansundar/doppio/internal/client"
	"github.com/shaansundar/doppio/internal/config"
)

func newSetupCmd() *cobra.Command {
	var workerDir string
	var noMoGe bool
	cmd := &cobra.Command{
		Use:   "setup",
		Short: "Install the Python inference worker environment",
		Long: `Create the worker's virtual environment with uv. By default this installs
the geometry-moge extra: MoGe-2 (MIT) from github.com/microsoft/MoGe at a pinned
commit, plus its pinned GitHub dependencies utils3d and pipeline.
Model weights download from Hugging Face on first use.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := config.ResolveWorkerDir(workerDir)
			if err != nil {
				return err
			}
			uv, err := exec.LookPath("uv")
			if err != nil {
				return errors.New("uv is required: https://docs.astral.sh/uv/getting-started/installation/")
			}
			args := []string{"sync", "--project", dir}
			if !noMoGe {
				args = append(args, "--extra", "geometry-moge")
			}
			fmt.Fprintf(cmd.ErrOrStderr(), "running: uv %v\n", args)
			c := exec.CommandContext(cmd.Context(), uv, args...)
			c.Stdout, c.Stderr = cmd.OutOrStdout(), cmd.ErrOrStderr()
			if err := c.Run(); err != nil {
				return fmt.Errorf("uv sync failed: %w", err)
			}
			fmt.Fprintln(cmd.ErrOrStderr(), "worker environment ready; check with `doppio doctor`")
			return nil
		},
	}
	cmd.Flags().StringVar(&workerDir, "worker-dir", "", "Python worker project directory")
	cmd.Flags().BoolVar(&noMoGe, "no-moge", false, "skip the MoGe geometry backend")
	return cmd
}

func newStopCmd(g *globalFlags) *cobra.Command {
	return &cobra.Command{
		Use:   "stop",
		Short: "Stop the background daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c, err := connect(cmd.Context(), g, false)
			if err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "daemon is not running")
				return nil
			}
			if err := c.Shutdown(cmd.Context()); err != nil {
				return err
			}
			return waitForExit(cmd.Context(), c)
		},
	}
}

func waitForExit(ctx context.Context, c *client.Client) error {
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		pingCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		err := c.Health(pingCtx)
		cancel()
		if err != nil {
			fmt.Fprintln(os.Stderr, "daemon stopped")
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return errors.New("daemon did not stop within 20s")
}
