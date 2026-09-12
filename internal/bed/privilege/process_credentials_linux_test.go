//go:build linux

package privilege

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
)

func TestBedUserWrapDropsRootIdentityAndCapabilities(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to switch to a foreign uid")
	}
	if _, err := hostprivilege.ProcessCredentialHelper(); err != nil {
		t.Skip("setpriv is not installed")
	}
	user, _ := NewBedUser(65534, 65534)
	cmd := exec.Command("/bin/sh", "-c", "id -u; id -g; awk '/^CapEff:/ {print $2} /^CapBnd:/ {print $2} /^NoNewPrivs:/ {print $2}' /proc/self/status")
	cmd.Dir = "/tmp"
	if err := user.Wrap(cmd); err != nil {
		t.Fatal(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("run wrapped command: %v: %s", err, out)
	}
	lines := strings.Fields(string(out))
	want := []string{"65534", "65534", "0000000000000000", "0000000000000000", "1"}
	if strings.Join(lines, ",") != strings.Join(want, ",") {
		t.Fatalf("process credentials = %v, want %v", lines, want)
	}
}

func TestFixedBedUserPreparesOwnership(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires root to hand over the test directory")
	}
	home := t.TempDir()
	file := filepath.Join(home, "workspace", "file")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	filesystem, err := bedfs.New(home)
	if err != nil {
		t.Fatal(err)
	}
	defer filesystem.Close()
	user, _ := NewBedUser(65534, 65534)
	if err := user.Prepare(filesystem); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	stat := info.Sys().(*syscall.Stat_t)
	if int(stat.Uid) != user.UID() || int(stat.Gid) != user.GID() {
		t.Fatalf("owner = %d:%d, want %d:%d", stat.Uid, stat.Gid, user.UID(), user.GID())
	}
}
