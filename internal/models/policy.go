package models

import (
	"errors"
	"fmt"
)

// Policy decides which model licenses a request may use.
type Policy string

const (
	// PolicyCommercial allows only models whose license permits commercial use.
	PolicyCommercial Policy = "commercial"
	// PolicyAny allows every model, including non-commercial ones.
	PolicyAny Policy = "any"
)

// ErrLicenseNotAllowed is returned when a model's license is excluded by the policy.
var ErrLicenseNotAllowed = errors.New("model license not allowed by policy")

// ErrModelUnavailable is returned for registered models with no stage yet.
var ErrModelUnavailable = errors.New("model is not available yet")

// ParsePolicy validates a policy string; empty means PolicyCommercial.
func ParsePolicy(s string) (Policy, error) {
	switch Policy(s) {
	case "", PolicyCommercial:
		return PolicyCommercial, nil
	case PolicyAny:
		return PolicyAny, nil
	}
	return "", fmt.Errorf("unknown license policy %q (want %q or %q)", s, PolicyCommercial, PolicyAny)
}

// Allows reports whether the policy permits the model's license.
func (p Policy) Allows(m Model) bool {
	return p == PolicyAny || m.License.Commercial
}

// Resolve returns the model if it exists, is available and passes the policy.
func (r *Registry) Resolve(id string, p Policy) (Model, error) {
	m, ok := r.Get(id)
	if !ok {
		return Model{}, fmt.Errorf("unknown model %q", id)
	}
	if m.Status != StatusAvailable {
		return Model{}, fmt.Errorf("%w: %s", ErrModelUnavailable, id)
	}
	if !p.Allows(m) {
		return Model{}, fmt.Errorf("%w: %s is %s (non-commercial); pass --allow-noncommercial to opt in",
			ErrLicenseNotAllowed, id, m.License.ID)
	}
	return m, nil
}
