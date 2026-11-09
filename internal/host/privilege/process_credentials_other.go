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

//go:build !linux

package privilege

import (
	"fmt"
	"os"
	"os/exec"
	"slices"
)

func ProcessCredentialHelper() (string, error) {
	return "", fmt.Errorf("privilege: credential switching is unsupported on this platform")
}

func WrapCredentials(_ *exec.Cmd, uid, gid int, groups []int) error {
	if uid != os.Geteuid() || gid != os.Getegid() {
		return fmt.Errorf("privilege: switching user is unsupported on this platform")
	}
	actual := DefaultGroups()
	slices.Sort(actual)
	desired := slices.Clone(groups)
	slices.Sort(desired)
	if !slices.Equal(actual, desired) {
		return fmt.Errorf("privilege: supplementary group switching is unsupported on this platform")
	}
	return nil
}

func WrapBedInit(_ *exec.Cmd, _ string, _, _ int, _ []int) {}

func DefaultGroups() []int { groups, _ := os.Getgroups(); return groups }

func ChownTree(string, int, int) error { return nil }
