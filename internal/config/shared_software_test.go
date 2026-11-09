package config

import (
	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"testing"
)

func TestSharedSoftwareConfig(t *testing.T) {
	t.Setenv("HOSTEL_BED_EXTRA_GIDS", "1000")
	t.Setenv("HOSTEL_SHARED_SOFTWARE", `[{"path":"/software","gid":1000,"policy":"required"}]`)
	c, err := Load(nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Bed.Filesystem.SharedSoftware) != 1 || c.Bed.Filesystem.SharedSoftware[0].Path != "/software" {
		t.Fatal("shared software declaration lost")
	}
	c, err = Load([]string{"--shared-software", `[{"path":"/other","gid":1000}]`}, Options{})
	if err != nil || c.Bed.Filesystem.SharedSoftware[0].Path != "/other" {
		t.Fatalf("flag precedence: %+v %v", c, err)
	}
	empty := []filesystem.SharedSoftwareDirectory{}
	c, err = Load([]string{"--shared-software", "invalid"}, Options{Bed: BedOptions{Filesystem: filesystem.Options{SharedSoftware: &empty}}})
	if err != nil || len(c.Bed.Filesystem.SharedSoftware) != 0 {
		t.Fatalf("explicit empty must override flag/env: %v", err)
	}
	for _, raw := range []string{
		`[{"path":"/software","gid":1001}]`,
		`[{"path":"relative","gid":1000}]`,
		`[{"path":"/","gid":1000}]`,
		`[{"path":"/software","gid":0}]`,
		`[{"path":"/software","gid":200001}]`,
		`[{"path":"/software","gid":1000,"policy":"unknown"}]`,
		`[{"path":"/software","gid":1000},{"path":"/software/nested","gid":1000}]`,
		`{}`,
	} {
		if _, err := Load([]string{"--shared-software", raw}, Options{}); err == nil {
			t.Fatalf("accepted invalid shared software: %s", raw)
		}
	}
}
