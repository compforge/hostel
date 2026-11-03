package bed

import (
	"github.com/qiankunli/hostel/protocol"
)

// RoomType is an observed isolation grade shared by filesystem and network.
type RoomType = protocol.RoomType

const (
	Dorm  = protocol.Dorm
	Room  = protocol.Room
	Suite = protocol.Suite
)

type RoomStatus = protocol.RoomStatus

// SummarizeRoom reports the weakest prepared subsystem guarantee.
// +spec=`Room is an observation, never an admission or mechanism-selection policy. An unprepared subsystem has no attained room.`
// +link=component://docs/isolation.md
func SummarizeRoom(files, network RoomType) RoomStatus {
	effective := files
	if Rank(network) < Rank(files) {
		effective = network
	}
	return RoomStatus{Effective: effective}
}

// Rank orders guarantees; an unprepared subsystem has no attained room.
func Rank(r RoomType) int {
	switch r {
	case Suite:
		return 3
	case Room:
		return 2
	case Dorm:
		return 1
	default:
		return 0
	}
}
