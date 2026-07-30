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
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestInProcExitBarrierSerializesProcessGroupSignal(t *testing.T) {
	originalSignal := signalProcessGroup
	t.Cleanup(func() { signalProcessGroup = originalSignal })

	signalEntered := make(chan struct{})
	releaseSignal := make(chan struct{})
	var calls atomic.Int32
	signalProcessGroup = func(int, syscall.Signal) error {
		calls.Add(1)
		close(signalEntered)
		<-releaseSignal
		return nil
	}
	proc := &inProcProc{pid: 424242, untrack: func() {}}

	killDone := make(chan struct{})
	go func() {
		proc.Kill()
		close(killDone)
	}()
	<-signalEntered

	barrierDone := make(chan struct{})
	go func() {
		proc.markExitedBeforeReap(nil)
		close(barrierDone)
	}()
	select {
	case <-barrierDone:
		t.Fatal("exit barrier bypassed an in-flight process-group signal")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseSignal)
	<-killDone
	<-barrierDone

	proc.Kill()
	if got := calls.Load(); got != 1 {
		t.Fatalf("process group signalled after exit barrier: calls=%d", got)
	}
}
