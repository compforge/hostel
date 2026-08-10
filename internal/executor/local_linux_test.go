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
	"context"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"github.com/qiankunli/hostel/internal/resource"
)

func TestLocalExecutorSetsParentDeathSignal(t *testing.T) {
	factory := NewLocalFactory(resource.Noop("test"))
	bedExecutor, err := factory.Create(context.Background(), "bed-pdeathsig")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/true")
	cmd.Env = os.Environ()
	process, err := bedExecutor.Start(context.Background(), "process-pdeathsig", cmd)
	if err != nil {
		t.Fatal(err)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Pdeathsig != syscall.SIGKILL {
		t.Fatalf("Pdeathsig = %+v, want SIGKILL", cmd.SysProcAttr)
	}
	if _, err := process.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := bedExecutor.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
