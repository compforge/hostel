package bed_test

import (
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func TestRoomSummarizesGuaranteesWithoutChangingSelections(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bed.RoomType
		files     isolation.Level
		user      privilege.Level
		net       network.Level
		want      bed.RoomType
	}{
		{"suite", bed.Suite, isolation.Private, privilege.Dedicated, network.Private, bed.Suite},
		{"shared network", bed.Suite, isolation.Private, privilege.Dedicated, network.Shared, bed.Room},
		{"confined files", bed.Suite, isolation.Confined, privilege.Dedicated, network.Private, bed.Room},
		{"shared identity", bed.Suite, isolation.Private, privilege.Shared, network.Private, bed.Dorm},
		{"shared files", bed.Suite, isolation.Shared, privilege.Dedicated, network.Private, bed.Dorm},
		{"request caps summary", bed.Room, isolation.Private, privilege.Dedicated, network.Private, bed.Room},
		{"dorm", bed.Dorm, isolation.Private, privilege.Dedicated, network.Private, bed.Dorm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			levels := map[string]bed.Level{"filesystem": tc.files, "privilege": tc.user, "network": tc.net}
			status := bed.SummarizeRoom(tc.requested, levels)
			if status.Effective != tc.want || status.Requested != tc.requested {
				t.Fatalf("%+v, want %s", status, tc.want)
			}
			if levels["filesystem"] != tc.files || levels["privilege"] != tc.user || levels["network"] != tc.net {
				t.Fatal("summary rewrote a component selection")
			}
			if tc.want != tc.requested && len(status.Reasons) == 0 {
				t.Fatal("missing downgrade reason")
			}
		})
	}
}

func TestParseRoomType(t *testing.T) {
	for value, want := range map[string]bed.RoomType{"": bed.Suite, "auto": bed.Suite, "suite": bed.Suite, "room": bed.Room, "dorm": bed.Dorm} {
		if got, err := bed.ParseRoomType(value); err != nil || got != want {
			t.Fatalf("%q: %s %v", value, got, err)
		}
	}
	if _, err := bed.ParseRoomType("private"); err == nil {
		t.Fatal("accepted domain level as public profile")
	}
}
