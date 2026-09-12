package filesystem

import (
	"os/exec"
	"slices"
	"testing"
)

func TestViewsPreserveArgumentsAndCallerPaths(t *testing.T) {
	mappings := []Mapping{{Source: "/data with spaces", Target: "/project"}, {Source: "/cache", Target: "/tools"}}
	original := []string{"/bin/sh", "-c", "printf '%s' '$literal'"}
	for _, kind := range []string{"pathshim", "proot"} {
		t.Run(kind, func(t *testing.T) {
			cmd := exec.Command(original[0], original[1:]...)
			cmd.Dir = "/carrier-cwd"
			if kind == "pathshim" {
				(Pathshim{Path: "/helpers/pathshim"}).Wrap(cmd, mappings, "/project")
			} else {
				(PRoot{Path: "/helpers/proot"}).Wrap(cmd, mappings, "/project")
			}
			if !slices.Equal(cmd.Args[len(cmd.Args)-len(original):], original) {
				t.Fatalf("command changed: %q", cmd.Args)
			}
			if cmd.Dir != "/carrier-cwd" {
				t.Fatalf("carrier cwd changed: %s", cmd.Dir)
			}
			sourceTarget := "/data with spaces:/project"
			if kind == "proot" {
				sourceTarget += "!"
			}
			if !slices.Contains(cmd.Args, sourceTarget) {
				t.Fatalf("mapping split or changed: %q", cmd.Args)
			}
			for _, arg := range cmd.Args {
				if arg == "/workspace" {
					t.Fatal("mechanism injected a domain path")
				}
			}
		})
	}
}
func TestPathshimProbeUsesNativeSubcommand(t *testing.T) {
	cmd := (Pathshim{Path: "/pathshim"}).ProbeCommand(t.Context(), []Mapping{{Source: "/source", Target: "/target"}})
	want := []string{"/pathshim", "probe", "--bind", "/source:/target"}
	if !slices.Equal(cmd.Args, want) {
		t.Fatalf("probe argv = %q", cmd.Args)
	}
}
