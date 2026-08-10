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

// Package bedinit implements the per-bed init process (docs/kernel.md
// 〈进程树〉, S1): a tiny spawner-reaper the daemon re-execs once per bed. Bed
// commands are forked BY bedinit (parentage is decided by who forks), so the
// bed owns a real process tree: teardown = SIGTERM bedinit → it kills every
// descendant (a /proc ppid scan also catches reparented setsid orphans — it is
// the subreaper) and exits. The daemon talks to it over a unix socket, one
// connection per spawn, stdio crossing as SCM_RIGHTS fds.
//
// Shape follows containerd's shim (small per-unit process owning a tree,
// IPC to the daemon); tini/dumb-init don't fit (pure reapers, no spawn IPC)
// and a shell doesn't either (in-band stdin protocol — the exact fragility
// that killed the shared foreground shell).
package bedinit

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// InitArg is the hidden subcommand hostel re-execs into to become a bed's
// init: `hostel __bedinit --socket <path> --bed <id>`.
const InitArg = "__bedinit"

const (
	socketNetwork  = "unixpacket"
	maxMessageSize = 128 << 10
)

// spawnRequest asks bedinit to fork one fully-specified command. Argv[0] is an
// absolute path (the daemon resolves via exec.LookPath); Env is the COMPLETE
// child environment. Three SCM_RIGHTS fds ride along: stdin, stdout, stderr.
type spawnRequest struct {
	Argv []string `json:"argv"`
	Dir  string   `json:"dir,omitempty"`
	Env  []string `json:"env"`
}

// signalRequest is daemon → bedinit after a successful spawn. Bedinit accepts
// the signal only while pid is still present in its live-child table.
type signalRequest struct {
	Signal int `json:"signal"`
}

// ExitStatus is the kernel-level terminal fact observed by bedinit. Keeping
// signal separate from exit code avoids the lossy 128+signal convention at
// the process boundary.
type ExitStatusKind string

const (
	ExitStatusExited   ExitStatusKind = "exited"
	ExitStatusSignaled ExitStatusKind = "signaled"
)

type ExitStatus struct {
	Kind       ExitStatusKind `json:"kind"`
	ExitCode   int            `json:"exit_code,omitempty"`
	Signal     int            `json:"signal,omitempty"`
	CoreDumped bool           `json:"core_dumped,omitempty"`
}

// reply is bedinit → daemon. Exactly two replies per connection: {pid} once
// the child is running (or {error}), then {exit} when it is reaped.
type reply struct {
	Pid   int         `json:"pid,omitempty"`
	Exit  *ExitStatus `json:"exit,omitempty"`
	Error string      `json:"error,omitempty"`
}

// writeMsg sends one length-prefixed JSON message, with fds attached as
// SCM_RIGHTS. unixpacket preserves this write as one protocol frame.
func writeMsg(conn *net.UnixConn, v any, fds []int) error {
	payload, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(payload) > maxMessageSize {
		return fmt.Errorf("bedinit: message too large: %d bytes", len(payload))
	}
	buf := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint32(buf, uint32(len(payload)))
	copy(buf[4:], payload)
	var oob []byte
	if len(fds) > 0 {
		oob = syscall.UnixRights(fds...)
	}
	n, oobn, err := conn.WriteMsgUnix(buf, oob, nil)
	if err != nil {
		return err
	}
	if n != len(buf) {
		return io.ErrShortWrite
	}
	if oobn != len(oob) {
		return fmt.Errorf("bedinit: short control write: got %d, want %d", oobn, len(oob))
	}
	return nil
}

// readMsg reads exactly one unixpacket frame and collects any SCM_RIGHTS fds
// attached to it. Message boundaries are part of the protocol: accepting
// trailing bytes would hide a sender that accidentally combined two replies.
func readMsg(conn *net.UnixConn, v any) ([]int, error) {
	buf := make([]byte, 4+maxMessageSize)
	oob := make([]byte, syscall.CmsgSpace(16*4))
	n, oobn, flags, _, err := conn.ReadMsgUnix(buf, oob)
	fds, rightsErr := parseRights(oob[:oobn])
	fail := func(err error) ([]int, error) {
		closeFDs(fds)
		return nil, err
	}
	if err != nil {
		return fail(err)
	}
	if rightsErr != nil {
		return fail(rightsErr)
	}
	if flags&(unix.MSG_TRUNC|unix.MSG_CTRUNC) != 0 {
		return fail(fmt.Errorf("bedinit: message or control data truncated"))
	}
	if n < 4 {
		return fail(fmt.Errorf("bedinit: short message: %d bytes", n))
	}
	need := int(binary.BigEndian.Uint32(buf[:4]))
	if need != n-4 {
		return fail(fmt.Errorf("bedinit: message length mismatch: header=%d payload=%d", need, n-4))
	}
	if err := json.Unmarshal(buf[4:n], v); err != nil {
		return fail(fmt.Errorf("bedinit: decode message: %w", err))
	}
	return fds, nil
}

func parseRights(oob []byte) ([]int, error) {
	if len(oob) == 0 {
		return nil, nil
	}
	msgs, err := syscall.ParseSocketControlMessage(oob)
	if err != nil {
		return nil, fmt.Errorf("bedinit: parse control message: %w", err)
	}
	var fds []int
	for _, m := range msgs {
		got, err := syscall.ParseUnixRights(&m)
		if err != nil {
			return fds, fmt.Errorf("bedinit: parse rights: %w", err)
		}
		fds = append(fds, got...)
	}
	return fds, nil
}

func closeFDs(fds []int) {
	for _, fd := range fds {
		_ = syscall.Close(fd)
	}
}
