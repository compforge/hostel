package bed_test

import (
	"github.com/qiankunli/hostel/internal/bed"
	"testing"
)

func TestRoomMinimumAcrossSubsystems(t *testing.T) {
	levels := []bed.RoomType{"", bed.Dorm, bed.Room, bed.Suite}
	for fi, files := range levels {
		for ni, network := range levels {
			got := bed.SummarizeRoom(files, network)
			if got.Effective != levels[min(fi, ni)] {
				t.Fatalf("files=%q network=%q room=%+v", files, network, got)
			}
		}
	}
}
