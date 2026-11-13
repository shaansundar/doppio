package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shaansundar/doppio/internal/client"
	"github.com/shaansundar/doppio/internal/jobs"
	"github.com/shaansundar/doppio/internal/scp"
)

type analyzeFlags struct {
	preset             string
	out                string
	jsonOut            bool
	allowNonCommercial bool
	geometryModel      string
	noEXIFPrior        bool
	timeout            time.Duration
}

func newAnalyzeCmd(g *globalFlags) *cobra.Command {
	f := &analyzeFlags{}
	cmd := &cobra.Command{
		Use:   "analyze IMAGE",
		Short: "Analyze an image and write its Spatial Context Pack",
		Long: `Analyze IMAGE and download its Spatial Context Pack: scene.json (camera,
depth statistics), conditioning maps (maps/) and raw arrays (arrays/).

Presets: fast (small model, 1024 px), standard (large model, 2048 px),
full (large model, 3072 px).`,
		Example: `  doppio analyze room.jpg
  doppio analyze room.jpg --preset fast --out ./room-context
  doppio analyze room.jpg --json | jq .camera`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAnalyze(cmd.Context(), g, f, args[0], cmd.OutOrStdout(), cmd.ErrOrStderr())
		},
	}
	cmd.Flags().StringVarP(&f.preset, "preset", "p", "standard", "fast, standard or full")
	cmd.Flags().StringVarP(&f.out, "out", "o", "", "output directory (default ./<image>.doppio)")
	cmd.Flags().BoolVar(&f.jsonOut, "json", false, "print scene.json to stdout instead of a summary")
	cmd.Flags().BoolVar(&f.allowNonCommercial, "allow-noncommercial", false, "allow models whose license forbids commercial use")
	cmd.Flags().StringVar(&f.geometryModel, "geometry-model", "", "override the geometry model (see `doppio models list`)")
	cmd.Flags().BoolVar(&f.noEXIFPrior, "no-exif-prior", false, "ignore the EXIF focal length when estimating the camera")
	cmd.Flags().DurationVar(&f.timeout, "timeout", 15*time.Minute, "give up after this long")
	return cmd
}

func runAnalyze(ctx context.Context, g *globalFlags, f *analyzeFlags, image string, stdout, stderr io.Writer) error {
	info, err := os.Stat(image)
	if err != nil {
		return &exitError{code: 2, err: fmt.Errorf("cannot read image: %w", err)}
	}
	if info.IsDir() {
		return &exitError{code: 2, err: fmt.Errorf("%s is a directory", image)}
	}
	ctx, cancel := context.WithTimeout(ctx, f.timeout)
	defer cancel()

	c, err := connect(ctx, g, true)
	if err != nil {
		return err
	}
	started := time.Now()
	job, err := c.Analyze(ctx, image, client.AnalyzeRequest{
		Preset: f.preset, AllowNonCommercial: f.allowNonCommercial,
		GeometryModel: f.geometryModel, DisableEXIFPrior: f.noEXIFPrior,
	})
	if err != nil {
		return withInputExitCode(err)
	}
	progress := newProgressPrinter(stderr)
	job, err = c.WaitJob(ctx, job.ID, 250*time.Millisecond, progress.update)
	progress.done()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			cancelCtx, stop := context.WithTimeout(context.Background(), 3*time.Second)
			_ = c.CancelJob(cancelCtx, job.ID)
			stop()
		}
		return err
	}
	if job.Status != jobs.StatusSucceeded {
		msg := "analysis did not complete"
		code := 1
		if job.Error != nil {
			msg = job.Error.Message
			if job.Error.Kind == jobs.ErrorInvalidInput {
				code = 2
			}
		}
		return &exitError{code: code, err: errors.New(msg)}
	}

	out := f.out
	if out == "" {
		base := filepath.Base(image)
		out = strings.TrimSuffix(base, filepath.Ext(base)) + ".doppio"
	}
	manifest, err := c.DownloadPack(ctx, job.PackID, out)
	if err != nil {
		return err
	}
	sceneBytes, err := os.ReadFile(filepath.Join(out, scp.SceneFile))
	if err != nil {
		return fmt.Errorf("failed to read scene.json: %w", err)
	}
	if f.jsonOut {
		_, err = stdout.Write(sceneBytes)
		return err
	}
	var scene scp.Scene
	if err := json.Unmarshal(sceneBytes, &scene); err != nil {
		return fmt.Errorf("failed to decode scene.json: %w", err)
	}
	printSummary(stdout, image, out, time.Since(started), manifest, scene)
	return nil
}

