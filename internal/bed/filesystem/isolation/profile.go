package isolation

import "github.com/qiankunli/hostel/internal/bed"

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
