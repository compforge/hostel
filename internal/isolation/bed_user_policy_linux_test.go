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

//go:build linux

package isolation

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/privilege"
)

type testFileBoundary struct{}

func (testFileBoundary) Name() string                 { return "test-file" }
func (testFileBoundary) Level() Level                 { return Dorm }
func (testFileBoundary) Available() bool              { return true }
func (testFileBoundary) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (testFileBoundary) WorkspaceMounted() bool       { return false }
func (testFileBoundary) Wrap(cmd *exec.Cmd, _ *bedfs.FS, _ string) error {
	cmd.Args = append([]string{"file-helper", "--", cmd.Path}, cmd.Args[1:]...)
	cmd.Path = "/file-helper"
	return nil
}

func TestUIDIsolationResolvesDifferentBedUsers(t *testing.T) {
	configured, err := privilege.NewBedUser(1000, 1000)
	if err != nil {
		t.Fatal(err)
	}
	resolve := func(home string) privilege.BedUser {
		if err := os.MkdirAll(home, 0o755); err != nil {
			t.Fatal(err)
		}
		fs, err := bedfs.New(home)
		if err != nil {
			t.Fatal(err)
		}
		defer fs.Close()
		user, err := BedUserFor(&resolved{boundary: &uidIso{}}, fs, configured)
		if err != nil {
			t.Fatal(err)
		}
		return user
	}
	a := resolve(filepath.Join(t.TempDir(), "a"))
	b := resolve(filepath.Join(t.TempDir(), "b"))
	if a.UID() == b.UID() {
		t.Fatalf("sample beds resolved the same uid %d", a.UID())
	}
	for _, user := range []privilege.BedUser{a, b} {
		if user.UID() < uidBase || user.UID() >= uidBase+uidRange || user.GID() != user.UID() {
			t.Fatalf("resolved user = %d:%d", user.UID(), user.GID())
		}
	}
	report := DescribeBedUser(&resolved{boundary: &uidIso{}}, configured)
	if report.Strategy != "per_bed" || report.UIDMin != uidBase || report.UIDMax != uidBase+uidRange-1 {
		t.Fatalf("report = %+v", report)
	}
}

func TestEnvironmentDropsUserOutsideFileBoundary(t *testing.T) {
	setpriv, err := privilege.ProcessCredentialHelper()
	if err != nil {
		t.Skip("setpriv is not installed")
	}
	environment := Bind(testFileBoundary{}, nil, nil, privilege.CurrentBedUser())
	cmd := exec.Command("/bin/echo", "ok")
	if err := environment.Wrap(cmd, ""); err != nil {
		t.Fatal(err)
	}
	if cmd.Path != setpriv {
		t.Fatalf("outer command = %q, want %q", cmd.Path, setpriv)
	}
	separator := -1
	for index, arg := range cmd.Args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 || separator+1 >= len(cmd.Args) || cmd.Args[separator+1] != "/file-helper" {
		t.Fatalf("wrapped args = %q", cmd.Args)
	}
}
