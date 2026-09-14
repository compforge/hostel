package bed_test

import (
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/store"
	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestDomainLevelRoomMapping(t *testing.T) {
	for _, tc := range []struct {
		name  string
		level bed.Level
		room  bed.RoomType
	}{
		{"filesystem/shared", isolation.Shared, bed.Dorm},
		{"filesystem/confined", isolation.Confined, bed.Room},
		{"filesystem/private", isolation.Private, bed.Suite},
		{"privilege/shared", privilege.Shared, bed.Dorm},
		{"privilege/dedicated", privilege.Dedicated, bed.Suite},
		{"network/shared", network.Shared, bed.Room},
		{"network/private", network.Private, bed.Suite},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.level.Room(); got != tc.room {
				t.Fatalf("Room() = %s, want %s", got, tc.room)
			}
		})
	}
}

func TestLevelStatusDoesNotExposeSelection(t *testing.T) {
	m := new(privilege.Manager)
	supported := []privilege.Level{privilege.Shared, privilege.Dedicated}
	m.SetSelection(privilege.Selection{Supported: supported, Expected: privilege.Shared, Effective: privilege.Shared})
	before := m.LevelStatus()
	m.SetSelection(privilege.Selection{Supported: supported, Expected: privilege.Dedicated, Effective: privilege.Dedicated})
	if after := m.LevelStatus(); !reflect.DeepEqual(before, after) {
		t.Fatalf("selection changed capability facts: before=%+v after=%+v", before, after)
	}
	before.Supported[0] = privilege.Dedicated
	if m.LevelStatus().Supported[0] != privilege.Shared {
		t.Fatal("caller mutated component capability snapshot")
	}
}

func TestBaselineIsNotAnUngradedComponent(t *testing.T) {
	iso, err := isolation.Resolve(hostfacts.Snapshot{}, isolation.Config{
		Level: "shared", Bwrap: feature.Off, Landlock: feature.Off,
		UID: feature.Off, PRoot: feature.Off, Pathshim: feature.Off,
	}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files := filesystem.NewManager(iso, bed.StatusWriter[bed.FilesystemStatus]{})
	net := network.NewConfigured(t.Context(), network.Config{NetNS: feature.Off})
	for name, status := range map[string]bed.LevelStatus{
		"filesystem": files.LevelStatus(),
		"privilege":  new(privilege.Manager).LevelStatus(),
		"network":    net.LevelStatus(),
	} {
		if len(status.Supported) != 1 {
			t.Errorf("%s must report a baseline: %+v", name, status)
		}
	}
	for name, status := range map[string]bed.LevelStatus{
		"executor": new(executor.Manager).LevelStatus(),
		"resource": new(resource.Manager).LevelStatus(),
		"store":    new(store.Manager).LevelStatus(),
	} {
		if len(status.Supported) != 0 {
			t.Errorf("%s invented an isolation guarantee: %+v", name, status)
		}
	}
}
