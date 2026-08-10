//go:build linux

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

package executor

import (
	"os/exec"
	"syscall"

	"github.com/qiankunli/hostel/internal/resource"
)

func bindProcessCgroup(cmd *exec.Cmd, tracker resource.Tracker, bedID string) (func(), error) {
	group, err := tracker.OpenGroup(bedID)
	if err != nil {
		return nil, err
	}
	if group == nil {
		return func() {}, nil
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.UseCgroupFD = true
	cmd.SysProcAttr.CgroupFD = int(group.Fd())
	return func() { _ = group.Close() }, nil
}
