package manager

import (
	"context"
	"errors"
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

func reportingManager(t *testing.T, files isolation.Level, private bool) (*Manager, *ratedFiles) {
	t.Helper()
	m := newTestManager(t)
	t.Cleanup(func() {
		if err := m.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	iso := &ratedFiles{Isolator: m.iso, level: files}
	m.iso = iso
	m.files = filesystem.NewManager(iso, m.owners.Filesystem)
	if private {
		m.SetNetworkManager(network.WithProvider(&policyInitializationNetwork{}, m.owners.Network))
	}
	return m, iso
}

// +case:id=subsystem_realization_report,desc=`Prepare every fs/net capability combination`,expect=`Beds become Ready and preserve each subsystem's actual guarantees without a room admission gate`
func TestSubsystemRealizationDoesNotGateAdmission(t *testing.T) {
	for _, files := range []isolation.Level{isolation.Shared, isolation.Confined, isolation.Private} {
		for _, private := range []bool{false, true} {
			t.Run(files.String()+"/"+map[bool]string{false: "shared", true: "private"}[private], func(t *testing.T) {
				m, _ := reportingManager(t, files, private)
				want := bed.Dorm
				if private {
					want = files.Room()
				}
				b, err := declareTestBed(m, t.Context(), "tenant")
				if err != nil {
					t.Fatal(err)
				}
				if _, ok := m.Get("tenant"); !ok {
					t.Fatal("prepared Bed not published")
				}
				state := b.Bed.Status()
				if got := m.BedRoomStatus(b.Bed); got.Effective != want || state.Filesystem.Room != files.Room() {
					t.Fatalf("room=%+v subsystems=%+v", got, state)
				}
			})
		}
	}
}

func TestBedReportsPreparedGuaranteesInsteadOfInstanceCeiling(t *testing.T) {
	m, files := reportingManager(t, isolation.Private, true)
	files.lowerOnPrepare = true
	if m.RoomStatus().Effective != bed.Suite {
		t.Fatal("fixture must start with private capabilities")
	}
	b, err := declareTestBed(m, t.Context(), "tenant")
	if err != nil {
		t.Fatalf("weaker prepared guarantees must remain observable: %v", err)
	}
	if got := m.BedRoomStatus(b.Bed); got.Effective != bed.Dorm {
		t.Fatalf("room=%+v", got)
	}
}

type unavailableReportNetwork struct {
	*network.Manager
	err error
}

func (p unavailableReportNetwork) Status() network.Status {
	return network.Status{Err: p.err, Effective: network.Shared, Cause: hostel.CauseCarrier, Reason: "IPv4 forwarding unavailable"}
}
func (unavailableReportNetwork) Acquire(context.Context, string) (network.Attachment, error) {
	panic("shared network must not allocate a namespace")
}
func TestBedReportsNetworkFailureWithoutRejectingSharedExecution(t *testing.T) {
	m, _ := reportingManager(t, isolation.Private, false)
	original := hostel.WrapError(hostel.ErrUnavailable, "IPv4 forwarding", hostel.CauseCarrier, errors.New("forwarding disabled"))
	m.SetNetworkManager(network.WithProvider(unavailableReportNetwork{err: original}, m.owners.Network))
	b, err := declareTestBed(m, t.Context(), "tenant")
	if err != nil {
		t.Fatal(err)
	}
	got := b.Bed.Status().Network
	if got.Room != bed.Dorm || got.Cause != string(hostel.CauseCarrier) || got.Reason == "" {
		t.Fatalf("missing network realization evidence: %+v", got)
	}
}
