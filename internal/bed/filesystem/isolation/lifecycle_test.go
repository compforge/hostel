package isolation

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

func (fakeMech) Prepare(context.Context, *bedfs.FS) error { return nil }
func (fakeMech) Release(context.Context, *bedfs.FS) error { return nil }
func (fakeMech) PrivilegedEntry() bool                    { return false }
