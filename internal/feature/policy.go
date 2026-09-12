// Package feature describes component-owned runtime choices, not Linux capabilities.
package feature

import "fmt"

type Policy string

const (
	Auto     Policy = "auto"
	Off      Policy = "off"
	Required Policy = "required"
)

// Effective gives omitted programmatic configuration the same default as startup.
func (p Policy) Effective() Policy {
	if p == "" {
		return Auto
	}
	return p
}
func (p Policy) Validate() error {
	switch p.Effective() {
	case Auto, Off, Required:
		return nil
	}
	return fmt.Errorf("invalid feature policy %q: expected auto, off or required", p)
}

// Requirements belong to this implementation, not to the abstract feature name.
// Declarations are not proof of availability; Probe and Reason retain execution evidence.
type Requirements struct {
	Capabilities []string `json:"capabilities,omitempty"`
	Tools        []string `json:"tools,omitempty"`
	Conditions   []string `json:"conditions,omitempty"`
}
type Status struct {
	Policy       Policy       `json:"policy"`
	Requirements Requirements `json:"requirements"`
	Probe        string       `json:"probe"` // not_probed / available / unavailable
	Selected     bool         `json:"selected"`
	Reason       string       `json:"reason,omitempty"`
}

func Describe(p Policy, requirements Requirements, probed, available, selected bool, reason string) Status {
	s := Status{Policy: p.Effective(), Requirements: requirements, Probe: "not_probed", Reason: reason}
	if p.Effective() == Off {
		s.Reason = "disabled_by_config"
		return s
	}
	if probed {
		s.Probe = "unavailable"
		if available {
			s.Probe = "available"
		}
	}
	s.Selected = selected
	return s
}

// CheckRequired is called after component-owned selection, before publishing readiness.
func (s Status) CheckRequired(name string) error {
	if s.Policy == Required && !s.Selected {
		reason := s.Reason
		if reason == "" {
			reason = "feature unavailable or excluded by component selection"
		}
		return fmt.Errorf("%s required but not selected: %s", name, reason)
	}
	return nil
}
