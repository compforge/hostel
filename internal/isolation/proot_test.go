// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bedfs"
)

func TestWorkspaceViewProbesSupportedHelpersBeforeApplyingPriority(t *testing.T) {
	root := t.TempDir()
	pathshim := fakePathshim(t, "bind-view", 0)
	proot := fakeProot(t)
	t.Setenv("PATH", filepath.Dir(proot)+string(os.PathListSeparator)+filepath.Dir(pathshim))
	probes := map[string]ProbeReport{}
	exitCode := 0

	workspace, report := resolveWorkspaceView(
		direct{}, root, nil,
		ProbeReport{Attempted: true, ExitCode: &exitCode},
		probes,
	)
	if report.Mode != "proot" || !report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	if _, ok := workspace.(*prootView); !ok {
		t.Fatalf("selected workspace backend = %T, want proot", workspace)
	}
	if !probes["pathshim"].succeeded() || !probes["proot"].succeeded() {
		t.Fatalf("candidate probes = %+v", probes)
	}
	for _, name := range []string{"pathshim", "proot"} {
		probe := probes[name]
		if probe.ConfiguredPath != name || probe.ResolvedPath == "" || !probe.Exists || !probe.Executable {
			t.Fatalf("%s discovery = %+v", name, probe)
		}
	}
}

func TestWorkspaceViewUsesPathshimWhenPtraceFails(t *testing.T) {
	root := t.TempDir()
	pathshim := fakePathshim(t, "bind-view", 0)
	proot := fakeProot(t)
	t.Setenv("PATH", filepath.Dir(proot)+string(os.PathListSeparator)+filepath.Dir(pathshim))
	probes := map[string]ProbeReport{}

	workspace, report := resolveWorkspaceView(
		direct{}, root, nil,
		ProbeReport{Attempted: true, Error: "ptrace TRACEME: operation not permitted"},
		probes,
	)
	if report.Mode != "pathshim" || !report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	if _, ok := workspace.(*pathshimView); !ok {
		t.Fatalf("selected workspace backend = %T, want pathshim", workspace)
	}
	prootProbe := probes["proot"]
	if !prootProbe.Exists || !prootProbe.Executable || prootProbe.ResolvedPath == "" || prootProbe.Attempted || prootProbe.Error != "" {
		t.Fatalf("proot discovery must survive ptrace failure: %+v", prootProbe)
	}
}

func TestWorkspaceViewReportsMissingHelpers(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	probes := map[string]ProbeReport{}
	_, report := resolveWorkspaceView(
		direct{}, t.TempDir(), nil,
		ProbeReport{Attempted: true, Error: "ptrace denied"},
		probes,
	)
	if report.Mode != "carrier" || report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	for _, name := range []string{"pathshim", "proot"} {
		probe := probes[name]
		if probe.ConfiguredPath != name || probe.ResolvedPath != "" || probe.Exists || probe.Executable || probe.Attempted || probe.Error == "" {
			t.Fatalf("missing %s probe = %+v", name, probe)
		}
	}
}

func TestWorkspaceViewReportsPresentNonExecutableHelper(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, prootCommand), []byte("not executable"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
	probes := map[string]ProbeReport{}
	exitCode := 0
	_, _ = resolveWorkspaceView(
		direct{}, t.TempDir(), nil,
		ProbeReport{Attempted: true, ExitCode: &exitCode},
		probes,
	)
	probe := probes["proot"]
	if !probe.Exists || probe.Executable || probe.ResolvedPath == "" || probe.Attempted || probe.Error == "" {
		t.Fatalf("non-executable proot probe = %+v", probe)
	}
}

func TestProotWrapsWorkspaceAndConfiguredProjections(t *testing.T) {
	root := t.TempDir()
	fs := newTestFS(t, root)
	projection, err := bedfs.NewPathProjection("/memory", "/mnt/memory")
	if err != nil {
		t.Fatal(err)
	}
	runtime := &resolved{
		boundary:  prefixRoom{},
		workspace: &prootView{path: "/usr/bin/proot", projections: []bedfs.PathProjection{projection}},
	}
	cmd := exec.Command("/bin/sh", "-c", "true")
	if err := runtime.Wrap(cmd, fs, filepath.Join(fs.Workspace(), "sub")); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/room-helper", "--",
		"/usr/bin/proot", "-v", "-1",
		"-b", fs.Workspace() + ":/workspace!",
		"-b", filepath.Join(fs.Home(), "memory") + ":/mnt/memory!",
		"-w", "/workspace/sub",
	}
	if len(cmd.Args) < len(want) || !slices.Equal(cmd.Args[:len(want)], want) {
		t.Fatalf("wrapper argv = %v, want prefix %v", cmd.Args, want)
	}
}

func TestWorkspaceViewFallsBackToCarrierWhenBothHelpersFail(t *testing.T) {
	pathshim := fakePathshim(t, "passthrough", 1)
	proot := fakeFailingProot(t)
	t.Setenv("PATH", filepath.Dir(proot)+string(os.PathListSeparator)+filepath.Dir(pathshim))
	probes := map[string]ProbeReport{}
	exitCode := 0
	_, report := resolveWorkspaceView(
		direct{}, t.TempDir(), nil,
		ProbeReport{Attempted: true, ExitCode: &exitCode},
		probes,
	)
	if report.Mode != "carrier" || report.Available || !strings.Contains(report.Reason, "pathshim") || !strings.Contains(report.Reason, "proot") {
		t.Fatalf("workspace view = %+v", report)
	}
}

func fakeProot(t *testing.T) string {
	t.Helper()
	return fakeNamedHelper(t, prootCommand, "#!/bin/sh\nprintf 'proot-view\\n/workspace\\n'\n")
}

func fakeFailingProot(t *testing.T) string {
	t.Helper()
	return fakeNamedHelper(t, prootCommand, "#!/bin/sh\nprintf 'proot failed\\n' >&2\nexit 1\n")
}

func fakeNamedHelper(t *testing.T, name, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

var _ workspaceBackend = (*prootView)(nil)
var _ Isolator = (*resolved)(nil)
