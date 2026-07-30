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

package bed

import (
	"errors"
	"fmt"
	"os/exec"

	"golang.org/x/sys/unix"
)

var waitid = unix.Waitid

// waitCommandBeforeReap observes exit without reaping, publishes the terminal
// state, then lets os/exec reap. The callback can therefore serialize numeric
// PID/PGID signals while the kernel still reserves the group leader's PID.
func waitCommandBeforeReap(cmd *exec.Cmd, markBeforeReap func(error) error) error {
	var (
		info       unix.Siginfo
		barrierErr error
	)
	for {
		err := waitid(
			unix.P_PID,
			cmd.Process.Pid,
			&info,
			unix.WEXITED|unix.WNOWAIT,
			nil,
		)
		if err == nil {
			break
		}
		if errors.Is(err, unix.EINTR) {
			continue
		}
		barrierErr = fmt.Errorf(
			"bed: waitid WNOWAIT for pid %d: %w",
			cmd.Process.Pid,
			err,
		)
		break
	}
	if err := markBeforeReap(barrierErr); err != nil {
		// The callback could not make signalling safe. Do not reap: retaining
		// the zombie keeps the numeric PID reserved instead of exposing a
		// reused process group to a later Kill.
		return errors.Join(barrierErr, err)
	}
	if barrierErr != nil {
		return errors.Join(barrierErr, cmd.Wait())
	}
	return cmd.Wait()
}
