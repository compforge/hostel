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
	"slices"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

// indexOfSeq finds the start of a consecutive argument sequence.
func indexOfSeq(argv []string, seq ...string) int {
	for i := 0; i+len(seq) <= len(argv); i++ {
		if slices.Equal(argv[i:i+len(seq)], seq) {
			return i
		}
	}
	return -1
}

func TestBuildBwrapArgsUsesBedRootAndMasksReexposedSiblings(t *testing.T) {
	argv := buildBwrapArgs("/opt/beds", "/opt/beds/alice/data", "/opt/beds/alice/data/workspace", bedfs.DefaultWorkdir, []string{"/root", "/home"}, nil, "/usr", "/opt")
	bindHome := indexOfSeq(argv, "--bind", "/opt/beds/alice/data", "/")
	runtime := indexOfSeq(argv, "--ro-bind", "/opt", "/opt")
	maskRoot := indexOfSeq(argv, "--tmpfs", "/opt/beds")
	bindWorkspace := indexOfSeq(argv, "--bind", "/opt/beds/alice/data/workspace", bedfs.DefaultWorkdir)
	if bindHome < 0 || !(bindHome < runtime && runtime < maskRoot && maskRoot < bindWorkspace) {
		t.Fatalf("Bed root/runtime/mask order wrong: %v", argv)
	}
	if indexOfSeq(argv, "--ro-bind", "/", "/") >= 0 {
		t.Fatalf("carrier root must never be inherited: %v", argv)
	}
	for _, p := range []string{"/root", "/home", "/tmp", "/mnt"} {
		if indexOfSeq(argv, "--tmpfs", p) >= 0 || indexOfSeq(argv, "--bind", p, p) >= 0 {
			t.Errorf("Bed data must not be hidden or replaced with carrier data: %s argv=%v", p, argv)
		}
	}
	if indexOfSeq(argv, "--chdir", bedfs.DefaultWorkdir) < 0 {
		t.Errorf("missing --chdir %s", bedfs.DefaultWorkdir)
	}
	if !slices.Contains(argv, "--die-with-parent") {
		t.Errorf("missing --die-with-parent")
	}
	if argv[len(argv)-1] != "--" {
		t.Errorf("argv must end with --, got %q", argv[len(argv)-1])
	}
}

// TestBuildBwrapArgsK8sReachable locks in the two flags that let suite work in
// an ordinary (non-privileged) k8s pod: a user namespace (else root bwrap hits
// EPERM on the privileged clone) and NO pid namespace + a bound /proc (else the
// procfs remount fails under k8s's masked /proc). Regressing either silently
// drops suite back to a lower tier on every real cluster.
func TestBuildBwrapArgsK8sReachable(t *testing.T) {
	argv := buildBwrapArgs("/ws", "/ws/b/data", "/ws/b/data/workspace", bedfs.DefaultWorkdir, nil, nil)
	if !slices.Contains(argv, "--unshare-user") {
		t.Errorf("missing --unshare-user (suite needs userns in a non-privileged pod); argv=%v", argv)
	}
	if slices.Contains(argv, "--unshare-pid") {
		t.Errorf("--unshare-pid present: forces a procfs remount that fails under k8s masked /proc")
	}
	if indexOfSeq(argv, "--ro-bind", "/proc", "/proc") < 0 {
		t.Errorf("/proc must be RO-bound (not --proc) to avoid remounting under masked /proc; argv=%v", argv)
	}
	if slices.Contains(argv, "--proc") {
		t.Errorf("--proc present: remounts procfs, which masked /proc forbids")
	}
}

