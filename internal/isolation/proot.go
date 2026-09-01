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

type prootView struct {
	path string
}

func (p *prootView) Mode() string                 { return "proot" }
func (p *prootView) View(fs *bedfs.FS) bedfs.View { return bedfs.WorkspaceView(fs) }
func (p *prootView) Mounted() bool                { return false }

func (p *prootView) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	guestCwd, err := p.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return err
	}
	userArgs := cmd.Args
	cmd.Args = make([]string, 0, len(userArgs)+8)
	cmd.Args = append(cmd.Args,
		p.path,
		"-v", "-1",
		"-b", fs.Workspace()+":"+bedfs.WorkspacePath+"!",
		"-w", guestCwd,
	)
	cmd.Args = append(cmd.Args, userArgs...)
	cmd.Path = p.path
	return nil
}

func newProotView(base Boundary, workspaceRoot, path string) (workspaceBackend, WorkspaceViewReport, ProbeReport) {
	probe := ProbeReport{ConfiguredPath: path}
	resolvedPath, err := exec.LookPath(path)
	if err != nil {
		reason := "find binary: " + err.Error()
		probe.Error = reason
		log.Printf("isolation: proot workspace view unavailable (%s)", reason)
		return nil, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: reason}, probe
	}
	resolvedPath, err = filepath.Abs(resolvedPath)
	if err != nil {
		reason := "resolve binary: " + err.Error()
		probe.Error = reason
		log.Printf("isolation: proot workspace view unavailable (%s)", reason)
		return nil, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: reason}, probe
	}
	probe = probeProot(base, workspaceRoot, resolvedPath)
	probe.ConfiguredPath = path
	probe.ResolvedPath = resolvedPath
	if probe.Error != "" {
		log.Printf("isolation: proot workspace view unavailable (%s)", probe.Error)
		return nil, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: probe.Error}, probe
	}
	log.Printf("isolation: proot workspace view probe succeeded path=%s", resolvedPath)
	workspace := &prootView{path: resolvedPath}
	return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: true}, probe
}

func probeProot(base Boundary, workspaceRoot, path string) ProbeReport {
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return ProbeReport{Error: "create workspace root: " + err.Error()}
	}
	probeHome, err := os.MkdirTemp(workspaceRoot, ".proot-probe-*")
	if err != nil {
		return ProbeReport{Error: "create probe bed: " + err.Error()}
	}
	defer os.RemoveAll(probeHome)
	probeWorkspace := filepath.Join(probeHome, "workspace")
	if err := os.MkdirAll(probeWorkspace, 0o755); err != nil {
		return ProbeReport{Error: "create probe workspace: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(probeWorkspace, ".hostel-proot-probe"), []byte("proot-view"), 0o644); err != nil {
		return ProbeReport{Error: "write probe marker: " + err.Error()}
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
	cmd := exec.CommandContext(ctx, path,
		"-v", "-1",
		"-b", probeWorkspace+":"+bedfs.WorkspacePath+"!",
		"-w", bedfs.WorkspacePath,
		"/bin/sh", "-c", "cat /workspace/.hostel-proot-probe; printf '\\n'; pwd",
	)
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
	if strings.TrimSpace(report.Stdout) != "proot-view\n/workspace" {
		report.Error = "unexpected probe output: " + strings.TrimSpace(report.Stdout)
	}
	return report
}
