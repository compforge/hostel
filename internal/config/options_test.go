package config

import (
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/store"
	"github.com/qiankunli/hostel/internal/bed/tool"
)

func ptr[T any](v T) *T { return &v }
func TestExplicitSubsystemConfigOverridesEnvironment(t *testing.T) {
	t.Setenv("HOSTEL_SYNC", "cas")
	t.Setenv("HOSTEL_S3_BUCKET", "env-bucket")
	t.Setenv("HOSTEL_S3_ENDPOINT", "https://env.invalid")
	t.Setenv("HOSTEL_S3_ACCESS_KEY_ID", "env-access")
	t.Setenv("HOSTEL_S3_PATH_STYLE", "true")
	t.Setenv("HOSTEL_SYNC_AUTO_PACK_FILE_THRESHOLD", "300")
	t.Setenv("HOSTEL_MAX_BEDS", "15")
	options := Options{MaxBeds: ptr(0), Bed: BedOptions{
		Filesystem: filesystem.Options{Bwrap: ptr(tool.Off), PRoot: ptr(tool.Auto)},
		Store:      store.Options{Sync: ptr("auto"), Bucket: ptr("explicit-bucket"), Endpoint: ptr(""), PathStyle: ptr(false), AutoPackFileThreshold: ptr(0)},
	}}
	c, err := Load([]string{"--max-beds", "50"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxBeds != 0 || c.Bed.Filesystem.Level != "private" || c.Bed.Filesystem.Bwrap != tool.Off || c.Bed.Filesystem.PRoot != tool.Auto {
		t.Fatal("explicit subsystem options lost")
	}
	s := c.Bed.Store
	if s.Sync != "auto" || s.Bucket != "explicit-bucket" || s.Endpoint != "" || s.PathStyle || s.AutoPackFileThreshold != 0 {
		t.Fatal("explicit zero-valued store settings lost")
	}
	if s.AccessKeyID != "env-access" {
		t.Fatal("omitted store option did not inherit env")
	}
	t.Setenv("HOSTEL_SYNC", "noop")
	if c.Bed.Store.Sync != "auto" {
		t.Fatal("resolved config changed with environment")
	}
}
func TestStartupRejectsFeatureConflictsAndUnknownFlags(t *testing.T) {
	for _, args := range [][]string{{"--not-a-hostel-flag"}, {"unexpected"}, {"--isolation", "suite"}} {
		if _, err := Load(args, Options{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	_, err := Load(nil, Options{Bed: BedOptions{Filesystem: filesystem.Options{Bwrap: ptr(tool.Required)}}})
	if err != nil {
		t.Fatalf("required bwrap must remain configurable: %v", err)
	}
}

func TestObjectSizeConfigPrecedence(t *testing.T) {
	t.Setenv("HOSTEL_PACK_TARGET_BYTES", "")
	t.Setenv("HOSTEL_S3_PART_SIZE_BYTES", "")
	c := mustLoad(t, nil)
	if c.Bed.Store.PackTargetBytes != 32<<20 || c.Bed.Store.S3PartSizeBytes != 8<<20 {
		t.Fatal("incorrect defaults")
	}
	t.Setenv("HOSTEL_PACK_TARGET_BYTES", "67108864")
	t.Setenv("HOSTEL_S3_PART_SIZE_BYTES", "5242880")
	c = mustLoad(t, nil)
	if c.Bed.Store.PackTargetBytes != 64<<20 || c.Bed.Store.S3PartSizeBytes != 5<<20 {
		t.Fatal("env sizes lost")
	}
	args := []string{"--pack-target-bytes", "16777216", "--s3-part-size-bytes", "8388608"}
	c = mustLoad(t, args)
	if c.Bed.Store.PackTargetBytes != 16<<20 || c.Bed.Store.S3PartSizeBytes != 8<<20 {
		t.Fatal("CLI sizes lost")
	}
	c, err := Load(args, Options{Bed: BedOptions{Store: store.Options{PackTargetBytes: ptr(32 << 20), S3PartSizeBytes: ptr(int64(6 << 20))}}})
	if err != nil {
		t.Fatal(err)
	}
	if c.Bed.Store.PackTargetBytes != 32<<20 || c.Bed.Store.S3PartSizeBytes != 6<<20 {
		t.Fatal("explicit sizes lost")
	}
	for _, args := range [][]string{{"--pack-target-bytes", "0"}, {"--s3-part-size-bytes", "4194304"}, {"--s3-part-size-bytes", "5368709121"}} {
		if _, err := Load(args, Options{}); err == nil {
			t.Fatalf("accepted invalid size: %v", args)
		}
	}
}
