package bedfs

import (
	"os"
	"path/filepath"
)

// PrepareTempdir owns only the Bed-local directory, never an external mapping.
// Reusing this backing directory lets commands, shells and Services exchange
// temporary files across Executor restarts without making them durable data.
func (o *FS) PrepareTempdir() error {
	if err := o.EnsureDir(filepath.Join(o.Rootfs(), "tmp")); err != nil {
		return err
	}
	return o.root.Chmod("tmp", os.ModeSticky|0777)
}
