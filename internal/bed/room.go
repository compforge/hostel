package bed

import (
	"fmt"
	"maps"
	"slices"
)

// RoomType is the user-facing profile. Domains own its translation to their
// levels; it is never a filesystem backend name.
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

// SummarizeRoom compares profile satisfaction, not incomparable domain levels.
// +spec=`A room requires confined files and dedicated identity; a suite additionally requires private files and network. Downgrading the summary never downgrades allocations.`
func SummarizeRoom(requested RoomType, selected map[string]Level) RoomStatus {
	r := RoomStatus{Requested: requested, Effective: requested}
	if len(selected) == 0 {
		r.Effective = Dorm
	}
	for _, domain := range slices.Sorted(maps.Keys(selected)) {
		room := selected[domain].Room()
		if room.rank() < r.Effective.rank() {
			r.Effective = room
		}
		if room.rank() < requested.rank() {
			r.Reasons = append(r.Reasons, domain+" below requested profile")
		}
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
