package scp

import (
	"math"
	"strings"
	"testing"
	"time"

	"github.com/shaansundar/doppio/internal/models"
)

const testSHA = "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"

func f(v float64) *float64 { return &v }

func sampleScene() Scene {
	return Scene{
		Schema:      SchemaScene,
		Image:       ImageInfo{Width: 1024, Height: 768, SHA256: testSHA},
		Conventions: DefaultConventions(),
		Camera:      NewPinholeCamera(800, 800, 512, 384, 65.2, 51.3, 1024, 768, "model"),
		DepthStats:  DepthStats{NearM: f(0.8), MedianM: f(3.1), FarM: f(12), ValidFraction: 0.93, LikelySky: true},
		Quality:     Quality{MetricScale: true, Warnings: []string{}},
	}
}

func sampleManifest() Manifest {
	return Manifest{
		Schema: SchemaManifest, PackID: testSHA, CreatedAt: time.Unix(1700000000, 0).UTC(),
		DoppioVersion: "0.1.0", Preset: "fast", LicensePolicy: "commercial", CommercialUseOK: true,
		Input: Input{SHA256: testSHA, Filename: "room.jpg", OriginalWidth: 4032, OriginalHeight: 3024, Width: 1024, Height: 768},
		Models: []ModelUse{{ID: "moge2-vits-normal", Task: "geometry", HFRepo: "Ruicheng/moge-2-vits-normal",
			Revision: "26b477f41595707c5db6770294c0d1721e8ed4ed", License: models.License{ID: "MIT", Commercial: true}}},
		Stages:    []StageRun{{Name: "ingest", Version: "1", Key: testSHA, ElapsedSeconds: 0.1}},
		Artifacts: []Artifact{{Name: "depth_inv16", Path: "maps/depth_inv16.png", MediaType: "image/png", SHA256: testSHA, Bytes: 10}},
		Warnings:  []string{},
	}
}

func TestSampleDocumentsMatchSchemas(t *testing.T) {
	if err := ValidateScene(sampleScene()); err != nil {
		t.Errorf("scene: %v", err)
	}
	if err := ValidateManifest(sampleManifest()); err != nil {
		t.Errorf("manifest: %v", err)
	}
}

func TestSchemaRejectsBadDocuments(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Manifest, *Scene)
		scene  bool
	}{
		{"wrong schema id", func(m *Manifest, _ *Scene) { m.Schema = "doppio.manifest/v0" }, false},
		{"bad preset", func(m *Manifest, _ *Scene) { m.Preset = "turbo" }, false},
		{"traversal artifact path", func(m *Manifest, _ *Scene) { m.Artifacts[0].Path = "../etc/passwd.png" }, false},
		{"nil warnings", func(m *Manifest, _ *Scene) { m.Warnings = nil }, false},
		{"no stages", func(m *Manifest, _ *Scene) { m.Stages = []StageRun{} }, false},
		{"zero focal", func(_ *Manifest, s *Scene) { s.Camera.Fx = 0 }, true},
		{"fov over 180", func(_ *Manifest, s *Scene) { s.Camera.FovXDeg = 190 }, true},
		{"unknown camera source", func(_ *Manifest, s *Scene) { s.Camera.Source = "guess" }, true},
		{"valid fraction > 1", func(_ *Manifest, s *Scene) { s.DepthStats.ValidFraction = 1.5 }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, s := sampleManifest(), sampleScene()
			tt.mutate(&m, &s)
			var err error
			if tt.scene {
				err = ValidateScene(s)
			} else {
				err = ValidateManifest(m)
			}
			if err == nil {
				t.Fatal("want validation error")
			}
		})
	}
}

func TestNewPinholeCameraDerives35mmEquivalent(t *testing.T) {
	// 3:2 frame with focal = width / 2 -> 90 deg hfov -> 18 mm equivalent.
	c := NewPinholeCamera(1500, 1500, 1500, 1000, 90, 67.38, 3000, 2000, "model")
	if math.Abs(c.Focal35mmEquiv-18) > 1e-9 {
		t.Fatalf("Focal35mmEquiv = %v, want 18", c.Focal35mmEquiv)
	}
	if c.K[0][0] != 1500 || c.K[1][2] != 1000 || c.K[2][2] != 1 {
		t.Fatalf("K = %v", c.K)
	}
}

func TestWriteAndReadManifest(t *testing.T) {
	dir := t.TempDir()
	if err := WriteJSON(dir, ManifestFile, sampleManifest()); err != nil {
		t.Fatal(err)
	}
	m, err := ReadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if m.PackID != testSHA || m.Input.Filename != "room.jpg" {
		t.Fatalf("round trip mismatch: %+v", m)
	}

	bad := sampleManifest()
	bad.Schema = "other"
	_ = WriteJSON(dir, ManifestFile, bad)
	if _, err := ReadManifest(dir); err == nil || !strings.Contains(err.Error(), "unsupported manifest schema") {
		t.Fatalf("want schema error, got %v", err)
	}
	if _, err := ReadManifest(t.TempDir()); err == nil {
		t.Fatal("want error for missing manifest")
	}
}
