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
	"flag"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Run is the bed-init process entry
// (`hostel __bedinit --socket S --bed B --executor E`).
// It never returns to the caller's main path — the exit code is the process's.
func Run(args []string) int {
	fs := flag.NewFlagSet(InitArg, flag.ContinueOnError)
	socket := fs.String("socket", "", "unix socket to serve spawn requests on")
	bed := fs.String("bed", "", "bed id (ps visibility only)")
	executorID := fs.String("executor", "", "executor id")
	if err := fs.Parse(args); err != nil || *socket == "" || *executorID == "" {
		log.Printf("bedinit: bad args (need --socket and --executor): %v", err)
		return 2
	}

	// Subreaper: every descendant orphaned anywhere below us reparents HERE,
	// not to pid 1 — that is what makes the /proc ppid scan in killAll able to
	// enumerate double-forked daemons, and what lets the reaper collect them.
	if err := unix.Prctl(unix.PR_SET_CHILD_SUBREAPER, 1, 0, 0, 0); err != nil {
		log.Printf("bedinit[%s]: set subreaper: %v", *bed, err)
		return 1
	}

	_ = os.Remove(*socket)
	// SOCK_SEQPACKET keeps the pid and exit replies as distinct frames even
	// when a short-lived command exits before the daemon reads either reply.
	ln, err := net.ListenUnix(socketNetwork, &net.UnixAddr{Name: *socket, Net: socketNetwork})
	if err != nil {
		log.Printf("bedinit[%s]: listen %s: %v", *bed, *socket, err)
		return 1
	}
	defer os.Remove(*socket)

	s := &server{
		bed:             *bed,
		executorID:      *executorID,
		state:           executorReady,
		processes:       make(map[string]*processRecord),
		byPID:           make(map[int]*processRecord),
		shutdown:        make(chan struct{}),
		childrenStopped: make(chan struct{}),
		listener:        ln,
	}
	// Install SIGCHLD handling before accepting Start RPCs. Registering inside
	// the goroutine leaves a startup window where a short-lived child can exit
	// before notification is active and remain an unreported zombie.
	sigc := make(chan os.Signal, 64)
	signal.Notify(sigc, syscall.SIGCHLD)
	defer signal.Stop(sigc)
	go s.reap(sigc)

	// Signals and the Shutdown RPC share one graceful path. There is no
	// os.Exit in the signal goroutine: waiters must receive terminal statuses
	// before the executor process disappears.
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(term)
	go func() {
		<-term
		s.requestShutdown()
	}()

	exitCode := 0
	for {
		conn, err := ln.AcceptUnix()
		if err != nil {
			select {
			case <-s.shutdown:
			default:
				log.Printf("bedinit[%s/%s]: accept: %v", *bed, *executorID, err)
				exitCode = 1
				s.requestShutdown()
			}
			break
		}
		s.handlers.Add(1)
		go func() {
			defer s.handlers.Done()
			s.serve(conn)
		}()
	}
	s.killAll()
	close(s.childrenStopped)
	s.handlers.Wait()
	return exitCode
}

type executorState string

const (
	executorReady    executorState = "ready"
	executorDraining executorState = "draining"
)

type server struct {
	bed        string
	executorID string
	listener   *net.UnixListener

	shutdownOnce    sync.Once
	shutdown        chan struct{}
	childrenStopped chan struct{}
	handlers        sync.WaitGroup

	mu        sync.Mutex
	state     executorState
	processes map[string]*processRecord
	byPID     map[int]*processRecord
}

type processRecord struct {
	id       string
	specHash string
	pid      int
	done     chan struct{}
	status   *ExitStatus
}

// reap is the single wait loop: dispatches exit codes for spawned children and
// silently collects adopted orphans. Nobody else may wait4 — os/exec is
// deliberately unused in this process.
func (s *server) reap(sigc <-chan os.Signal) {
	for range sigc {
		for {
			// Fork+registration, group signalling, and reaping share this
			// lock. Therefore a numeric PID can never be signalled after
			// Wait4 releases it for reuse.
			s.mu.Lock()
			var ws syscall.WaitStatus
			pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
			if pid <= 0 || err != nil {
				s.mu.Unlock()
				break
			}
			if !ws.Exited() && !ws.Signaled() {
				s.mu.Unlock()
				continue // stopped/continued: not terminal
			}
			status := ExitStatus{Kind: ExitStatusExited, ExitCode: ws.ExitStatus()}
			if ws.Signaled() {
				status = ExitStatus{
					Kind:       ExitStatusSignaled,
					Signal:     int(ws.Signal()),
					CoreDumped: ws.CoreDump(),
				}
			}
			if process, ok := s.byPID[pid]; ok {
				delete(s.byPID, pid)
				process.status = &status
				close(process.done)
			}
			s.mu.Unlock()
		}
	}
}

