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

//go:build linux

package isolation

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/qiankunli/hostel/internal/bedfs"
)

// bwrap confines each command under bubblewrap. Mount view per
// docs/data.md: RO host root with shared software rw at /usr/local,
// sibling beds masked out of existence, own workspace rw at the canonical
// /workspace, host user data and mounted secrets masked. Process environment
// ownership is enforced before this boundary by bed's process-env builder.
type bwrap struct {
	path        string   // bwrap binary (probed at boot)
	root        string   // parent dir of all bed workspaces (masked in-sandbox)
	maskPaths   []string // existing sensitive host paths to mask (computed once)
	projections []bedfs.PathProjection
}

// newBwrap probes bubblewrap at boot: binary present AND the FULL mount shape
// we will actually use starts (binary-present-but-broken — unprivileged userns
// disabled, or no /workspace mount point on the RO host root — must not count
// as isolated; a partial probe once let healthz report workspace_mount:true
// while every exec failed). On failure it falls back to direct so the daemon
// still boots and /healthz reports the truth.
// Probe pattern borrowed from OpenSandbox execd, extended to the real argv.
func newBwrap(facts HostFacts, workspaceRoot string, projections []bedfs.PathProjection) (Isolator, ProbeReport) {
	path := facts.BwrapPath
	report := ProbeReport{ConfiguredPath: "bwrap", ResolvedPath: path}
	if path == "" {
		report.Error = facts.bwrapLookupError
		return unavailable{name: "bwrap", lvl: Suite}, report
	}

	// bwrap cannot mkdir the mount point inside the read-only root bind, so
	// the canonical /workspace must exist on the HOST. Create it if we can
	// (in a pod hostel usually runs as root); if we can't, the full-shape
	// smoke below fails and we honestly degrade.
	if err := os.MkdirAll(bedfs.WorkspacePath, 0o755); err != nil {
		log.Printf("isolation: cannot ensure mount point %s on host: %v", bedfs.WorkspacePath, err)
	}
	// The workspace root may not exist yet at probe time (the bed manager
	// creates it later); the smoke test masks it, so it must exist now.
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		log.Printf("isolation: cannot create workspace root %s: %v", workspaceRoot, err)
	}

	masks := resolveMaskPaths(defaultMaskCandidates)
	report = bwrapSmoke(path, workspaceRoot, masks, projections)
	report.ConfiguredPath = "bwrap"
	report.ResolvedPath = path
	if report.failed() {
		log.Printf("isolation: bwrap found but unusable (%s)", report.Error)
		// Point the operator at the usual k8s cause: userns is on yet bwrap
		// dies at mount because containerd's default AppArmor profile denies
		// mount(2). Surfaced here AND in /healthz (HostFacts.apparmor_profile)
		// so the fix (an AppArmor-unconfined annotation on the carrier pod) is
		// discoverable without shelling in.
		if facts.AppArmorProfile != "" && facts.UnprivilegedUserns {
			log.Printf("isolation: suite blocked despite unprivileged userns — AppArmor profile %q likely denies mount; "+
				"grant the pod an AppArmor-unconfined annotation to reach suite (else degrading to a lower tier)", facts.AppArmorProfile)
		}
		return unavailable{name: "bwrap", lvl: Suite}, report
	}
	return &bwrap{
		path:        path,
		root:        workspaceRoot,
		maskPaths:   masks,
		projections: append([]bedfs.PathProjection(nil), projections...),
	}, report
}

// resolveMaskPaths filters candidates to existing directories and dedupes them
// by their symlink-resolved real path. The dedup matters on a typical k8s pod
// where /var/run is a symlink to /run: /run/secrets and /var/run/secrets then
// name the SAME directory, and masking both with --tmpfs makes bwrap fail once
// the first tmpfs replaces the shared target ("Can't mkdir /var/run/secrets:
// No such file or directory") — which silently drops the whole carrier from
// suite to a weaker tier. Keeping the first candidate per real path (list order
// = priority) still hides both names through the one surviving mount.
func resolveMaskPaths(candidates []string) []string {
	var masks []string
	seen := make(map[string]bool, len(candidates))
	for _, p := range candidates {
		fi, err := os.Stat(p)
		if err != nil || !fi.IsDir() {
			continue
		}
		real, err := filepath.EvalSymlinks(p)
		if err != nil {
			real = p
		}
		if seen[real] {
			continue
		}
		seen[real] = true
		masks = append(masks, p)
	}
	return masks
}

// bwrapSmoke runs `true` under the exact argv shape used for real commands —
// namespaces, masking, and the /workspace bind all get exercised, so whatever
// passes here works for beds too.
func bwrapSmoke(path, workspaceRoot string, masks []string, projections []bedfs.PathProjection) ProbeReport {
	probeHome, err := os.MkdirTemp(workspaceRoot, ".probe-*")
	if err != nil {
		return ProbeReport{Error: fmt.Sprintf("smoke test: temp bed_home: %v", err)}
	}
	defer os.RemoveAll(probeHome)
	probeWorkspace := filepath.Join(probeHome, "workspace")
	if err := os.MkdirAll(probeWorkspace, 0o755); err != nil {
		return ProbeReport{Error: fmt.Sprintf("smoke test: workspace: %v", err)}
	}
	for _, projection := range projections {
		if err := os.MkdirAll(projection.CarrierPath(probeHome), 0o755); err != nil {
			return ProbeReport{Error: fmt.Sprintf("smoke test: projection source: %v", err)}
		}
	}

	argv := buildBwrapArgs(workspaceRoot, probeHome, probeWorkspace, projections, bedfs.WorkspacePath, masks)
	cmd := exec.Command(path, append(argv, "true")...)
	report := runExecProbe(cmd)
	if report.Error != "" {
		report.Error = "smoke test: " + report.Error
	}
	return report
}

func (b *bwrap) Name() string           { return "bwrap" }
func (b *bwrap) Level() Level           { return Suite }
func (b *bwrap) Available() bool        { return true } // only constructed when probe passed
func (b *bwrap) WorkspaceMounted() bool { return true }
func (b *bwrap) View(fs *bedfs.FS) bedfs.View {
	return bedfs.MountedProjectedView(fs, bwrapBedHomeMountPoint, b.projections)
}

func (b *bwrap) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	// No silent degradation past this point: this isolator passed the boot
	// probe, so any failure to build the sandbox is a hard error.
	processCwd, err := b.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return err
	}
	argv := buildBwrapArgs(b.root, fs.Home(), fs.Workspace(), b.projections, processCwd, b.maskPaths)
	userArgs := cmd.Args
	cmd.Args = make([]string, 0, len(argv)+len(userArgs)+1)
	cmd.Args = append(cmd.Args, b.path)
	cmd.Args = append(cmd.Args, argv...)
	cmd.Args = append(cmd.Args, userArgs...)
	cmd.Path = b.path
	// The outer bwrap process still needs a carrier-visible cwd. The actual
	// command cwd is applied by bwrap after the mount view exists.
	cmd.Dir = fs.Workspace()
	return nil
}
