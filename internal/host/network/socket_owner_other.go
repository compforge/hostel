//go:build !linux

package network

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// Non-Linux development uses lsof to correlate the listening socket with the
// process group. Missing inspection capability fails closed, not probe-only.
func OwnsTCPListener(ctx context.Context, group, port int) (bool, error) {
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-a", "-g", strconv.Itoa(group), "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return strings.HasPrefix(string(out), "p"), err
}
