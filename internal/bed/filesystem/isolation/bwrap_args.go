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
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// This file has no build tag: the argv builder is pure string assembly so its
// tests run on every platform (the exec-ing side lives in bwrap_linux.go).

// bwrapBedHomeMountPoint is the mechanism-private projection of bed_home.
// It lives under bwrap's private /tmp because the carrier root is read-only;
// callers never need to know it and use BedFS client paths instead.
const bwrapBedHomeMountPoint = "/tmp/.hostel/bed"

// carrierSoftwareRoot is shared by every bed in the carrier. The host root is
// otherwise read-only under suite, so this path must be re-bound read-write for
// installs made by one command to remain available to later commands and beds.
// The carrier image owns its permissions and package-manager environment.
const carrierSoftwareRoot = "/usr/local"

// buildBwrapArgs assembles the bwrap argv (between the binary and the user
// command). Segment order is a contract — bwrap applies mounts in argv order
// and later mounts cover earlier ones (the masking below depends on it):
//
//  1. Namespaces. --unshare-user is REQUIRED to reach suite in an ordinary
//     (non-privileged) k8s pod: bwrap running as root would otherwise try a
//     privileged clone(NEWNS) and hit EPERM (no CAP_SYS_ADMIN); with a user
//     namespace it needs no host privilege, only the kernel's unprivileged
//     userns. We deliberately DON'T --unshare-pid: k8s masks /proc, and inside
//     a userns the kernel forbids mounting a fresh procfs over a masked one
//     ("mount proc: Operation not permitted"). A pid namespace is defence-in-
//     depth, not part of the data-isolation (path) contract, so we drop it and
//     bind the host /proc read-only instead. (uts/ipc unshares are cheap and
//     don't touch mounts.)
//  2. --ro-bind / /            — RO host root: toolchains stay usable
//  3. --bind /usr/local /usr/local — carrier-wide shared software, writable
//  4. --dev /dev, --ro-bind /proc /proc, --tmpfs /tmp — fresh dev/tmp; /proc
//     is bound (not --proc) so no procfs remount is needed under masked /proc
//  5. Masking: --tmpfs over workspaceRoot (sibling beds cease to exist),
//     and over each maskPath (host user data / mounted secrets)
//  6. Create the private BedFS mount point under /tmp, then bind bed_home
//     there. This gives every structured BedFS path an Executor-visible name.
//  7. Bind workspace and configured BedFS projections to their stable process
//     paths (must come AFTER the workspaceRoot mask).
//  8. --chdir <process cwd>, --die-with-parent, --
//
// maskPaths are host paths that exist. Environment ownership lives in bed's
// process-env builder, so isolation mechanisms never inherit or filter it.
func buildBwrapArgs(workspaceRoot, bedHome, workspace string, projections []bedfs.PathProjection, cwd string, maskPaths []string) []string {
	// Bed policy owns this mount order: mask siblings and credentials before
	// exposing the selected data roots. The host mechanism only encodes the plan.
	mounts := []hostfs.Mount{
		{Kind: hostfs.ReadOnlyBind, Source: "/", Target: "/"},
		{Kind: hostfs.Bind, Source: carrierSoftwareRoot, Target: carrierSoftwareRoot},
		{Kind: hostfs.Dev, Target: "/dev"},
		{Kind: hostfs.ReadOnlyBind, Source: "/proc", Target: "/proc"},
		{Kind: hostfs.Tmpfs, Target: "/tmp"},
		{Kind: hostfs.Tmpfs, Target: workspaceRoot},
	}
	for _, p := range maskPaths {
		mounts = append(mounts, hostfs.Mount{Kind: hostfs.Tmpfs, Target: p})
	}
	mounts = append(mounts,
		hostfs.Mount{Kind: hostfs.Directory, Target: "/tmp/.hostel"},
		hostfs.Mount{Kind: hostfs.Directory, Target: bwrapBedHomeMountPoint},
		hostfs.Mount{Kind: hostfs.Bind, Source: bedHome, Target: bwrapBedHomeMountPoint},
		hostfs.Mount{Kind: hostfs.Bind, Source: workspace, Target: bedfs.WorkspacePath},
	)
	for _, p := range projections {
		mounts = append(mounts, hostfs.Mount{Kind: hostfs.Bind, Source: p.CarrierPath(bedHome), Target: p.ProcessPath})
	}
	argv := (hostfs.Bubblewrap{UserNamespace: true, UTSNamespace: true, IPCNamespace: true, Mounts: mounts, Cwd: cwd, DieWithParent: true}).Args()
	return argv
}

// defaultMaskCandidates are host paths masked when they exist: host user data
// and platform-mounted credentials (e.g. K8s serviceaccount tokens). Secrets
// belong to hostel/managed services, never to arbitrary bed code.
var defaultMaskCandidates = []string{
	"/root",
	"/home",
	"/run/secrets",
	"/var/run/secrets",
}
