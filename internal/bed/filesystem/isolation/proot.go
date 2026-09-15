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
	"fmt"
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
	path            string
	mappingsBlocked bool
}

func (p *prootView) MappingSupport() bedfs.MappingSupport {
	return bedfs.MappingSupport{ReadWrite: !p.mappingsBlocked}
}
func (p *prootView) Mode() string  { return "proot" }
func (p *prootView) Mounted() bool { return false }
func (p *prootView) View(fs *bedfs.FS) bedfs.ProcessView {
	return bedfs.RootedView(fs, p.MappingSupport())
}

func (p *prootView) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	guestCwd, err := p.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return err
	}
	mappings := rootProcessMappings(fs, p.MappingSupport())
	executable := rootExecutable(fs, cmd.Path, p.MappingSupport())
	cmd.Args = append([]string{executable}, cmd.Args[1:]...)
	hostfs.PRoot{Path: p.path, Root: fs.Rootfs()}.Wrap(cmd, mappings, guestCwd)
	return nil
}

func newProotView(base Boundary, bedsRoot string, discovery hostfacts.ProbeReport) (processViewBackend, ProcessViewReport, hostfacts.ProbeReport) {
	probe := hostfacts.WithExecutionProbe(discovery, probeProot(base, bedsRoot, discovery.ResolvedPath))
	if probe.Error != "" {
		log.Printf("isolation: proot process view unavailable (%s)", probe.Error)
		return nil, ProcessViewReport{Mode: "carrier", Available: false, Reason: probe.Error}, probe
	}
	log.Printf("isolation: proot process view probe succeeded path=%s", discovery.ResolvedPath)
	workspace := &prootView{
		path:            discovery.ResolvedPath,
		mappingsBlocked: !base.AllowsMappings(),
	}
	return workspace, ProcessViewReport{Mode: workspace.Mode(), Available: true}, probe
}

func probeProot(base Boundary, bedsRoot, executable string) hostfacts.ProbeReport {
	if err := os.MkdirAll(bedsRoot, 0o755); err != nil {
		return hostfacts.ProbeReport{Error: "create workspace root: " + err.Error()}
	}
	probeHome, err := os.MkdirTemp(bedsRoot, ".proot-probe-*")
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
	if err := base.Prepare(context.Background(), fs); err != nil {
		return hostfacts.ProbeReport{Error: "prepare probe bed: " + err.Error()}
	}
	defer base.Release(context.Background(), fs)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", "mkdir -p /mnt/probe && printf root-view > /mnt/probe/file && cat /workspace/.hostel-proot-probe; printf '\n'; pwd")
	hostfs.PRoot{Path: executable, Root: fs.Rootfs()}.Wrap(cmd, rootProcessMappings(fs, bedfs.MappingSupport{ReadWrite: base.AllowsMappings()}), bedfs.DefaultWorkdir)
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
	if data, err := os.ReadFile(filepath.Join(probeHome, "mnt/probe/file")); err != nil || string(data) != "root-view" {
		report.Error = fmt.Sprintf("native root write did not reach BedFS: %v", err)
	}
	return report
}

var _ processViewBackend = (*prootView)(nil)
