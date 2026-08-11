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

package supervisor

import (
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestUnixpacketKeepsProtocolFramesSeparate(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "protocol.sock")
	addr := &net.UnixAddr{Name: socket, Net: socketNetwork}
	ln, err := net.ListenUnix(socketNetwork, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	serverErr := make(chan error, 1)
	go func() {
		conn, err := ln.AcceptUnix()
		if err != nil {
			serverErr <- err
			return
		}
		defer conn.Close()
		exit := ExitStatus{Kind: ExitStatusExited, ExitCode: 0}
		if err := writeMsg(conn, reply{Pid: 42}, nil); err != nil {
			serverErr <- err
			return
		}
		serverErr <- writeMsg(conn, reply{Exit: &exit}, nil)
	}()

	conn, err := net.DialUnix(socketNetwork, nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	var started reply
	if _, err := readMsg(conn, &started); err != nil {
		t.Fatalf("read pid: %v", err)
	}
	if started.Pid != 42 {
		t.Fatalf("pid = %d, want 42", started.Pid)
	}
	var exited reply
	if _, err := readMsg(conn, &exited); err != nil {
		t.Fatalf("read exit: %v", err)
	}
	if exited.Exit == nil || exited.Exit.Kind != ExitStatusExited || exited.Exit.ExitCode != 0 {
		t.Fatalf("exit = %+v, want exited with code 0", exited.Exit)
	}
	if err := <-serverErr; err != nil {
		t.Fatal(err)
	}
}

func TestReadMsgClosesRightsOnInvalidFrame(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "protocol.sock")
	addr := &net.UnixAddr{Name: socket, Net: socketNetwork}
	ln, err := net.ListenUnix(socketNetwork, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	client, err := net.DialUnix(socketNetwork, nil, addr)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	server, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	before := openFDCount(t)
	if _, _, err := server.WriteMsgUnix(
		[]byte{0, 0, 0}, syscall.UnixRights(int(r.Fd())), nil,
	); err != nil {
		t.Fatal(err)
	}
	var rep reply
	if _, err := readMsg(client, &rep); err == nil {
		t.Fatal("readMsg accepted a short frame")
	}
	if after := openFDCount(t); after != before {
		t.Fatalf("open fd count after invalid frame = %d, want %d", after, before)
	}
}

func openFDCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}
