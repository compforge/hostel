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
	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// This file has no build tag: the argv builder is pure string assembly so its
// tests run on every platform (the exec-ing side lives in bwrap_linux.go).

// carrierSoftwareRoot is shared by every bed in the carrier. Runtime overlays
// are otherwise read-only under private files, so it is re-bound read-write for
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
//  2. Bind bed_home to /: command, session and Service data share BedFS paths.
//  3. Overlay existing carrier runtime directories read-only; /usr/local
//     remains the carrier-wide writable software installation directory.
//  4. Fresh /dev and existing read-only /proc (no procfs remount under K8s).
//  5. Mask sensitive paths only if a runtime overlay re-exposed them.
//  6. Bind workspace and explicit mappings after masks.
//  7. --chdir <process cwd>, --die-with-parent, --
//
// maskPaths are host paths that exist. Environment ownership lives in bed's
// process-env builder, so isolation mechanisms never inherit or filter it.
// +why=`A private internal data mount only fixes structured API paths; native processes issue literal absolute paths. The Bed data root must be the default process root, with explicit runtime overlays instead of an inherited carrier root.`
func buildBwrapArgs(bedsRoot, bedHome, workspace string, cwd string, maskPaths []string, mappings []model.PathMapping, runtimeRoots ...string) []string {
	mounts := []hostfs.Mount{
		{Kind: hostfs.Bind, Source: bedHome, Target: "/"},
	}
	for _, p := range runtimeRoots {
		mounts = append(mounts, hostfs.Mount{Kind: hostfs.ReadOnlyBind, Source: p, Target: p})
	}
	if inRuntime(carrierSoftwareRoot, runtimeRoots) {
		mounts = append(mounts, hostfs.Mount{Kind: hostfs.Bind, Source: carrierSoftwareRoot, Target: carrierSoftwareRoot})
	}
	mounts = append(mounts,
		hostfs.Mount{Kind: hostfs.Dev, Target: "/dev"},
		hostfs.Mount{Kind: hostfs.ReadOnlyBind, Source: "/proc", Target: "/proc"},
	)
	for _, p := range append([]string{bedsRoot}, maskPaths...) {
		if inRuntime(p, runtimeRoots) {
			mounts = append(mounts, hostfs.Mount{Kind: hostfs.Tmpfs, Target: p})
		}
	}
	mounts = append(mounts,
		hostfs.Mount{Kind: hostfs.Bind, Source: workspace, Target: bedfs.DefaultWorkdir},
	)
	for _, m := range mappings {
		kind := hostfs.Bind
		if m.ReadOnly {
			kind = hostfs.ReadOnlyBind
			// /usr/local is otherwise rebound writable for shared software installs.
			// Protect the carrier spelling too, or the process could bypass ReadOnly.
			if model.PathsOverlap(m.HostPath, carrierSoftwareRoot) {
				mounts = append(mounts, hostfs.Mount{Kind: hostfs.ReadOnlyBind, Source: m.HostPath, Target: m.HostPath})
			}
		}
		mounts = append(mounts, hostfs.Mount{Kind: kind, Source: m.HostPath, Target: m.BedPath})
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
