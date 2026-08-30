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

	"github.com/qiankunli/hostel/internal/bedfs"
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

func TestBuildBwrapArgsMasksSiblingsBeforeBind(t *testing.T) {
	argv := buildBwrapArgs("/ws-root", "/ws-root/alice/data", "/ws-root/alice/data/workspace", bedfs.WorkspacePath, []string{"/root", "/home"})

	maskRoot := indexOfSeq(argv, "--tmpfs", "/ws-root")
	bindHome := indexOfSeq(argv, "--bind", "/ws-root/alice/data", bwrapBedHomeMountPoint)
	bindWorkspace := indexOfSeq(argv, "--bind", "/ws-root/alice/data/workspace", bedfs.WorkspacePath)
	roRoot := indexOfSeq(argv, "--ro-bind", "/", "/")
	if roRoot < 0 || maskRoot < 0 || bindHome < 0 || bindWorkspace < 0 {
		t.Fatalf("missing segments: roRoot=%d maskRoot=%d bindHome=%d bindWorkspace=%d\nargv=%v", roRoot, maskRoot, bindHome, bindWorkspace, argv)
	}
	// Order is the security property: RO root first, then the mask swallowing
	// all sibling beds, then re-binding only our BedFS views.
	if !(roRoot < maskRoot && maskRoot < bindHome && bindHome < bindWorkspace) {
		t.Fatalf("mask/bind order wrong: roRoot=%d maskRoot=%d bindHome=%d bindWorkspace=%d", roRoot, maskRoot, bindHome, bindWorkspace)
	}
	if indexOfSeq(argv, "--dir", "/tmp/.hostel") < 0 || indexOfSeq(argv, "--dir", bwrapBedHomeMountPoint) < 0 {
		t.Fatalf("private BedFS mount points must be created inside /tmp; argv=%v", argv)
	}

	for _, p := range []string{"/root", "/home"} {
		if indexOfSeq(argv, "--tmpfs", p) < 0 {
			t.Errorf("sensitive path %s not masked; argv=%v", p, argv)
		}
	}
	if indexOfSeq(argv, "--chdir", bedfs.WorkspacePath) < 0 {
		t.Errorf("missing --chdir %s", bedfs.WorkspacePath)
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
	argv := buildBwrapArgs("/ws", "/ws/b/data", "/ws/b/data/workspace", bedfs.WorkspacePath, nil)
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
	argv := buildBwrapArgs("/ws", "/ws/b/data", "/ws/b/data/workspace", bedfs.WorkspacePath, nil)
	roRoot := indexOfSeq(argv, "--ro-bind", "/", "/")
	sharedSoftware := indexOfSeq(argv, "--bind", carrierSoftwareRoot, carrierSoftwareRoot)
	if roRoot < 0 || sharedSoftware < 0 || roRoot >= sharedSoftware {
		t.Fatalf("carrier software must be reopened rw after the ro root bind: roRoot=%d sharedSoftware=%d argv=%v", roRoot, sharedSoftware, argv)
	}
}

// The workspace root may itself be /workspace (default config). The sequence
// must still be mask-then-bind so the bed's own dir replaces the mount point.
func TestBuildBwrapArgsRootEqualsMountPoint(t *testing.T) {
	argv := buildBwrapArgs("/workspace", "/workspace/b1/data", "/workspace/b1/data/workspace", bedfs.WorkspacePath, nil)
	mask := indexOfSeq(argv, "--tmpfs", "/workspace")
	bind := indexOfSeq(argv, "--bind", "/workspace/b1/data/workspace", bedfs.WorkspacePath)
	if mask < 0 || bind < 0 || mask >= bind {
		t.Fatalf("mask=%d bind=%d argv=%v", mask, bind, argv)
	}
}

func TestBuildBwrapArgsUsesProjectedCwd(t *testing.T) {
	argv := buildBwrapArgs("/ws", "/ws/b/data", "/ws/b/data/workspace", "/tmp/.hostel/bed/tmp/job", nil)
	if indexOfSeq(argv, "--chdir", "/tmp/.hostel/bed/tmp/job") < 0 {
		t.Fatalf("missing projected cwd; argv=%v", argv)
	}
}

func TestDirectUsesCarrierView(t *testing.T) {
	iso := New("direct", "/anything")
	if iso.WorkspaceMounted() {
		t.Fatal("direct must not report a workspace mount")
	}
	if iso.Name() != "direct" || !iso.Available() {
		t.Fatalf("direct basics: %s/%v", iso.Name(), iso.Available())
	}
}
