// Package tool describes the tools a Bed component can select and combine.
// A tool may be an external program, a kernel mechanism, or an identity-based
// technique; the component owns its selection and lifecycle.
package tool

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
	return fmt.Errorf("invalid tool policy %q: expected auto, off or required", p)
}

// Requirements describe the prerequisites for using a particular tool.
// Declarations are not proof of availability; Probe and Reason retain execution evidence.
type Requirements struct {
	Capabilities []string `json:"capabilities,omitempty"`
	// Tools lists required external programs, not every kind of Bed tool.
	Tools      []string `json:"tools,omitempty"`
	Conditions []string `json:"conditions,omitempty"`
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
			reason = "tool unavailable or excluded by component selection"
		}
		return fmt.Errorf("%s required but not selected: %s", name, reason)
	}
	return nil
}
