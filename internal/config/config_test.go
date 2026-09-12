package config

import (
	"os"
	"testing"
)

func TestBedUserConfig(t *testing.T) {
	wantUID, wantGID := os.Geteuid(), os.Getegid()
	if wantUID == 0 {
		wantUID, wantGID = 1000, 1000
	}
	if c := mustLoad(t, nil); c.Bed.Privilege.UID != wantUID || c.Bed.Privilege.GID != wantGID {
		t.Fatalf("default bed user = %d:%d, want %d:%d", c.Bed.Privilege.UID, c.Bed.Privilege.GID, wantUID, wantGID)
	}
	t.Setenv("HOSTEL_BED_UID", "1200")
	t.Setenv("HOSTEL_BED_GID", "1300")
	if c := mustLoad(t, nil); c.Bed.Privilege.UID != 1200 || c.Bed.Privilege.GID != 1300 {
		t.Fatalf("env bed user = %d:%d, want 1200:1300", c.Bed.Privilege.UID, c.Bed.Privilege.GID)
	}
	if c := mustLoad(t, []string{"-bed-uid", "1400", "-bed-gid", "1500"}); c.Bed.Privilege.UID != 1400 || c.Bed.Privilege.GID != 1500 {
		t.Fatalf("flag bed user = %d:%d, want 1400:1500", c.Bed.Privilege.UID, c.Bed.Privilege.GID)
	}
}

func TestIsolationAndManagedServiceConfigContract(t *testing.T) {
	// The three north-facing room types are configuration values; resolution to
	// a host mechanism is deliberately tested in internal/isolation.
	for _, mode := range []string{"dorm", "room", "suite", "auto"} {
		c := mustLoad(t, []string{"-isolation", mode, "-workspace-root", "/var/lib/hostel"})
		if c.Bed.Filesystem.Level != mode || c.WorkspaceRoot != "/var/lib/hostel" {
			t.Fatalf("mode %q: isolation=%q root=%q", mode, c.Bed.Filesystem.Level, c.WorkspaceRoot)
		}
	}
	// Managed services are optional and configured independently of isolation;
	// Chromium launch and attach forms are mutually exclusive deployment
	// contracts, both of which must survive config loading.
	launch := mustLoad(t, []string{"-chromium-path", "/usr/bin/chromium", "-chromium-debug-port", "9333"})
	if launch.ChromiumPath != "/usr/bin/chromium" || launch.ChromiumDebugPort != 9333 {
		t.Fatalf("launch config: %+v", launch)
	}
	attach := mustLoad(t, []string{"-chromium-cdp-url", "http://chromium:9222", "-chromium-debug-port", "0"})
	if attach.ChromiumCDPURL != "http://chromium:9222" || attach.ChromiumDebugPort != 0 {
		t.Fatalf("attach config: %+v", attach)
	}
}

func TestProjectedPathsConfig(t *testing.T) {
	if c := mustLoad(t, nil); c.Bed.Filesystem.ProjectedPaths != "" {
		t.Fatalf("default projected paths = %q", c.Bed.Filesystem.ProjectedPaths)
	}
	t.Setenv("HOSTEL_PROJECTED_PATHS", "/memory=/mnt/memory,/cache=/mnt/cache")
	c := mustLoad(t, nil)
	projections, err := ParseProjectedPaths(c.Bed.Filesystem.ProjectedPaths)
	if err != nil {
		t.Fatal(err)
	}
	if len(projections) != 2 || projections[0].BedPath != "/memory" || projections[1].ProcessPath != "/mnt/cache" {
		t.Fatalf("projections = %+v", projections)
	}
	for _, raw := range []string{
		"/memory",
		"/memory=/mnt/memory,/memory/cache=/mnt/cache",
		"/cache=/workspace/cache",
	} {
		if _, err := ParseProjectedPaths(raw); err == nil {
			t.Errorf("ParseProjectedPaths(%q) succeeded", raw)
		}
	}
}

