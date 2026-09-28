package supervisor

import (
	"crypto/sha256"
	"fmt"

	"golang.org/x/sys/unix"
)

// Extra files carry trusted process setup handles. Retried Starts must retain
// the same objects, rather than silently reusing a process created with another view.
func extraFileHash(fds []int) (string, error) {
	if len(fds) == 0 {
		return "", nil
	}
	h := sha256.New()
	for _, fd := range fds {
		var stat unix.Stat_t
		if err := unix.Fstat(fd, &stat); err != nil {
			return "", fmt.Errorf("stat extra file: %w", err)
		}
		fmt.Fprintf(h, "%d:%d:%d;", stat.Dev, stat.Ino, stat.Mode)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
