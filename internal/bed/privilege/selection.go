package privilege

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"time"

	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

type Level string

const (
	Shared    Level = "shared"
	Dedicated Level = "dedicated"
	UIDMin          = 200000
	UIDMax          = 299999
)

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
// Supplemental groups grant resource access without selecting a shared identity.
func Resolve(ctx context.Context, facts hostfacts.Snapshot, cfg Config, root string) (Selection, error) {
	s := Selection{Expected: Dedicated, Effective: Shared}
	if err := cfg.Validate(); err != nil {
		return s, err
	}
	inherited := CurrentBedUser()
	if cfg.SupplementaryGIDs != nil {
		inherited = inherited.withGroups(cfg.SupplementaryGIDs)
	}
	user := inherited
	canManageIdentity := runtime.GOOS == "linux" && len(MissingBedIdentityCapabilities(facts.EffectiveCaps)) == 0
	// Use a non-root shared baseline when it can be fully managed.
	if facts.EUID == 0 && canManageIdentity {
		user, _ = NewBedUser(1000, 1000)
		user = user.withGroups(cfg.SupplementaryGIDs)
	}
	base, cleanupErr := probeIdentity(ctx, root, user)
	if cleanupErr != nil {
		return s, cleanupErr
	}
	if !base.Succeeded() {
		if user.UID() == inherited.UID() && user.GID() == inherited.GID() {
			return s, fmt.Errorf("privilege: shared identity probe: %s", base.Error)
		}
		s.Reason = fmt.Sprintf("preferred Bed user %d:%d unavailable: %s; using daemon identity %d:%d", user.UID(), user.GID(), base.Error, facts.EUID, facts.EGID)
		user = inherited
		base, cleanupErr = probeIdentity(ctx, root, user)
		if cleanupErr != nil {
			return s, cleanupErr
		}
		if !base.Succeeded() {
			return s, fmt.Errorf("privilege: inherited identity probe: %s", base.Error)
		}
	}
	s.User, s.Policy = user, BedUserReport{Strategy: "fixed", UID: user.UID(), GID: user.GID(), SupplementaryGIDs: user.SupplementaryGIDs()}
	if s.Reason != "" {
		log.Printf("hostel privilege: %s", s.Reason)
	}
	s.Supported, s.Probe = []Level{Shared}, base
	if !canManageIdentity {
		if s.Reason == "" {
			s.Reason = "dedicated identity management unavailable"
		}
		return s, nil
	}
	dedicated, _ := NewBedUser(UIDMin, UIDMin)
	dedicated = dedicated.withGroups(cfg.SupplementaryGIDs)
	s.Probe, cleanupErr = probeIdentity(ctx, root, dedicated)
	if cleanupErr != nil {
		return s, cleanupErr
	}
	if !s.Probe.Succeeded() {
		if s.Reason == "" {
			s.Reason = s.Probe.Error
		}
		return s, nil
	}
	s.Supported = append(s.Supported, Dedicated)
	s.Effective = Dedicated
	s.Policy = BedUserReport{Strategy: "per_bed", UIDMin: UIDMin, UIDMax: UIDMax, SupplementaryGIDs: dedicated.SupplementaryGIDs()}
	return s, nil
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
