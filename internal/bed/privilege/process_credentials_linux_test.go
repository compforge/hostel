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
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestPreferredIdentityRejectsUnsafeInheritedCredentials(t *testing.T) {
	const child = "HOSTEL_TEST_UNSAFE_IDENTITY"
	if os.Getenv(child) == "1" {
		_, err := Resolve(t.Context(), hostfacts.Collect(), Config{}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "shared identity probe") {
			t.Fatalf("inherited root identity retained capabilities without rejection: %v", err)
		}
		return
	}
	if os.Geteuid() != 0 || len(MissingBedIdentityCapabilities(hostfacts.Collect().EffectiveCaps)) != 0 {
		t.Skip("requires root with identity-management capabilities to construct the child condition")
	}
	helper, err := exec.LookPath("setpriv")
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

func TestBedGroupsGrantSharedAccessWithoutSharingPrivateIdentity(t *testing.T) {
	if os.Geteuid() != 0 || len(MissingBedIdentityCapabilities(hostfacts.Collect().EffectiveCaps)) != 0 {
		t.Skip("requires identity capabilities")
	}
	base, err := os.MkdirTemp("", "hostel-group-access-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if err := os.Chmod(base, 0755); err != nil {
		t.Fatal(err)
	}
	shared, private := filepath.Join(base, "shared"), filepath.Join(base, "private")
	for _, path := range []string{shared, private} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chown(shared, 0, 1000); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(shared, os.ModeSetgid|0770); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(private, 200002, 200002); err != nil {
		t.Fatal(err)
	}
	for _, groups := range [][]int{nil, {1000}} {
		user, _ := NewBedUser(200001, 200001)
		user = user.withGroups(groups)
		command := "test ! -r " + private
		if len(groups) == 0 {
			command += " && test ! -w " + shared
		} else {
			command += " && touch " + shared + "/marker && awk '/^Groups:/ { if (NF != 2 || $2 != 1000) exit 1 }' /proc/self/status"
		}
		cmd := exec.Command("/bin/sh", "-c", command)
		cmd.Dir = base
		if err := user.Wrap(cmd); err != nil {
			t.Fatal(err)
		}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("groups %v: %v: %s", groups, err, out)
		}
	}
}

func TestExplicitGroupsCannotBeSilentlyIgnored(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("root fixture")
	}
	const child = "HOSTEL_GROUPS_ROOTLESS_TEST"
	if os.Getenv(child) == "1" {
		_, err := Resolve(t.Context(), hostfacts.Collect(), Config{SupplementaryGIDs: []int{1001}}, t.TempDir())
		if err == nil || !strings.Contains(err.Error(), "supplementary groups") {
			t.Fatalf("missing explicit groups accepted: %v", err)
		}
		return
	}
	helper, err := exec.LookPath("setpriv")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(helper, "--reuid=65534", "--regid=65534", "--groups=1000", "--no-new-privs", "--", os.Args[0], "-test.run=^TestExplicitGroupsCannotBeSilentlyIgnored$", "-test.v")
	cmd.Env = append(os.Environ(), child+"=1")
	cmd.Dir = "/tmp"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("group requirement: %v: %s", err, out)
	}
}
