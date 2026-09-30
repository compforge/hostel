package bed

import (
	"fmt"
)

// RoomType is the user-facing file isolation guarantee. Profiles may prefer
// additional capabilities, whose availability does not change this guarantee.
type RoomType string

const (
	Dorm  RoomType = "dorm"
	Room  RoomType = "room"
	Suite RoomType = "suite"
)

func ParseRoomType(value string) (RoomType, error) {
	switch value {
	case "", "auto", "suite":
		return Suite, nil
	case "dorm":
		return Dorm, nil
	case "room":
		return Room, nil
	default:
		return "", fmt.Errorf("invalid Bed room type %q: expected dorm, room, suite or auto", value)
	}
}

// RoomStatus summarizes guarantees without removing stronger domain features.
type RoomStatus struct {
	Requested RoomType `json:"requested"`
	Effective RoomType `json:"effective"`
	Reasons   []string `json:"reasons,omitempty"`
}

// SummarizeRoom reports the attained file guarantee within the requested profile.
// +spec=`Room requires confined files; suite requires a private file view. Additional network, identity and process capabilities are reported by their owners.`
func SummarizeRoom(requested, files RoomType) RoomStatus {
	r := RoomStatus{Requested: requested, Effective: requested}
	if files.rank() < requested.rank() {
		r.Effective = files
		r.Reasons = []string{"filesystem below requested profile"}
	}
	return r
}

func (r RoomType) rank() int {
	switch r {
	case Suite:
		return 2
	case Room:
		return 1
	default:
		return 0
	}
}
