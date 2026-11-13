package scp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestExamplesMatchSchema keeps the published examples/ documents valid.
func TestExamplesMatchSchema(t *testing.T) {
	files, err := filepath.Glob("../../examples/scene.*.json")
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no example scene documents found")
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var doc any
			if err := json.Unmarshal(data, &doc); err != nil {
				t.Fatal(err)
			}
			if err := ValidateScene(doc); err != nil {
				t.Fatal(err)
			}
		})
	}
}
