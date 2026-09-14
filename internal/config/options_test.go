package config

import (
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/store"
	"github.com/qiankunli/hostel/internal/feature"
)

func ptr[T any](v T) *T { return &v }
func TestExplicitComponentConfigOverridesEnvironment(t *testing.T) {
	t.Setenv("HOSTEL_ISOLATION", "suite")
	t.Setenv("HOSTEL_SYNC", "cas")
	t.Setenv("HOSTEL_S3_BUCKET", "env-bucket")
	t.Setenv("HOSTEL_S3_ENDPOINT", "https://env.invalid")
	t.Setenv("HOSTEL_S3_ACCESS_KEY_ID", "env-access")
	t.Setenv("HOSTEL_S3_PATH_STYLE", "true")
	t.Setenv("HOSTEL_SYNC_AUTO_PACK_FILE_THRESHOLD", "300")
	t.Setenv("HOSTEL_MAX_BEDS", "15")
	options := Options{MaxBeds: ptr(0), Bed: BedOptions{
		RoomType:   ptr("room"),
		Filesystem: filesystem.Options{Bwrap: ptr(feature.Off), PRoot: ptr(feature.Auto)},
		Store:      store.Options{Sync: ptr("auto"), Bucket: ptr("explicit-bucket"), Endpoint: ptr(""), PathStyle: ptr(false), AutoPackFileThreshold: ptr(0)},
	}}
	c, err := Load([]string{"--max-beds", "50"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxBeds != 0 || c.Bed.RoomType != "room" || c.Bed.Filesystem.Level != "confined" || c.Bed.Filesystem.Bwrap != feature.Off || c.Bed.Filesystem.PRoot != feature.Auto {
		t.Fatal("explicit component options lost")
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
	for _, args := range [][]string{{"--not-a-hostel-flag"}, {"unexpected"}, {"--isolation", "typo"}} {
		if _, err := Load(args, Options{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
	_, err := Load(nil, Options{Bed: BedOptions{RoomType: ptr("room"), Filesystem: filesystem.Options{Bwrap: ptr(feature.Required)}}})
	if err == nil {
		t.Fatal("accepted room with required bwrap")
	}
}
