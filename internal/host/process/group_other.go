//go:build !linux

package process

import (
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// The caller pins the leader until this function returns (kqueue on macOS).
func DrainGroup(leader int) error {
	for {
		_ = syscall.Kill(-leader, syscall.SIGKILL)
		out, err := exec.Command("ps", "-axo", "pid=,pgid=,stat=").Output()
		if err != nil {
			return err
		}
		live := false
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) == 3 && f[0] != strconv.Itoa(leader) && f[1] == strconv.Itoa(leader) && !strings.HasPrefix(f[2], "Z") {
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
