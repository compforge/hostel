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
	"io"
	"log"
	"os"

	"github.com/qiankunli/hostel/protocol"
	"golang.org/x/sys/unix"
)

// DetectInlineLimit samples a fresh socket in the daemon sender's network namespace.
func DetectInlineLimit(fallback int) protocol.SupervisorInlineLimitStatus {
	fd, err := unix.Socket(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	var sndbuf int
	if err == nil {
		defer unix.Close(fd)
		sndbuf, err = unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_SNDBUF)
	}
	report := resolveInlineLimit(sndbuf, err, fallback)
	log.Printf("supervisor: inline_limit_bytes=%d source=%s sndbuf_bytes=%d reason=%q", report.Value, report.Source, report.SendBufferBytes, report.Reason)
	return report
}

func resolveInlineLimit(sndbuf int, err error, fallback int) protocol.SupervisorInlineLimitStatus {
	if err != nil {
		return protocol.SupervisorInlineLimitStatus{Value: fallback, Source: "fallback", Reason: err.Error()}
	}
	// +why=`Linux AF_UNIX seqpacket reserves 32 bytes from sk_sndbuf; writeMsg adds a 4-byte length prefix.`
	limit := min(MaxMessageSize, max(0, sndbuf-32-4))
	return protocol.SupervisorInlineLimitStatus{Value: limit, Source: "detected", SendBufferBytes: sndbuf}
}

func newStartSpecFile(payload []byte) (_ *os.File, err error) {
	fd, err := unix.MemfdCreate("hostel-start-spec", unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "hostel-start-spec")
	defer func() {
		if err != nil {
			_ = file.Close()
		}
	}()
	if _, err = file.Write(payload); err != nil {
		return nil, fmt.Errorf("write start specification: %w", err)
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind start specification: %w", err)
	}
	seals := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err = unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, seals); err != nil {
		return nil, fmt.Errorf("seal start specification: %w", err)
	}
	return file, nil
}
