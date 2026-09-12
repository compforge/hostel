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

// paths converts between the client and carrier path spaces of one BedFS.
// Executor process paths are a separate projection owned by View: file
// placement does not change with the isolation mechanism.
//
//	client:  what callers say. The client's "/" IS the bed_home —
//	         the bed behaves as if it owned the whole pod filesystem. So
//	         /workspace/x, /tmp/x and a relative path (workspace-relative,
//	         OpenSandbox SDK contract) all name places inside one bed, and
//	         the mapping is injective: echoes reproduce the path as sent.
//	host:    where it really lives — {workspace-root}/{bed id}/data/x on the
//	         carrier host. The daemon's own BedFS operations work here.
//
// Immutable value; safe to copy.
type paths struct {
	home string // bed_home carrier dir ({bed dir}/data) — the bed's "/"
}

// newPaths builds the converter for one bed, anchored at bed_home.
func newPaths(home string) paths {
	return paths{home: filepath.Clean(home)}
}

// Home is the bed_home host dir this converter is anchored at.
func (p paths) Home() string { return p.home }

// WorkspaceHost is the host dir of the bed's workspace: the private-root
// subdir the client names WorkspacePath. Derived, not stored — the client
// namespace IS bed_home, so /workspace resolves by the general rule.
func (p paths) WorkspaceHost() string {
	return filepath.Join(p.home, filepath.FromSlash(strings.TrimPrefix(WorkspacePath, "/")))
}

// FromClient maps a client path to the host path. The client's "/" is the
// bed_home, so every absolute path lands inside the bed by the same
// rule (/workspace/x included — no aliasing, echoes stay symmetric); relative
// paths are workspace-relative per the OpenSandbox SDK contract. Bed selection
// has already happened before this conversion, so isolation level must not
// change the mapping result.
func (p paths) FromClient(cp string) (string, error) {
	if cp == "" {
		return "", fmt.Errorf("bedfs: empty path")
	}
	if strings.HasPrefix(cp, "~") {
		return "", fmt.Errorf("bedfs: %q: home-relative paths are not supported", cp)
	}
	rel := cp
	if !path.IsAbs(cp) {
		rel = path.Join(WorkspacePath, cp) // workspace-relative
	}
	// Normalize under a fake root to neutralize any ".." segments.
	clean := path.Clean("/" + strings.TrimPrefix(rel, "/"))
	full := filepath.Join(p.home, filepath.FromSlash(clean))
	if r, err := filepath.Rel(p.home, full); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("bedfs: path %q escapes the bed", cp)
	}
	return full, nil
}

// ToClient maps a host path under bed_home back to its client form.
// Inverse of FromClient on absolute paths: a file uploaded as /tmp/x is
// reported as /tmp/x, one uploaded as /workspace/x as /workspace/x.
func (p paths) ToClient(host string) string {
	rel, err := filepath.Rel(p.home, host)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}
