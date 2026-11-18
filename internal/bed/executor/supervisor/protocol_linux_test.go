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
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
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

func TestStartUsesDetectedInlineLimit(t *testing.T) {
	const fallback = 4096
	limit := resolveInlineLimit(65536, nil, fallback)
	if limit.Value != 65500 || limit.Source != "detected" || limit.SendBufferBytes != 65536 {
		t.Fatalf("64 KiB socket: got %+v", limit)
	}
	if got := resolveInlineLimit(1<<20, nil, fallback); got.Value != MaxMessageSize {
		t.Fatalf("protocol cap: got %+v", got)
	}
	if got := resolveInlineLimit(0, syscall.EPERM, fallback); got.Value != fallback || got.Source != "fallback" || got.Reason != syscall.EPERM.Error() {
		t.Fatalf("probe failure: got %+v", got)
	}

	for _, tc := range []struct {
		name     string
		envBytes int
		wantFD   bool
	}{
		{"small", 1024, false}, {"between_socket_and_protocol_limits", 80 << 10, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			socket := filepath.Join(t.TempDir(), "start.sock")
			ln, err := net.ListenUnix(socketNetwork, &net.UnixAddr{Name: socket, Net: socketNetwork})
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			env := []string{"VALUE=" + strings.Repeat("x", tc.envBytes)}
			server := make(chan error, 1)
			go func() {
				conn, err := ln.AcceptUnix()
				if err != nil {
					server <- err
					return
				}
				defer conn.Close()
				var req request
				fds, err := readMsg(conn, &req)
				defer closeFDs(fds)
				if err == nil && req.SpecInFD != tc.wantFD {
					err = fmt.Errorf("spec_in_fd=%t, want %t", req.SpecInFD, tc.wantFD)
				}
				if err == nil && tc.wantFD {
					if len(fds) != 4 {
						err = fmt.Errorf("got %d descriptors, want 4", len(fds))
					} else {
						var spec startSpec
						spec, err = readStartSpec(fds[3])
						if err == nil && (len(spec.Env) != 1 || spec.Env[0] != env[0]) {
							err = fmt.Errorf("incomplete environment in memfd")
						}
					}
				}
				if err == nil {
					err = writeMsg(conn, reply{ExecutorID: testExecutorID, Pid: 42}, nil)
				}
				server <- err
			}()
			client := NewClient(socket, testExecutorID, limit.Value)
			_, err = client.Start("start", []string{"/bin/true"}, "", env, os.Stdin, os.Stdout, os.Stderr)
			if err != nil {
				t.Fatal(err)
			}
			if err := <-server; err != nil {
				t.Fatal(err)
			}
		})
	}
}
