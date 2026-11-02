package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
)

// Simulate reported guarantees only; real kernel boundaries belong to Linux E2E.
type ratedFiles struct {
	isolation.Isolator
	level          isolation.Level
	lowerOnPrepare bool
}

func (f *ratedFiles) Level() isolation.Level { return f.level }
func (f *ratedFiles) Prepare(ctx context.Context, fs *bedfs.FS) error {
	if err := f.Isolator.Prepare(ctx, fs); err != nil {
		return err
	}
	if f.lowerOnPrepare {
		f.level = isolation.Shared
	}
	return nil
}

func floorManager(t *testing.T, floor bed.RoomType, files isolation.Level, private bool) (*Manager, *ratedFiles) {
	t.Helper()
	m := newTestManager(t)
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	m.roomType = floor
	iso := &ratedFiles{Isolator: m.iso, level: files}
	m.iso = iso
	m.files = filesystem.NewManager(iso, m.owners.Filesystem)
	if private {
		m.SetNetworkManager(network.WithProvider(&policyInitializationNetwork{}, m.owners.Network))
	}
	return m, iso
}

// +case:id=isolation_floor_admission,desc=`All fs/net combinations under dorm/room/suite floors`,expect=`Only combinations meeting the floor become Ready; stronger domain facts remain visible`
func TestIsolationFloorAdmission(t *testing.T) {
	for _, floor := range []bed.RoomType{bed.Dorm, bed.Room, bed.Suite} {
		for _, files := range []isolation.Level{isolation.Shared, isolation.Confined, isolation.Private} {
			for _, private := range []bool{false, true} {
				t.Run(string(floor)+"/"+files.String()+"/"+map[bool]string{false: "shared", true: "private"}[private], func(t *testing.T) {
					m, _ := floorManager(t, floor, files, private)
					want := bed.Dorm
					if private {
						want = files.Room()
					}
					b, err := declareTestBed(m, t.Context(), "tenant")
					if bed.Rank(want) < bed.Rank(floor) {
						if !errors.Is(err, hostel.ErrUnavailable) {
							t.Fatalf("expected admission failure: %v", err)
						}
						if m.BedModel("tenant") != nil {
							t.Fatal("rejection allocated a Bed")
						}
						if _, err := os.Stat(filepath.Join(m.root, "tenant")); !errors.Is(err, os.ErrNotExist) {
							t.Fatalf("rejection prepared files: %v", err)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					report := m.BedRoomStatus(b.Bed)
					if !report.Satisfied || report.Effective != want || report.Requested != floor || b.Bed.Status().Filesystem.Room != files.Room() {
						t.Fatalf("room=%+v components=%+v", report, b.Status())
					}
				})
			}
		}
	}
}

func TestIsolationRecheckedAfterBedPreparation(t *testing.T) {
	m, files := floorManager(t, bed.Suite, isolation.Private, true)
	files.lowerOnPrepare = true
	if !m.RoomStatus().Satisfied {
		t.Fatal("test requires admissible startup facts")
	}
	if _, err := declareTestBed(m, t.Context(), "tenant"); !errors.Is(err, hostel.ErrUnavailable) {
		t.Fatalf("preparation did not recheck actual room: %v", err)
	}
	if _, ok := m.Get("tenant"); ok {
		t.Fatal("weaker Bed published Ready")
	}
}

func TestProbeDoesNotRequireTenantIsolationFloor(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	m.roomType = bed.Suite
	if err := m.ProbeEnvironment(t.Context()); err != nil {
		t.Fatalf("capability probe must remain usable below tenant floor: %v", err)
	}
	if _, err := declareTestBed(m, t.Context(), "tenant"); !errors.Is(err, hostel.ErrUnavailable) {
		t.Fatalf("probe bypass escaped into tenant admission: %v", err)
	}
}

type unavailableFloorNetwork struct{ *network.Manager }

func (unavailableFloorNetwork) Status() network.Status {
	return network.Status{Effective: network.Shared, Cause: hostel.CauseCarrier, Reason: "IPv4 forwarding unavailable"}
}
func (unavailableFloorNetwork) Acquire(context.Context, string) (network.Attachment, error) {
	panic("failed floor must not allocate a network")
}
func TestIsolationAdmissionPreservesNetworkCause(t *testing.T) {
	m, _ := floorManager(t, bed.Room, isolation.Private, false)
	m.SetNetworkManager(network.WithProvider(unavailableFloorNetwork{}, m.owners.Network))
	_, err := declareTestBed(m, t.Context(), "tenant")
	if !errors.Is(err, hostel.ErrUnavailable) || hostel.CauseOf(err) != hostel.CauseCarrier {
		t.Fatalf("lost carrier prerequisite cause: %v", err)
	}
}
