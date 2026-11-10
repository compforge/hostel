//go:build linux

package network

import "context"

func defaultRoutes(ctx context.Context) ([]byte, error) {
	return run(ctx, "", "ip", "-j", "-4", "route", "show", "table", "all", "default")
}
