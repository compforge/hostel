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

// This file has no build tag: the argv builder is pure string assembly so its
// tests run on every platform (the exec-ing side lives in bwrap_linux.go).

// BwrapMountPoint is where a bed's workspace is bind-mounted inside the
// sandbox. Fixed and canonical: it makes shell paths and file-API paths the
// same string, matching OpenSandbox SDK expectations. Must stay equal to
// fsops.VirtualPrefix (the same contract seen from the file-API side); the
// packages deliberately don't import each other, so keep the two constants
// in sync by hand.
const BwrapMountPoint = "/workspace"

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
//  6. --bind <bed workspace> /workspace — own data only, canonical name
//     (must come AFTER the workspaceRoot mask so it re-opens only our dir)
//  7. --chdir /workspace, --die-with-parent, --
//
// maskPaths are host paths that exist. Environment ownership lives in bed's
// process-env builder, so isolation mechanisms never inherit or filter it.
func buildBwrapArgs(workspaceRoot, wsPath string, maskPaths []string) []string {
	argv := []string{
		// 1.
		"--unshare-user", "--unshare-uts", "--unshare-ipc",
		// 2.
		"--ro-bind", "/", "/",
		// 3.
		"--bind", carrierSoftwareRoot, carrierSoftwareRoot,
		// 4.
		"--dev", "/dev",
		"--ro-bind", "/proc", "/proc",
		"--tmpfs", "/tmp",
	}
	// 5. Mask BEFORE binding our workspace: if workspaceRoot were masked after,
	// the tmpfs would swallow the bed's own mount too.
	argv = append(argv, "--tmpfs", workspaceRoot)
	for _, p := range maskPaths {
		argv = append(argv, "--tmpfs", p)
	}
	// 6.
	argv = append(argv, "--bind", wsPath, BwrapMountPoint)
	// 7.
	argv = append(argv,
		"--chdir", BwrapMountPoint,
		"--die-with-parent",
		"--",
	)
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
