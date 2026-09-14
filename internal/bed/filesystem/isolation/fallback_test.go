package isolation

import (
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/internal/feature"
)

func TestCombinationFallbackExhaustsCandidatesWithoutChangingPolicies(t *testing.T) {
	cfg := Config{DedicatedIdentity: true}
	candidates := []Boundary{fakeMech{"bwrap", Private, true}, fakeMech{"landlock", Confined, true}, fakeMech{"uid", Confined, true}}
	var attempts []string
	for {
		boundary, ceiling := selectBoundary(cfg, candidates)
		if ceiling != Private {
			t.Fatal("composition exclusions changed the observed ceiling")
		}
		view := "carrier"
		if boundary.Name() == "bwrap" {
			view = "mount"
		} else if cfg.Excluded["proot"] == "" {
			view = "proot"
		} else if cfg.Excluded["pathshim"] == "" {
			view = "pathshim"
		}
		attempts = append(attempts, boundary.Name()+"/"+view)
		selected := &resolved{boundary: boundary, workspaceView: WorkspaceViewReport{Mode: view}}
		next, ok := NextCombination(cfg, selected, "combined command failed")
		if !ok {
			break
		}
		if !reflect.DeepEqual(cfg.policies(), next.policies()) {
			t.Fatal("fallback rewrote user policy")
		}
		cfg = next
		if len(attempts) > 10 {
			t.Fatal("fallback failed to make progress")
		}
	}
	want := []string{"bwrap/mount", "landlock/proot", "landlock/pathshim", "landlock/carrier", "uid/proot", "uid/pathshim", "uid/carrier", "direct/proot", "direct/pathshim", "direct/carrier"}
	if !reflect.DeepEqual(attempts, want) {
		t.Fatalf("attempts=%v want=%v", attempts, want)
	}
}

func TestCombinationFallbackPreservesRequiredMembersAndInput(t *testing.T) {
	cfg := Config{Landlock: feature.Required, Excluded: map[string]string{"bwrap": "previous failure"}}
	selected := &resolved{boundary: fakeMech{"landlock", Confined, true}, workspaceView: WorkspaceViewReport{Mode: "proot"}}
	next, ok := NextCombination(cfg, selected, "bad combination")
	if !ok || next.Excluded["proot"] == "" || cfg.Excluded["proot"] != "" || next.Landlock != feature.Required {
		t.Fatalf("fallback changed its input or required boundary: before=%+v after=%+v", cfg, next)
	}
	selected.workspaceView.Mode = "carrier"
	if _, ok := NextCombination(next, selected, "still failing"); ok {
		t.Fatal("dropped required boundary")
	}
	cfg = Config{PRoot: feature.Required}
	selected.workspaceView.Mode = "proot"
	next, ok = NextCombination(cfg, selected, "bad combination")
	if !ok || next.Excluded["landlock"] == "" || next.Excluded["proot"] != "" || next.PRoot != feature.Required {
		t.Fatal("required helper was not retained across boundary fallback")
	}
}
