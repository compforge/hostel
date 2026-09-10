package isolation

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"

	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/network"
)

// Environment binds the selected file view and one network allocation to a
// resident Bed. Executor replacement does not replace this environment.
type Environment struct {
	files   Isolator
	fs      *bedfs.FS
	network network.Attachment
}

func Bind(files Isolator, fs *bedfs.FS, net network.Attachment) *Environment {
	return &Environment{files: files, fs: fs, network: net}
}

func (e *Environment) View() bedfs.View { return e.files.View(e.fs) }
func (e *Environment) Gateway() string {
	if e.network == nil {
		return ""
	}
	return e.network.Gateway()
}
func (e *Environment) Close(ctx context.Context) error {
	if e.network == nil {
		return nil
	}
	return e.network.Close(ctx)
}

type identity struct{ uid, gid int }
type finalizer func(*exec.Cmd, *identity)
type identityBoundary interface{ identity(*bedfs.FS) identity }
type finalizingIsolator interface {
	wrapFinalized(*exec.Cmd, *bedfs.FS, string, finalizer) error
}

// Wrap is the single command/session composition entry. Privileged preparation
// stays outside the final drop; user code and workspace helpers stay inside.
// +rule=`Network entry, file view and identity setup must finish before user code receives control; a selected environment never falls back on execution failure.`
func (e *Environment) Wrap(cmd *exec.Cmd, cwd string) error {
	if e.network == nil {
		return e.files.Wrap(cmd, e.fs, cwd)
	}
	path, err := exec.LookPath("setpriv")
	if err != nil {
		return fmt.Errorf("isolation: final privilege helper: %w", err)
	}
	finish := func(cmd *exec.Cmd, user *identity) { finalizeCommand(path, cmd, user) }
	if f, ok := e.files.(finalizingIsolator); ok {
		if err := f.wrapFinalized(cmd, e.fs, cwd, finish); err != nil {
			return err
		}
	} else {
		finish(cmd, nil)
		if err := e.files.Wrap(cmd, e.fs, cwd); err != nil {
			return err
		}
	}
	return e.network.Enter(cmd)
}

func finalizeCommand(path string, cmd *exec.Cmd, user *identity) {
	args := []string{path, "--bounding-set=-all", "--inh-caps=-all", "--ambient-caps=-all", "--no-new-privs"}
	if user != nil {
		// setpriv performs the group/UID transition and capability drop as one
		// operation, while it still holds the privileges required for both.
		args = append(args, "--reuid="+strconv.Itoa(user.uid), "--regid="+strconv.Itoa(user.gid), "--clear-groups")
	}
	args = append(args, "--", cmd.Path)
	cmd.Args = append(args, cmd.Args[1:]...)
	cmd.Path = path
}

func (r *resolved) wrapFinalized(cmd *exec.Cmd, fs *bedfs.FS, cwd string, finish finalizer) error {
	if err := r.workspace.Wrap(cmd, fs, cwd); err != nil {
		return err
	}
	if boundary, ok := r.boundary.(identityBoundary); ok {
		user := boundary.identity(fs)
		finish(cmd, &user)
		cmd.Dir = commandCwd(fs, cwd)
		return nil
	}
	finish(cmd, nil)
	return r.boundary.Wrap(cmd, fs, cwd)
}
