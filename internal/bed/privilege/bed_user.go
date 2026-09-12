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

// Package privilege owns the operating-system privileges applied to Bed
// processes and files. Resource authorization and isolation-policy selection
// belong to their respective packages.
package privilege

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

// BedUser is the Unix user selected for processes and files owned by one Bed.
type BedUser struct {
	uid int
	gid int
}

// BedUserReport describes how an instance assigns Unix users to Bed processes.
type BedUserReport struct {
	Strategy string `json:"strategy"`
	UID      int    `json:"uid,omitempty"`
	GID      int    `json:"gid,omitempty"`
	UIDMin   int    `json:"uid_min,omitempty"`
	UIDMax   int    `json:"uid_max,omitempty"`
}

// Identity is the daemon's effective Unix identity at startup.
type Identity struct {
	UID int `json:"uid"`
	GID int `json:"gid"`
}

// SetprivReport records whether the credential-switching helper was resolved.
// It is captured once during Manager construction; diagnostics reads do not
// repeat PATH discovery.
type SetprivReport struct {
	Required  bool   `json:"required"`
	Available bool   `json:"available"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// Requirements names the Linux capabilities needed to run and clean up Bed
// processes under an identity other than the daemon's own identity.
type Requirements struct {
	Capabilities        []string `json:"capabilities"`
	MissingCapabilities []string `json:"missing_capabilities,omitempty"`
	Satisfied           bool     `json:"satisfied"`
}

// Report is the cached, operator-facing privilege configuration and verdict.
type Report struct {
	ReservedUsers          int           `json:"reserved_users"`
	PreconditionsSatisfied bool          `json:"preconditions_satisfied"`
	Daemon                 Identity      `json:"daemon"`
	BedUser                BedUserReport `json:"bed_user"`
	Setpriv                SetprivReport `json:"setpriv"`
	Requirements           Requirements  `json:"requirements"`
}

// CapabilityRequirement identifies one Linux capability in CapEff.
type CapabilityRequirement struct {
	Bit  uint
	Name string
}

// A root Hostel that hands a Bed tree to another uid and later supervises that
// process needs the complete set below. Kubernetes drop: ["ALL"] removes all
// of them, including CAP_KILL and CAP_SETPCAP, so deployments must add the
// requirements explicitly.
var bedIdentityCapabilities = []CapabilityRequirement{
	{0, "CAP_CHOWN"},
	{1, "CAP_DAC_OVERRIDE"},
	{3, "CAP_FOWNER"},
	{5, "CAP_KILL"},
	{6, "CAP_SETGID"},
	{7, "CAP_SETUID"},
	{8, "CAP_SETPCAP"},
}

// RequiredBedIdentityCapabilities returns a copy of the requirements used by
// both UID isolation selection and diagnostics.
func RequiredBedIdentityCapabilities() []CapabilityRequirement {
	return append([]CapabilityRequirement(nil), bedIdentityCapabilities...)
}

// MissingBedIdentityCapabilities returns required names absent from CapEff.
func MissingBedIdentityCapabilities(effectiveCaps uint64) []string {
	var missing []string
	for _, capability := range bedIdentityCapabilities {
		if effectiveCaps&(uint64(1)<<capability.Bit) == 0 {
			missing = append(missing, capability.Name)
		}
	}
	return missing
}

// NewReport captures privilege diagnostics from startup facts. effectiveCaps
// is Linux CapEff; it is zero on other platforms.
func NewReport(bedUser BedUserReport, effectiveCaps uint64) Report {
	report := Report{
		Daemon:  Identity{UID: os.Geteuid(), GID: os.Getegid()},
		BedUser: bedUser,
	}
	report.Setpriv.Required = runtime.GOOS == "linux"
	if path, err := ProcessCredentialHelper(); err != nil {
		report.Setpriv.Error = err.Error()
	} else {
		report.Setpriv.Available = true
		report.Setpriv.Path = path
	}

	needsSwitch := bedUser.Strategy == "per_bed" || bedUser.UID != report.Daemon.UID || bedUser.GID != report.Daemon.GID
	if needsSwitch {
		for _, capability := range RequiredBedIdentityCapabilities() {
			report.Requirements.Capabilities = append(report.Requirements.Capabilities, capability.Name)
		}
		report.Requirements.MissingCapabilities = MissingBedIdentityCapabilities(effectiveCaps)
	}
	sort.Strings(report.Requirements.Capabilities)
	sort.Strings(report.Requirements.MissingCapabilities)
	report.Requirements.Satisfied = len(report.Requirements.MissingCapabilities) == 0
	report.PreconditionsSatisfied = report.Requirements.Satisfied && (!report.Setpriv.Required || report.Setpriv.Available)
	return report
}

// NewBedUser validates a configured Bed user. UID/GID zero are forbidden:
// Hostel may need root itself, but caller-controlled Bed code must not inherit it.
func NewBedUser(uid, gid int) (BedUser, error) {
	if uid <= 0 || gid <= 0 {
		return BedUser{}, fmt.Errorf("privilege: bed user must be non-root, got %d:%d", uid, gid)
	}
	return BedUser{uid: uid, gid: gid}, nil
}

// CurrentBedUser preserves the embedding process identity. cmd/hostel replaces
// it with the configured non-root user before it can initialize any Bed.
func CurrentBedUser() BedUser {
	return BedUser{uid: os.Geteuid(), gid: os.Getegid()}
}

func (u BedUser) UID() int { return u.uid }
func (u BedUser) GID() int { return u.gid }

// Prepare hands the BedFS tree to the same user that will run its commands.
// BedFS refreshes the owner afterwards so later daemon-side file API writes are
// handed back to that user as well.
func (u BedUser) Prepare(fs *bedfs.FS) error {
	if u.uid != os.Geteuid() || u.gid != os.Getegid() {
		if err := chownTree(fs.Home(), u.uid, u.gid); err != nil {
			return fmt.Errorf("prepare bed user %d:%d: %w", u.uid, u.gid, err)
		}
	}
	return fs.RefreshOwner()
}

// Wrap applies the Bed's process credentials outside any filesystem helper.
func (u BedUser) Wrap(cmd *exec.Cmd) error { return wrapBedUser(cmd, u) }
