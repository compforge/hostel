//go:build !linux && !darwin

package bedfs

import (
	"os"
	"time"
)

func creationTime(info os.FileInfo) time.Time { return info.ModTime() }
