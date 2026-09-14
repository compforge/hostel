package network

import "github.com/qiankunli/hostel/internal/bed"

type Level string

const (
	Shared  Level = "shared"
	Private Level = "private"
)

func ExpectedLevel(room bed.RoomType) Level {
	if room == bed.Suite {
		return Private
	}
	return Shared
}
func (c Config) ForRoom(room bed.RoomType) Config { c.Level = ExpectedLevel(room); return c }
func (l Level) Room() bed.RoomType {
	if l == Private {
		return bed.Suite
	}
	return bed.Room
}
