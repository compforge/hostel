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
		Filesystem: filesystem.Options{Level: ptr("room"), Bwrap: ptr(feature.Off), PRoot: ptr(feature.Auto)},
		Store:      store.Options{Sync: ptr("auto"), Bucket: ptr("explicit-bucket"), Endpoint: ptr(""), PathStyle: ptr(false), AutoPackFileThreshold: ptr(0)},
	}}
	c, err := Load([]string{"--max-beds", "50"}, options)
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxBeds != 0 || c.Bed.Filesystem.Level != "room" || c.Bed.Filesystem.Bwrap != feature.Off || c.Bed.Filesystem.PRoot != feature.Auto {
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
	_, err := Load(nil, Options{Bed: BedOptions{Filesystem: filesystem.Options{Level: ptr("room"), Bwrap: ptr(feature.Required)}}})
	if err == nil {
		t.Fatal("accepted room with required bwrap")
	}
}

func TestTypedStorePathsOverrideEnvironmentWithoutResplitting(t *testing.T) {
	t.Setenv("HOSTEL_PERSISTED_PATHS", "invalid")
	paths := []string{"/workspace", "/data,with,commas"}
	c, err := Load(nil, Options{Bed: BedOptions{Store: store.Options{PersistedPaths: &paths}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bed.Store.PersistedPaths) != 2 || c.Bed.Store.PersistedPaths[1] != "/data,with,commas" {
		t.Fatal(c.Bed.Store.PersistedPaths)
	}
	paths[0] = "/changed"
	if c.Bed.Store.PersistedPaths[0] != "/workspace" {
		t.Fatal("runtime config aliases caller input")
	}
}
