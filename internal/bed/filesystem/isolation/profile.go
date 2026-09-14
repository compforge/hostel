package isolation

import "github.com/qiankunli/hostel/internal/bed"

func ExpectedLevel(room bed.RoomType) Level {
	switch room {
	case bed.Dorm:
		return Shared
	case bed.Room:
		return Confined
	default:
		return Private
	}
}
func (l Level) Room() bed.RoomType {
	switch l {
	case Private:
		return bed.Suite
	case Confined:
		return bed.Room
	default:
		return bed.Dorm
	}
}

// ForRoom sets the domain expectation. Mechanism policies still constrain which
// implementations may provide it, including required workspace helpers.
func (c Config) ForRoom(room bed.RoomType) Config {
	c.Level = ExpectedLevel(room).String()
	return c
}