func (s *server) serve(conn *net.UnixConn) {
	defer conn.Close()
	var req request
	fds, err := readMsg(conn, &req)
	defer closeFDs(fds)
	if err != nil {
		_ = writeMsg(conn, reply{ExecutorID: s.executorID, Error: "read request: " + err.Error()}, nil)
		return
	}
	if req.ExecutorID != s.executorID {
		_ = writeMsg(conn, reply{ExecutorID: s.executorID, Error: "stale executor identity"}, nil)
		return
	}

	var rep reply
	switch req.Operation {
	case opDescribe:
		rep = reply{ExecutorID: s.executorID}
	case opStart:
		rep = s.start(req, fds)
	case opGet:
		rep = s.get(req.ProcessID)
	case opWait:
		rep = s.wait(req.ProcessID)
	case opSignal:
		rep = s.signal(req.ProcessID, req.Signal)
	case opShutdown:
		s.requestShutdown()
		<-s.childrenStopped
		rep = reply{ExecutorID: s.executorID}
	default:
		rep = reply{ExecutorID: s.executorID, Error: "unknown operation"}
	}
	_ = writeMsg(conn, rep, nil)
}

func (s *server) start(req request, fds []int) reply {
	base := reply{ExecutorID: s.executorID, ProcessID: req.ProcessID}
	if req.ProcessID == "" || len(req.Argv) == 0 || len(fds) != 3 {
		base.Error = "start needs process_id, argv and exactly 3 fds"
		return base
	}
	if req.SpecHash != specHash(req.Argv, req.Dir, req.Env) {
		base.Error = "invalid process specification fingerprint"
		return base
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.processes[req.ProcessID]; ok {
		if existing.specHash != req.SpecHash {
			base.Error = "process id reused with different specification"
			return base
		}
		return s.replyForLocked(existing)
	}
	if s.state != executorReady {
		base.Error = "executor is draining"
		return base
	}
	pid, err := syscall.ForkExec(req.Argv[0], req.Argv, &syscall.ProcAttr{
		Dir:   req.Dir,
		Env:   req.Env,
		Files: []uintptr{uintptr(fds[0]), uintptr(fds[1]), uintptr(fds[2])},
		Sys: &syscall.SysProcAttr{
			Setpgid:   true,
			Pdeathsig: syscall.SIGKILL,
		},
	})
	if err != nil {
		base.Error = "fork: " + err.Error()
		return base
	}
	process := &processRecord{id: req.ProcessID, specHash: req.SpecHash, pid: pid, done: make(chan struct{})}
	s.processes[process.id] = process
	s.byPID[pid] = process
	return s.replyForLocked(process)
}

func (s *server) get(processID string) reply {
	s.mu.Lock()
	defer s.mu.Unlock()
	process, ok := s.processes[processID]
	if !ok {
		return reply{ExecutorID: s.executorID, ProcessID: processID, Error: "process not found"}
	}
	return s.replyForLocked(process)
}

func (s *server) wait(processID string) reply {
	s.mu.Lock()
	process, ok := s.processes[processID]
	if !ok {
		s.mu.Unlock()
		return reply{ExecutorID: s.executorID, ProcessID: processID, Error: "process not found"}
	}
	done := process.done
	s.mu.Unlock()
	<-done
	return s.get(processID)
}

func (s *server) signal(processID string, signal int) reply {
	base := reply{ExecutorID: s.executorID, ProcessID: processID}
	if signal != int(syscall.SIGKILL) {
		base.Error = "only SIGKILL is supported"
		return base
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	process, ok := s.processes[processID]
	if !ok {
		base.Error = "process not found"
		return base
	}
	if process.status == nil {
		_ = signalSpawnedProcessGroup(process.pid, syscall.SIGKILL)
	}
	return s.replyForLocked(process)
}

func (s *server) replyForLocked(process *processRecord) reply {
	rep := reply{ExecutorID: s.executorID, ProcessID: process.id, Pid: process.pid}
	if process.status == nil {
		rep.State = processRunning
		return rep
	}
	status := *process.status
	rep.State = processExited
	rep.Exit = &status
	return rep
}

func (s *server) requestShutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		s.state = executorDraining
		s.mu.Unlock()
		close(s.shutdown)
		_ = s.listener.Close()
	})
}

var signalSpawnedProcessGroup = func(pid int, signal syscall.Signal) error {
	return syscall.Kill(-pid, signal)
}

// killAll force-kills every descendant. The /proc ppid scan enumerates both
// direct children and reparented (setsid/double-fork) orphans — as subreaper
// we are their parent now; killing by pgid AND pid per round, re-scanning
// until the tree is empty, converges even when kills race fresh forks.
func (s *server) killAll() {
	self := os.Getpid()
	for range 50 {
		pids := childrenOf(self)
		if len(pids) == 0 && s.runningCount() == 0 {
			return
		}
		for _, pid := range pids {
			_ = syscall.Kill(-pid, syscall.SIGKILL)
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		time.Sleep(20 * time.Millisecond) // let the reaper drain
	}
	log.Printf("bedinit[%s]: descendants survived kill loop", s.bed)
}

func (s *server) runningCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.byPID)
}

// childrenOf lists live pids whose ppid is p (via /proc/*/stat; comm may
// contain anything, so fields are taken after the LAST ')').
func childrenOf(p int) []int {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	var out []int
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		stat, err := os.ReadFile("/proc/" + e.Name() + "/stat")
		if err != nil {
			continue
		}
		rest := string(stat)
		if i := strings.LastIndexByte(rest, ')'); i >= 0 {
			rest = rest[i+1:]
		}
		f := strings.Fields(rest)
		if len(f) >= 2 && f[1] == strconv.Itoa(p) {
			out = append(out, pid)
		}
	}
	return out
}
