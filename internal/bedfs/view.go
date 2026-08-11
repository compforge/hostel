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

// WorkspacePath is the stable client and Executor path of the Bed workspace.
const WorkspacePath = "/workspace"

// View projects one BedFS from carrier paths into an Executor's filesystem
// namespace. Isolation mechanisms choose the projection; BedFS owns its path
// semantics so command cwd and file APIs cannot drift apart.
type View struct {
	fs             *FS
	homeMount      string
	workspaceMount string
}

// HostView is used when an Executor shares the carrier mount namespace.
func HostView(fs *FS) View { return View{fs: fs} }

// MountedView is used when the Executor has a private mount namespace. The
// whole bed_home has an internal mount for complete BedFS reachability, while
// the workspace keeps its stable public /workspace path.
func MountedView(fs *FS, homeMount, workspaceMount string) View {
	return View{fs: fs, homeMount: path.Clean(homeMount), workspaceMount: path.Clean(workspaceMount)}
}

// Path maps a confined carrier path into this Executor view.
func (v View) Path(host string) (string, error) {
	homeRel, ok := relativeTo(v.fs.Home(), host)
	if !ok {
		return "", fmt.Errorf("bedfs: carrier path %q is outside bed_home %q", host, v.fs.Home())
	}
	if v.homeMount == "" {
		return filepath.Clean(host), nil
	}
	if workspaceRel, inWorkspace := relativeTo(v.fs.Workspace(), host); inWorkspace {
		return joinProcessPath(v.workspaceMount, workspaceRel), nil
	}
	return joinProcessPath(v.homeMount, homeRel), nil
}

// Home returns the process-visible bed_home root.
func (v View) Home() string {
	home, _ := v.Path(v.fs.Home())
	return home
}

// Workspace returns the process-visible default workspace.
func (v View) Workspace() string {
	workspace, _ := v.Path(v.fs.Workspace())
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
