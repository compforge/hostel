package manager

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/osx"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/pillow"
)

// A local identity owns the shared Bed even while it has no resident runtime. Renaming its data
// to .gc-ID does not end its Unix identity or permit reuse by another Bed.
// All fields below are guarded by Manager.mu; only the cleanup owner does I/O.
type localIdentity struct {
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
	local := &localIdentity{bed: model.New(id, "", model.Spec{Dir: filepath.Join(m.root, id)})}
	m.localIdentities[id] = local
	return local
}

// recoverLocalIdentities reserves recorded identities before allocating missing
// ones. Network has already reconciled its journal; admission is still closed.
// +spec=`Reuse persisted UID/GID; allocate missing identities only after reserving every recorded UID/GID, and persist before use.`
// +link:doc=docs/bed.md
func (m *Manager) recoverLocalIdentities() error {
	records, err := os.ReadDir(filepath.Join(m.root, ".identities"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	owners := make(map[model.ID]string)
	pending := make(map[string]localIdentityRecord)
	for _, entry := range records {
		name, ok := strings.CutSuffix(entry.Name(), ".local")
		if !ok {
			continue
		}
		if entry.IsDir() || validBedID(name) != nil {
			return fmt.Errorf("invalid local identity record %s", entry.Name())
		}
		data, err := os.ReadFile(m.identityPath(name))
		if err != nil {
			return fmt.Errorf("read bed %s identity: %w", name, err)
		}
		var record localIdentityRecord
		if err := json.Unmarshal(data, &record); err != nil {
			return fmt.Errorf("read bed %s identity: %w", name, err)
		}
		id := model.ID(record.ID)
		_, hexErr := hex.DecodeString(strings.TrimPrefix(record.ID, "bed-"))
		if !strings.HasPrefix(record.ID, "bed-") || len(record.ID) != 36 || hexErr != nil {
			return fmt.Errorf("invalid local identity for bed %s", name)
		}
		if owner, exists := owners[id]; exists {
			return fmt.Errorf("local identity %s is owned by both %s and %s", id, owner, name)
		}
		owners[id] = name
		if len(record.Pillows) > 0 {
			record.Pillows, err = pillow.Normalize(record.Pillows)
			if err != nil {
				return fmt.Errorf("recover bed %s pillows: %w", name, err)
			}
		}
		volumes, err := model.NormalizeVolumes(record.Volumes)
		if err != nil {
			return fmt.Errorf("recover bed %s volumes: %w", name, err)
		}
		local := &localIdentity{bed: model.New(name, id, model.Spec{Dir: filepath.Join(m.root, name), Pillows: record.Pillows, Volumes: volumes})}
		if err := m.privileges.Restore(local.bed, record.UID, record.GID); err != nil {
			return fmt.Errorf("recover bed %s privilege: %w", name, err)
		}
		m.localIdentities[name] = local
		pending[name] = record
	}
	entries, err := os.ReadDir(m.root)
	if err != nil {
		return err
	}
	present := make(map[string]bool)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := strings.TrimPrefix(entry.Name(), gcTmpPrefix)
		if validBedID(name) != nil {
			continue
		}
		local := m.localIdentityLocked(name)
		present[name] = true
		if strings.HasPrefix(entry.Name(), gcTmpPrefix) {
			local.cleanup = &localCleanup{}
		}
	}
	for name, local := range m.localIdentities {
		record := pending[name]
		if err := m.privileges.Reserve(local.bed); err != nil {
			return fmt.Errorf("allocate bed %s privilege: %w", name, err)
		}
		// Missing fields are filled once. Directory ownership never selects a UID;
		// Privilege.Prepare hands retained files to the authoritative identity.
		if record.UID == nil || record.GID == nil {
			if err := m.saveLocalIdentity(local); err != nil {
				return fmt.Errorf("save recovered bed %s identity: %w", name, err)
			}
		}
		identity, err := m.privileges.Identity(local.bed)
		if err != nil {
			return err
		}
		m.setLifecycle(local.bed, model.LifecycleStatus{Phase: PhaseDormant, Reason: "NotResident", UpdatedAt: time.Now()})
		log.Printf("hostel local identity recovered: bed=%s id=%s uid=%d gid=%d allocated_uid=%t allocated_gid=%t", name, local.bed.ID, identity.UID, identity.GID, record.UID == nil, record.GID == nil)
	}
	// Empty reservations can be forgotten after Network reconciliation. Their
	// record is removed before the in-memory lease becomes reusable.
	for name, local := range m.localIdentities {
		if !present[name] {
			if err := m.cleanLocalIdentity(context.Background(), local, false); err != nil {
				return err
			}
		}
	}
	return nil
}

// This daemon-owned record is outside the replaceable BedFS tree. Snapshot
// stage-in cannot overwrite it and portable snapshots never carry host IDs.
func (m *Manager) identityPath(name string) string {
	return filepath.Join(m.root, ".identities", name+".local")
}

func (m *Manager) saveLocalIdentity(local *localIdentity) error {
	path := m.identityPath(local.bed.Name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	identity, err := m.privileges.Identity(local.bed)
	if err != nil {
		return err
	}
	spec := local.bed.Spec()
	data, err := json.Marshal(localIdentityRecord{Volumes: spec.Volumes, ID: local.bed.ID.String(), UID: &identity.UID, GID: &identity.GID, Pillows: spec.Pillows})
	if err != nil {
		return err
	}
	return osx.WriteFileAtomic(path, data, 0o600)
}

type localIdentityRecord struct {
	UID     *int               `json:"uid"`
	GID     *int               `json:"gid"`
	ID      string             `json:"id"`
	Volumes []model.VolumeSpec `json:"volumes"`
	Pillows []model.PillowSpec `json:"pillows"`
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
	return m.beds[id] != nil || (initialization != nil && initialization.snapshot().Phase == PhaseInitializing) || m.retirements[id] != nil || m.purges[id] != nil
}

// cleanLocalIdentity is the only path to Forget. Foreground callers own the
// purge/retirement fence; GC installs a local fence under the same Manager lock.
// A concurrent caller joins the exact attempt rather than replaying cleanup by ID.
// +spec=`Forget requires all local trees to be gone; failed deletion keeps the local identity and UID reserved.`
func (m *Manager) cleanLocalIdentity(ctx context.Context, local *localIdentity, removeRoot bool) error {
	for {
		m.mu.Lock()
		if m.localIdentities[local.bed.Name] != local {
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
		if err == nil && m.localIdentities[local.bed.Name] == local {
			local.cleanup = nil
		}
		close(cleanup.done)
		m.mu.Unlock()
		if err != nil {
			log.Printf("hostel local cleanup failed: bed=%s err=%v", local.bed.Name, err)
		}
		return err
	}
}

func (m *Manager) removeIdentityTrees(ctx context.Context, local *localIdentity, removeRoot bool) error {
	dir := filepath.Join(m.root, local.bed.Name)
	garbage := filepath.Join(m.root, gcTmpPrefix+local.bed.Name)
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := m.removeLocalTree(garbage); err != nil {
		return fmt.Errorf("remove bed %s GC directory: %w", local.bed.Name, err)
	}
	if removeRoot {
		// Persist deletion intent through a rename before destructive removal.
		// An interrupted RemoveAll is therefore recovered as cleanup, never cold data.
		if err := os.Rename(dir, garbage); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("claim bed %s directory: %w", local.bed.Name, err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := m.removeLocalTree(garbage); err != nil {
			return fmt.Errorf("remove bed %s directory: %w", local.bed.Name, err)
		}
	}
	for _, path := range []string{dir, garbage} {
		if _, err := os.Lstat(path); err == nil {
			return nil
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect bed %s local identity: %w", local.bed.Name, err)
		}
	}
	// History is bound to this Bed lifetime, never to a subsequently reused name.
	m.executions.forgetBed(local.bed.Name)
	// The caller has stopped this allocation's runtime resources. Delete the
	// durable reservation before releasing its in-memory lease: failed removal
	// must keep the UID occupied. A crash between these steps is safe because
	// no processes, network allocations or local trees remain.
	if err := os.Remove(m.identityPath(local.bed.Name)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove bed %s identity: %w", local.bed.Name, err)
	}
	if err := m.privileges.Forget(ctx, local.bed); err != nil {
		return err
	}

	m.mu.Lock()
	if m.localIdentities[local.bed.Name] == local {
		delete(m.localIdentities, local.bed.Name)
		m.setLifecycle(local.bed, model.LifecycleStatus{Phase: PhaseDormant, Reason: "Forgotten", UpdatedAt: time.Now()})
	}
	m.mu.Unlock()
	log.Printf("hostel local identity forgotten: bed=%s", local.bed.Name)
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
