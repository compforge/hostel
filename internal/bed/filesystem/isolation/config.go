package isolation

import (
	"fmt"

	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/feature"
)

// Config controls selection without changing host observations or BedFS ownership.
type Config struct {
	// ProjectedPaths adds BedFS-to-process mappings alongside the built-in /workspace.
	ProjectedPaths       string
	DormReadFallbackRoot string
	Level                string
	Bwrap                feature.Policy
	Landlock             feature.Policy
	UID                  feature.Policy
	PRoot                feature.Policy
	Pathshim             feature.Policy
}

// Options preserves explicit Auto independently of omitted configuration.
type Options struct {
	ProjectedPaths       *string
	DormReadFallbackRoot *string
	Level                *string
	Bwrap                *feature.Policy
	Landlock             *feature.Policy
	UID                  *feature.Policy
	PRoot                *feature.Policy
	Pathshim             *feature.Policy
}

func (c Config) Validate() error {
	switch c.Level {
	case "", "auto", "dorm", "room", "suite":
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
		if policies[name] == feature.Required {
			required++
		}
	}
	if required > 1 {
		return fmt.Errorf("filesystem: bwrap, landlock and uid are mutually exclusive")
	}
	if c.PRoot == feature.Required && c.Pathshim == feature.Required {
		return fmt.Errorf("filesystem: proot and pathshim are mutually exclusive")
	}
	if c.Bwrap == feature.Required && parseRequest(c.Level) < Suite {
		return fmt.Errorf("filesystem: bwrap requires suite level")
	}
	if (c.Landlock == feature.Required || c.UID == feature.Required) && parseRequest(c.Level) < Room {
		return fmt.Errorf("filesystem: landlock/uid require at least room level")
	}
	if c.Bwrap == feature.Required && (c.PRoot == feature.Required || c.Pathshim == feature.Required) {
		return fmt.Errorf("filesystem: bwrap mount view conflicts with required workspace helper")
	}
	return nil
}
func (c Config) policies() map[string]feature.Policy {
	return map[string]feature.Policy{"bwrap": c.Bwrap.Effective(), "landlock": c.Landlock.Effective(), "uid": c.UID.Effective(), "proot": c.PRoot.Effective(), "pathshim": c.Pathshim.Effective()}
}
func requirements(name string) feature.Requirements {
	switch name {
	case "bwrap":
		return feature.Requirements{Tools: []string{"bwrap"}, Conditions: []string{"Linux user/mount namespaces permitted", "bwrap execution probe"}}
	case "landlock":
		return feature.Requirements{Conditions: []string{"Linux Landlock ABI", "Landlock confinement probe"}}
	case "uid":
		names := []string{}
		for _, requirement := range privilege.RequiredBedIdentityCapabilities() {
			names = append(names, requirement.Name)
		}
		return feature.Requirements{Capabilities: names, Tools: []string{"setpriv"}, Conditions: []string{"UID isolation probe"}}
	case "proot":
		return feature.Requirements{Tools: []string{"proot"}, Conditions: []string{"ptrace permitted", "PRoot execution probe"}}
	default:
		return feature.Requirements{Tools: []string{"pathshim"}, Conditions: []string{"pathshim execution probe"}}
	}
}
