//go:build linux

package network

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// OwnsTCPListener correlates a LISTEN socket with a live process group. A
// successful HTTP probe alone could belong to an unrelated port squatter.
// Namespace tables come from group members, not the daemon's network scope.
func OwnsTCPListener(ctx context.Context, group, port int) (bool, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return false, err
	}
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		root := filepath.Join("/proc", entry.Name())
		stat, err := os.ReadFile(filepath.Join(root, "stat"))
		if err != nil {
			continue
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			continue
		}
		fields := strings.Fields(string(stat)[end+1:])
		if len(fields) < 3 || fields[2] != strconv.Itoa(group) {
			continue
		}
		inodes := make(map[string]bool)
		for _, table := range []string{"tcp", "tcp6"} {
			data, err := os.ReadFile(filepath.Join(root, "net", table))
			if err != nil {
				continue
			}
			for _, line := range strings.Split(string(data), "\n") {
				f := strings.Fields(line)
				if len(f) < 10 || f[3] != "0A" {
					continue
				}
				_, p, ok := strings.Cut(f[1], ":")
				if !ok {
					continue
				}
				n, _ := strconv.ParseInt(p, 16, 32)
				if int(n) == port {
					inodes["socket:["+f[9]+"]"] = true
				}
			}
		}
		fds, err := os.ReadDir(filepath.Join(root, "fd"))
		if err != nil {
			return false, fmt.Errorf("inspect service socket ownership: %w", err)
		}
		for _, fd := range fds {
			target, _ := os.Readlink(filepath.Join(root, "fd", fd.Name()))
			if inodes[target] {
				return true, nil
			}
		}
	}
	return false, nil
}
