package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/shaansundar/doppio/internal/config"
	"github.com/shaansundar/doppio/internal/worker"
)

func newDoctorCmd(g *globalFlags) *cobra.Command {
	var workerDir string
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check the doppio installation and daemon",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runDoctor(cmd.Context(), g, workerDir, cmd.OutOrStdout())
		},
	}
	cmd.Flags().StringVar(&workerDir, "worker-dir", "", "Python worker project directory")
	return cmd
}

func runDoctor(ctx context.Context, g *globalFlags, workerDirFlag string, w io.Writer) error {
	failures := 0
	check := func(ok bool, label, detail string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
			failures++
		}
		fmt.Fprintf(w, "[%s] %-16s %s\n", mark, label, detail)
	}

	paths, err := config.DefaultPaths()
	check(err == nil, "paths", fmt.Sprintf("config %s, cache %s", paths.ConfigDir, paths.CacheDir))

	if info, err := os.Stat(paths.TokenFile()); err == nil {
		check(info.Mode().Perm()&0o077 == 0, "token", fmt.Sprintf("%s (%v)", paths.TokenFile(), info.Mode().Perm()))
	} else {
		check(true, "token", "not created yet (made on first run)")
	}

	dir, err := config.ResolveWorkerDir(workerDirFlag)
	if err != nil {
		check(false, "worker project", err.Error())
	} else {
		check(true, "worker project", dir)
		exe := worker.DefaultCommand(dir)
		_, err := os.Stat(exe)
		check(err == nil, "worker env", map[bool]string{true: exe, false: "missing; run `doppio setup`"}[err == nil])
		mogeDirs, _ := filepath.Glob(filepath.Join(dir, ".venv", "lib", "python3*", "site-packages", "moge"))
		check(len(mogeDirs) > 0, "geometry (MoGe)", map[bool]string{
			true: "installed", false: "not installed; run `doppio setup` (installs the geometry-moge extra)"}[len(mogeDirs) > 0])
	}

	_, uvErr := exec.LookPath("uv")
	check(uvErr == nil, "uv", map[bool]string{true: "found", false: "not found in PATH (needed by `doppio setup`)"}[uvErr == nil])

	c, err := connect(ctx, g, false)
	if err != nil {
		check(true, "daemon", "not running (starts automatically on `doppio analyze`)")
	} else {
		infoCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
		info, err := c.Info(infoCtx)
		cancel()
		if err != nil {
			check(false, "daemon", err.Error())
		} else {
			check(true, "daemon", fmt.Sprintf("%s at %s", info["version"], g.addr))
			if wk, ok := info["worker"].(map[string]any); ok {
				if wk["ok"] == true {
					check(true, "worker", fmt.Sprintf("device %v, torch %v", wk["device"], wk["torch_version"]))
				} else {
					check(false, "worker", fmt.Sprint(wk["error"]))
				}
			}
		}
	}
	if failures > 0 {
		return &exitError{code: 1, err: fmt.Errorf("%d check(s) failed", failures)}
	}
	return nil
}
