package isolation

import (
	"maps"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

// NextCombination excludes only an optional member of the failed composition.
// A required member remains selected while its optional neighbors are tried.
func NextCombination(current Config, selected Isolator, reason string) (Config, bool) {
	report, ok := selected.(Report)
	if !ok {
		return current, false
	}
	current.Excluded = maps.Clone(current.Excluded)
	if current.Excluded == nil {
		current.Excluded = make(map[string]string)
	}
	switch report.ProcessView().Mode {
	case "proot":
		if current.PRoot.Effective() == tool.Auto {
			current.Excluded["proot"] = reason
			return current, true
		}
	case "pathshim":
		if current.Pathshim.Effective() == tool.Auto {
			current.Excluded["pathshim"] = reason
			return current, true
		}
	}
	var policy *tool.Policy
	switch selected.Name() {
	case "bwrap":
		policy = &current.Bwrap
	case "landlock":
		policy = &current.Landlock
	case "uid":
		policy = &current.UID
	}
	if policy == nil || policy.Effective() != tool.Auto {
		return current, false
	}
	current.Excluded[selected.Name()] = reason
	// A different boundary can make a previously incompatible path helper work.
	delete(current.Excluded, "proot")
	delete(current.Excluded, "pathshim")
	return current, true
}
