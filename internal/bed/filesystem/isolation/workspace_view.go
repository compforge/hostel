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
	"log"
	"os/exec"
	"strings"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

const (
	pathshimCommand = "pathshim"
	prootCommand    = "proot"
)

// workspaceBackend owns only the process-visible BedFS projection. Security
// boundaries are composed outside it by wrapRuntimeCommand.
type workspaceBackend interface {
	Mode() string
	View(*bedfs.FS) bedfs.View
	Mounted() bool
	Wrap(*exec.Cmd, *bedfs.FS, string) error
}

type carrierWorkspace struct{}

func (carrierWorkspace) Mode() string                 { return "carrier" }
func (carrierWorkspace) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (carrierWorkspace) Mounted() bool                { return false }
func (carrierWorkspace) Wrap(*exec.Cmd, *bedfs.FS, string) error {
	return nil
}

type mountedWorkspace struct {
	view func(*bedfs.FS) bedfs.View
}

func (mountedWorkspace) Mode() string                   { return "mount" }
func (m mountedWorkspace) View(fs *bedfs.FS) bedfs.View { return m.view(fs) }
func (mountedWorkspace) Mounted() bool                  { return true }
func (mountedWorkspace) Wrap(*exec.Cmd, *bedfs.FS, string) error {
	return nil
}

type workspaceMounter interface {
	View(*bedfs.FS) bedfs.View
	WorkspaceMounted() bool
}

// wrapRuntimeCommand fixes composition order in one place: first project the
// user command into its workspace view, then put that whole command inside the
// selected security boundary.
func wrapRuntimeCommand(boundary Boundary, workspace workspaceBackend, cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	if err := workspace.Wrap(cmd, fs, cwd); err != nil {
		return err
	}
	return boundary.Wrap(cmd, fs, cwd)
}

// resolveWorkspaceView discovers the conventional helper names through PATH,
// probes every candidate whose runtime prerequisite is satisfied, and applies
// priority only after support is known. Helper packaging is therefore an image
// concern rather than Hostel configuration.
//
// +spec=`Below suite, Hostel discovers pathshim and PRoot through PATH, probes every candidate whose prerequisites are satisfied, then resolves the process view in PRoot → pathshim → carrier order without changing the selected isolation level.`
// +case:id=workspace_view_fallback,desc=`Vary helper discovery, ptrace, pathshim, and PRoot probe outcomes independently`,expect=`Diagnostics preserve discovery facts; PRoot wins when usable, pathshim is next, and carrier is the final fallback`
func resolveWorkspaceView(base Boundary, workspaceRoot string, projections []bedfs.PathProjection, ptraceProbe hostfacts.ProbeReport, probes map[string]hostfacts.ProbeReport) (workspaceBackend, WorkspaceViewReport) {
	return resolveWorkspaceViewWithConfig(base, workspaceRoot, projections, ptraceProbe, probes, Config{})
}
func resolveWorkspaceViewWithConfig(base Boundary, workspaceRoot string, projections []bedfs.PathProjection, ptraceProbe hostfacts.ProbeReport, probes map[string]hostfacts.ProbeReport, config Config) (workspaceBackend, WorkspaceViewReport) {
	pathshimDiscovery := hostfacts.ProbeReport{Error: "disabled_by_config"}
	prootDiscovery := hostfacts.ProbeReport{Error: "disabled_by_config"}
	if config.Pathshim.Effective() != feature.Off {
		pathshimDiscovery = hostfacts.DiscoverExecutable(pathshimCommand)
	}
	if config.PRoot.Effective() != feature.Off {
		prootDiscovery = hostfacts.DiscoverExecutable(prootCommand)
	}
	probes["pathshim"] = pathshimDiscovery
	probes["proot"] = prootDiscovery
	if config.Pathshim.Effective() == feature.Off {
		probes["pathshim"] = hostfacts.ProbeReport{}
	}
	if config.PRoot.Effective() == feature.Off {
		probes["proot"] = hostfacts.ProbeReport{}
	}

	if mounter, ok := base.(workspaceMounter); ok && mounter.WorkspaceMounted() {
		workspace := mountedWorkspace{view: mounter.View}
		return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: true}
	}

	reasons := make([]string, 0, 3)
	var pathshimCandidate, prootCandidate workspaceBackend
	var pathshimReport, prootReport WorkspaceViewReport

	if pathshimDiscovery.Error == "" {
		candidate, report, probe := newPathshimView(base, workspaceRoot, projections, pathshimDiscovery)
		probes["pathshim"] = probe
		if report.Available {
			pathshimCandidate, pathshimReport = candidate, report
		} else {
			reasons = append(reasons, "pathshim: "+report.Reason)
		}
	} else {
		reasons = append(reasons, "pathshim: "+pathshimDiscovery.Error)
	}

	if prootDiscovery.Error != "" {
		reasons = append(reasons, "proot: "+prootDiscovery.Error)
	} else if !ptraceProbe.Succeeded() {
		reasons = append(reasons, "ptrace: "+rawProbeFailure(ptraceProbe))
	} else {
		candidate, report, probe := newProotView(base, workspaceRoot, projections, prootDiscovery)
		probes["proot"] = probe
		if report.Available {
			prootCandidate, prootReport = candidate, report
		} else {
			reasons = append(reasons, "proot: "+report.Reason)
		}
	}

	if prootCandidate != nil && config.Pathshim != feature.Required {
		log.Printf("isolation: workspace view selected mode=proot")
		return prootCandidate, prootReport
	}
	if pathshimCandidate != nil {
		log.Printf("isolation: workspace view selected mode=pathshim")
		return pathshimCandidate, pathshimReport
	}

	reason := strings.Join(reasons, "; ")
	log.Printf("isolation: workspace view helpers unavailable (%s); using carrier paths", reason)
	workspace := carrierWorkspace{}
	return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: false, Reason: reason}
}

func rawProbeFailure(probe hostfacts.ProbeReport) string {
	exitCode := "null"
	if probe.ExitCode != nil {
		exitCode = fmt.Sprint(*probe.ExitCode)
	}
	return fmt.Sprintf("attempted=%t exit_code=%s error=%q", probe.Attempted, exitCode, probe.Error)
}
