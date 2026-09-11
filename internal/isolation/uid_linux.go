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
	"fmt"
	"hash/fnv"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/privilege"
)

// Bed uids live in a fixed high band, ASSUMED unused by the host — not
// guaranteed: this range can overlap /etc/subuid userns mappings (the 2nd
// default user gets 231072..) or LDAP/service accounts. Under our threat model
// (a bed straying into another bed, not adversarial uid-squatting) that's
// acceptable; a bed colliding with a real host identity is a deployment
// concern, documented in docs/isolation.md. The uid is derived from the
// data dir path (no registry), so Prepare (chown) and Wrap (setuid) agree with
// no shared state and it stays stable across restarts. Two beds hashing to the
// same uid is possible but rare; a colliding pair degrades to mutual access
// (dorm between just those two) — never a crash.
// bedUID derives a bed's dedicated uid from its data dir. gid == uid: each bed
// gets a private primary group of the same number.
func bedUID(dataDir string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(dataDir))
	return uidBase + int(h.Sum32()%uidRange)
}

// uidIso is the room mechanism realized with classic Unix DAC: each bed's
// processes run as a dedicated uid, its data dir owned 0700 by that uid. A bed
// can't ACCESS another's data (EACCES) and — a bonus over landlock — can't
// signal, ptrace, or read /proc/<pid> of another bed's processes either.
// Siblings stay visible (dir names listable, /tmp and system paths shared): the
// "private room, shared toilet" tier. Needs the daemon to hold
// CAP_SETUID/SETGID/CHOWN (root, or a setcap'd binary) but no special kernel —
// so it fills the room slot where Landlock is absent (old/custom kernels).
type uidIso struct{}

func newUID(facts HostFacts, workspaceRoot string) (Isolator, ProbeReport) {
	helper, helperErr := privilege.ProcessCredentialHelper()
	if helperErr != nil {
		discovery := ProbeReport{ConfiguredPath: "setpriv", Error: "find binary: " + helperErr.Error()}
		return unavailable{name: "uid", lvl: Room}, discovery
	}
	discovery := discoverExecutable(helper)
	discovery.ConfiguredPath = "setpriv"
	// Missing caps isn't an error — many environments simply don't grant them;
	// the resolver falls through to the next mechanism and logs honestly.
	if miss := missingUIDCaps(facts); miss != "" {
		return unavailable{name: "uid", lvl: Room}, discovery
	}
	if err := os.MkdirAll(workspaceRoot, 0o755); err != nil {
		log.Printf("isolation: cannot create workspace root %s: %v", workspaceRoot, err)
	}
	// Caps present ≠ enforcement works. Prove the whole chain once — chown →
	// setuid → no_new_privs → EACCES on a sibling — exactly as production runs.
	report := withExecutionProbe(discovery, uidSmoke(workspaceRoot))
	if report.failed() {
		log.Printf("isolation: uid isolation caps present but unusable (%s)", report.Error)
		return unavailable{name: "uid", lvl: Room}, report
	}
	return &uidIso{}, report
}

// capsForUID: the effective capabilities the daemon needs to run beds under
// dedicated uids — SETUID/SETGID to drop, CHOWN to hand the data dir over.
var capsForUID = []struct {
	bit  uint
	name string
}{
	{capCHOWN, "CAP_CHOWN"}, {capSETGID, "CAP_SETGID"}, {capSETUID, "CAP_SETUID"},
}

// missingUIDCaps returns a comma-joined list of the required caps absent from
// the host's effective set ("" = all present), read from the shared HostFacts.
func missingUIDCaps(facts HostFacts) string {
	var miss []string
	for _, c := range capsForUID {
		if !facts.HasCap(c.bit) {
			miss = append(miss, c.name)
		}
	}
	return strings.Join(miss, ",")
}

