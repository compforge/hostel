package manager

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

func baselineRuntimeConfig() RuntimeConfig {
	return RuntimeConfig{
		Room:       bed.Suite,
		Filesystem: isolation.Config{Bwrap: feature.Off, Landlock: feature.Off, UID: feature.Off, PRoot: feature.Off, Pathshim: feature.Off},
		Network:    network.Config{NetNS: feature.Off},
		Resource:   resource.Config{Cgroup: feature.Off},
		Executor:   executor.Config{Backend: "local"},
	}
}

func TestRuntimeBaselineExercisesCompositionAndCleansScratch(t *testing.T) {
	root := t.TempDir()
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	selected, err := ResolveRuntime(ctx, facts, root, "/bin/bash", baselineRuntimeConfig(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Attempts) != 1 || selected.Attempts[0].Error != "" || selected.Executor.Backend != "local" {
		t.Fatalf("selection=%+v", selected)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("scratch allocations left behind: %v %v", entries, err)
	}
}

func TestRuntimeFallbackOnlyAfterCleanOptionalFailure(t *testing.T) {
	for _, tc := range []struct {
		name      string
		policy    feature.Policy
		fatal     bool
		calls     int
		wantError bool
	}{
		{"optional retries", feature.Auto, false, 2, false},
		{"required retained", feature.Required, false, 1, true},
		{"cleanup stops retries", feature.Auto, true, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := baselineRuntimeConfig()
			cfg.Network.NetNS = tc.policy
			facts := hostfacts.Collect()
			facts.EffectiveCaps = 0
			calls := 0
			cleanupErr := errors.New("namespace cleanup failed")
			probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, s RuntimeSelection, _ *hostnetwork.PortManager) (CombinationAttempt, error) {
				calls++
				a := CombinationAttempt{Filesystem: s.Files.Name(), Network: string(s.Network.Level), Executor: "local", Identity: s.Identity.Effective}
				if tc.fatal {
					return a, &ProbeCleanupError{Err: cleanupErr}
				}
				if s.Network.Level == network.Private {
					a.Error = "namespace and environment did not compose"
				}
				return a, nil
			}
			result, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", cfg, nil, probe)
			if (err != nil) != tc.wantError || calls != tc.calls {
				t.Fatalf("calls=%d error=%v result=%+v", calls, err, result)
			}
			if tc.fatal && !errors.Is(err, cleanupErr) {
				t.Fatalf("lost cleanup error: %v", err)
			}
			if err == nil && (result.Network.Level != network.Shared || result.Network.NetNS != feature.Auto || result.Attempts[0].Error == "") {
				t.Fatalf("fallback hid policy or attempt: %+v", result)
			}
		})
	}
}

func TestRuntimeFailureDoesNotLoopOnBaseline(t *testing.T) {
	facts := hostfacts.Collect()
	facts.EffectiveCaps = 0
	calls := 0
	probe := func(_ context.Context, _ hostfacts.Snapshot, _, _ string, _ RuntimeConfig, _ RuntimeSelection, _ *hostnetwork.PortManager) (CombinationAttempt, error) {
		calls++
		return CombinationAttempt{Filesystem: "direct", Network: "shared", Error: "combined command failed"}, nil
	}
	_, err := resolveRuntime(t.Context(), facts, t.TempDir(), "/bin/bash", baselineRuntimeConfig(), nil, probe)
	if err == nil || !strings.Contains(err.Error(), "no executable Bed combination") {
		t.Fatalf("bad combination: %v", err)
	}
	if calls != 1 {
		t.Fatalf("repeated baseline attempt %d times", calls)
	}
}
