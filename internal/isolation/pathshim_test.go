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
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bedfs"
)

func TestPathshimViewWrapsWorkspaceWithoutChangingIsolation(t *testing.T) {
	root := t.TempDir()
	probe := fakePathshim(t, "bind-view", 0)
	projection, err := bedfs.NewPathProjection("/memory", "/mnt/memory")
	if err != nil {
		t.Fatal(err)
	}
	iso := New("dorm", root, WithPathshim(probe), WithPathProjections([]bedfs.PathProjection{projection}))
	report := iso.(Report).WorkspaceView()
	if report.Mode != "pathshim" || !report.Available {
		t.Fatalf("workspace view = %+v", report)
	}
	if iso.Name() != "direct" || iso.Level() != Dorm || iso.WorkspaceMounted() {
		t.Fatalf("pathshim changed isolation facts: %s/%s mount=%v", iso.Name(), iso.Level(), iso.WorkspaceMounted())
	}

	home := filepath.Join(root, "bed", "data")
	workspace := filepath.Join(home, "workspace")
	if err := os.MkdirAll(filepath.Join(workspace, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := bedfs.New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	if err := iso.(Preparer).Prepare(fs); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(filepath.Join(home, "memory")); err != nil || !fi.IsDir() {
		t.Fatalf("projection source was not prepared: %v, %v", fi, err)
	}
	cmd := exec.Command("/bin/sh", "-c", "pwd")
	if err := iso.Wrap(cmd, fs, filepath.Join(workspace, "sub")); err != nil {
		t.Fatal(err)
	}
	wantPrefix := []string{
		probe, "--quiet",
		"--bind", workspace + ":/workspace",
		"--bind", filepath.Join(home, "memory") + ":/mnt/memory",
		"--cwd", "/workspace/sub", "--",
	}
	if len(cmd.Args) < len(wantPrefix) || !slices.Equal(cmd.Args[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("pathshim argv = %v, want prefix %v", cmd.Args, wantPrefix)
	}
	if cmd.Dir != filepath.Join(workspace, "sub") {
		t.Fatalf("outer cwd = %q", cmd.Dir)
	}
}

func TestPathshimProbeFailureFallsBackToCarrierView(t *testing.T) {
	root := t.TempDir()
	iso := New("dorm", root, WithPathshim(fakePathshim(t, "passthrough", 1)))
	report := iso.(Report).WorkspaceView()
	if report.Mode != "carrier" || report.Available || !strings.Contains(report.Reason, "passthrough") {
		t.Fatalf("workspace view = %+v", report)
	}
	if iso.View(newTestFS(t, root)).Workspace() == bedfs.WorkspacePath {
		t.Fatal("failed probe must not advertise /workspace")
	}
}

func TestPathshimRunsInsideSelectedRoomMechanism(t *testing.T) {
	root := t.TempDir()
	fs := newTestFS(t, root)
	view := &pathshimView{base: prefixRoom{}, path: "/usr/bin/pathshim"}
	cmd := exec.Command("/bin/sh", "-c", "true")
	if err := view.Wrap(cmd, fs, ""); err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/room-helper", "--", "/usr/bin/pathshim", "--quiet"}
	if len(cmd.Args) < len(want) || !slices.Equal(cmd.Args[:len(want)], want) {
		t.Fatalf("wrapper order = %v, want prefix %v", cmd.Args, want)
	}
}

type prefixRoom struct{}

func (prefixRoom) Name() string                 { return "landlock" }
func (prefixRoom) Level() Level                 { return Room }
func (prefixRoom) Available() bool              { return true }
func (prefixRoom) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (prefixRoom) WorkspaceMounted() bool       { return false }
func (prefixRoom) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	cmd.Args = append([]string{"/usr/bin/room-helper", "--"}, cmd.Args...)
	cmd.Path = "/usr/bin/room-helper"
	cmd.Dir = commandCwd(fs, cwd)
	return nil
}

func fakePathshim(t *testing.T, output string, exitCode int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pathshim")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' %s\nexit %d\n", output, exitCode)
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestFS(t *testing.T, root string) *bedfs.FS {
	t.Helper()
	home := filepath.Join(root, "fallback", "data")
	if err := os.MkdirAll(filepath.Join(home, "workspace"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs, err := bedfs.New(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fs.Close() })
	return fs
}
