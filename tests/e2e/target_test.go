//go:build e2e

package e2e_test

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	binaryEnv   = "HOSTEL_E2E_BINARY"
	imageEnv    = "HOSTEL_E2E_IMAGE"
	userlandEnv = "HOSTEL_E2E_USERLAND"
	pathshimEnv = "HOSTEL_E2E_PATHSHIM"
	prootEnv    = "HOSTEL_E2E_PROOT"
)

type targetOptions struct {
	isolation        string
	maxBeds          int
	allowPtrace      bool
	helperPath       string
	pathshim         string
	pathshimHostPath string
	proot            string
	prootHostPath    string
	workspaceRoot    string
}

type target struct {
	client *apiClient
}

// startTarget owns one real hostel process. Tests talk only to its public HTTP
// API; process/container lifecycle is fixture plumbing, not an alternate server.
func startTarget(t *testing.T, options targetOptions) *target {
	t.Helper()
	if options.isolation == "" {
		options.isolation = "dorm"
	}
	if options.maxBeds == 0 {
		options.maxBeds = 4
	}

	binary := strings.TrimSpace(os.Getenv(binaryEnv))
	image := strings.TrimSpace(os.Getenv(imageEnv))
	if binary == "" && image == "" {
		t.Skipf("set %s to a hostel binary or %s to a container image", binaryEnv, imageEnv)
	}
	if binary != "" && image != "" {
		t.Fatalf("set only one of %s and %s", binaryEnv, imageEnv)
	}

	port := reservePort(t)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	baseURL := "http://" + addr
	if image != "" {
		startImageTarget(t, image, addr, options)
	} else {
		startBinaryTarget(t, binary, addr, options)
	}

	c := newAPIClient(baseURL)
	deadline := time.Now().Add(45 * time.Second)
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var health healthView
		_, lastErr = c.json(ctx, "GET", "/healthz", "", nil, &health)
		cancel()
		if lastErr == nil && health.OK {
			return &target{client: c}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("hostel did not become ready at %s: %v", baseURL, lastErr)
	return nil
}

func reservePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve loopback port: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("release loopback port: %v", err)
	}
	return port
}

func startBinaryTarget(t *testing.T, binary, addr string, options targetOptions) {
	t.Helper()
	absolute, err := filepath.Abs(binary)
	if err != nil {
		t.Fatalf("resolve hostel binary: %v", err)
	}
	if _, err := os.Stat(absolute); err != nil {
		t.Fatalf("hostel binary %s: %v", absolute, err)
	}

	logPath := filepath.Join(t.TempDir(), "hostel.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		t.Fatalf("create hostel log: %v", err)
	}
	workspaceRoot := options.workspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = filepath.Join(t.TempDir(), "beds")
	}
	pathshim := options.pathshim
	if options.pathshimHostPath != "" {
		pathshim = options.pathshimHostPath
	}
	if pathshim == "" {
		pathshim = strings.TrimSpace(os.Getenv(pathshimEnv))
	}
	proot := options.proot
	if options.prootHostPath != "" {
		proot = options.prootHostPath
	}
	if proot == "" {
		proot = strings.TrimSpace(os.Getenv(prootEnv))
	}
	cmd := exec.Command(absolute,
		"--addr", addr,
		"--workspace-root", workspaceRoot,
		"--isolation", options.isolation,
		"--executor", "auto",
		"--store", "noop",
		"--max-beds", fmt.Sprint(options.maxBeds),
		"--max-pinned-beds", fmt.Sprint(options.maxBeds),
		"--admission-cpu-threshold", "0",
		"--admission-memory-threshold", "0",
		"--bed-idle-timeout", "0",
	)
	searchPath := options.helperPath
	if searchPath == "" {
		searchPath = prependHelperDirs(os.Getenv("PATH"), pathshim, proot)
	}
	if searchPath != "" {
		cmd.Env = append(os.Environ(), "PATH="+searchPath)
	}
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		t.Fatalf("start hostel binary: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	t.Cleanup(func() {
		_ = cmd.Process.Signal(os.Interrupt)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
		_ = logFile.Close()
		if t.Failed() {
			if raw, err := os.ReadFile(logPath); err == nil {
				t.Logf("hostel log:\n%s", raw)
			}
		}
	})
}

