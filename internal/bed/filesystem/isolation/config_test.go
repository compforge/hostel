package isolation

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestOffFeaturesDoNotExecuteHelpers(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "executed")
	helper := filepath.Join(t.TempDir(), "bwrap")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\ntouch '"+marker+"'\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	config := Config{Level: "private", Bwrap: tool.Off, Landlock: tool.Off, UID: tool.Off, PRoot: tool.Off, Pathshim: tool.Off}
	iso, err := Resolve(hostfacts.Snapshot{BwrapPath: helper}, config, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if iso.Name() != "direct" {
		t.Fatalf("selected %s", iso.Name())
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("disabled bwrap was executed")
	}
	for name, status := range iso.(Report).Diagnostics().Tools {
		if status.Policy != tool.Off || status.Probe != "not_probed" || status.Selected || status.Reason != "disabled_by_config" {
			t.Fatalf("%s: %+v", name, status)
		}
	}
}
func TestRequiredBoundaryCannotSilentlyDegrade(t *testing.T) {
	_, err := Resolve(hostfacts.Snapshot{}, Config{Bwrap: tool.Required, Landlock: tool.Off, UID: tool.Off, PRoot: tool.Off, Pathshim: tool.Off}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "filesystem.bwrap required") {
		t.Fatalf("required missing bwrap: %v", err)
	}
}
func TestFeatureConflicts(t *testing.T) {
	for _, c := range []Config{
		{Bwrap: tool.Required, UID: tool.Required},
		{Landlock: tool.Required, UID: tool.Required},
		{PRoot: tool.Required, Pathshim: tool.Required},
		{Level: "confined", Bwrap: tool.Required},
		{Level: "shared", UID: tool.Required},
		{Bwrap: tool.Required, Pathshim: tool.Required},
		{Bwrap: "typo"},
	} {
		if err := c.Validate(); err == nil {
			t.Errorf("accepted %+v", c)
		}
	}
}

func TestRequiredFeatureWinsOverAutomaticPriority(t *testing.T) {
	candidates := []Boundary{fakeMech{"bwrap", Private, true}, fakeMech{"landlock", Confined, true}, fakeMech{"uid", Confined, true}}
	for _, tc := range []struct {
		config Config
		want   string
	}{
		{Config{Level: "confined"}, "landlock"},
		{Config{UID: tool.Required}, "uid"},
		{Config{Landlock: tool.Required}, "landlock"},
		{Config{Bwrap: tool.Off, Landlock: tool.Off}, "uid"},
		{Config{Pathshim: tool.Required}, "landlock"},
	} {
		chosen, _ := selectBoundary(tc.config, candidates)
		if chosen.Name() != tc.want {
			t.Fatalf("config=%+v selected=%s want=%s", tc.config, chosen.Name(), tc.want)
		}
	}
}
func TestRequiredPathshimOverridesAvailablePRoot(t *testing.T) {
	proot := fakeProot(t)
	pathshim := fakePathshim(t, "bind-view", 0)
	t.Setenv("PATH", filepath.Dir(proot)+string(os.PathListSeparator)+filepath.Dir(pathshim))
	zero := 0
	probes := map[string]hostfacts.ProbeReport{}
	_, report := resolveProcessViewWithConfig(direct{}, t.TempDir(), hostfacts.ProbeReport{Attempted: true, ExitCode: &zero}, probes, Config{Pathshim: tool.Required})
	if !probes["proot"].Succeeded() || !probes["pathshim"].Succeeded() {
		t.Fatal("fixture did not supply two available views")
	}
	if report.Mode != "pathshim" {
		t.Fatalf("required pathshim replaced by %s", report.Mode)
	}
}
