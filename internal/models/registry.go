// Package models loads the model registry and applies the license policy.
package models

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"sort"

	"gopkg.in/yaml.v3"
)

//go:embed registry.yaml
var embeddedRegistry []byte

// Status says whether a model is wired to a pipeline stage yet.
type Status string

const (
	StatusAvailable Status = "available"
	StatusPlanned   Status = "planned"
)

// License describes the terms a model's weights are distributed under.
type License struct {
	ID         string `yaml:"id" json:"id"`
	Commercial bool   `yaml:"commercial" json:"commercial"`
	Gated      bool   `yaml:"gated,omitempty" json:"gated,omitempty"`
	URL        string `yaml:"url,omitempty" json:"url,omitempty"`
	Notes      string `yaml:"notes,omitempty" json:"notes,omitempty"`
}

// Model is one registry entry.
type Model struct {
	ID        string  `yaml:"id" json:"id"`
	Task      string  `yaml:"task" json:"task"`
	Family    string  `yaml:"family" json:"family"`
	Status    Status  `yaml:"status" json:"status"`
	HFRepo    string  `yaml:"hf_repo,omitempty" json:"hf_repo,omitempty"`
	Revision  string  `yaml:"revision,omitempty" json:"revision,omitempty"`
	Filename  string  `yaml:"filename,omitempty" json:"filename,omitempty"`
	URL       string  `yaml:"url,omitempty" json:"url,omitempty"`
	SizeBytes int64   `yaml:"size_bytes,omitempty" json:"size_bytes,omitempty"`
	MemoryGB  float64 `yaml:"memory_gb" json:"memory_gb"`
	License   License `yaml:"license" json:"license"`
}

// Registry is an immutable, validated set of models.
type Registry struct {
	models []Model
	byID   map[string]Model
}

type registryFile struct {
	Version int     `yaml:"version"`
	Models  []Model `yaml:"models"`
}

// Default returns the registry embedded in the binary.
func Default() (*Registry, error) {
	return Parse(embeddedRegistry)
}

// Load reads a registry from path.
func Load(path string) (*Registry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read model registry: %w", err)
	}
	return Parse(data)
}

// Parse decodes and validates registry YAML.
func Parse(data []byte) (*Registry, error) {
	var file registryFile
	if err := yaml.Unmarshal(data, &file); err != nil {
		return nil, fmt.Errorf("failed to parse model registry: %w", err)
	}
	if file.Version != 1 {
		return nil, fmt.Errorf("unsupported model registry version %d", file.Version)
	}
	reg := &Registry{byID: make(map[string]Model, len(file.Models))}
	var errs []error
	for i, m := range file.Models {
		if err := validate(m); err != nil {
			errs = append(errs, fmt.Errorf("model #%d (%q): %w", i, m.ID, err))
			continue
		}
		if _, dup := reg.byID[m.ID]; dup {
			errs = append(errs, fmt.Errorf("duplicate model id %q", m.ID))
			continue
		}
		reg.byID[m.ID] = m
		reg.models = append(reg.models, m)
	}
	if len(errs) > 0 {
		return nil, fmt.Errorf("invalid model registry: %w", errors.Join(errs...))
	}
	sort.SliceStable(reg.models, func(i, j int) bool { return reg.models[i].ID < reg.models[j].ID })
	return reg, nil
}

func validate(m Model) error {
	switch {
	case m.ID == "":
		return errors.New("id is required")
	case m.Task == "":
		return errors.New("task is required")
	case m.Family == "":
		return errors.New("family is required")
	case m.Status != StatusAvailable && m.Status != StatusPlanned:
		return fmt.Errorf("status must be %q or %q", StatusAvailable, StatusPlanned)
	case m.HFRepo == "" && m.URL == "":
		return errors.New("hf_repo or url is required")
	case m.HFRepo != "" && m.Revision == "":
		return errors.New("hf_repo models must pin a revision")
	case m.License.ID == "":
		return errors.New("license.id is required")
	case m.MemoryGB <= 0:
		return errors.New("memory_gb must be positive")
	}
	return nil
}

// Get returns the model with the given id.
func (r *Registry) Get(id string) (Model, bool) {
	m, ok := r.byID[id]
	return m, ok
}

// All returns every model sorted by id.
func (r *Registry) All() []Model {
	return append([]Model(nil), r.models...)
}
