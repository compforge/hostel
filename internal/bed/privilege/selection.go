package privilege

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

type Level string

const (
	Shared    Level = "shared"
	Dedicated Level = "dedicated"
	UIDMin          = 200000
	UIDMax          = 299999
)

func ExpectedLevel(room bed.RoomType) Level {
	if room == bed.Dorm {
		return Shared
	}
	return Dedicated
}
func (l Level) Room() bed.RoomType {
	if l == Dedicated {
		return bed.Suite
	}
	return bed.Dorm
}

type Selection struct {
	Expected  Level                 `json:"expected"`
	Effective Level                 `json:"effective"`
	Supported []Level               `json:"supported"`
	Reason    string                `json:"reason,omitempty"`
	Probe     hostfacts.ProbeReport `json:"probe"`
	Policy    BedUserReport         `json:"user"`
	User      BedUser               `json:"-"`
}

// Resolve owns identity selection independently of the filesystem mechanism.
// Explicit users are requirements; automatic identity can fall back to the
// daemon identity only after verifying actual child credentials and file access.
func Resolve(ctx context.Context, facts hostfacts.Snapshot, cfg Config, room bed.RoomType, root string) (Selection, error) {
	s := Selection{Expected: ExpectedLevel(room), Effective: Shared}
	user := CurrentBedUser()
	if cfg.Explicit {
		var err error
		user, err = NewBedUser(cfg.UID, cfg.GID)
		if err != nil {
			return s, err
		}
	}
	if cfg.Explicit && (user.UID() != facts.EUID || user.GID() != facts.EGID) {
		if runtime.GOOS != "linux" || len(MissingBedIdentityCapabilities(facts.EffectiveCaps)) != 0 {
			return s, fmt.Errorf("privilege: explicit Bed user %d:%d cannot be managed with current capabilities", user.UID(), user.GID())
		}
	}
	// Keep the existing non-root fixed identity when it can be fully managed.
	if !cfg.Explicit && facts.EUID == 0 && len(MissingBedIdentityCapabilities(facts.EffectiveCaps)) == 0 {
		user, _ = NewBedUser(1000, 1000)
	}
	s.User, s.Policy = user, BedUserReport{Strategy: "fixed", UID: user.UID(), GID: user.GID()}
	base, cleanupErr := probeIdentity(ctx, root, user)
	if cleanupErr != nil {
		return s, cleanupErr
	}
	if !base.Succeeded() {
		if cfg.Explicit || user == CurrentBedUser() {
			return s, fmt.Errorf("privilege: shared identity probe: %s", base.Error)
		}
		user = CurrentBedUser()
		base, cleanupErr = probeIdentity(ctx, root, user)
		if cleanupErr != nil {
			return s, cleanupErr
		}
		if !base.Succeeded() {
			return s, fmt.Errorf("privilege: inherited identity probe: %s", base.Error)
		}
		s.User, s.Policy = user, BedUserReport{Strategy: "fixed", UID: user.UID(), GID: user.GID()}
	}
	s.Supported, s.Probe = []Level{Shared}, base
	if runtime.GOOS != "linux" || len(MissingBedIdentityCapabilities(facts.EffectiveCaps)) != 0 {
		s.Reason = "dedicated identity management unavailable"
		return s, nil
	}
	dedicated, _ := NewBedUser(UIDMin, UIDMin)
	s.Probe, cleanupErr = probeIdentity(ctx, root, dedicated)
	if cleanupErr != nil {
		return s, cleanupErr
	}
	if !s.Probe.Succeeded() {
		s.Reason = s.Probe.Error
		return s, nil
	}
	s.Supported = append(s.Supported, Dedicated)
	// Probe support independently of the profile and fixed-user preference.
	// Those inputs constrain selection, not what the host demonstrated.
	if cfg.Explicit {
		s.Reason = "explicit fixed Bed user"
		return s, nil
	}
	if s.Expected == Shared {
		return s, nil
	}
	s.Effective = Dedicated
	s.Policy = BedUserReport{Strategy: "per_bed", UIDMin: UIDMin, UIDMax: UIDMax}
	return s, nil
}

// WithoutDedicatedIdentity preserves capability facts while choosing the
// already-probed shared identity after all stronger combinations failed.
func (s Selection) WithoutDedicatedIdentity(reason string) (Selection, bool) {
	if s.Effective != Dedicated {
		return s, false
	}
	s.Effective, s.Reason = Shared, "dedicated identity combination unavailable: "+reason
	s.Policy = BedUserReport{Strategy: "fixed", UID: s.User.UID(), GID: s.User.GID()}
	return s, true
}

func probeIdentity(ctx context.Context, root string, user BedUser) (report hostfacts.ProbeReport, cleanupErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	home, err := os.MkdirTemp(root, ".identity-probe-")
	if err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}, nil
	}
	defer func() {
		if err := os.RemoveAll(home); err != nil {
			cleanupErr = fmt.Errorf("identity probe cleanup %s: %w", home, err)
		}
	}()
	if err := os.Chmod(home, 0755); err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}, nil
	}
	data := filepath.Join(home, "data")
	if err := os.Mkdir(data, 0700); err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}, nil
	}
	if user.UID() != os.Geteuid() || user.GID() != os.Getegid() {
		if err := os.Chown(data, user.UID(), user.GID()); err != nil {
			return hostfacts.ProbeReport{Error: err.Error()}, nil
		}
	}
	command := "test \"$(/usr/bin/id -u)\" = " + strconv.Itoa(user.UID()) +
		" && test \"$(/usr/bin/id -g)\" = " + strconv.Itoa(user.GID()) + " && printf identity > marker"
	if runtime.GOOS == "linux" {
		sets := "Inh|Prm|Eff|Amb"
		if user.UID() == 0 {
			sets += "|Bnd"
		}
		command += " && /usr/bin/awk '$1 == \"NoNewPrivs:\" { n=$2 } $1 ~ /^Cap(" + sets + "):/ && $2 != \"0000000000000000\" { bad=1 } END { exit (bad || n != 1) }' /proc/self/status"
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", command)
	cmd.Dir = data
	if err := user.Wrap(cmd); err != nil {
		return hostfacts.ProbeReport{Error: err.Error()}, nil
	}
	return hostfacts.RunExecProbe(cmd), nil
}
