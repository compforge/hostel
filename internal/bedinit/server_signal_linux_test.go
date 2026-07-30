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

package bedinit

import (
	"syscall"
	"testing"
)

func TestServerSignalsOnlyLiveChildIdentity(t *testing.T) {
	originalSignal := signalSpawnedProcessGroup
	t.Cleanup(func() { signalSpawnedProcessGroup = originalSignal })

	var calls int
	signalSpawnedProcessGroup = func(pid int, signal syscall.Signal) error {
		calls++
		if pid != 42 || signal != syscall.SIGKILL {
			t.Fatalf("signal = (%d, %s), want (42, killed)", pid, signal)
		}
		return nil
	}
	s := &server{watchers: map[int]chan int{42: make(chan int, 1)}}
	s.signalProcessGroupIfRunning(42, syscall.SIGKILL)

	s.mu.Lock()
	delete(s.watchers, 42) // the reaper releases the numeric identity
	s.mu.Unlock()
	s.signalProcessGroupIfRunning(42, syscall.SIGKILL)
	if calls != 1 {
		t.Fatalf("signal calls = %d, want 1", calls)
	}
}