func TestPersistedPathsConfig(t *testing.T) {
	if c := mustLoad(t, nil); len(c.Bed.Store.PersistedPaths) != 1 || c.Bed.Store.PersistedPaths[0] != "/workspace" {
		t.Fatalf("default persisted paths = %q", c.Bed.Store.PersistedPaths)
	}
	t.Setenv("HOSTEL_PERSISTED_PATHS", "/workspace,/home/agent")
	paths := mustLoad(t, nil).Bed.Store.PersistedPaths
	if len(paths) != 2 || paths[0] != "/workspace" || paths[1] != "/home/agent" {
		t.Fatalf("persist paths = %v", paths)
	}
	for _, raw := range []string{"", "/", "workspace", "/workspace,/workspace/cache"} {
		if _, err := ParsePersistedPaths(raw); err == nil {
			t.Errorf("ParsePersistedPaths(%q) succeeded", raw)
		}
	}
}

func TestDormReadFallbackRootIsExplicit(t *testing.T) {
	if c := mustLoad(t, nil); c.Bed.Filesystem.DormReadFallbackRoot != "" {
		t.Fatalf("default dorm read fallback root = %q, want disabled", c.Bed.Filesystem.DormReadFallbackRoot)
	}
	t.Setenv("HOSTEL_DORM_READ_FALLBACK_ROOT", "/")
	if c := mustLoad(t, nil); c.Bed.Filesystem.DormReadFallbackRoot != "/" {
		t.Fatalf("env dorm read fallback root = %q, want /", c.Bed.Filesystem.DormReadFallbackRoot)
	}
	if c := mustLoad(t, []string{"-dorm-read-fallback-root", "/carrier"}); c.Bed.Filesystem.DormReadFallbackRoot != "/carrier" {
		t.Fatalf("flag dorm read fallback root = %q, want /carrier", c.Bed.Filesystem.DormReadFallbackRoot)
	}
}

func TestBedCapacityConfig(t *testing.T) {
	t.Setenv("HOSTEL_MAX_BEDS", "12")
	t.Setenv("HOSTEL_MAX_PINNED_BEDS", "4")
	t.Setenv("HOSTEL_BED_PRESSURE_THRESHOLD_PERCENT", "75")
	t.Setenv("HOSTEL_ADMISSION_CPU_THRESHOLD", "85")
	t.Setenv("HOSTEL_ADMISSION_MEMORY_THRESHOLD", "80")
	c := mustLoad(t, nil)
	if c.MaxBeds != 12 || c.MaxPinnedBeds != 4 || c.BedPressureThresholdPercent != 75 || c.Bed.Resource.Admission.CPUThresholdPercent != 85 || c.Bed.Resource.Admission.MemoryThresholdPercent != 80 {
		t.Fatalf("env capacity = %+v, want beds 12/4 and thresholds 75/85/80", c)
	}

	c = mustLoad(t, []string{"-max-beds", "20", "-max-pinned-beds", "7", "-bed-pressure-threshold-percent", "60", "-admission-cpu-threshold", "75", "-admission-memory-threshold", "70"})
	if c.MaxBeds != 20 || c.MaxPinnedBeds != 7 || c.BedPressureThresholdPercent != 60 || c.Bed.Resource.Admission.CPUThresholdPercent != 75 || c.Bed.Resource.Admission.MemoryThresholdPercent != 70 {
		t.Fatalf("flag capacity = %+v, want beds 20/7 and thresholds 60/75/70", c)
	}
}

func TestResourceAdmissionThresholdDefaults(t *testing.T) {
	c := mustLoad(t, nil)
	if c.BedPressureThresholdPercent != 80 || c.Bed.Resource.Admission.CPUThresholdPercent != 90 || c.Bed.Resource.Admission.MemoryThresholdPercent != 90 {
		t.Fatalf("default thresholds = bed %d, resources %d/%d; want 80 and 90/90",
			c.BedPressureThresholdPercent, c.Bed.Resource.Admission.CPUThresholdPercent, c.Bed.Resource.Admission.MemoryThresholdPercent)
	}
}

