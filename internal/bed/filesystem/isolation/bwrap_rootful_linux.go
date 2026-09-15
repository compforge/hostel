//go:build linux

package isolation

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sync"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
	"golang.org/x/sys/unix"
)

type rootfulMounts struct {
	helper string
	mu     sync.Mutex
	views  map[*bedfs.FS]*hostfs.MountNamespace
}

func canPrepareRootful(facts hostfacts.Snapshot) bool {
	if facts.EUID != 0 {
		return false
	}
	for _, capability := range []uint{unix.CAP_SYS_ADMIN, unix.CAP_SYS_CHROOT, unix.CAP_SETUID, unix.CAP_SETGID, unix.CAP_SETPCAP} {
		if !facts.HasCap(capability) {
			return false
		}
	}
	return true
}

func (b *bwrap) Prepare(fs *bedfs.FS) error {
	return b.PrepareContext(context.Background(), fs)
}

func (b *bwrap) PrepareContext(ctx context.Context, fs *bedfs.FS) error {
	if b.rootful == nil {
		return nil
	}
	b.rootful.mu.Lock()
	prepared := b.rootful.views[fs] != nil
	b.rootful.mu.Unlock()
	if prepared {
		return nil
	}
	// /proc supplies the trusted executable FD during preparation. Silently
	// overriding a requested mapping would violate the caller's data contract.
	for _, mapping := range fs.PathMappings() {
		if model.PathsOverlap(mapping.BedPath, "/proc") {
			return fmt.Errorf("rootful filesystem: /proc is reserved for runtime preparation")
		}
	}
	args := b.args(fs.Rootfs(), fs.Workdir(), bedfs.DefaultWorkdir, fs.PathMappings())
	view, err := hostfs.PrepareMountNamespace(ctx, b.path, b.rootful.helper, args)
	if err != nil {
		return err
	}
	b.rootful.mu.Lock()
	b.rootful.views[fs] = view
	b.rootful.mu.Unlock()
	log.Printf("filesystem: prepared Bed mount namespace mode=rootful")
	return nil
}

func (b *bwrap) Release(fs *bedfs.FS) error {
	if b.rootful == nil {
		return nil
	}
	b.rootful.mu.Lock()
	defer b.rootful.mu.Unlock()
	view := b.rootful.views[fs]
	if view == nil {
		return nil
	}
	if err := view.Close(); err != nil {
		return err
	}
	delete(b.rootful.views, fs)
	return nil
}

// WrapPrepared uses resources prepared by Bed Manager. False means this
// boundary uses the unprivileged path, not that execution may retry or degrade.
func (b *bwrap) WrapPrepared(cmd *exec.Cmd, fs *bedfs.FS, cwd string, uid, gid int) (bool, error) {
	if b.rootful == nil {
		return false, nil
	}
	b.rootful.mu.Lock()
	view := b.rootful.views[fs]
	b.rootful.mu.Unlock()
	if view == nil {
		return true, fmt.Errorf("rootful filesystem: Bed mount namespace was not prepared")
	}
	processCwd, err := b.View(fs).Path(commandCwd(fs, cwd))
	if err != nil {
		return true, err
	}
	cmd.Path = rootExecutable(fs, cmd.Path, b.View(fs).MappingSupport())
	view.Wrap(cmd, uid, gid, processCwd)
	return true, nil
}

func (b *bwrap) rootfulSmoke() hostfacts.ProbeReport {
	home, err := os.MkdirTemp(b.root, ".probe-rootful-*")
	if err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}
	}
	defer os.RemoveAll(home)
	fs, err := bedfs.New(home)
	if err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}
	}
	defer fs.Close()
	if err := os.MkdirAll(fs.Workdir(), 0755); err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}
	}
	if err := b.Prepare(fs); err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}
	}
	cmd := exec.Command("/bin/sh", "-c", "mkdir -p /mnt/probe && printf root-view > /mnt/probe/file")
	_, err = b.WrapPrepared(cmd, fs, "", os.Geteuid(), os.Getegid())
	var report hostfacts.ProbeReport
	if err == nil {
		report = hostfacts.RunExecProbe(cmd)
	} else {
		report.Error = err.Error()
	}
	if report.Succeeded() {
		data, readErr := os.ReadFile(home + "/mnt/probe/file")
		if readErr != nil || string(data) != "root-view" {
			report.Error = fmt.Sprintf("native root write did not reach BedFS: %v", readErr)
		}
	}
	if err := b.Release(fs); err != nil {
		report.Error = fmt.Sprintf("rootful probe cleanup: %v", err)
	}
	return report
}
