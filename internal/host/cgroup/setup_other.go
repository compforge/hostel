//go:build !linux

package cgroup

import "fmt"

func Setup(string, string, string) (*Group, error) {
	return nil, fmt.Errorf("cgroup v2 requires Linux")
}
