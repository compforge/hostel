package filesystem

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

type Config struct{ isolation.Config }
type Options struct {
	DormReadFallbackRoot *string
	Bwrap                *tool.Policy
	Landlock             *tool.Policy
	UID                  *tool.Policy
	PRoot                *tool.Policy
	Pathshim             *tool.Policy
}
