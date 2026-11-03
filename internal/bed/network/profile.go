package network

import "github.com/qiankunli/hostel/internal/bed"

type Level string

const (
	Shared  Level = "shared"
	Private Level = "private"
)

// Room maps realized network guarantees, not tool availability. The current
// implementation has shared and private paths; private also satisfies room.
func (l Level) Room() bed.RoomType {
	if l == Private {
		return bed.Suite
	}
	return bed.Dorm
}
