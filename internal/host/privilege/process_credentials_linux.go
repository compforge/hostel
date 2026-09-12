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

package privilege

import (
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const credentialHelper = "setpriv"

// ProcessCredentialHelper resolves the helper independently from workspace
// helper discovery. A deployment may narrow PATH to disable pathshim/PRoot;
// distro setpriv remains a core runtime helper at its standard location.
func ProcessCredentialHelper() (string, error) {
	if path, err := exec.LookPath(credentialHelper); err == nil {
		return path, nil
	}
	for _, candidate := range []string{"/usr/bin/setpriv", "/bin/setpriv"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			return filepath.Clean(path), nil
		}
	}
	return "", fmt.Errorf("exec: %q: executable file not found", credentialHelper)
}

// WrapCredentials applies process credentials before user code. setpriv performs
// identity switching and capability removal in one operation while the child
// still has the privileges required for both.
func WrapCredentials(cmd *exec.Cmd, uid, gid int) error {
	path, err := ProcessCredentialHelper()
	if err != nil {
		return fmt.Errorf("privilege: credential helper: %w", err)
	}
	args := []string{path}
	// Dropping the bounding set requires CAP_SETPCAP. A root daemon has it and
	// removes that ceiling as defense in depth. An ordinary daemon cannot do so;
	// clearing all active sets plus no_new_privs still prevents the child from
	// acquiring capabilities through a later exec.
	if os.Geteuid() == 0 {
		args = append(args, "--bounding-set=-all")
	}
	args = append(args, "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs")
	if uid != os.Geteuid() || gid != os.Getegid() {
		args = append(args,
			"--reuid="+strconv.Itoa(uid),
			"--regid="+strconv.Itoa(gid),
			"--clear-groups",
		)
	}
	args = append(args, "--", cmd.Path)
	cmd.Args = append(args, cmd.Args[1:]...)
	cmd.Path = path
	return nil
}

// ChownTree recursively chowns root to uid:gid. Lchown retargets symlinks,
// rather than their referents; WalkDir does not descend through symlinked dirs.
// Multiply-linked regular files are skipped because changing their owner could
// hand the target user ownership of an inode also reachable outside the requested tree.
func ChownTree(root string, uid, gid int) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type().IsRegular() {
			info, infoErr := entry.Info()
			if infoErr != nil {
				return infoErr
			}
			if stat, ok := info.Sys().(*syscall.Stat_t); ok && stat.Nlink > 1 {
				return nil
			}
		}
		return os.Lchown(path, uid, gid)
	})
}
