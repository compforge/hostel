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
	"fmt"
	"net"
	"os"
	"syscall"
)

// Client addresses one executor instance. Every operation uses a fresh socket;
// process state lives in bed-init, never in a connection.
type Client struct {
	socket     string
	executorID string
}

// RemoteError is a semantic rejection returned by a live Executor. Callers
// must not mistake it for transport loss and replace the Executor.
type RemoteError struct {
	Operation string
	Message   string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("bedinit: %s: %s", e.Operation, e.Message)
}

func NewClient(socket, executorID string) *Client {
	return &Client{socket: socket, executorID: executorID}
}

func (c *Client) Describe() error {
	_, err := c.call(request{Operation: opDescribe, ExecutorID: c.executorID}, nil)
	return err
}

// Start is idempotent by processID + spec fingerprint. If the first response
// is lost after fork, retrying cannot launch a duplicate command.
func (c *Client) Start(processID string, argv []string, dir string, env []string, stdin, stdout, stderr *os.File) (int, error) {
	req := request{
		Operation:  opStart,
		ExecutorID: c.executorID,
		ProcessID:  processID,
		SpecHash:   specHash(argv, dir, env),
		Argv:       argv,
		Dir:        dir,
		Env:        env,
	}
	fds := []int{int(stdin.Fd()), int(stdout.Fd()), int(stderr.Fd())}
	rep, err := c.call(req, fds)
	if err != nil {
		return 0, err
	}
	if rep.Pid <= 0 {
		return 0, fmt.Errorf("bedinit: start %s returned invalid pid %d", processID, rep.Pid)
	}
	return rep.Pid, nil
}

func (c *Client) Get(processID string) (running bool, status *ExitStatus, err error) {
	rep, err := c.call(request{Operation: opGet, ExecutorID: c.executorID, ProcessID: processID}, nil)
	if err != nil {
		return false, nil, err
	}
	return rep.State == processRunning, rep.Exit, nil
}

func (c *Client) Wait(processID string) (ExitStatus, error) {
	rep, err := c.call(request{Operation: opWait, ExecutorID: c.executorID, ProcessID: processID}, nil)
	if err != nil {
		return ExitStatus{}, err
	}
	if rep.Exit == nil {
		return ExitStatus{}, fmt.Errorf("bedinit: wait %s returned without terminal status", processID)
	}
	return *rep.Exit, nil
}

func (c *Client) Kill(processID string) error {
	_, err := c.call(request{
		Operation:  opSignal,
		ExecutorID: c.executorID,
		ProcessID:  processID,
		Signal:     int(syscall.SIGKILL),
	}, nil)
	return err
}

func (c *Client) Shutdown() error {
	_, err := c.call(request{Operation: opShutdown, ExecutorID: c.executorID}, nil)
	return err
}

func (c *Client) call(req request, fds []int) (reply, error) {
	raddr := &net.UnixAddr{Name: c.socket, Net: socketNetwork}
	conn, err := net.DialUnix(socketNetwork, nil, raddr)
	if err != nil {
		return reply{}, fmt.Errorf("bedinit: dial executor %s: %w", c.executorID, err)
	}
	defer conn.Close()
	if err := writeMsg(conn, req, fds); err != nil {
		return reply{}, fmt.Errorf("bedinit: send %s: %w", req.Operation, err)
	}
	var rep reply
	if _, err := readMsg(conn, &rep); err != nil {
		return reply{}, fmt.Errorf("bedinit: read %s: %w", req.Operation, err)
	}
	if rep.ExecutorID != c.executorID {
		return reply{}, fmt.Errorf("bedinit: executor mismatch: got %q want %q", rep.ExecutorID, c.executorID)
	}
	if rep.Error != "" {
		return reply{}, &RemoteError{Operation: string(req.Operation), Message: rep.Error}
	}
	return rep, nil
}
