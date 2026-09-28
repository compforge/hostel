//go:build !linux

package filesystem

import "fmt"

const DeriveMountArg = "__mount_derive"

const CaptureArg = "__mount_capture"
const BedInitMountArg = "__bedinit_mount"

func RunMountHelper([]string) error { return fmt.Errorf("mount namespaces require Linux") }
