package bed_test

import (
	"github.com/qiankunli/hostel/internal/bed"
	"testing"
)

func TestRoomMinimumAcrossDomains(t *testing.T) {
	levels := []bed.RoomType{bed.Dorm, bed.Room, bed.Suite}
	for floorIndex, floor := range levels {
		for fsIndex, files := range levels {
			for netIndex, network := range levels {
				status := bed.SummarizeRoom(floor, files, network)
				expected := levels[min(fsIndex, netIndex)]
				if status.Requested != floor || status.Effective != expected || status.Satisfied != (min(fsIndex, netIndex) >= floorIndex) {
					t.Fatalf("floor=%s files=%s network=%s status=%+v", floor, files, network, status)
				}
			}
		}
	}
	if s := bed.SummarizeRoom(bed.Dorm, "", bed.Suite); s.Satisfied || s.Effective != "" {
		t.Fatalf("unprepared files reported ready: %+v", s)
	}
}

func TestParseRoomType(t *testing.T) {
	for value, want := range map[string]bed.RoomType{"": bed.Dorm, "auto": bed.Dorm, "suite": bed.Suite, "room": bed.Room, "dorm": bed.Dorm} {
		if got, err := bed.ParseRoomType(value); err != nil || got != want {
			t.Fatalf("%q: %s %v", value, got, err)
		}
	}
	if _, err := bed.ParseRoomType("private"); err == nil {
		t.Fatal("accepted mechanism level as public floor")
	}
}
