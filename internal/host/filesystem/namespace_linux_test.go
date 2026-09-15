//go:build linux

package filesystem

import (
	hostprocess "github.com/qiankunli/hostel/internal/host/process"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestMountEntryDefersWorkloadEnvironment(t *testing.T) {
	f, err := os.Open("/")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ns := &MountNamespace{mount: f, root: f, uts: f, ipc: f, helper: "/trusted/hostel"}
	cmd := exec.Command("/bin/sh", "-c", "echo ok")
	cmd.Env = []string{"LD_PRELOAD=/mnt/plugin.so", "GODEBUG=inittrace=1", "PATH=/mnt/tools", "TOKEN=private"}
	want := append([]string(nil), cmd.Env...)
	hostprocess.WrapBedInit(cmd, hostprocess.BedInitPath, "/mnt/work")
	ns.Wrap(cmd)
	if cmd.Path != "/trusted/hostel" || cmd.Dir != "/" {
		t.Fatalf("unsafe bootstrap: %+v", cmd)
	}
	for _, entry := range cmd.Env {
		if !strings.HasPrefix(entry, "HOSTEL_WORKLOAD_ENV_") && entry != "PATH=/usr/bin:/bin" {
			t.Fatalf("workload env exposed to helper: %q", entry)
		}
	}
	for i, entry := range want {
		_, got, _ := strings.Cut(cmd.Env[i+1], "=")
		if got != entry {
			t.Fatalf("entry changed: got %q want %q", got, entry)
		}
	}
	if cmd.Args[4] != "--" || cmd.Args[5] != hostprocess.BedInitPath {
		t.Fatalf("namespace entry must preserve the sealed workload: %v", cmd.Args)
	}
}
