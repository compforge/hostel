package manager

import (
	"context"
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
	launch  func(*exec.Cmd, string) error
}

func bindEnvironment(files isolation.Isolator, fs *bedfs.FS, net network.Attachment, user privilege.BedUser) *Environment {
	return &Environment{files: files, fs: fs, network: net, user: user,
		launch: isolation.BindBedInit(files, fs, user.UID(), user.GID())}
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
	if err := e.launch(cmd, cwd); err != nil {
		return err
	}
	if e.network == nil {
		return nil
	}
	return e.network.Enter(cmd)
}
