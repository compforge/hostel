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
	"path/filepath"
	"testing"
)

// TestPathsFromClient pins path rebasing as a BedFS contract. Isolation only
// selects an Executor view; it never changes where client data is stored.
func TestPathsFromClient(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		client string
		rel    string
	}{
		{client: "/workspace", rel: "workspace"},
		{client: "/workspace/a.txt", rel: "workspace/a.txt"},
		{client: "/tmp/workspace/job", rel: "tmp/workspace/job"},
		{client: "tmp/workspace/job", rel: "workspace/tmp/workspace/job"}, // relative = workspace-relative
		{client: "/"},
	}

	paths := newPaths(root)
	for _, tc := range cases {
		got, err := paths.FromClient(tc.client)
		if err != nil {
			t.Fatalf("FromClient(%q): %v", tc.client, err)
		}
		want := filepath.Join(root, filepath.FromSlash(tc.rel))
		if got != want {
			t.Errorf("FromClient(%q) = %q, want %q", tc.client, got, want)
		}
	}
}

func TestViewPath(t *testing.T) {
	root := t.TempDir()
	fs := New(root)
	workspace := fs.Workspace()

	t.Run("private mount", func(t *testing.T) {
		view := MountedView(fs, "/tmp/.hostel/bed", WorkspacePath)
		cases := []struct{ host, want string }{
			{root, "/tmp/.hostel/bed"},
			{filepath.Join(root, "tmp", "x"), "/tmp/.hostel/bed/tmp/x"},
			{workspace, "/workspace"},
			{filepath.Join(workspace, "sub"), "/workspace/sub"},
		}
		for _, tc := range cases {
			got, err := view.Path(tc.host)
			if err != nil || got != tc.want {
				t.Errorf("Path(%q) = %q,%v want %q", tc.host, got, err, tc.want)
			}
		}
		if _, err := view.Path(filepath.Dir(root)); err == nil {
			t.Error("Path outside bed_home must be refused")
		}
	})

	t.Run("carrier mount namespace", func(t *testing.T) {
		view := HostView(fs)
		host := filepath.Join(root, "tmp", "x") // whole bed_home reachable
		got, err := view.Path(host)
		if err != nil || got != host {
			t.Errorf("Path(%q) = %q,%v want the carrier path back", host, got, err)
		}
	})
}

// TestPathsRoundTrip pins FromClient/ToClient as inverses on absolute client
// paths — the "own pod" contract: a path is echoed back exactly as sent, for
// workspace and non-workspace locations alike.
func TestPathsRoundTrip(t *testing.T) {
	root := t.TempDir()
	p := newPaths(root)
	for _, cp := range []string{"/", "/workspace", "/workspace/a.txt", "/workspace/x/y", "/tmp/workspace/job", "/etc/hosts"} {
		host, err := p.FromClient(cp)
		if err != nil {
			t.Fatalf("FromClient(%q): %v", cp, err)
		}
		if back := p.ToClient(host); back != cp {
			t.Errorf("ToClient(FromClient(%q)) = %q", cp, back)
		}
	}
}
