//go:build linux

package isolation

import (
	"os/exec"
	"testing"

	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	"golang.org/x/sys/unix"
)

func TestRootfulRequiresActualHostCapabilities(t *testing.T) {
	facts := hostfacts.Snapshot{EUID: 0}
	if canPrepareRootful(facts) {
		t.Fatal("UID zero alone granted rootful preparation")
	}
	for _, cap := range []uint{unix.CAP_SYS_ADMIN, unix.CAP_SYS_CHROOT, unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_SETPCAP} {
		facts.EffectiveCaps |= 1 << cap
	}
	if !canPrepareRootful(facts) {
		t.Fatal("complete rootful preparation prerequisites rejected")
	}
	facts.EUID = 1000
	if canPrepareRootful(facts) {
		t.Fatal("non-root helper cannot use bwrap's rootful mode")
	}
}

func TestRootfulExecutionNeverPreparesOrFallsBack(t *testing.T) {
	b := &bwrap{rootful: &rootfulMounts{}}
	err := b.Wrap(exec.Command("/bin/true"), nil, "")
	if err == nil {
		t.Fatal("execution without prepared resources must fail closed")
	}
}
