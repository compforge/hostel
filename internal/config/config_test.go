package config

import (
	"slices"
	"testing"
)

func TestIsolationAndManagedServiceConfigContract(t *testing.T) {
	// The three north-facing room types are configuration values; resolution to
	// a host mechanism is deliberately tested in internal/isolation.
	for _, mode := range []string{"dorm", "room", "suite", "auto"} {
		c := Load([]string{"-isolation", mode, "-workspace-root", "/var/lib/hostel"})
		if c.IsolationMode != mode || c.WorkspaceRoot != "/var/lib/hostel" {
			t.Fatalf("mode %q: isolation=%q root=%q", mode, c.IsolationMode, c.WorkspaceRoot)
		}
	}
	// Managed services are optional and configured independently of isolation;
	// Chromium launch and attach forms are mutually exclusive deployment
	// contracts, both of which must survive config loading.
	launch := Load([]string{"-chromium-path", "/usr/bin/chromium", "-chromium-debug-port", "9333"})
	if launch.ChromiumPath != "/usr/bin/chromium" || launch.ChromiumDebugPort != 9333 {
		t.Fatalf("launch config: %+v", launch)
	}
	attach := Load([]string{"-chromium-cdp-url", "http://chromium:9222", "-chromium-debug-port", "0"})
	if attach.ChromiumCDPURL != "http://chromium:9222" || attach.ChromiumDebugPort != 0 {
		t.Fatalf("attach config: %+v", attach)
	}
}

func TestBedEnvPassthroughConfig(t *testing.T) {
	t.Setenv("HOSTEL_BED_ENV_PASSTHROUGH", "PATH, LANG,PATH,UV_TOOL_DIR")
	c := Load(nil)
	want := []string{"PATH", "LANG", "UV_TOOL_DIR"}
	if !slices.Equal(c.BedEnvPassthrough, want) {
		t.Fatalf("BedEnvPassthrough = %v, want %v", c.BedEnvPassthrough, want)
	}

	c = Load([]string{"-bed-env-passthrough", "PATH,TERM"})
	want = []string{"PATH", "TERM"}
	if !slices.Equal(c.BedEnvPassthrough, want) {
		t.Fatalf("flag BedEnvPassthrough = %v, want %v", c.BedEnvPassthrough, want)
	}
}

func TestBedCapacityConfig(t *testing.T) {
	t.Setenv("HOSTEL_MAX_BEDS", "12")
	t.Setenv("HOSTEL_MAX_ACTIVE_BEDS", "4")
	c := Load(nil)
	if c.MaxBeds != 12 || c.MaxActiveBeds != 4 {
		t.Fatalf("env capacity = max %d active %d, want 12/4", c.MaxBeds, c.MaxActiveBeds)
	}

	c = Load([]string{"-max-beds", "20", "-max-active-beds", "7"})
	if c.MaxBeds != 20 || c.MaxActiveBeds != 7 {
		t.Fatalf("flag capacity = max %d active %d, want 20/7", c.MaxBeds, c.MaxActiveBeds)
	}
}