func TestExecutorConfig(t *testing.T) {
	if c := mustLoad(t, nil); c.Bed.Executor.Backend != "auto" {
		t.Fatalf("default executor = %q, want auto", c.Bed.Executor.Backend)
	}
	t.Setenv("HOSTEL_EXECUTOR", "supervisor")
	if c := mustLoad(t, nil); c.Bed.Executor.Backend != "supervisor" {
		t.Fatalf("env executor = %q, want supervisor", c.Bed.Executor.Backend)
	}
	if c := mustLoad(t, []string{"-executor", "local"}); c.Bed.Executor.Backend != "local" {
		t.Fatalf("flag executor = %q, want local", c.Bed.Executor.Backend)
	}
}

func TestStoreAutoPackThresholdConfig(t *testing.T) {
	if c := mustLoad(t, nil); c.Bed.Store.Sync != "auto" || c.Bed.Store.AutoPackFileThreshold != 100 {
		t.Fatalf("default store config = %q/%d, want auto/100", c.Bed.Store.Sync, c.Bed.Store.AutoPackFileThreshold)
	}
	t.Setenv("HOSTEL_SYNC_AUTO_PACK_FILE_THRESHOLD", "25000")
	if c := mustLoad(t, nil); c.Bed.Store.AutoPackFileThreshold != 25_000 {
		t.Fatalf("env auto pack threshold = %d, want 25000", c.Bed.Store.AutoPackFileThreshold)
	}
	if c := mustLoad(t, []string{"-sync-auto-pack-file-threshold", "0"}); c.Bed.Store.AutoPackFileThreshold != 0 {
		t.Fatalf("flag auto pack threshold = %d, want disabled", c.Bed.Store.AutoPackFileThreshold)
	}
}

func TestHostelOwnedS3Config(t *testing.T) {
	t.Setenv("AWS_REGION", "ignored")
	t.Setenv("HOSTEL_S3_REGION", "cn-beijing")
	t.Setenv("HOSTEL_S3_ACCESS_KEY_ID", "access-key")
	t.Setenv("HOSTEL_S3_SECRET_ACCESS_KEY", "secret-key")
	t.Setenv("HOSTEL_S3_SESSION_TOKEN", "session-token")
	c := mustLoad(t, nil)
	if c.Bed.Store.Region != "cn-beijing" || c.Bed.Store.AccessKeyID != "access-key" ||
		c.Bed.Store.SecretAccessKey != "secret-key" || c.Bed.Store.SessionToken != "session-token" {
		t.Fatalf("Hostel S3 config = %+v", c)
	}
}

func TestTracingConfig(t *testing.T) {
	t.Setenv("HOSTEL_ENABLE_TRACING", "true")
	t.Setenv("HOSTEL_OTEL_TRACES_GRPC_ENDPOINT", "http://collector:4317")
	t.Setenv("HOSTEL_OTEL_TRACES_HTTP_ENDPOINT", "http://collector:4318/v1/traces")
	c := mustLoad(t, nil)
	if !c.EnableTracing || c.OTLPTracesGRPCEndpoint != "http://collector:4317" ||
		c.OTLPTracesHTTPEndpoint != "http://collector:4318/v1/traces" {
		t.Fatalf("env tracing config = %+v", c)
	}

	c = mustLoad(t, []string{
		"-enable-tracing=false",
		"-otel-traces-grpc-endpoint", "http://override:14317",
		"-otel-traces-http-endpoint", "http://override:14318/v1/traces",
	})
	if c.EnableTracing || c.OTLPTracesGRPCEndpoint != "http://override:14317" ||
		c.OTLPTracesHTTPEndpoint != "http://override:14318/v1/traces" {
		t.Fatalf("flag tracing config = %+v", c)
	}
}

func TestSyncEnvironmentAndFlagPriority(t *testing.T) {
	t.Setenv("HOSTEL_SYNC", "noop")
	if cfg := mustLoad(t, nil); cfg.Bed.Store.Sync != "noop" {
		t.Fatalf("env sync=%s", cfg.Bed.Store.Sync)
	}
	if cfg := mustLoad(t, []string{"--sync", "restic"}); cfg.Bed.Store.Sync != "restic" {
		t.Fatalf("flag sync=%s", cfg.Bed.Store.Sync)
	}
}
