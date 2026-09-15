package isolation

import (
	"fmt"

	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

// Config controls selection without changing host observations or BedFS ownership.
type Config struct {
	DormReadFallbackRoot string
	Level                string
	Bwrap                tool.Policy
	Landlock             tool.Policy
	UID                  tool.Policy
	PRoot                tool.Policy
	Pathshim             tool.Policy
	// DedicatedIdentity is resolved by Privilege before filesystem selection.
	DedicatedIdentity bool
	// Excluded records failed startup combinations without rewriting user policy.
	Excluded map[string]string
}

// Options preserves explicit Auto independently of omitted configuration.
type Options struct {
	DormReadFallbackRoot *string
	Bwrap                *tool.Policy
	Landlock             *tool.Policy
	UID                  *tool.Policy
	PRoot                *tool.Policy
	Pathshim             *tool.Policy
}

func (c Config) Validate() error {
	switch c.Level {
	case "", "auto", "shared", "confined", "private":
	default:
		return fmt.Errorf("invalid isolation level %q", c.Level)
	}
	policies := c.policies()
	for name, p := range policies {
		if err := p.Validate(); err != nil {
			return fmt.Errorf("filesystem.%s: %w", name, err)
		}
	}
	required := 0
	for _, name := range []string{"bwrap", "landlock", "uid"} {
		if policies[name] == tool.Required {
			required++
		}
	}
	if required > 1 {
		return fmt.Errorf("filesystem: bwrap, landlock and uid are mutually exclusive")
	}
	if c.PRoot == tool.Required && c.Pathshim == tool.Required {
		return fmt.Errorf("filesystem: proot and pathshim are mutually exclusive")
	}
	if c.Bwrap == tool.Required && parseRequest(c.Level) < Private {
		return fmt.Errorf("filesystem: bwrap requires private files")
	}
	if (c.Landlock == tool.Required || c.UID == tool.Required) && parseRequest(c.Level) < Confined {
		return fmt.Errorf("filesystem: landlock/uid require at least confined files")
	}
	if c.Bwrap == tool.Required && (c.PRoot == tool.Required || c.Pathshim == tool.Required) {
		return fmt.Errorf("filesystem: bwrap mount view conflicts with required workspace helper")
	}
	return nil
}
func (c Config) policies() map[string]tool.Policy {
	return map[string]tool.Policy{"bwrap": c.Bwrap.Effective(), "landlock": c.Landlock.Effective(), "uid": c.UID.Effective(), "proot": c.PRoot.Effective(), "pathshim": c.Pathshim.Effective()}
}
func requirements(name string) tool.Requirements {
	switch name {
	case "bwrap":
		return tool.Requirements{Tools: []string{"bwrap"}, Conditions: []string{"rootful mount authority or unprivileged user namespace", "bwrap execution probe"}}
	case "landlock":
		return tool.Requirements{Conditions: []string{"Linux Landlock ABI", "Landlock confinement probe"}}
	case "uid":
		names := []string{}
		for _, requirement := range privilege.RequiredBedIdentityCapabilities() {
			names = append(names, requirement.Name)
		}
		return tool.Requirements{Capabilities: names, Conditions: []string{"Hostel credential entry", "UID isolation probe"}}
	case "proot":
		return tool.Requirements{Tools: []string{"proot"}, Conditions: []string{"ptrace permitted", "PRoot execution probe"}}
	default:
		return tool.Requirements{Tools: []string{"pathshim"}, Conditions: []string{"pathshim execution probe"}}
	}
}
