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

package isolation

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	ptraceProbeTracee  = "/bin/true"
	ptraceProbeTimeout = 3 * time.Second
)

// These are the baseline options PRoot installs before entering its trace
// loop. PTRACE_O_TRACESECCOMP is deliberately excluded: PRoot treats that as
// an optional acceleration and falls back to these options when unavailable.
const prootPtraceOptions = unix.PTRACE_O_TRACESYSGOOD |
	unix.PTRACE_O_TRACEFORK |
	unix.PTRACE_O_TRACEVFORK |
	unix.PTRACE_O_TRACEVFORKDONE |
	unix.PTRACE_O_TRACEEXEC |
	unix.PTRACE_O_TRACECLONE |
	unix.PTRACE_O_TRACEEXIT

// runPtraceProbe exercises the minimum tracer/tracee handshake PRoot relies
// on. SysProcAttr.Ptrace makes the child call PTRACE_TRACEME before exec; the
// parent then installs PRoot's baseline options and advances one syscall stop.
// Keeping this as a real smoke probe captures seccomp, Yama, capability and
// container-runtime policy together without guessing which policy denied it.
func runPtraceProbe() (report ProbeReport) {
	report.ConfiguredPath = ptraceProbeTracee
	if _, err := os.Stat(ptraceProbeTracee); err != nil {
		report.Error = fmt.Sprintf("resolve ptrace probe tracee %s: %v", ptraceProbeTracee, err)
		return report
	}
	report.ResolvedPath = ptraceProbeTracee
	report.Attempted = true
	started := time.Now()
	defer func() { report.DurationMS = time.Since(started).Milliseconds() }()

	// Linux records the ptracer by thread, not merely by process. The thread
	// that forks the TRACEME child must therefore own every following ptrace
	// request until the child is detached.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	proc, err := os.StartProcess(ptraceProbeTracee, []string{ptraceProbeTracee}, &os.ProcAttr{
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
		Sys:   &syscall.SysProcAttr{Ptrace: true},
	})
	if err != nil {
		report.Error = fmt.Sprintf("ptrace TRACEME: %v", err)
		return report
	}
	defer proc.Release()

	pid := proc.Pid
	reaped := false
	defer func() {
		if !reaped {
			cleanupPtraceTracee(pid)
		}
	}()
	deadline := started.Add(ptraceProbeTimeout)

	status, err := waitPtraceEvent(pid, deadline)
	if err != nil {
		report.Error = fmt.Sprintf("wait for ptrace exec stop: %v", err)
		return report
	}
	if !status.Stopped() {
		report.Error = fmt.Sprintf("wait for ptrace exec stop: unexpected status %#x", uint32(status))
		if status.Exited() || status.Signaled() {
			reaped = true
		}
		return report
	}
	if err := unix.PtraceSetOptions(pid, prootPtraceOptions); err != nil {
		report.Error = fmt.Sprintf("ptrace SETOPTIONS: %v", err)
		return report
	}
	if err := unix.PtraceSyscall(pid, 0); err != nil {
		report.Error = fmt.Sprintf("ptrace SYSCALL: %v", err)
		return report
	}
	status, err = waitPtraceEvent(pid, deadline)
	if err != nil {
		report.Error = fmt.Sprintf("wait for ptrace syscall stop: %v", err)
		return report
	}
	if !status.Stopped() {
		report.Error = fmt.Sprintf("wait for ptrace syscall stop: unexpected status %#x", uint32(status))
		if status.Exited() || status.Signaled() {
			reaped = true
		}
		return report
	}
	if err := unix.PtraceDetach(pid); err != nil {
		report.Error = fmt.Sprintf("ptrace DETACH: %v", err)
		return report
	}

	status, err = waitPtraceEvent(pid, deadline)
	if err != nil {
		report.Error = fmt.Sprintf("wait for ptrace tracee exit: %v", err)
		return report
	}
	reaped = true
	if !status.Exited() {
		report.Error = fmt.Sprintf("ptrace tracee did not exit normally: status %#x", uint32(status))
		return report
	}
	code := status.ExitStatus()
	report.ExitCode = &code
	if code != 0 {
		report.Error = fmt.Sprintf("ptrace tracee exited with code %d", code)
	}
	return report
}

func waitPtraceEvent(pid int, deadline time.Time) (unix.WaitStatus, error) {
	for {
		var status unix.WaitStatus
		waited, err := unix.Wait4(pid, &status, unix.WNOHANG, nil)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if waited == pid {
			return status, nil
		}
		if time.Now().After(deadline) {
			return 0, errors.New("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func cleanupPtraceTracee(pid int) {
	_ = unix.Kill(pid, unix.SIGKILL)
	deadline := time.Now().Add(100 * time.Millisecond)
	for time.Now().Before(deadline) {
		status, err := waitPtraceEvent(pid, deadline)
		if errors.Is(err, unix.ECHILD) {
			return
		}
		if err != nil || status.Exited() || status.Signaled() {
			return
		}
		if status.Stopped() {
			_ = unix.PtraceCont(pid, int(unix.SIGKILL))
		}
	}
}