// uidSmoke proves the mechanism bites, using the exact production form: prepare
// two sibling dirs owned by DIFFERENT bed uids, then run through BedUser as one
// and check it can write its own dir but gets EACCES on the sibling's
// secret. Catches a silently-broken setuid (e.g. no CAP_SETUID) that the cap
// bits alone wouldn't — same honesty contract as landlockSmoke.
func uidSmoke(workspaceRoot string) ProbeReport {
	base, err := os.MkdirTemp(workspaceRoot, ".uidprobe-*")
	if err != nil {
		return ProbeReport{Error: fmt.Sprintf("smoke test: temp dir: %v", err)}
	}
	defer os.RemoveAll(base)
	// The probe process (a bed uid) must be able to TRAVERSE base to reach the
	// two dirs under it — MkdirTemp makes it 0700, which would block a non-root
	// uid at the door.
	if err := os.Chmod(base, 0o755); err != nil {
		return ProbeReport{Error: "smoke test: " + err.Error()}
	}
	own := filepath.Join(base, "own")
	sibling := filepath.Join(base, "sibling")
	secret := filepath.Join(sibling, "secret")
	if err := os.MkdirAll(own, 0o755); err != nil {
		return ProbeReport{Error: "smoke test: " + err.Error()}
	}
	if err := os.MkdirAll(sibling, 0o755); err != nil {
		return ProbeReport{Error: "smoke test: " + err.Error()}
	}
	if err := os.WriteFile(secret, []byte("s"), 0o600); err != nil {
		return ProbeReport{Error: "smoke test: " + err.Error()}
	}
	if err := prepareUIDDir(own, bedUID(own)); err != nil {
		return ProbeReport{Error: "smoke test: prepare own: " + err.Error()}
	}
	if err := prepareUIDDir(sibling, bedUID(sibling)); err != nil {
		return ProbeReport{Error: "smoke test: prepare sibling: " + err.Error()}
	}

	script := fmt.Sprintf("echo ok > probe.txt || exit 10; cat %q >/dev/null 2>&1 && exit 11; exit 0", secret)
	cmd := exec.Command("/bin/sh", "-c", script)
	// Production chdirs before dropping UID; the probe must exercise the same order.
	cmd.Dir = own
	user, err := privilege.NewBedUser(bedUID(own), bedUID(own))
	if err != nil {
		return ProbeReport{Error: "smoke test: " + err.Error()}
	}
	if err := user.Wrap(cmd); err != nil {
		return ProbeReport{Error: "smoke test: " + err.Error()}
	}
	report := runExecProbe(cmd)
	if report.ExitCode == nil || *report.ExitCode == 0 {
		return report
	}
	switch *report.ExitCode {
	case 10:
		report.Error = "smoke test: own data dir not writable under the bed uid"
	case 11:
		report.Error = "smoke test: sibling data still readable — uid isolation not enforced"
	default:
		report.Error = "smoke test: " + report.Error
	}
	return report
}

func (u *uidIso) Name() string                 { return "uid" }
func (u *uidIso) Level() Level                 { return Room }
func (u *uidIso) Available() bool              { return true } // only constructed when the smoke passed
func (u *uidIso) View(fs *bedfs.FS) bedfs.View { return bedfs.HostView(fs) }
func (u *uidIso) WorkspaceMounted() bool       { return false }

func (u *uidIso) Wrap(cmd *exec.Cmd, fs *bedfs.FS, cwd string) error {
	cmd.Dir = commandCwd(fs, cwd)
	return nil
}

// Prepare tightens bed_home before BedUser hands the restored tree to this
// Bed's dedicated uid. Ownership is centralized in BedUser so every isolation
// mechanism follows the same file/process identity invariant.
func (u *uidIso) Prepare(fs *bedfs.FS) error {
	return os.Chmod(fs.Home(), 0o700)
}

func prepareUIDDir(dir string, uid int) error {
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	filesystem, err := bedfs.New(dir)
	if err != nil {
		return err
	}
	defer filesystem.Close()
	user, err := privilege.NewBedUser(uid, uid)
	if err != nil {
		return err
	}
	return user.Prepare(filesystem)
}
func (u *uidIso) bedUser(fs *bedfs.FS, _ privilege.BedUser) (privilege.BedUser, error) {
	uid := bedUID(fs.Home())
	return privilege.NewBedUser(uid, uid)
}

func (*uidIso) dedicatedBedUsers() bool { return true }
