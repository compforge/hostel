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
	hostprocess "github.com/qiankunli/hostel/internal/host/process"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

const bedInitCredentialArg = "__bedinit_credentials"

// ProcessCredentialHelper uses the running image, independent of workload PATH
// and external credential utilities. Probes and real execution use this entry.
func ProcessCredentialHelper() (string, error) {
	return hostprocess.Executable(), nil
}

// WrapCredentials is the complete credential-only launch path, also used by
// identity probes. Filesystem composition uses WrapBedInit around a sealed workload.
func WrapCredentials(cmd *exec.Cmd, uid, gid int) error {
	cwd := cmd.Dir
	if cwd == "" {
		cwd = "."
	}
	hostprocess.WrapBedInit(cmd, hostprocess.Executable(), cwd)
	WrapBedInit(cmd, hostprocess.Executable(), uid, gid)
	return nil
}

// WrapBedInit installs the credential stage of bedinit.
// It keeps workload env sealed until the final bedinit stage.
func WrapBedInit(cmd *exec.Cmd, helper string, uid, gid int) {
	args := []string{helper, bedInitCredentialArg, strconv.Itoa(uid), strconv.Itoa(gid), "--", cmd.Path}
	cmd.Args, cmd.Path = append(args, cmd.Args[1:]...), helper
}

func init() {
	if len(os.Args) < 2 || os.Args[1] != bedInitCredentialArg {
		return
	}
	err := runBedInit(os.Args[2:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "hostel bedinit (credentials):", err)
		os.Exit(1)
	}
	os.Exit(0)
}

func runBedInit(args []string) error {
	if len(args) < 4 || args[2] != "--" {
		return fmt.Errorf("invalid credential arguments")
	}
	uid, err := strconv.Atoi(args[0])
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(args[1])
	if err != nil {
		return err
	}
	// Pin the trusted image while it is still readable to the daemon. Embedded
	// runtimes may live below a directory that the final UID cannot traverse.
	executable := args[3]
	if executable == hostprocess.Executable() || executable == hostprocess.BedInitPath {
		f, err := os.Open(executable)
		if err != nil {
			return err
		}
		defer f.Close()
		executable = fmt.Sprintf("/proc/self/fd/%d", f.Fd())
	}
	if err := DropCredentials(uid, gid); err != nil {
		return err
	}
	return syscall.Exec(executable, args[3:], os.Environ())
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
