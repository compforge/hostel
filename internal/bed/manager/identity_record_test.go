package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func readTestIdentity(t *testing.T, m *Manager, name string) localIdentityRecord {
	t.Helper()
	data, err := os.ReadFile(m.identityPath(name))
	if err != nil {
		t.Fatal(err)
	}
	var record localIdentityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	return record
}

// +case:id=identity_recovery_optional_uid,desc=`Recover missing UID before a record that already occupies the first slot`,expect=`Recorded UID is reserved first; missing identity is allocated and persisted once`
// +link:doc=docs/bed.md
func TestIdentityRecoveryReservesBeforeAllocatingMissing(t *testing.T) {
	m := newTestManager(t)
	p, err := privilege.NewManager(privilege.BedUserReport{Strategy: "per_bed", UIDMin: 200000, UIDMax: 200001}, privilege.CurrentBedUser(), 0, m.owners.Privilege, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.privileges = p
	for _, name := range []string{"a-missing", "z-recorded"} {
		writeTestIdentity(t, m.root, name, 200000, 200000)
		if err := os.MkdirAll(filepath.Join(m.root, name, "rootfs"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	path := m.identityPath("a-missing")
	missing := readTestIdentity(t, m, "a-missing")
	missing.UID, missing.GID = nil, nil
	raw, _ := json.Marshal(missing)
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	for name, uid := range map[string]int{"a-missing": 200001, "z-recorded": 200000} {
		record := readTestIdentity(t, m, name)
		if record.UID == nil || record.GID == nil || *record.UID != uid || *record.GID != uid {
			t.Fatalf("%s identity=%+v", name, record)
		}
		if name == "a-missing" && record.ID != missing.ID {
			t.Fatal("missing UID replaced Bed ID")
		}
	}
	if m.privileges.Status().ReservedUsers != 2 {
		t.Fatal("retained identities not reserved")
	}
	// Directory owner is deliberately the test process, not either reserved UID.
	// A second recovery must keep the records rather than derive owners from disk.
	if err := m.recoverLocalIdentities(); err != nil {
		t.Fatal(err)
	}
	if got := readTestIdentity(t, m, "a-missing"); *got.UID != 200001 {
		t.Fatal("recovery reassigned UID")
	}
}

func TestIdentityRecoveryFillsMissingRecordAndGID(t *testing.T) {
	for _, missingRecord := range []bool{true, false} {
		t.Run(map[bool]string{true: "record", false: "gid"}[missingRecord], func(t *testing.T) {
			m := newTestManager(t)
			if err := os.MkdirAll(filepath.Join(m.root, "retained", "rootfs"), 0700); err != nil {
				t.Fatal(err)
			}
			var originalID string
			if !missingRecord {
				writeTestIdentity(t, m.root, "retained", os.Geteuid(), os.Getegid())
				record := readTestIdentity(t, m, "retained")
				originalID = record.ID
				record.GID = nil
				raw, _ := json.Marshal(record)
				if err := os.WriteFile(m.identityPath("retained"), raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			if err := m.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer m.Close(context.Background())
			record := readTestIdentity(t, m, "retained")
			if record.UID == nil || record.GID == nil || *record.UID != os.Geteuid() || *record.GID != os.Getegid() {
				t.Fatalf("identity=%+v", record)
			}
			if originalID != "" && originalID != record.ID {
				t.Fatal("GID completion replaced identity")
			}
		})
	}
}

func TestIdentityRecoveryRejectsConflictsAndCorruption(t *testing.T) {
	for _, kind := range []string{"corrupt", "duplicate-id", "duplicate-uid", "invalid-uid", "invalid-gid"} {
		t.Run(kind, func(t *testing.T) {
			m := newTestManager(t)
			p, err := privilege.NewManager(privilege.BedUserReport{Strategy: "per_bed", UIDMin: 200000, UIDMax: 200001}, privilege.CurrentBedUser(), 0, m.owners.Privilege, nil)
			if err != nil {
				t.Fatal(err)
			}
			m.privileges = p
			writeTestIdentity(t, m.root, "a", 200000, 200000)
			writeTestIdentity(t, m.root, "b", 200001, 200001)
			a, b := readTestIdentity(t, m, "a"), readTestIdentity(t, m, "b")
			switch kind {
			case "duplicate-id":
				b.ID = a.ID
			case "duplicate-uid":
				b.UID, b.GID = a.UID, a.GID
			case "invalid-uid":
				n := -1
				b.UID = &n
			case "invalid-gid":
				n := -1
				b.GID = &n
			}
			raw, _ := json.Marshal(b)
			if kind == "corrupt" {
				raw = []byte("{")
			}
			if err := os.WriteFile(m.identityPath("b"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			if err := m.Start(t.Context()); err == nil {
				t.Fatal("invalid record accepted")
			}
			if after, err := os.ReadFile(m.identityPath("b")); err != nil || string(after) != string(raw) {
				t.Fatal("failed recovery overwrote evidence")
			}
		})
	}
}

func TestIdentityPersistenceFailurePrecedesBedPreparation(t *testing.T) {
	m := newTestManager(t)
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// A regular file prevents atomic record creation after startup succeeds.
	if err := os.WriteFile(filepath.Join(m.root, ".identities"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := declareTestBed(m, t.Context(), "unprepared"); err == nil || !strings.Contains(err.Error(), "save local bed identity") {
		t.Fatalf("initialize=%v", err)
	}
	if _, err := os.Stat(filepath.Join(m.root, "unprepared")); !os.IsNotExist(err) {
		t.Fatalf("BedFS prepared before identity: %v", err)
	}
	if err := os.Remove(filepath.Join(m.root, ".identities")); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryLocalCleanups(t.Context()); err != nil {
		t.Fatal(err)
	}
	if m.privileges.Status().ReservedUsers != 0 {
		t.Fatal("empty failed initialization leaked lease")
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// +case:id=identity_remove_failure_retains_lease,desc=`Fail record removal after deleting Bed data`,expect=`UID remains reserved until record removal succeeds`
func TestIdentityRecordRemovalFailureRetainsLease(t *testing.T) {
	m := newIdentityTestManager(t, t.TempDir())
	local := claimTestUser(t, m, "same")
	path := m.identityPath("same")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	blocker := filepath.Join(path, "busy")
	if err := os.WriteFile(blocker, []byte("busy"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := m.cleanLocalIdentity(t.Context(), local, true); err == nil {
		t.Fatal("removal failure swallowed")
	}
	assertUserReserved(t, m)
	if err := os.Remove(blocker); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryLocalCleanups(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := m.privileges.Reserve(model.New("other", "", model.Spec{})); err != nil {
		t.Fatal(err)
	}
}

// +case:id=identity_recovery_independent_fields,desc=`Recover UID-only, GID-only and complete records together`,expect=`Each existing field survives and only missing fields are allocated without collisions`
// +link:doc=docs/bed.md
func TestIdentityRecoveryPreservesEachPresentField(t *testing.T) {
	m := newTestManager(t)
	p, err := privilege.NewManager(privilege.BedUserReport{Strategy: "per_bed", UIDMin: 200000, UIDMax: 200002}, privilege.CurrentBedUser(), 0, m.owners.Privilege, nil)
	if err != nil {
		t.Fatal(err)
	}
	m.privileges = p
	for name, pair := range map[string][2]int{"uid-only": {200000, 200000}, "gid-only": {200002, 200002}, "complete": {200002, 200000}} {
		writeTestIdentity(t, m.root, name, pair[0], pair[1])
		record := readTestIdentity(t, m, name)
		if name == "uid-only" {
			record.GID = nil
		}
		if name == "gid-only" {
			record.UID = nil
		}
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(m.identityPath(name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(m.root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer m.Close(context.Background())
	for name, want := range map[string][2]int{"uid-only": {200000, 200001}, "gid-only": {200001, 200002}, "complete": {200002, 200000}} {
		record := readTestIdentity(t, m, name)
		if record.UID == nil || record.GID == nil || *record.UID != want[0] || *record.GID != want[1] {
			t.Fatalf("%s record=%+v want=%v", name, record, want)
		}
	}
}
