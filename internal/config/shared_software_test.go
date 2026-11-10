package config

import "testing"

func TestSharedSoftwareConfigurationIsInternal(t *testing.T) {
	for _, flag := range []string{"--shared-software", "--shared-directories", "--bed-extra-gids"} {
		if _, err := Load([]string{flag, "[]"}, Options{}); err == nil {
			t.Fatalf("accepted removed option %s", flag)
		}
	}
}
