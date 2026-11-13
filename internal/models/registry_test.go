package models

import (
	"errors"
	"strings"
	"testing"
)

func TestDefaultRegistryIsValid(t *testing.T) {
	reg, err := Default()
	if err != nil {
		t.Fatalf("Default() error = %v", err)
	}
	for _, id := range []string{"moge2-vits-normal", "moge2-vitl-normal"} {
		m, ok := reg.Get(id)
		if !ok {
			t.Fatalf("default registry missing %s", id)
		}
		if m.Status != StatusAvailable || !m.License.Commercial {
			t.Errorf("%s: want available commercial model, got status=%s commercial=%v", id, m.Status, m.License.Commercial)
		}
		if len(m.Revision) != 40 {
			t.Errorf("%s: revision must be a full commit sha, got %q", id, m.Revision)
		}
	}
	all := reg.All()
	for i := 1; i < len(all); i++ {
		if all[i-1].ID >= all[i].ID {
			t.Fatalf("All() not sorted: %s before %s", all[i-1].ID, all[i].ID)
		}
	}
}

func TestParseRejectsInvalidEntries(t *testing.T) {
	base := "version: 1\nmodels:\n"
	tests := []struct {
		name    string
		yaml    string
		wantErr string
	}{
		{"bad version", "version: 2\nmodels: []\n", "unsupported model registry version"},
		{"missing id", base + "  - {task: t, family: f, status: available, hf_repo: r, revision: x, memory_gb: 1, license: {id: MIT}}\n", "id is required"},
		{"bad status", base + "  - {id: a, task: t, family: f, status: soon, hf_repo: r, revision: x, memory_gb: 1, license: {id: MIT}}\n", "status must be"},
		{"no source", base + "  - {id: a, task: t, family: f, status: available, memory_gb: 1, license: {id: MIT}}\n", "hf_repo or url"},
		{"unpinned", base + "  - {id: a, task: t, family: f, status: available, hf_repo: r, memory_gb: 1, license: {id: MIT}}\n", "pin a revision"},
		{"no license", base + "  - {id: a, task: t, family: f, status: available, hf_repo: r, revision: x, memory_gb: 1, license: {}}\n", "license.id"},
		{"no memory", base + "  - {id: a, task: t, family: f, status: available, hf_repo: r, revision: x, license: {id: MIT}}\n", "memory_gb"},
		{"duplicate", base +
			"  - {id: a, task: t, family: f, status: available, hf_repo: r, revision: x, memory_gb: 1, license: {id: MIT}}\n" +
			"  - {id: a, task: t, family: f, status: available, hf_repo: r, revision: x, memory_gb: 1, license: {id: MIT}}\n", "duplicate model id"},
		{"bad yaml", "version: [", "failed to parse"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.yaml))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Parse() error = %v, want containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestPolicyResolve(t *testing.T) {
	reg, err := Parse([]byte(`version: 1
models:
  - {id: free, task: geometry, family: f, status: available, hf_repo: r, revision: x, memory_gb: 1, license: {id: MIT, commercial: true}}
  - {id: nc, task: geometry, family: f, status: available, hf_repo: r, revision: x, memory_gb: 1, license: {id: CC-BY-NC-4.0, commercial: false}}
  - {id: later, task: geometry, family: f, status: planned, hf_repo: r, revision: x, memory_gb: 1, license: {id: MIT, commercial: true}}
`))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		id      string
		policy  Policy
		wantErr error
		wantAny bool // any error
	}{
		{"free", PolicyCommercial, nil, false},
		{"nc", PolicyCommercial, ErrLicenseNotAllowed, false},
		{"nc", PolicyAny, nil, false},
		{"later", PolicyAny, ErrModelUnavailable, false},
		{"missing", PolicyAny, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.id+"/"+string(tt.policy), func(t *testing.T) {
			_, err := reg.Resolve(tt.id, tt.policy)
			switch {
			case tt.wantAny && err == nil:
				t.Fatal("want error, got nil")
			case tt.wantErr != nil && !errors.Is(err, tt.wantErr):
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			case tt.wantErr == nil && !tt.wantAny && err != nil:
				t.Fatalf("unexpected error %v", err)
			}
		})
	}
}

func TestParsePolicy(t *testing.T) {
	for in, want := range map[string]Policy{"": PolicyCommercial, "commercial": PolicyCommercial, "any": PolicyAny} {
		got, err := ParsePolicy(in)
		if err != nil || got != want {
			t.Errorf("ParsePolicy(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	if _, err := ParsePolicy("yolo"); err == nil {
		t.Error("ParsePolicy(yolo) want error")
	}
}
