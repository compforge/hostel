//go:build !linux

package executor

import (
	"fmt"
	"os/exec"
)

func preparePIDNamespace(*exec.Cmd) (func(), error) {
	return nil, fmt.Errorf("private PID namespaces require Linux")
}
