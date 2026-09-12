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

package facts

import (
	"os"
	"os/exec"
	"runtime"
)

// Snapshot contains boot-time host observations. A fact can rule a mechanism
// out, but only an execution probe can establish that the mechanism works.
type Snapshot struct {
	KernelRelease      string `json:"kernel_release"`      // uname release ("" off Linux)
	EUID               int    `json:"euid"`                // effective uid of the observing process
	EGID               int    `json:"egid"`                // effective gid of the observing process
	EffectiveCaps      uint64 `json:"effective_caps"`      // CapEff bitmask from /proc/self/status
	LandlockABI        int    `json:"landlock_abi"`        // Landlock ABI version; 0 = absent/unsupported
	BwrapPath          string `json:"bwrap_path"`          // resolved bubblewrap binary ("" = not found)
	UnprivilegedUserns bool   `json:"unprivileged_userns"` // kernel hint: unprivileged user namespaces allowed
	CgroupV2           bool   `json:"cgroup_v2"`           // unified cgroup v2 hierarchy present
	// AppArmorProfile is the process confinement label, empty if unconfined
	// or unavailable. Policy owners decide what this implies for their operation.
	AppArmorProfile string `json:"apparmor_profile"`

	// System preserves the detailed observations that do not belong in
	// /healthz's compact host summary.
	System           SystemFacts `json:"-"`
	BwrapLookupError string      `json:"-"`
}

// HasCap reports whether capability bit (e.g. capSETUID) is in the effective
// set. bit must be < 64; a larger value shifts out to a well-defined 0 (Go
// shift semantics), so HasCap returns false rather than crashing — but that's a
// misuse, not a real "cap absent". Always false off Linux, where EffectiveCaps
// is 0.
func (f Snapshot) HasCap(bit uint) bool { return f.EffectiveCaps&(1<<bit) != 0 }

// Collect probes the host once at boot. The bubblewrap lookup is
// cross-platform; the kernel/caps/Landlock/userns/cgroup facts are filled
// per-OS by osFacts (all zero off Linux).
func Collect() Snapshot {
	f := osFacts()
	f.EUID = os.Geteuid()
	f.EGID = os.Getegid()
	f.System.Runtime.OS = runtime.GOOS
	f.System.Runtime.Arch = runtime.GOARCH
	f.System.Runtime.KernelRelease = f.KernelRelease
	f.System.Process.EUID = f.EUID
	f.System.Process.EGID = f.EGID
	if p, err := exec.LookPath("bwrap"); err == nil {
		f.BwrapPath = p
	} else {
		f.BwrapLookupError = err.Error()
	}
	return f
}