// withInputExitCode maps request errors the user can fix (bad image, bad
// option, license refusal, too large) to exit code 2.
func withInputExitCode(err error) error {
	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.StatusCode {
		case 400, 403, 413, 415, 422:
			return &exitError{code: 2, err: err}
		}
	}
	return err
}

func printSummary(w io.Writer, image, out string, elapsed time.Duration, m scp.Manifest, s scp.Scene) {
	cached := true
	for _, st := range m.Stages {
		cached = cached && st.Cached
	}
	note := ""
	if cached {
		note = " (cached)"
	}
	fmt.Fprintf(w, "Analyzed %s  %dx%d  preset %s  %s%s\n", filepath.Base(image), s.Image.Width, s.Image.Height,
		m.Preset, elapsed.Round(10*time.Millisecond), note)
	fmt.Fprintf(w, "  camera  %.1f° x %.1f° FOV, %.0f mm equiv. (%s)\n",
		s.Camera.FovXDeg, s.Camera.FovYDeg, s.Camera.Focal35mmEquiv, s.Camera.Source)
	if s.DepthStats.NearM != nil && s.DepthStats.FarM != nil && s.DepthStats.MedianM != nil {
		sky := ""
		if s.DepthStats.LikelySky {
			sky = ", sky likely"
		}
		fmt.Fprintf(w, "  depth   %.2f m to %.2f m (median %.2f m), %.0f%% valid%s\n",
			*s.DepthStats.NearM, *s.DepthStats.FarM, *s.DepthStats.MedianM, s.DepthStats.ValidFraction*100, sky)
	}
	names := make([]string, 0, len(m.Models))
	for _, mu := range m.Models {
		names = append(names, fmt.Sprintf("%s (%s)", mu.ID, mu.License.ID))
	}
	usage := "commercial use OK"
	if !m.CommercialUseOK {
		usage = "NOT cleared for commercial use"
	}
	fmt.Fprintf(w, "  models  %s; %s\n", strings.Join(names, ", "), usage)
	for _, warn := range append(append([]string{}, s.Quality.Warnings...), m.Warnings...) {
		fmt.Fprintf(w, "  note    %s\n", warn)
	}
	fmt.Fprintf(w, "  pack    %s  (scene.json, maps/, arrays/)\n", out)
}

// progressPrinter renders a single updating status line on terminals.
type progressPrinter struct {
	w    io.Writer
	tty  bool
	last string
}

func newProgressPrinter(w io.Writer) *progressPrinter {
	p := &progressPrinter{w: w}
	if f, ok := w.(*os.File); ok {
		if info, err := f.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			p.tty = true
		}
	}
	return p
}

func (p *progressPrinter) update(j client.Job) {
	if !p.tty {
		return
	}
	line := string(j.Status)
	if j.Status == jobs.StatusRunning && j.Progress.Total > 0 {
		line = fmt.Sprintf("[%d/%d] %-9s %3.0f%%  %s", j.Progress.Index+1, j.Progress.Total,
			j.Progress.Stage, j.Progress.Fraction*100, j.Progress.Message)
	}
	if line != p.last {
		fmt.Fprintf(p.w, "\r\033[K%s", line)
		p.last = line
	}
}

func (p *progressPrinter) done() {
	if p.tty && p.last != "" {
		fmt.Fprint(p.w, "\r\033[K")
	}
}
