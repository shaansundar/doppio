// Command doppio is the CLI and local daemon for 3D spatial context.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/shaansundar/doppio/internal/config"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "0.1.0-dev"

// exitError carries a specific process exit code.
type exitError struct {
	code int
	err  error
}

func (e *exitError) Error() string { return e.err.Error() }
func (e *exitError) Unwrap() error { return e.err }

type globalFlags struct {
	addr string
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := newRootCmd().ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "doppio:", err)
		var ee *exitError
		if errors.As(err, &ee) {
			os.Exit(ee.code)
		}
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	g := &globalFlags{}
	root := &cobra.Command{
		Use:   "doppio",
		Short: "Local 3D spatial context for image generation and editing models",
		Long: `doppio analyzes an image and returns its 3D spatial context — camera,
metric depth, surface normals and conditioning maps — as a Spatial Context
Pack that image generation and editing models can use.

All processing runs locally. Only commercially licensed models are used
unless you pass --allow-noncommercial.`,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	defaultAddr := config.DefaultAddr
	if env := os.Getenv("DOPPIO_ADDR"); env != "" {
		defaultAddr = env
	}
	root.PersistentFlags().StringVar(&g.addr, "addr", defaultAddr, "daemon address (loopback only; env DOPPIO_ADDR)")
	root.AddCommand(
		newAnalyzeCmd(g),
		newServeCmd(g),
		newModelsCmd(),
		newDoctorCmd(g),
		newSetupCmd(),
		newStopCmd(g),
		&cobra.Command{
			Use:   "version",
			Short: "Print the doppio version",
			Run:   func(cmd *cobra.Command, _ []string) { fmt.Fprintln(cmd.OutOrStdout(), "doppio", version) },
		},
	)
	return root
}
