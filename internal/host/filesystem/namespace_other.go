//go:build !linux

package filesystem

import "fmt"

const CaptureArg = "__mount_capture"
const EnterArg = "__mount_enter"

func RunMountHelper([]string) error { return fmt.Errorf("mount namespaces require Linux") }
