package executor

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServiceCompletionDrainsStubbornChildGroup(t *testing.T) {
	e, err := NewLocalFactory(nil).Create(t.Context(), "service-bed")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Shutdown(ctx)
	})
	dir := t.TempDir()
	cmd := exec.Command("/bin/sh", "-c", `trap 'exit 0' TERM; sh -c 'trap "" TERM; echo $$ > child; while :; do sleep 1; done' & wait`)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	p, err := e.(*localExecutor).StartService(t.Context(), "service", cmd)
	if err != nil {
		t.Fatal(err)
	}
	var child int
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); {
		data, _ := os.ReadFile(filepath.Join(dir, "child"))
		child, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		if child > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if child == 0 {
		t.Fatal("child did not start")
	}
	if err := p.(interface{ Signal(syscall.Signal) error }).Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := p.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	out, _ := exec.Command("ps", "-p", strconv.Itoa(child), "-o", "stat=").Output()
	if state := strings.TrimSpace(string(out)); state != "" && !strings.HasPrefix(state, "Z") {
		t.Fatalf("completion published with live child %d: %s", child, state)
	}
}
