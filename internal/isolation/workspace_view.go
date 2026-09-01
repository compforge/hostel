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

	"github.com/qiankunli/hostel/internal/bedfs"
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

func (m mountedWorkspace) Mode() string                 { return "mount" }
func (m mountedWorkspace) View(fs *bedfs.FS) bedfs.View { return m.view(fs) }
func (mountedWorkspace) Mounted() bool                  { return true }
func (mountedWorkspace) Wrap(*exec.Cmd, *bedfs.FS, string) error {
	return nil
}

type workspaceMounter interface {
	View(*bedfs.FS) bedfs.View
	WorkspaceMounted() bool
}

// wrapRuntimeCommand fixes the composition order in one place: first turn the
// user command into the selected workspace-view command, then place that whole
// command inside the selected security boundary.
func wrapRuntimeCommand(boundary Boundary, workspace workspaceBackend, cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	if err := workspace.Wrap(cmd, fs, cwd); err != nil {
		return err
	}
	return boundary.Wrap(cmd, fs, cwd)
}

// resolveWorkspaceView owns capability discovery and policy selection
// independently of the isolation-level resolver. Every candidate whose host
// prerequisite is satisfied is probed before selection; priority only breaks
// ties between candidates that actually work. PRoot is gated by the cheap
// ptrace smoke so a denied ptrace policy does not create a second noisy process
// failure during boot.
//
// +spec=`Below suite, Hostel probes every candidate whose host prerequisites are satisfied, then resolves the canonical workspace view in PRoot → pathshim → carrier order without changing the selected isolation level or mount capability.`
// +case:id=workspace_view_fallback,desc=`Vary pathshim, ptrace, and PRoot probe outcomes independently`,expect=`PRoot wins when usable; otherwise pathshim wins when usable; carrier paths are used only when neither works`
func resolveWorkspaceView(base Boundary, workspaceRoot, pathshimPath, prootPath string, ptraceProbe ProbeReport, probes map[string]ProbeReport) (workspaceBackend, WorkspaceViewReport) {
	if pathshimPath != "" {
		probes["pathshim"] = ProbeReport{ConfiguredPath: pathshimPath}
	}
	if prootPath != "" {
		probes["proot"] = ProbeReport{ConfiguredPath: prootPath}
	}
	if mounter, ok := base.(workspaceMounter); ok && mounter.WorkspaceMounted() {
		workspace := mountedWorkspace{view: mounter.View}
		return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: true}
	}

	reasons := make([]string, 0, 2)
	var pathshimCandidate, prootCandidate workspaceBackend
	var pathshimReport, prootReport WorkspaceViewReport
	if pathshimPath != "" {
		candidate, report, probe := newPathshimView(base, workspaceRoot, pathshimPath)
		probes["pathshim"] = probe
		if report.Available {
			pathshimCandidate, pathshimReport = candidate, report
		} else {
			reasons = append(reasons, "pathshim: "+report.Reason)
		}
	}

	if prootPath != "" {
		if ptraceProbe.succeeded() {
			candidate, report, probe := newProotView(base, workspaceRoot, prootPath)
			probes["proot"] = probe
			if report.Available {
				prootCandidate, prootReport = candidate, report
			} else {
				reasons = append(reasons, "proot: "+report.Reason)
			}
		} else {
			reasons = append(reasons, "ptrace: "+rawProbeFailure(ptraceProbe))
		}
	}

	if prootCandidate != nil {
		log.Printf("isolation: workspace view selected mode=proot")
		return prootCandidate, prootReport
	}
	if pathshimCandidate != nil {
		log.Printf("isolation: workspace view selected mode=pathshim")
		return pathshimCandidate, pathshimReport
	}

	if len(reasons) == 0 {
		workspace := carrierWorkspace{}
		return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: false}
	}
	reason := strings.Join(reasons, "; ")
	log.Printf("isolation: workspace view helpers unavailable (%s); using carrier paths", reason)
	workspace := carrierWorkspace{}
	return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: false, Reason: reason}
}

func rawProbeFailure(probe ProbeReport) string {
	exitCode := "null"
	if probe.ExitCode != nil {
		exitCode = fmt.Sprint(*probe.ExitCode)
	}
	return fmt.Sprintf("attempted=%t exit_code=%s error=%q", probe.Attempted, exitCode, probe.Error)
}
