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
	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiankunli/hostel/internal/bedfs"
)

// pathshimView is deliberately a decorator around the selected security
// mechanism. Room therefore applies its restriction before pathshim starts,
// while suite keeps its real mount and never uses this helper.
type pathshimView struct {
	base Isolator
	path string
}

func (p *pathshimView) Name() string                 { return p.base.Name() }
func (p *pathshimView) Level() Level                 { return p.base.Level() }
func (p *pathshimView) Available() bool              { return p.base.Available() }
func (p *pathshimView) WorkspaceMounted() bool       { return false }
func (p *pathshimView) View(fs *bedfs.FS) bedfs.View { return bedfs.WorkspaceView(fs) }
func (p *pathshimView) Prepare(fs *bedfs.FS) error {
	if preparer, ok := p.base.(Preparer); ok {
		return preparer.Prepare(fs)
	}
	return nil
}

func (p *pathshimView) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	guestCwd, err := p.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return err
	}
	userArgs := cmd.Args
	cmd.Args = make([]string, 0, len(userArgs)+8)
	cmd.Args = append(cmd.Args, p.path, "--quiet", "--bind", fs.Workspace()+":"+bedfs.WorkspacePath, "--cwd", guestCwd, "--")
	cmd.Args = append(cmd.Args, userArgs...)
	cmd.Path = p.path
	return p.base.Wrap(cmd, fs, commandCwd(fs, cwd))
}

// +spec=`A pathshim workspace view improves /workspace compatibility without changing isolation level or mount capability.`
// +case:id=pathshim_workspace_view,desc=`Probe and run one command through the selected dorm or room mechanism`,expect=`/workspace maps to Bed workspace, command semantics survive, and probe failure falls back to carrier paths`
func newPathshimView(base Isolator, workspaceRoot, path string) (Isolator, WorkspaceViewReport, ProbeReport) {
	probe := ProbeReport{ConfiguredPath: path}
	resolvedPath, err := exec.LookPath(path)
	if err != nil {
		reason := "find binary: " + err.Error()
		probe.Error = reason
		log.Printf("isolation: pathshim workspace view unavailable (%s); using carrier paths", reason)
		return base, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: reason}, probe
	}
	resolvedPath, err = filepath.Abs(resolvedPath)
	if err != nil {
		reason := "resolve binary: " + err.Error()
		probe.Error = reason
		log.Printf("isolation: pathshim workspace view unavailable (%s); using carrier paths", reason)
		return base, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: reason}, probe
	}
	probe = probePathshim(base, workspaceRoot, resolvedPath)
	probe.ConfiguredPath = path
	probe.ResolvedPath = resolvedPath
	reason := probe.Error
	if reason != "" {
		log.Printf("isolation: pathshim workspace view unavailable (%s); using carrier paths", reason)
		return base, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: reason}, probe
	}
	log.Printf("isolation: workspace view=pathshim path=%s", resolvedPath)
	return &pathshimView{base: base, path: resolvedPath}, WorkspaceViewReport{Mode: "pathshim", Available: true}, probe
}

func probePathshim(base Isolator, workspaceRoot, path string) ProbeReport {
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return ProbeReport{Error: "create workspace root: " + err.Error()}
	}
	probeHome, err := os.MkdirTemp(workspaceRoot, ".pathshim-probe-*")
	if err != nil {
		return ProbeReport{Error: "create probe bed: " + err.Error()}
	}
	defer os.RemoveAll(probeHome)
	probeWorkspace := filepath.Join(probeHome, "workspace")
	if err := os.MkdirAll(probeWorkspace, 0o755); err != nil {
		return ProbeReport{Error: "create probe workspace: " + err.Error()}
	}
	fs, err := bedfs.New(probeHome)
	if err != nil {
		return ProbeReport{Error: err.Error()}
	}
	defer fs.Close()
	if preparer, ok := base.(Preparer); ok {
		if err := preparer.Prepare(fs); err != nil {
			return ProbeReport{Error: "prepare probe bed: " + err.Error()}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "probe", "--bind", probeWorkspace+":"+bedfs.WorkspacePath)
	if err := base.Wrap(cmd, fs, probeWorkspace); err != nil {
		return ProbeReport{Error: "wrap probe: " + err.Error()}
	}
	report := runExecProbe(cmd)
	if ctx.Err() != nil {
		report.Error = "probe timed out"
		return report
	}
	if report.failed() {
		detail := strings.TrimSpace(report.Stdout + report.Stderr)
		if detail == "" {
			detail = report.Error
		}
		report.Error = detail
		return report
	}
	if strings.TrimSpace(report.Stdout) != "bind-view" {
		report.Error = "unexpected probe output: " + strings.TrimSpace(report.Stdout)
	}
	return report
}
