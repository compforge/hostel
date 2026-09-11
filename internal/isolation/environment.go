package isolation

import (
	"context"
	"fmt"
	"os/exec"

	"github.com/qiankunli/hostel/internal/bedfs"
	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/privilege"
)

// Environment binds the selected file view and one network allocation to a
// resident Bed. Executor replacement does not replace this environment.
type Environment struct {
	files   Isolator
	fs      *bedfs.FS
	network network.Attachment
	user    privilege.BedUser
}

func Bind(files Isolator, fs *bedfs.FS, net network.Attachment, user privilege.BedUser) *Environment {
	return &Environment{files: files, fs: fs, network: net, user: user}
}

func (e *Environment) View() bedfs.View           { return e.files.View(e.fs) }
func (e *Environment) BedUser() privilege.BedUser { return e.user }
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

// Wrap is the single command/session composition entry. Privileged preparation
// stays outside the final drop; user code and workspace helpers stay inside.
// +rule=`Network entry, file view and identity setup must finish before user code receives control; a selected environment never falls back on execution failure.`
func (e *Environment) Wrap(cmd *exec.Cmd, cwd string) error {
	// Preserve exec.Cmd's normal startup contract before replacing cmd.Path
	// with helpers such as setpriv or bwrap. Without this check a missing shell
	// appears to start successfully and only fails inside the helper process.
	path, err := exec.LookPath(cmd.Path)
	if err != nil {
		return fmt.Errorf("isolation: resolve command %q: %w", cmd.Path, err)
	}
	cmd.Path = path
	if err := e.files.Wrap(cmd, e.fs, cwd); err != nil {
		return err
	}
	// The selected file mechanisms run without daemon privileges. Drop to the
	// Bed user outside them so bwrap's user namespace maps the final UID rather
	// than only root; network.Enter remains outside because it needs NET_ADMIN.
	if err := e.user.Wrap(cmd); err != nil {
		return err
	}
	if e.network == nil {
		return nil
	}
	return e.network.Enter(cmd)
}
