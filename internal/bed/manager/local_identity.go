package manager

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	model "github.com/qiankunli/hostel/internal/bed"
)

// A local identity outlives its resident Bed. In particular, renaming its data
// to .gc-ID does not end its Unix identity or permit reuse by another Bed.
// All fields below are guarded by Manager.mu; only the cleanup owner does I/O.
type localIdentity struct {
	id      string
	bed     *model.Bed
	cleanup *localCleanup
}
type localCleanup struct {
	done       chan struct{}
	running    bool
	removeRoot bool
	err        error
}

func (m *Manager) localIdentityLocked(id string) *localIdentity {
	if local := m.localIdentities[id]; local != nil {
		return local
	}
	local := &localIdentity{id: id, bed: model.New(id, 0, model.Spec{Dir: filepath.Join(m.root, id), RecoveryDirs: []string{
		filepath.Join(m.root, id, "data"), filepath.Join(m.root, gcTmpPrefix+id, "data"),
	}})}
	m.localIdentities[id] = local
	return local
}

// recoverLocalIdentities runs before admission. Restoring a cleanup fence is
// mandatory even when disk-watermark GC is disabled.
func (m *Manager) recoverLocalIdentities() error {
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		id := strings.TrimPrefix(entry.Name(), gcTmpPrefix)
		if validBedID(id) != nil {
			continue
		}
		local := m.localIdentityLocked(id)
		if strings.HasPrefix(entry.Name(), gcTmpPrefix) {
			local.cleanup = &localCleanup{}
		}
	}
	for _, local := range m.localIdentities {
		if err := m.privileges.Recover(context.Background(), local.bed); err != nil {
			return fmt.Errorf("recover bed %s privilege: %w", local.id, err)
		}
	}
	return nil
}

// waitForLocalCleanup only joins an active attempt. An idle/failed cleanup is
// reported immediately; a create request must not depend on a future GC tick.
func (m *Manager) waitForLocalCleanup(ctx context.Context, id string) error {
	for {
		m.mu.Lock()
		local := m.localIdentities[id]
		if local == nil || local.cleanup == nil {
			m.mu.Unlock()
			return nil
		}
		cleanup := local.cleanup
		if !cleanup.running {
			err := fmt.Errorf("%w: local cleanup pending for %s", ErrBedUnavailable, id)
			if cleanup.err != nil {
				err = fmt.Errorf("%w: %v", err, cleanup.err)
			}
			m.mu.Unlock()
			return err
		}
		done := cleanup.done
		m.mu.Unlock()
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (m *Manager) bedIdentityInUseLocked(id string) bool {
	initialization := m.initializations[id]
	return m.beds[id] != nil || (initialization != nil && initialization.status.Phase == PhaseInitializing) || m.retirements[id] != nil || m.purges[id] != nil
}

// cleanLocalIdentity is the only path to Forget. Foreground callers own the
// purge/retirement fence; GC installs a local fence under the same Manager lock.
// A concurrent caller joins the exact attempt rather than replaying cleanup by ID.
// +spec=`Forget requires all local trees to be gone; failed deletion keeps the local identity and UID reserved.`
func (m *Manager) cleanLocalIdentity(ctx context.Context, local *localIdentity, removeRoot bool) error {
	for {
		m.mu.Lock()
		if m.localIdentities[local.id] != local {
			m.mu.Unlock()
			return nil
		}
		cleanup := local.cleanup
		if cleanup != nil && cleanup.running {
			done := cleanup.done
			m.mu.Unlock()
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			m.mu.Lock()
			err := cleanup.err
			m.mu.Unlock()
			if err != nil {
				return err
			}
			continue
		}
		if err := ctx.Err(); err != nil {
			m.mu.Unlock()
			return err
		}
		// Each attempt keeps an immutable completion result for its waiters.
		removeRoot = removeRoot || (cleanup != nil && cleanup.removeRoot)
		cleanup = &localCleanup{removeRoot: removeRoot, running: true, done: make(chan struct{})}
		local.cleanup = cleanup
		m.mu.Unlock()

		err := m.removeIdentityTrees(ctx, local, cleanup.removeRoot)
		m.mu.Lock()
		cleanup.err = err
		cleanup.running = false
		if err == nil && m.localIdentities[local.id] == local {
			local.cleanup = nil
		}
		close(cleanup.done)
		m.mu.Unlock()
		if err != nil {
			log.Printf("hostel local cleanup failed: bed=%s err=%v", local.id, err)
		}
		return err
	}
}

func (m *Manager) removeIdentityTrees(ctx context.Context, local *localIdentity, removeRoot bool) error {
	dir := filepath.Join(m.root, local.id)
	garbage := filepath.Join(m.root, gcTmpPrefix+local.id)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.removeLocalTree(garbage); err != nil {
		return fmt.Errorf("remove bed %s GC directory: %w", local.id, err)
	}
	if removeRoot {
		// Persist deletion intent through a rename before destructive removal.
		// An interrupted RemoveAll is therefore recovered as cleanup, never cold data.
		if err := os.Rename(dir, garbage); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("claim bed %s directory: %w", local.id, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.removeLocalTree(garbage); err != nil {
			return fmt.Errorf("remove bed %s directory: %w", local.id, err)
		}
	}
	for _, path := range []string{dir, garbage} {
		if _, err := os.Lstat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect bed %s local identity: %w", local.id, err)
		}
	}
	// The caller has stopped this allocation's runtime resources. The cleanup
	// fence remains visible until Forget completes, even if the hook fails.
	forget := model.NewSequence(model.Forget, model.Participant{Name: "privilege", Lifecycle: m.privileges})
	if err := forget.Run(ctx, local.bed); err != nil {
		return err
	}
	m.mu.Lock()
	if m.localIdentities[local.id] == local {
		delete(m.localIdentities, local.id)
	}
	m.mu.Unlock()
	log.Printf("hostel local identity forgotten: bed=%s", local.id)
	return nil
}

// RetryLocalCleanups completes already-claimed cold directories independently
// of watermark GC. Resident/retiring Beds retain their own runtime cleanup owner.
func (m *Manager) RetryLocalCleanups(ctx context.Context) error {
	m.mu.Lock()
	pending := make([]*localIdentity, 0)
	for id, local := range m.localIdentities {
		if local.cleanup != nil && !m.bedIdentityInUseLocked(id) {
			pending = append(pending, local)
		}
	}
	m.mu.Unlock()
	var result error
	for _, local := range pending {
		if err := ctx.Err(); err != nil {
			return errors.Join(result, err)
		}
		result = errors.Join(result, m.cleanLocalIdentity(ctx, local, false))
	}
	return result
}

type LocalCleanupReport struct {
	BedID   string `json:"bed_id"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

func (m *Manager) localCleanupReports() []LocalCleanupReport {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]LocalCleanupReport, 0)
	for id, local := range m.localIdentities {
		if local.cleanup == nil {
			continue
		}
		report := LocalCleanupReport{BedID: id, Running: local.cleanup.running}
		if local.cleanup.err != nil {
			report.Error = local.cleanup.err.Error()
		}
		result = append(result, report)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].BedID < result[j].BedID })
	return result
}
