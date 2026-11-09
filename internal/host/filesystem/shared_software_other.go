//go:build !linux

package filesystem

import (
	"context"
	"fmt"
)

func PrepareSharedSoftware(context.Context, string, int) (int, error) {
	return 0, fmt.Errorf("shared software default ACL preparation requires Linux")
}
