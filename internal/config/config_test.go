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
	t.Setenv("HOSTEL_MAX_PINNED_BEDS", "4")
	t.Setenv("HOSTEL_ADMISSION_CPU_THRESHOLD", "85")
	t.Setenv("HOSTEL_ADMISSION_MEMORY_THRESHOLD", "80")
	c := Load(nil)
	if c.MaxBeds != 12 || c.MaxPinnedBeds != 4 || c.AdmissionCPUThreshold != 85 || c.AdmissionMemoryThreshold != 80 {
		t.Fatalf("env capacity = %+v, want beds 12/4 and thresholds 85/80", c)
	}

	c = Load([]string{"-max-beds", "20", "-max-pinned-beds", "7", "-admission-cpu-threshold", "75", "-admission-memory-threshold", "70"})
	if c.MaxBeds != 20 || c.MaxPinnedBeds != 7 || c.AdmissionCPUThreshold != 75 || c.AdmissionMemoryThreshold != 70 {
		t.Fatalf("flag capacity = %+v, want beds 20/7 and thresholds 75/70", c)
	}
}

func TestResourceAdmissionThresholdDefaults(t *testing.T) {
	c := Load(nil)
	if c.AdmissionCPUThreshold != 90 || c.AdmissionMemoryThreshold != 90 {
		t.Fatalf("default resource thresholds = %d/%d, want 90/90", c.AdmissionCPUThreshold, c.AdmissionMemoryThreshold)
	}
}