func startImageTarget(t *testing.T, image, addr string, options targetOptions) {
	t.Helper()
	name := fmt.Sprintf("hostel-e2e-%d-%d", os.Getpid(), time.Now().UnixNano())
	workspaceRoot := options.workspaceRoot
	if workspaceRoot == "" {
		workspaceRoot = "/tmp/" + name + "-beds"
	}
	args := []string{
		"run", "--detach", "--rm", "--name", name, "--network", "host",
		"-e", "HOSTEL_ADDR=" + addr,
		// Most isolation E2E keeps carrier paths outside the guest /workspace
		// bind. A test may override this to reproduce a real carrier-root layout.
		"-e", "HOSTEL_WORKSPACE_ROOT=" + workspaceRoot,
		"-e", "HOSTEL_ISOLATION=" + options.isolation,
		"-e", "HOSTEL_EXECUTOR=auto",
		"-e", "HOSTEL_STORE=noop",
		"-e", fmt.Sprintf("HOSTEL_MAX_BEDS=%d", options.maxBeds),
		"-e", fmt.Sprintf("HOSTEL_MAX_PINNED_BEDS=%d", options.maxBeds),
		"-e", "HOSTEL_ADMISSION_CPU_THRESHOLD=0",
		"-e", "HOSTEL_ADMISSION_MEMORY_THRESHOLD=0",
		"-e", "HOSTEL_BED_IDLE_TIMEOUT=0",
	}
	if options.allowPtrace {
		args = append(args, "--cap-add", "SYS_PTRACE")
	}
	helperPaths := make([]string, 0, 2)
	if options.pathshimHostPath != "" {
		const guestPath = "/tmp/pathshim"
		args = append(args, "--volume", options.pathshimHostPath+":"+guestPath+":ro")
		options.pathshim = guestPath
	}
	if options.pathshim != "" {
		helperPaths = append(helperPaths, options.pathshim)
	}
	if options.prootHostPath != "" {
		const guestPath = "/tmp/proot"
		args = append(args, "--volume", options.prootHostPath+":"+guestPath+":ro")
		options.proot = guestPath
	}
	if options.proot != "" {
		helperPaths = append(helperPaths, options.proot)
	}
	searchPath := options.helperPath
	if searchPath == "" && len(helperPaths) > 0 {
		searchPath = prependHelperDirs("/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin", helperPaths...)
	}
	if searchPath != "" {
		args = append(args, "-e", "PATH="+searchPath)
	}
	args = append(args, image)
	output, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("start hostel image %s: %v\n%s", image, err, output)
	}
	t.Cleanup(func() {
		var logs bytes.Buffer
		logCmd := exec.Command("docker", "logs", name)
		logCmd.Stdout = &logs
		logCmd.Stderr = &logs
		_ = logCmd.Run()
		_ = exec.Command("docker", "rm", "--force", name).Run()
		if t.Failed() {
			t.Logf("hostel container log:\n%s", logs.String())
		}
	})
}

func prependHelperDirs(base string, helpers ...string) string {
	paths := make([]string, 0, len(helpers)+1)
	seen := map[string]bool{}
	for _, helper := range helpers {
		if helper = strings.TrimSpace(helper); helper == "" {
			continue
		}
		dir := filepath.Dir(helper)
		if !seen[dir] {
			paths = append(paths, dir)
			seen[dir] = true
		}
	}
	if base != "" {
		paths = append(paths, base)
	}
	return strings.Join(paths, string(os.PathListSeparator))
}
