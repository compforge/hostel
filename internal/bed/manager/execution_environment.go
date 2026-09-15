package manager

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"os/exec"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
)

// Environment binds the selected file view and one network allocation to a
// resident Bed. Executor replacement does not replace this environment.
type Environment struct {
	files   isolation.Isolator
	fs      *bedfs.FS
	network network.Attachment
	user    privilege.BedUser
}

func bindEnvironment(files isolation.Isolator, fs *bedfs.FS, net network.Attachment, user privilege.BedUser) *Environment {
	return &Environment{files: files, fs: fs, network: net, user: user}
}

func (e *Environment) View() bedfs.ProcessView    { return e.files.View(e.fs) }
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

// Wrap is the single command/session/Service composition entry. The caller must
// finalize cmd.Env first: privileged entry carries it as inert payload until
// the workload credential transition. Never replace cmd.Env after this call.
// +rule=`Bed Manager owns resource preparation; execution only enters the prepared environment and drops privilege before user code. A selected environment never falls back on execution failure.`
func (e *Environment) Wrap(cmd *exec.Cmd, cwd string) error {
	// Preserve exec.Cmd's normal startup contract before replacing cmd.Path
	// with helpers such as setpriv or bwrap. Without this check a missing shell
	// appears to start successfully and only fails inside the helper process.
	path, err := exec.LookPath(cmd.Path)
	if err != nil {
		return fmt.Errorf("isolation: resolve command %q: %w", cmd.Path, err)
	}
	cmd.Path = path
	if prepared, ok := e.files.(isolation.PreparedExecution); ok {
		used, err := prepared.WrapPrepared(cmd, e.fs, cwd, e.user.UID(), e.user.GID())
		if err != nil {
			return err
		}
		if used {
			if e.network != nil {
				return e.network.Enter(cmd)
			}
			return nil
		}
	}
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
