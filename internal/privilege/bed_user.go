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

	"github.com/qiankunli/hostel/internal/bedfs"
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
