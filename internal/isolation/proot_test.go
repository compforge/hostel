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
)

func TestWorkspaceViewFallsBackFromPathshimToProot(t *testing.T) {
	root := t.TempDir()
	probes := map[string]ProbeReport{}
	exitCode := 0
	workspaceView, report := resolveWorkspaceView(
		direct{},
		root,
		fakePathshim(t, "passthrough", 1),
		fakeProot(t),
		ProbeReport{Attempted: true, ExitCode: &exitCode},
		probes,
	)
	if report.Mode != "proot" || !report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	iso := &resolved{boundary: direct{}, workspace: workspaceView}
	if iso.Name() != "direct" || iso.Level() != Dorm || iso.WorkspaceMounted() {
		t.Fatalf("proot changed isolation facts: %s/%s mount=%v", iso.Name(), iso.Level(), iso.WorkspaceMounted())
	}
	if !probes["pathshim"].failed() || !probes["proot"].succeeded() {
		t.Fatalf("fallback probes = %+v", probes)
	}

	fs := newTestFS(t, root)
	workspace := fs.Workspace()
	cmd := exec.Command("/bin/sh", "-c", "pwd")
	if err := iso.Wrap(cmd, fs, filepath.Join(workspace, "sub")); err != nil {
		t.Fatal(err)
	}
	want := []string{probes["proot"].ResolvedPath, "-v", "-1", "-b", workspace + ":/workspace!", "-w", "/workspace/sub"}
	if len(cmd.Args) < len(want) || !slices.Equal(cmd.Args[:len(want)], want) {
		t.Fatalf("proot argv = %v, want prefix %v", cmd.Args, want)
	}
}

func TestWorkspaceViewProbesSupportedCandidatesBeforeApplyingPriority(t *testing.T) {
	root := t.TempDir()
	probes := map[string]ProbeReport{}
	exitCode := 0
	workspaceView, report := resolveWorkspaceView(
		direct{},
		root,
		fakePathshim(t, "bind-view", 0),
		fakeProot(t),
		ProbeReport{Attempted: true, ExitCode: &exitCode},
		probes,
	)
	if report.Mode != "proot" || !report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	if _, ok := workspaceView.(*prootView); !ok {
		t.Fatalf("selected workspace backend = %T, want proot", workspaceView)
	}
	if !probes["pathshim"].succeeded() || !probes["proot"].succeeded() {
		t.Fatalf("candidate probes = %+v", probes)
	}
}

func TestWorkspaceViewUsesPathshimWhenProotPrerequisiteFails(t *testing.T) {
	root := t.TempDir()
	probes := map[string]ProbeReport{}
	workspaceView, report := resolveWorkspaceView(
		direct{},
		root,
		fakePathshim(t, "bind-view", 0),
		fakeProot(t),
		ProbeReport{Attempted: true, Error: "ptrace TRACEME: operation not permitted"},
		probes,
	)
	if report.Mode != "pathshim" || !report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	if _, ok := workspaceView.(*pathshimView); !ok {
		t.Fatalf("selected workspace backend = %T, want pathshim", workspaceView)
	}
	if !probes["pathshim"].succeeded() || probes["proot"].Attempted {
		t.Fatalf("candidate probes = %+v", probes)
	}
}

func TestWorkspaceViewDoesNotTryProotWhenPtraceProbeFails(t *testing.T) {
	root := t.TempDir()
	probes := map[string]ProbeReport{}
	_, report := resolveWorkspaceView(
		direct{},
		root,
		fakePathshim(t, "passthrough", 1),
		fakeProot(t),
		ProbeReport{Attempted: true, Error: "ptrace TRACEME: operation not permitted"},
		probes,
	)
	if report.Mode != "carrier" || report.Available || !strings.Contains(report.Reason, "operation not permitted") {
		t.Fatalf("workspace view = %+v", report)
	}
	if probes["proot"].Attempted {
		t.Fatalf("proot probe must stay unattempted: %+v", probes["proot"])
	}
}

func TestCarrierWorkspaceNeverAdvertisesCanonicalPath(t *testing.T) {
	workspace, report := resolveWorkspaceView(direct{}, t.TempDir(), "", "", ProbeReport{}, map[string]ProbeReport{})
	if workspace.Mode() != "carrier" || report.Mode != "carrier" || report.Available || report.Reason != "" {
		t.Fatalf("carrier workspace view = backend=%s report=%+v", workspace.Mode(), report)
	}
}

func TestProotRunsInsideSelectedRoomMechanism(t *testing.T) {
	root := t.TempDir()
	fs := newTestFS(t, root)
	runtime := &resolved{boundary: prefixRoom{}, workspace: &prootView{path: "/usr/bin/proot"}}
	cmd := exec.Command("/bin/sh", "-c", "true")
	if err := runtime.Wrap(cmd, fs, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/room-helper", "--", "/usr/bin/proot", "-v", "-1"}
	if len(cmd.Args) < len(want) || !slices.Equal(cmd.Args[:len(want)], want) {
		t.Fatalf("wrapper order = %v, want prefix %v", cmd.Args, want)
	}
}

var _ workspaceBackend = (*prootView)(nil)
var _ Runtime = (*resolved)(nil)

func fakeProot(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "proot")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'proot-view\\n/workspace\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}
