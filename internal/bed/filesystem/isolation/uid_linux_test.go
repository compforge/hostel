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
	"path/filepath"
	"syscall"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

// missingUIDCaps reads the shared hostfacts.Snapshot and names the absent caps — the
// honest-degrade signal the resolver relies on. Cross-check it against the live
// facts so the cap-bit wiring can't silently drift.
func TestMissingUIDCaps(t *testing.T) {
	facts := hostfacts.Collect()
	miss := missingUIDCaps(facts)
	t.Logf("effective caps %#x, missing uid caps: %q", facts.EffectiveCaps, miss)
	wantAllPresent := len(privilege.MissingBedIdentityCapabilities(facts.EffectiveCaps)) == 0
	if wantAllPresent != (miss == "") {
		t.Fatalf("missingUIDCaps=%q but HasCap(all)=%v — cap-bit wiring drifted", miss, wantAllPresent)
	}
}

// hostfacts.Collect must probe without panicking and yield self-consistent facts
// on any Linux host (root or not).
func TestCollectHostFactsSane(t *testing.T) {
	f := hostfacts.Collect()
	t.Logf("host facts: %+v", f)
	if f.KernelRelease == "" {
		t.Error("KernelRelease empty on Linux (uname should populate it)")
	}
	if f.EUID != os.Geteuid() {
		t.Errorf("EUID = %d, want %d", f.EUID, os.Geteuid())
	}
	if f.EGID != os.Getegid() {
		t.Errorf("EGID = %d, want %d", f.EGID, os.Getegid())
	}
	if f.LandlockABI < 0 {
		t.Errorf("LandlockABI = %d, must be >= 0", f.LandlockABI)
	}
}

// prepareUIDDir sets 0700 and chowns the tree. chown needs CAP_CHOWN, so gate
// the ownership assertion on being able to actually chown; the mode change is
// checkable unprivileged.
func TestPrepareUIDDir(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	f := filepath.Join(sub, "f")
	if err := os.WriteFile(f, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	const uid = uidBase + 42
	err := prepareUIDDir(dir, uid)
	if os.Geteuid() != 0 {
		// Non-root: Lchown to a foreign uid is refused; prepareUIDDir surfaces
		// it. We can still confirm the dir mode was tightened first.
		fi, serr := os.Stat(dir)
		if serr != nil {
			t.Fatal(serr)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Fatalf("dir mode = %o, want 0700", fi.Mode().Perm())
		}
		t.Skipf("unprivileged: chown refused as expected (%v)", err)
	}
	if err != nil {
		t.Fatalf("prepareUIDDir: %v", err)
	}
	for _, p := range []string{dir, sub, f} {
		fi, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		st := fi.Sys().(*syscall.Stat_t)
		if int(st.Uid) != uid || int(st.Gid) != uid {
			t.Fatalf("%s owned %d:%d, want %d:%d", p, st.Uid, st.Gid, uid, uid)
		}
	}
}

func TestUIDPrepareRefreshesBedFSOwner(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to hand over the test directory")
	}
	home := t.TempDir()
	fs, err := bedfs.New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer fs.Close()
	iso := &uidIso{}
	if err := iso.Prepare(fs); err != nil {
		t.Fatal(err)
	}
	user, err := privilege.NewBedUser(uidBase+42, uidBase+42)
	if err != nil {
		t.Fatal(err)
	}
	if err := user.Prepare(fs); err != nil {
		t.Fatal(err)
	}
	if err := fs.Write("/workspace/nested/file", []byte("owned"), 0); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"workspace/nested", "workspace/nested/file"} {
		info, err := os.Stat(filepath.Join(home, path))
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Sys().(*syscall.Stat_t).Uid; got != uint32(uidBase+42) {
			t.Fatalf("%s owner=%d, want %d", path, got, uidBase+42)
		}
	}
}
