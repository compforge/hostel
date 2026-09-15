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

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// bwrap confines each command under bubblewrap. Mount view per
// docs/filesystem.md: Bed data at / with shared runtime overlays, writable
// carrier software at /usr/local, and no inherited carrier data root. Environment
// ownership is enforced before this boundary by bed's process-env builder.
type bwrap struct {
	path      string   // bwrap binary (probed at boot)
	root      string   // parent dir of all Bed directories (masked in-sandbox)
	maskPaths []string // existing sensitive host paths to mask (computed once)
	rootful   *rootfulMounts
}

// newBwrap probes bubblewrap at boot: binary present AND the FULL mount shape
// we will actually use starts (binary-present-but-broken — unprivileged userns
// disabled, or the native data root isn't writable — must not count
// as isolated; a partial probe once let healthz report process_view.mode=mount
// while every exec failed). On failure it falls back to direct so the daemon
// still boots and /healthz reports the truth.
// Probe pattern borrowed from OpenSandbox execd, extended to the real argv.
func newBwrap(facts hostfacts.Snapshot, bedsRoot string) (Isolator, hostfacts.ProbeReport) {
	path := facts.BwrapPath
	report := hostfacts.ProbeReport{
		ConfiguredPath: "bwrap",
		ResolvedPath:   path,
		Exists:         path != "",
		Executable:     path != "",
	}
	if path == "" {
		report.Error = facts.BwrapLookupError
		return unavailable{name: "bwrap", lvl: Private}, report
	}

	// The workspace root may not exist yet at probe time (the bed manager
	// creates it later); the smoke test masks it, so it must exist now.
	if err := os.MkdirAll(bedsRoot, 0o755); err != nil {
		log.Printf("isolation: cannot create workspace root %s: %v", bedsRoot, err)
	}

	masks := resolveMaskPaths(defaultMaskCandidates)
	if canPrepareRootful(facts) {
		helper, err := hostfs.StaticBootstrap()
		if err == nil {
			candidate := &bwrap{path: path, root: bedsRoot, maskPaths: masks,
				rootful: &rootfulMounts{helper: helper, views: make(map[*bedfs.FS]*hostfs.MountNamespace)}}
			rootfulProbe := candidate.rootfulSmoke()
			if rootfulProbe.Succeeded() {
				rootfulProbe.ConfiguredPath, rootfulProbe.ResolvedPath = "bwrap", path
				rootfulProbe.Exists, rootfulProbe.Executable = true, true
				log.Printf("isolation: bwrap selected preparation=rootful")
				return candidate, rootfulProbe
			}
			log.Printf("isolation: rootful bwrap unavailable: %s; probing unprivileged preparation", rootfulProbe.Error)
		} else {
			log.Printf("isolation: rootful bwrap unavailable: %v; probing unprivileged preparation", err)
		}
	}
	report = bwrapSmoke(path, bedsRoot, masks)
	report.ConfiguredPath = "bwrap"
	report.ResolvedPath = path
	report.Exists = true
	report.Executable = true
	if report.Failed() {
		log.Printf("isolation: bwrap found but unusable (%s)", report.Error)
		// Point the operator at the usual k8s cause: userns is on yet bwrap
		// dies at mount because containerd's default AppArmor profile denies
		// mount(2). Surfaced here AND in /healthz (hostfacts.Snapshot.apparmor_profile)
		// so the fix (an AppArmor-unconfined annotation on the carrier pod) is
		// discoverable without shelling in.
		if facts.AppArmorProfile != "" && facts.UnprivilegedUserns {
			log.Printf("isolation: suite blocked despite unprivileged userns — AppArmor profile %q likely denies mount; "+
				"grant the pod an AppArmor-unconfined annotation to reach suite (else degrading to a lower tier)", facts.AppArmorProfile)
		}
		return unavailable{name: "bwrap", lvl: Private}, report
	}
	return &bwrap{
		path:      path,
		root:      bedsRoot,
		maskPaths: masks,
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

// bwrapSmoke proves native absolute writes reach the same data root as the file
// API. Additional mappings come from each BedFS when Wrap is called.
func bwrapSmoke(path, bedsRoot string, masks []string) hostfacts.ProbeReport {
	probeHome, err := os.MkdirTemp(bedsRoot, ".probe-*")
	if err != nil {
		return hostfacts.ProbeReport{Error: fmt.Sprintf("smoke test: temp bed_home: %v", err)}
	}
	defer os.RemoveAll(probeHome)
	probeWorkspace := filepath.Join(probeHome, "workspace")
	if err := os.MkdirAll(probeWorkspace, 0o755); err != nil {
		return hostfacts.ProbeReport{Error: fmt.Sprintf("smoke test: workspace: %v", err)}
	}
	argv := buildBwrapArgs(bedsRoot, probeHome, probeWorkspace, bedfs.DefaultWorkdir, masks, nil, existingRuntimePaths()...)
	cmd := exec.Command(path, append(argv, "/bin/sh", "-c", "mkdir -p /mnt/probe && printf root-view > /mnt/probe/file")...)
	report := hostfacts.RunExecProbe(cmd)
	if report.Succeeded() {
		data, err := os.ReadFile(filepath.Join(probeHome, "mnt/probe/file"))
		if err != nil || string(data) != "root-view" {
			report.Error = fmt.Sprintf("native root write did not reach BedFS: %v", err)
		}
	}
	if report.Error != "" {
		report.Error = "smoke test: " + report.Error
	}
	return report
}

func (b *bwrap) Name() string     { return "bwrap" }
func (b *bwrap) Level() Level     { return Private }
func (b *bwrap) Available() bool  { return true } // only constructed when probe passed
func (b *bwrap) MountsRoot() bool { return true }
func (b *bwrap) View(fs *bedfs.FS) bedfs.ProcessView {
	return bedfs.RootedView(fs, bedfs.MappingSupport{ReadWrite: true, ReadOnly: true})
}

func (b *bwrap) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	if b.rootful != nil {
		return b.enterPrepared(cmd, fs)
	}
	// No silent degradation past this point: this isolator passed the boot
	// probe, so any failure to build the sandbox is a hard error.
	processCwd, err := b.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return err
	}
	executable := rootExecutable(fs, cmd.Path, b.View(fs).MappingSupport())
	argv := b.args(fs.Rootfs(), fs.Workdir(), processCwd, fs.PathMappings())
	argv = append(argv[:len(argv)-1], systemFileArgs(fs)...)
	argv = append(argv, "--")
	userArgs := append([]string{executable}, cmd.Args[1:]...)
	cmd.Args = make([]string, 0, len(argv)+len(userArgs)+1)
	cmd.Args = append(cmd.Args, b.path)
	cmd.Args = append(cmd.Args, argv...)
	cmd.Args = append(cmd.Args, userArgs...)
	cmd.Path = b.path
	// The outer bwrap process still needs a carrier-visible cwd. The actual
	// command cwd is applied by bwrap after the mount view exists.
	cmd.Dir = fs.Workdir()
	return nil
}

func (b *bwrap) AllowsMappings() bool { return true }

func (b *bwrap) args(home, workspace, cwd string, mappings []model.PathMapping) []string {
	args := buildBwrapArgs(b.root, home, workspace, cwd, b.maskPaths, mappings, existingRuntimePaths()...)
	if b.rootful != nil {
		// Rootful preparation creates a mount namespace using existing host
		// authority. No user namespace or UID remapping is needed.
		filtered := args[:0]
		for _, arg := range args {
			if arg != "--unshare-user" {
				filtered = append(filtered, arg)
			}
		}
		args = filtered
	}
	return args
}
