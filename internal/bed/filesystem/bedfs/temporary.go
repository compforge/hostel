package bedfs

import (
	"os"
	"path/filepath"
)

// PrepareTempdir owns only the Bed-local directory, never an external mapping.
// Reusing this backing directory lets commands, shells and Pillows exchange
// temporary files across Executor restarts without making them durable data.
// +spec=`Default temporary data belongs to the Bed, survives Executor replacement, and is reclaimed with BedHome; explicit mounts retain provider ownership.`
// +link=component://docs/volumes.md
func (o *FS) PrepareTempdir() error {
	if err := o.EnsureDir(filepath.Join(o.Rootfs(), "tmp")); err != nil {
		return err
	}
	return o.root.Chmod("tmp", os.ModeSticky|0777)
}
