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
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"

	"context"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

type prootView struct {
	path        string
	projections []bedfs.PathProjection
}

func (p *prootView) Mode() string  { return "proot" }
func (p *prootView) Mounted() bool { return false }
func (p *prootView) View(fs *bedfs.FS) bedfs.View {
	return bedfs.ProjectedView(fs, p.projections)
}

func (p *prootView) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	guestCwd, err := p.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return err
	}
	hostfs.PRoot{Path: p.path}.Wrap(cmd, processMappings(fs, p.projections), guestCwd)
	return nil
}

func newProotView(base Boundary, workspaceRoot string, projections []bedfs.PathProjection, discovery hostfacts.ProbeReport) (workspaceBackend, WorkspaceViewReport, hostfacts.ProbeReport) {
	probe := hostfacts.WithExecutionProbe(discovery, probeProot(base, workspaceRoot, discovery.ResolvedPath, projections))
	if probe.Error != "" {
		log.Printf("isolation: proot process view unavailable (%s)", probe.Error)
		return nil, WorkspaceViewReport{Mode: "carrier", Available: false, Reason: probe.Error}, probe
	}
	log.Printf("isolation: proot process view probe succeeded path=%s projections=%d", discovery.ResolvedPath, len(projections))
	workspace := &prootView{
		path:        discovery.ResolvedPath,
		projections: append([]bedfs.PathProjection(nil), projections...),
	}
	return workspace, WorkspaceViewReport{Mode: workspace.Mode(), Available: true}, probe
}

func probeProot(base Boundary, workspaceRoot, executable string, projections []bedfs.PathProjection) hostfacts.ProbeReport {
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		return hostfacts.ProbeReport{Error: "create workspace root: " + err.Error()}
	}
	probeHome, err := os.MkdirTemp(workspaceRoot, ".proot-probe-*")
	if err != nil {
		return hostfacts.ProbeReport{Error: "create probe bed: " + err.Error()}
	}
	defer os.RemoveAll(probeHome)
	probeWorkspace := filepath.Join(probeHome, "workspace")
	if err := os.MkdirAll(probeWorkspace, 0o755); err != nil {
		return hostfacts.ProbeReport{Error: "create probe workspace: " + err.Error()}
	}
	if err := os.WriteFile(filepath.Join(probeWorkspace, ".hostel-proot-probe"), []byte("proot-view"), 0o644); err != nil {
		return hostfacts.ProbeReport{Error: "write probe marker: " + err.Error()}
	}
	fs, err := bedfs.New(probeHome)
	if err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}
	}
	defer fs.Close()
	for _, projection := range projections {
		hostPath, err := fs.Resolve(projection.BedPath)
		if err != nil {
			return hostfacts.ProbeReport{Error: "resolve probe projection: " + err.Error()}
		}
		if err := fs.EnsureDir(hostPath); err != nil {
			return hostfacts.ProbeReport{Error: "create probe projection: " + err.Error()}
		}
	}
	if preparer, ok := base.(Preparer); ok {
		if err := preparer.Prepare(fs); err != nil {
			return hostfacts.ProbeReport{Error: "prepare probe bed: " + err.Error()}
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "cat /workspace/.hostel-proot-probe; printf '\n'; pwd")
	hostfs.PRoot{Path: executable}.Wrap(cmd, processMappings(fs, projections), bedfs.WorkspacePath)
	if err := base.Wrap(cmd, fs, probeWorkspace); err != nil {
		return hostfacts.ProbeReport{Error: "wrap probe: " + err.Error()}
	}
	report := hostfacts.RunExecProbe(cmd)
	if ctx.Err() != nil {
		report.Error = "probe timed out"
		return report
	}
	if report.Failed() {
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

var _ workspaceBackend = (*prootView)(nil)
