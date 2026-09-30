package bed_test

import (
	"reflect"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func TestPrivilegeSupportedLevelsDoNotExposeSelection(t *testing.T) {
	m, err := privilege.NewManager(privilege.BedUserReport{Strategy: "fixed"}, privilege.CurrentBedUser(), 0, bed.StatusWriter[bed.PrivilegeStatus]{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	supported := []privilege.Level{privilege.Shared, privilege.Dedicated}
	m.SetSelection(privilege.Selection{Supported: supported, Expected: privilege.Shared, Effective: privilege.Shared})
	before := m.Status().Selection.Supported
	m.SetSelection(privilege.Selection{Supported: supported, Expected: privilege.Dedicated, Effective: privilege.Dedicated})
	if after := m.Status().Selection.Supported; !reflect.DeepEqual(before, after) {
		t.Fatalf("selection changed capability facts: before=%+v after=%+v", before, after)
	}
	before[0] = privilege.Dedicated
	if m.Status().Selection.Supported[0] != privilege.Shared {
		t.Fatal("caller mutated component capability snapshot")
	}
}
