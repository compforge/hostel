package process

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
)

const BedInitArg = "__bedinit_exec"
const BedInitPath = "/.hostel/bedinit"
const workloadEnvPrefix = "HOSTEL_WORKLOAD_ENV_"

// Executable selects Hostel itself, never a tool found through workload PATH.
// Rooted views expose this trusted executable read-only at BedInitPath.
func Executable() string {
	path, err := os.Executable()
	if err != nil {
		panic(err)
	}
	return path
}

// WrapBedInit installs the final bedinit stage and seals caller configuration.
// RunBedInit must execute inside the selected boundary, after dropping credentials.
func WrapBedInit(cmd *exec.Cmd, helper, cwd string) {
	argv := append([]string{helper, BedInitArg, cwd, "--"}, cmd.Args...)
	cmd.Path, cmd.Args, cmd.Err = helper, argv, nil
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	for index, entry := range env {
		cmd.Env = append(cmd.Env, workloadEnvPrefix+strconv.Itoa(index)+"="+entry)
	}
}

// RunBedInit completes bedinit with env, cwd and executable lookup.
// Call only in the disposable bedinit process, after boundary entry and credential drop.
func RunBedInit(args []string) error {
	if len(args) < 3 || args[1] != "--" {
		return fmt.Errorf("invalid workload arguments")
	}
	var env []string
	for index := 0; ; index++ {
		entry, ok := os.LookupEnv(workloadEnvPrefix + strconv.Itoa(index))
		if !ok {
			break
		}
		env = append(env, entry)
	}
	os.Clearenv()
	for _, entry := range env {
		key, value, ok := strings.Cut(entry, "=")
		if !ok {
			return fmt.Errorf("invalid workload environment entry")
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	if err := os.Chdir(args[0]); err != nil {
		return fmt.Errorf("workload cwd: %w", err)
	}
	path, err := exec.LookPath(args[2])
	if err != nil {
		return fmt.Errorf("workload executable: %w", err)
	}
	return syscall.Exec(path, args[2:], os.Environ())
}

// Re-exec works for the daemon and embedded runtimes/test binaries alike.
func init() {
	if len(os.Args) > 1 && os.Args[1] == BedInitArg {
		if err := RunBedInit(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "hostel bedinit (exec):", err)
			os.Exit(1)
		}
		os.Exit(0)
	}
}
