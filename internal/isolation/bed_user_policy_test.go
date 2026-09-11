package isolation

import (
	"testing"

	"github.com/qiankunli/hostel/internal/privilege"
)

func TestFixedBedUserReport(t *testing.T) {
	user, err := privilege.NewBedUser(1200, 1300)
	if err != nil {
		t.Fatal(err)
	}
	report := DescribeBedUser(direct{}, user)
	if report.Strategy != "fixed" || report.UID != 1200 || report.GID != 1300 {
		t.Fatalf("report = %+v", report)
	}
}
