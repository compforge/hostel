//go:build linux

// Package process contains host process-identity primitives, not Bed policy.
package process

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DrainGroup must run while the group leader is an unreaped child: that zombie
// pins its numeric identity. Services run in foreground and must not detach
// descendants into other groups. Zombies cannot write or retain sockets.
func DrainGroup(leader int) error {
	for {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		entries, err := os.ReadDir("/proc")
		if err != nil {
			return err
		}
		live := false
		for _, entry := range entries {
			if entry.Name() == strconv.Itoa(leader) {
				continue
			}
			data, err := os.ReadFile(filepath.Join("/proc", entry.Name(), "stat"))
			if err != nil {
				continue
			}
			end := strings.LastIndexByte(string(data), ')')
			if end < 0 {
				continue
			}
			fields := strings.Fields(string(data)[end+1:])
			if len(fields) >= 3 && fields[2] == strconv.Itoa(leader) && fields[0] != "Z" && fields[0] != "X" {
				live = true
				break
			}
		}
		if !live {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
}
