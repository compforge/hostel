//go:build linux

package isolation

import (
	"os/exec"
	"slices"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	"golang.org/x/sys/unix"
)

func TestRootfulRequiresActualHostCapabilities(t *testing.T) {
	facts := hostfacts.Snapshot{EUID: 0}
	if canPrepareRootful(facts) {
		t.Fatal("UID zero alone granted rootful preparation")
	}
	for _, cap := range []uint{unix.CAP_SYS_ADMIN, unix.CAP_SYS_CHROOT, unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_SETPCAP, unix.CAP_DAC_OVERRIDE} {
		facts.EffectiveCaps |= 1 << cap
	}
	if !canPrepareRootful(facts) {
		t.Fatal("complete rootful preparation prerequisites rejected")
	}
	facts.EffectiveCaps &^= 1 << unix.CAP_DAC_OVERRIDE
	if canPrepareRootful(facts) {
		t.Fatal("rootful preparation must traverse private Bed directories")
	}
	facts.EffectiveCaps |= 1 << unix.CAP_DAC_OVERRIDE
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

func TestRootfulReadOnlyRemountOrder(t *testing.T) {
	const mask = "/etc/masked"
	b := &bwrap{root: "/beds", maskPaths: []string{mask}}
	for _, tc := range []struct {
		name     string
		readOnly bool
	}{{"writable", false}, {"read-only", true}} {
		t.Run(tc.name, func(t *testing.T) {
			fs, err := bedfs.New(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer fs.Close()
			fs.PrepareProcessRoot(tc.readOnly)
			file := hostfs.Mapping{Source: "/generated/hosts", Target: "/etc/hosts"}
			fs.SetSystemFiles([]hostfs.Mapping{file})
			mounts := b.pillowMounts(fs)
			maskIndex := slices.Index(mounts, hostfs.Mount{Kind: hostfs.Tmpfs, Target: mask})
			fileIndex := slices.Index(mounts, hostfs.Mount{Kind: hostfs.ReadOnlyBind, Source: file.Source, Target: file.Target})
			if maskIndex < 0 || fileIndex < 0 {
				t.Fatalf("missing tmpfs mask or system file binding: %+v", mounts)
			}
			if !tc.readOnly {
				for _, mount := range mounts {
					if mount.Kind == hostfs.RemountReadOnly {
						t.Fatalf("unexpected read-only remount: %+v", mount)
					}
				}
				return
			}
			maskRemount := hostfs.Mount{Kind: hostfs.RemountReadOnly, Target: mask}
			if got := slices.Index(mounts, maskRemount); got != maskIndex+1 {
				t.Fatalf("mask at %d, read-only remount at %d", maskIndex, got)
			}
			rootRemount := hostfs.Mount{Kind: hostfs.RemountReadOnly, Target: "/"}
			if got := slices.Index(mounts, rootRemount); got != len(mounts)-1 {
				t.Fatalf("root remount at %d, want final operation after file binding at %d", got, fileIndex)
			}
		})
	}
}
