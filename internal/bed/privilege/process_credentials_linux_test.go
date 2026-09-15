//go:build linux

package privilege

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostprivilege "github.com/qiankunli/hostel/internal/host/privilege"
)

func TestPreferredIdentityRejectsUnsafeInheritedCredentials(t *testing.T) {
	const child = "HOSTEL_TEST_UNSAFE_IDENTITY"
	if os.Getenv(child) == "1" {
		_, err := Resolve(t.Context(), hostfacts.Collect(), Config{UID: 1000, GID: 1000, Configured: true}, bed.Dorm, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "shared identity probe") {
			t.Fatalf("inherited root identity retained capabilities without rejection: %v", err)
		}
		return
	}
	if os.Geteuid() != 0 || len(MissingBedIdentityCapabilities(hostfacts.Collect().EffectiveCaps)) != 0 {
		t.Skip("requires root with identity-management capabilities to construct the child condition")
	}
	helper, err := hostprivilege.ProcessCredentialHelper()
	if err != nil {
		t.Fatal(err)
	}
	// Remove both the ability to switch UID and the ability to clear the
	// remaining bounding capabilities, only in this disposable test subprocess.
	cmd := exec.Command(helper, "--bounding-set=-setuid,-setpcap", "--no-new-privs", "--", os.Args[0], "-test.run=^TestPreferredIdentityRejectsUnsafeInheritedCredentials$", "-test.v")
	cmd.Env = append(os.Environ(), child+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("unsafe inherited identity regression: %v: %s", err, out)
	}
}

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
