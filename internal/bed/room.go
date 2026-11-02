package bed

import (
	"fmt"

	"github.com/qiankunli/hostel/protocol"
)

// RoomType is a minimum isolation contract shared by filesystem and network.
type RoomType = protocol.RoomType

const (
	Dorm  = protocol.Dorm
	Room  = protocol.Room
	Suite = protocol.Suite
)

// The legacy auto spelling retains best-effort admission; selection always
// attempts the strongest allowed combination, independently of this floor.
func ParseRoomType(value string) (RoomType, error) {
	switch value {
	case "", "auto", "dorm":
		return Dorm, nil
	case "room":
		return Room, nil
	case "suite":
		return Suite, nil
	default:
		return "", fmt.Errorf("invalid Bed room type %q: expected dorm, room or suite", value)
	}
}

type RoomStatus = protocol.RoomStatus

// SummarizeRoom reports actual guarantees without capping them at the floor.
// +spec=`Bed isolation is the minimum of prepared filesystem and network rooms; the requested room is an admission floor, never a selection ceiling.`
// +link=component://docs/isolation.md
func SummarizeRoom(required, files, network RoomType) RoomStatus {
	if required == "" {
		required = Dorm
	}
	r := RoomStatus{Requested: required, Effective: files}
	if Rank(network) < Rank(files) {
		r.Effective = network
	}
	if Rank(files) < Rank(required) {
		r.Reasons = append(r.Reasons, "filesystem below required isolation")
	}
	if Rank(network) < Rank(required) {
		r.Reasons = append(r.Reasons, "network below required isolation")
	}
	r.Satisfied = Rank(r.Effective) >= Rank(required)
	return r
}

// Rank orders guarantees; an unprepared component has no attained room.
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
