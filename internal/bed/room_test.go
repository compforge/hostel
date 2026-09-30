package bed_test

import (
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
)

func TestRoomSummarizesFileGuarantee(t *testing.T) {
	for _, tc := range []struct {
		name      string
		requested bed.RoomType
		files     isolation.Level
		want      bed.RoomType
	}{
		{"suite", bed.Suite, isolation.Private, bed.Suite},
		{"confined files", bed.Suite, isolation.Confined, bed.Room},
		{"shared files", bed.Suite, isolation.Shared, bed.Dorm},
		{"request caps summary", bed.Room, isolation.Private, bed.Room},
		{"dorm", bed.Dorm, isolation.Private, bed.Dorm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status := bed.SummarizeRoom(tc.requested, tc.files.Room())
			if status.Effective != tc.want || status.Requested != tc.requested {
				t.Fatalf("%+v, want %s", status, tc.want)
			}
			if (tc.want != tc.requested) != (len(status.Reasons) != 0) {
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