func TestBuildBwrapArgsSharesCarrierSoftware(t *testing.T) {
	argv := buildBwrapArgs("/ws", "/ws/b/data", "/ws/b/data/workspace", bedfs.DefaultWorkdir, nil, nil, "/usr")
	roRoot := indexOfSeq(argv, "--ro-bind", "/usr", "/usr")
	sharedSoftware := indexOfSeq(argv, "--bind", carrierSoftwareRoot, carrierSoftwareRoot)
	if roRoot < 0 || sharedSoftware < 0 || roRoot >= sharedSoftware {
		t.Fatalf("carrier software must be reopened rw after the ro root bind: roRoot=%d sharedSoftware=%d argv=%v", roRoot, sharedSoftware, argv)
	}
}

// The carrier beds root may be /workspace. It is not inherited, so masking that
// spelling would only hide this Bed's own default directory.
func TestBuildBwrapArgsRootEqualsMountPoint(t *testing.T) {
	argv := buildBwrapArgs("/workspace", "/workspace/b1/data", "/workspace/b1/data/workspace", bedfs.DefaultWorkdir, nil, nil)
	mask := indexOfSeq(argv, "--tmpfs", "/workspace")
	bind := indexOfSeq(argv, "--bind", "/workspace/b1/data/workspace", bedfs.DefaultWorkdir)
	if mask >= 0 || bind < 0 {
		t.Fatalf("mask=%d bind=%d argv=%v", mask, bind, argv)
	}
}

func TestBuildBwrapArgsUsesProjectedCwd(t *testing.T) {
	argv := buildBwrapArgs("/ws", "/ws/b/data", "/ws/b/data/workspace", "/tmp/job", nil, nil)
	if indexOfSeq(argv, "--chdir", "/tmp/job") < 0 {
		t.Fatalf("missing projected cwd; argv=%v", argv)
	}
}

func TestBuildBwrapArgsAddsBedMappingsAfterWorkspace(t *testing.T) {
	mapping := model.PathMapping{HostPath: "/volume", BedPath: "/mnt/memory"}
	argv := buildBwrapArgs(
		"/ws", "/ws/b/data", "/ws/b/data/workspace",
		bedfs.DefaultWorkdir, nil,
		[]model.PathMapping{mapping})
	workspaceBind := indexOfSeq(argv, "--bind", "/ws/b/data/workspace", bedfs.DefaultWorkdir)
	mappingBind := indexOfSeq(argv, "--bind", "/volume", "/mnt/memory")
	if workspaceBind < 0 || mappingBind <= workspaceBind {
		t.Fatalf("mapping must be bound after workspace: workspace=%d mapping=%d argv=%v", workspaceBind, mappingBind, argv)
	}
}

func TestDirectUsesCarrierView(t *testing.T) {
	iso := New(hostfacts.Collect(), "shared", t.TempDir())
	if iso.MountsRoot() {
		t.Fatal("direct must not report a workspace mount")
	}
	if iso.Name() != "direct" || !iso.Available() {
		t.Fatalf("direct basics: %s/%v", iso.Name(), iso.Available())
	}
}

func TestBuildBwrapArgsUsesBedMappings(t *testing.T) {
	mappings := []model.PathMapping{
		{HostPath: "/volumes/project", BedPath: "/project"},
		{HostPath: "/usr/local/reference", BedPath: "/reference", ReadOnly: true},
	}
	argv := buildBwrapArgs("/beds", "/beds/b/data", "/beds/b/data/workspace", "/project", nil, mappings)
	if indexOfSeq(argv, "--bind", "/volumes/project", "/project") < 0 {
		t.Fatalf("missing RW mapping: %v", argv)
	}
	if indexOfSeq(argv, "--ro-bind", "/usr/local/reference", "/reference") < 0 {
		t.Fatalf("missing RO mapping: %v", argv)
	}
	if indexOfSeq(argv, "--ro-bind", "/usr/local/reference", "/usr/local/reference") < 0 {
		t.Fatalf("carrier path bypasses read-only mapping: %v", argv)
	}
	if indexOfSeq(argv, "--chdir", "/project") < 0 {
		t.Fatalf("missing mapped cwd: %v", argv)
	}
}
