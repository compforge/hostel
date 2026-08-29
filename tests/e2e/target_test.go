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
)

type targetOptions struct {
	isolation string
	maxBeds   int
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
	workspaceRoot := filepath.Join(t.TempDir(), "beds")
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
	args := []string{
		"run", "--detach", "--rm", "--name", name, "--network", "host",
		"-e", "HOSTEL_ADDR=" + addr,
		"-e", "HOSTEL_ISOLATION=" + options.isolation,
		"-e", "HOSTEL_EXECUTOR=auto",
		"-e", "HOSTEL_STORE=noop",
		"-e", fmt.Sprintf("HOSTEL_MAX_BEDS=%d", options.maxBeds),
		"-e", fmt.Sprintf("HOSTEL_MAX_PINNED_BEDS=%d", options.maxBeds),
		"-e", "HOSTEL_ADMISSION_CPU_THRESHOLD=0",
		"-e", "HOSTEL_ADMISSION_MEMORY_THRESHOLD=0",
		"-e", "HOSTEL_BED_IDLE_TIMEOUT=0",
		image,
	}
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
