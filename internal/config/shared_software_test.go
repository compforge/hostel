package config

import "testing"

func TestSharedSoftwareConfigurationIsInternal(t *testing.T) {
	for _, flag := range []string{"--shared-software", "--shared-directories", "--bed-extra-gids"} {
		if _, err := Load([]string{flag, "[]"}, Options{}); err == nil {
			t.Fatalf("accepted removed option %s", flag)
		}
	}
}

func TestCarrierPublicGID(t *testing.T) {
	t.Setenv("HOSTEL_PUBLIC_GID", "198888")
	cfg := mustLoad(t, nil)
	if cfg.Bed.Network.PublicGID != 198888 || cfg.Bed.Privilege.PublicGID != 198888 {
		t.Fatalf("public gid: network=%d privilege=%d", cfg.Bed.Network.PublicGID, cfg.Bed.Privilege.PublicGID)
	}
	for _, raw := range []string{"", "root", "-1", "0", "200000", "299999", "4294967295", "4294967296"} {
		t.Setenv("HOSTEL_PUBLIC_GID", raw)
		if _, err := Load(nil, Options{}); err == nil {
			t.Fatalf("accepted HOSTEL_PUBLIC_GID=%q", raw)
		}
	}
}
