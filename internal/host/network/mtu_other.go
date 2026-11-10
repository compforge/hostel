//go:build !linux

package network

import (
	"context"
	"fmt"
)

func defaultRoutes(context.Context) ([]byte, error) {
	return nil, fmt.Errorf("default-route MTU discovery requires Linux")
}
