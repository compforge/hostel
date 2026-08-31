package config

import "testing"

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

func TestPathshimConfig(t *testing.T) {
	if c := Load(nil); c.PathshimPath != "pathshim" {
		t.Fatalf("default pathshim = %q, want PATH lookup", c.PathshimPath)
	}
	t.Setenv("HOSTEL_PATHSHIM", "/opt/hostel/pathshim")
	if c := Load(nil); c.PathshimPath != "/opt/hostel/pathshim" {
		t.Fatalf("env pathshim = %q", c.PathshimPath)
	}
	if c := Load([]string{"-pathshim", ""}); c.PathshimPath != "" {
		t.Fatalf("disabled pathshim = %q", c.PathshimPath)
	}
}

func TestDormReadFallbackRootIsExplicit(t *testing.T) {
	if c := Load(nil); c.DormReadFallbackRoot != "" {
		t.Fatalf("default dorm read fallback root = %q, want disabled", c.DormReadFallbackRoot)
	}
	t.Setenv("HOSTEL_DORM_READ_FALLBACK_ROOT", "/")
	if c := Load(nil); c.DormReadFallbackRoot != "/" {
		t.Fatalf("env dorm read fallback root = %q, want /", c.DormReadFallbackRoot)
	}
	if c := Load([]string{"-dorm-read-fallback-root", "/carrier"}); c.DormReadFallbackRoot != "/carrier" {
		t.Fatalf("flag dorm read fallback root = %q, want /carrier", c.DormReadFallbackRoot)
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

func TestExecutorConfig(t *testing.T) {
	if c := Load(nil); c.Executor != "auto" {
		t.Fatalf("default executor = %q, want auto", c.Executor)
	}
	t.Setenv("HOSTEL_EXECUTOR", "supervisor")
	if c := Load(nil); c.Executor != "supervisor" {
		t.Fatalf("env executor = %q, want supervisor", c.Executor)
	}
	if c := Load([]string{"-executor", "local"}); c.Executor != "local" {
		t.Fatalf("flag executor = %q, want local", c.Executor)
	}
}

func TestStoreAutoPackThresholdConfig(t *testing.T) {
	if c := Load(nil); c.StoreBackend != "auto" || c.AutoPackFileThreshold != 100 {
		t.Fatalf("default store config = %q/%d, want auto/100", c.StoreBackend, c.AutoPackFileThreshold)
	}
	t.Setenv("HOSTEL_STORE_AUTO_PACK_FILE_THRESHOLD", "25000")
	if c := Load(nil); c.AutoPackFileThreshold != 25_000 {
		t.Fatalf("env auto pack threshold = %d, want 25000", c.AutoPackFileThreshold)
	}
	if c := Load([]string{"-store-auto-pack-file-threshold", "0"}); c.AutoPackFileThreshold != 0 {
		t.Fatalf("flag auto pack threshold = %d, want disabled", c.AutoPackFileThreshold)
	}
}

func TestHostelOwnedS3Config(t *testing.T) {
	t.Setenv("AWS_REGION", "ignored")
	t.Setenv("HOSTEL_S3_REGION", "cn-beijing")
	t.Setenv("HOSTEL_S3_ACCESS_KEY_ID", "access-key")
	t.Setenv("HOSTEL_S3_SECRET_ACCESS_KEY", "secret-key")
	t.Setenv("HOSTEL_S3_SESSION_TOKEN", "session-token")
	c := Load(nil)
	if c.S3Region != "cn-beijing" || c.S3AccessKeyID != "access-key" ||
		c.S3SecretAccessKey != "secret-key" || c.S3SessionToken != "session-token" {
		t.Fatalf("Hostel S3 config = %+v", c)
	}
}

func TestTracingConfig(t *testing.T) {
	t.Setenv("HOSTEL_ENABLE_TRACING", "true")
	t.Setenv("HOSTEL_OTEL_TRACES_GRPC_ENDPOINT", "http://collector:4317")
	t.Setenv("HOSTEL_OTEL_TRACES_HTTP_ENDPOINT", "http://collector:4318/v1/traces")
	c := Load(nil)
	if !c.EnableTracing || c.OTLPTracesGRPCEndpoint != "http://collector:4317" ||
		c.OTLPTracesHTTPEndpoint != "http://collector:4318/v1/traces" {
		t.Fatalf("env tracing config = %+v", c)
	}

	c = Load([]string{
		"-enable-tracing=false",
		"-otel-traces-grpc-endpoint", "http://override:14317",
		"-otel-traces-http-endpoint", "http://override:14318/v1/traces",
	})
	if c.EnableTracing || c.OTLPTracesGRPCEndpoint != "http://override:14317" ||
		c.OTLPTracesHTTPEndpoint != "http://override:14318/v1/traces" {
		t.Fatalf("flag tracing config = %+v", c)
	}
}
