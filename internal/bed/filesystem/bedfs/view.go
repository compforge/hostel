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

package bedfs

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// DefaultWorkdir is the default Bed working directory and relative API path base.
const DefaultWorkdir = "/workspace"

// ProcessView projects one BedFS from carrier paths into an Executor's filesystem
// namespace. Isolation mechanisms choose the projection; BedFS owns its path
// semantics so command cwd and file APIs cannot drift apart.
type ProcessView struct {
	fs           *FS
	mappings     MappingSupport
	homeMount    string
	workdirMount string
}

// HostView is used when an Executor shares the carrier mount namespace.
func HostView(fs *FS) ProcessView { return ProcessView{fs: fs} }

// RootedView preserves Bed absolute paths in a process with a Bed-owned root.
// Runtime/system overlays are supplied by the execution mechanism, not by the
// file API. Read-only mapping support remains independent of root projection.
func RootedView(fs *FS, support MappingSupport) ProcessView {
	return ProcessView{fs: fs, mappings: support, homeMount: "/", workdirMount: DefaultWorkdir}
}

// RedirectedView gives the workdir its stable process path and honors the
// BedFS mappings. Other paths in the default root keep their carrier spelling;
// user-space helpers do not claim a complete guest root.
func RedirectedView(fs *FS, support MappingSupport) ProcessView {
	return ProcessView{fs: fs, workdirMount: DefaultWorkdir, mappings: support}
}

// Path maps a confined carrier path into this Executor view.
func (v ProcessView) Path(host string) (string, error) {
	for _, mapping := range v.fs.PathMappings() {
		if rel, ok := relativeTo(mapping.HostPath, host); ok {
			if v.mappings.Supports(mapping.ReadOnly) {
				return joinProcessPath(mapping.BedPath, rel), nil
			}
			if v.homeMount == "/" {
				// An unrealized mapping uses the Bed-local candidate, just as
				// the file Reader does; its carrier source is not mounted here.
				return joinProcessPath(mapping.BedPath, rel), nil
			}
			// The API still owns this mapping even when the process view cannot
			// redirect it. Structured cwd can use the source's carrier spelling.
			return filepath.Clean(host), nil
		}
	}
	homeRel, ok := relativeTo(v.fs.Rootfs(), host)
	if !ok {
		return "", fmt.Errorf("bedfs: carrier path %q is outside bed_home %q", host, v.fs.Rootfs())
	}
	if workspaceRel, inWorkspace := relativeTo(v.fs.Workdir(), host); inWorkspace {
		if v.workdirMount != "" {
			return joinProcessPath(v.workdirMount, workspaceRel), nil
		}
	}
	if v.homeMount == "" {
		return filepath.Clean(host), nil
	}
	return joinProcessPath(v.homeMount, homeRel), nil
}

// Rootfs returns the process-visible bed_home root.
func (v ProcessView) Rootfs() string {
	home, _ := v.Path(v.fs.Rootfs())
	return home
}

// Workdir returns the process-visible default workspace.
func (v ProcessView) Workdir() string {
	workspace, _ := v.Path(v.fs.Workdir())
	return workspace
}

func relativeTo(root, candidate string) (string, bool) {
	rel, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", false
	}
	return rel, true
}

func joinProcessPath(root, rel string) string {
	if rel == "." {
		return root
	}
	return path.Join(root, filepath.ToSlash(rel))
}

// MappingSupport describes process-path behavior, independently of isolation Level.
// ReadOnly means the process mapping can enforce the declaration; API mutations
// enforce ReadOnly regardless of this support.
type MappingSupport struct {
	ReadWrite bool `json:"read_write"`
	ReadOnly  bool `json:"read_only"`
}

func (s MappingSupport) Supports(readOnly bool) bool {
	if readOnly {
		return s.ReadOnly
	}
	return s.ReadWrite
}

func (v ProcessView) MappingSupport() MappingSupport { return v.mappings }
