package view

import (
	"fmt"
	"strconv"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

type FileInfo struct {
	Path       string    `json:"path"`
	Type       string    `json:"type"`
	Size       int64     `json:"size"`
	ModifiedAt time.Time `json:"modified_at"`
	CreatedAt  time.Time `json:"created_at"`
	Owner      string    `json:"owner"`
	Group      string    `json:"group"`
	Mode       int       `json:"mode"`
}

func File(f bedfs.FileInfo) FileInfo {
	mode, _ := strconv.Atoi(strconv.FormatInt(int64(f.Mode), 8))
	return FileInfo{Path: f.Path, Type: f.Type, Size: f.Size, ModifiedAt: f.ModifiedAt, CreatedAt: f.CreatedAt, Owner: f.Owner, Group: f.Group, Mode: mode}
}

type Permission struct {
	Owner string `json:"owner"`
	Group string `json:"group"`
	Mode  int    `json:"mode"`
}

// The wire carries octal digits as decimal integers, while BedFS uses mode bits.
func (p Permission) Native() (bedfs.Permission, error) {
	n, err := strconv.ParseUint(strconv.Itoa(p.Mode), 8, 12)
	if err != nil || n > 0777 {
		return bedfs.Permission{}, fmt.Errorf("mode must contain permission digits between 000 and 777")
	}
	return bedfs.Permission{Owner: p.Owner, Group: p.Group, Mode: int(n)}, nil
}
