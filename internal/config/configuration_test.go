package config

import (
	"testing"
)

func TestConfigurationSourcePrecedence(t *testing.T) {
	t.Setenv("HOSTEL_CONFIGURATION_SOURCES", `{"env":"/env"}`)
	c, err := Load([]string{"--configuration-sources", `{"flag":"/flag"}`}, Options{})
	if err != nil || c.Bed.Configuration.Sources["flag"] != "/flag" {
		t.Fatalf("flag configuration: %v", err)
	}
	empty := map[string]string{}
	c, err = Load([]string{"--configuration-sources", "invalid"}, Options{Bed: BedOptions{ConfigurationSources: &empty}})
	if err != nil || len(c.Bed.Configuration.Sources) != 0 {
		t.Fatalf("explicit empty configuration: %v", err)
	}
	if _, err := Load([]string{"--configuration-sources", `{"bad":"relative"}`}, Options{}); err == nil {
		t.Fatal("relative source accepted")
	}
}
