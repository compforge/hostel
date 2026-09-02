// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

// Package config loads hostel configuration from flags + environment.
package config

import (
	"flag"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/osx"
	"github.com/qiankunli/hostel/internal/bedfs"
)

// Config is the hostel runtime configuration. hostel is a generic sandbox
// data-plane manager: it can run standalone on a laptop/VM, but is primarily
// meant to run inside a pod, serving one or many beds (isolation units).
// DefaultAddr is the default HTTP listen address.
const DefaultAddr = ":8872"

const defaultAdmissionThresholdPercent = 90

const defaultBedPressureThresholdPercent = 80

const defaultAutoPackFileThreshold = 100

type Config struct {
	ShowVersion bool
	HealthCheck bool
	// EnableTracing exports W3C-propagated HTTP and domain traces over OTLP.
	// gRPC wins when both endpoints are set, matching sandctl deployment policy.
	EnableTracing          bool
	OTLPTracesGRPCEndpoint string
	OTLPTracesHTTPEndpoint string
	// Addr is the HTTP listen address.
	Addr string
	// WorkspaceRoot is the parent dir under which each bed gets its workspace
	// (<root>/<bedID>). In a pod this is typically a bind of shared network FS.
	WorkspaceRoot string
	// IsolationMode is the requested data-isolation level (room type):
	//   "dorm" | "room" | "suite" | "auto" (= environment ceiling, default).
	// Levels resolve to mechanisms (direct/landlock/bwrap) in internal/isolation;
	// effective = min(requested, ceiling), over-asks degrade honestly.
	IsolationMode string
	// PathshimPath optionally adds a best-effort workspace + configured process
	// view below suite. The binary is probed through the selected isolation
	// mechanism at startup; probe failure keeps carrier-path behavior.
	PathshimPath string
	// ProjectedPaths gives configured BedFS subtrees stable Executor paths.
	// Comma-separated BED_PATH=PROCESS_PATH pairs are parsed after Load so an
	// invalid deployment fails startup instead of silently dropping a mapping.
	ProjectedPaths string
	// PersistedPaths is the comma-separated set of BedFS paths included in Store
	// snapshots. Identity metadata is always durable and is not configured here.
	PersistedPaths string
	// DormReadFallbackRoot optionally exposes an exclusive dorm Executor's
	// process root through read-only file APIs after a BedFS miss. Empty is the
	// safe default for shared carriers; mutation APIs never use this root.
	DormReadFallbackRoot string
	// DefaultBed is the bed id used when a request omits one — lets simple
	// single-tenant callers ignore the bed concept entirely.
	DefaultBed string
	// BedIdleTTL is the retention added after a bed's latest accepted operation.
	// Zero disables automatic expiry.
	BedIdleTTL time.Duration
	// MaxBeds caps how many beds may exist at once (0 = unlimited). Applies to
	// NEW bed creation only, never to the default bed; the 429 it produces is
	// the backpressure/placement signal for an upstream scheduler.
	MaxBeds int
	// MaxPinnedBeds is the reference capacity for tenant beds that are running
	// work or whose latest data has not reached the durable store. It is not an
	// admission limit. Zero inherits MaxBeds; the default bed is exempt.
	MaxPinnedBeds int
	// BedPressureThresholdPercent is the shared high-watermark percentage for
	// occupied/max-beds and pinned/max-pinned-beds. Zero disables the signal.
	BedPressureThresholdPercent int
	// AdmissionCPUThreshold / AdmissionMemoryThreshold reject an idle tenant
	// bed's first operation when aggregate carrier usage reaches the configured
	// percentage. Zero disables that resource dimension.
	AdmissionCPUThreshold    int
	AdmissionMemoryThreshold int
	// Executor selects the Bed process realm: "auto" probes supervisor and falls
	// back to local; explicit "supervisor" fails startup when unavailable.
	Executor string

	// Workspace persistence (docs/store.md). Backend "auto" (default) is noop
	// without a bucket; with a bucket it defaults new beds to pack and detects
	// existing beds for backward compatibility.
	// "s3" stores one object per content-addressed chunk; "pack" groups chunks
	// into larger objects; "tar" uploads one full tar.gz. S3 credentials are
	// Hostel-owned configuration and never enter a bed process.
	StoreBackend      string
	S3Bucket          string
	S3Prefix          string
	S3Endpoint        string // S3-compatible endpoint (MinIO/TOS/Ceph); "" = AWS
	S3PathStyle       bool   // force path-style bucket addressing (for example MinIO)
	S3Region          string
	S3AccessKeyID     string
	S3SecretAccessKey string
	S3SessionToken    string
	// AutoPackFileThreshold switches an auto-routed bed from CAS to pack once
	// its persistable non-directory entry count exceeds this value. Zero
	// disables automatic switching.
	AutoPackFileThreshold int
	// PersistInterval is the periodic snapshot safety net (0 = only at
	// lifecycle boundaries). Bounds how much work a crash can lose.
	PersistInterval time.Duration
	// LuggageHighBytes / LuggageLowBytes govern orphaned local dirs left by an
	// unclean shutdown or older Hostel version. Normal evictions do not create
	// luggage. High 0 disables GC.
	LuggageHighBytes int64
	LuggageLowBytes  int64

	// Chromium amenity (docs/amenity.md): launch (path) or attach (CDP URL).
	ChromiumPath     string
	ChromiumCDPURL   string
	ChromiumIdleStop time.Duration
	// ChromiumDebugPort fixes a launched Chromium's --remote-debugging-port so
	// the per-bed CDP proxy has a stable upstream (0 = no proxy in launch mode;
	// attach mode uses the CDP URL). See docs/amenity.md〈per-bed CDP〉.
	ChromiumDebugPort int
	// ShellPath is the shell binary a bed's long-running session runs.
	ShellPath string
}

// Load builds Config from flags, with env fallbacks (HOSTEL_*).
func Load(args []string) *Config {
	fs := flag.NewFlagSet("hostel", flag.ContinueOnError)
	c := &Config{}
	fs.StringVar(&c.Addr, "addr", osx.EnvStr("HOSTEL_ADDR", DefaultAddr), "HTTP listen address")
	// Preflight flags handled by main (used by the image HEALTHCHECK); real
	// flags so addr resolution stays identical to the running server.
	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&c.HealthCheck, "health", false, "GET local /healthz and exit (0=ok)")
	fs.BoolVar(&c.EnableTracing, "enable-tracing", osx.EnvBool("HOSTEL_ENABLE_TRACING", false), "export OpenTelemetry traces")
	fs.StringVar(&c.OTLPTracesGRPCEndpoint, "otel-traces-grpc-endpoint", osx.EnvStr("HOSTEL_OTEL_TRACES_GRPC_ENDPOINT", ""), "OTLP gRPC traces endpoint")
	fs.StringVar(&c.OTLPTracesHTTPEndpoint, "otel-traces-http-endpoint", osx.EnvStr("HOSTEL_OTEL_TRACES_HTTP_ENDPOINT", ""), "OTLP HTTP traces endpoint")
	fs.StringVar(&c.WorkspaceRoot, "workspace-root", osx.EnvStr("HOSTEL_WORKSPACE_ROOT", "/workspace"), "parent dir for per-bed workspaces")
	fs.StringVar(&c.IsolationMode, "isolation", osx.EnvStr("HOSTEL_ISOLATION", "auto"), "data-isolation level: dorm | room | suite | auto (auto=env ceiling)")
	fs.StringVar(&c.PathshimPath, "pathshim", osx.EnvStr("HOSTEL_PATHSHIM", "pathshim"), "pathshim binary for the best-effort workspace and configured process view (empty=disabled)")
	fs.StringVar(&c.ProjectedPaths, "projected-paths", osx.EnvStr("HOSTEL_PROJECTED_PATHS", ""), "comma-separated BedFS-to-process path projections (for example /cache=/mnt/cache)")
	fs.StringVar(&c.PersistedPaths, "persisted-paths", osx.EnvStr("HOSTEL_PERSISTED_PATHS", "/workspace"), "comma-separated BedFS paths included in Store snapshots")
	fs.StringVar(&c.DormReadFallbackRoot, "dorm-read-fallback-root", osx.EnvStr("HOSTEL_DORM_READ_FALLBACK_ROOT", ""), "exclusive dorm process root used only for read fallback (empty=disabled)")
	fs.StringVar(&c.DefaultBed, "default-bed", osx.EnvStr("HOSTEL_DEFAULT_BED", "default"), "bed id used when a request omits one")
	fs.StringVar(&c.ShellPath, "shell", osx.EnvStr("HOSTEL_SHELL", "/bin/bash"), "shell for bed sessions")
	idle := fs.Duration("bed-idle-timeout", osx.EnvDuration("HOSTEL_BED_IDLE_TIMEOUT", 30*time.Minute), "reap a bed after this idle duration (0=never)")
	fs.IntVar(&c.MaxBeds, "max-beds", osx.EnvInt("HOSTEL_MAX_BEDS", 0), "max concurrent beds, 0=unlimited (default bed exempt)")
	fs.IntVar(&c.MaxPinnedBeds, "max-pinned-beds", osx.EnvInt("HOSTEL_MAX_PINNED_BEDS", 0), "pinned-bed pressure reference, 0=inherit max-beds (default bed exempt)")
	fs.IntVar(&c.BedPressureThresholdPercent, "bed-pressure-threshold-percent", osx.EnvInt("HOSTEL_BED_PRESSURE_THRESHOLD_PERCENT", defaultBedPressureThresholdPercent), "occupied/pinned bed pressure threshold percent, 0=disabled")
	fs.IntVar(&c.AdmissionCPUThreshold, "admission-cpu-threshold", osx.EnvInt("HOSTEL_ADMISSION_CPU_THRESHOLD", defaultAdmissionThresholdPercent), "reject new active beds at this carrier CPU usage percent, 0=disabled")
	fs.IntVar(&c.AdmissionMemoryThreshold, "admission-memory-threshold", osx.EnvInt("HOSTEL_ADMISSION_MEMORY_THRESHOLD", defaultAdmissionThresholdPercent), "reject new active beds at this carrier memory usage percent, 0=disabled")
	fs.StringVar(&c.Executor, "executor", osx.EnvStr("HOSTEL_EXECUTOR", "auto"), "executor backend: auto | supervisor | local")
	fs.StringVar(&c.StoreBackend, "store", osx.EnvStr("HOSTEL_STORE", "auto"), "workspace persistence backend: auto (per-bed detection) | noop | s3 | cas | pack | tar")
	fs.StringVar(&c.S3Bucket, "s3-bucket", osx.EnvStr("HOSTEL_S3_BUCKET", ""), "S3 bucket for bed snapshots")
	fs.StringVar(&c.S3Prefix, "s3-prefix", osx.EnvStr("HOSTEL_S3_PREFIX", "hostel"), "key prefix for bed snapshots")
	fs.StringVar(&c.S3Endpoint, "s3-endpoint", osx.EnvStr("HOSTEL_S3_ENDPOINT", ""), "S3-compatible endpoint (empty = AWS)")
	fs.BoolVar(&c.S3PathStyle, "s3-path-style", osx.EnvBool("HOSTEL_S3_PATH_STYLE", false), "use path-style S3 bucket addressing (default virtual-hosted style)")
	fs.StringVar(&c.S3Region, "s3-region", osx.EnvStr("HOSTEL_S3_REGION", ""), "S3 region")
	c.S3AccessKeyID = osx.EnvStr("HOSTEL_S3_ACCESS_KEY_ID", "")
	c.S3SecretAccessKey = osx.EnvStr("HOSTEL_S3_SECRET_ACCESS_KEY", "")
	c.S3SessionToken = osx.EnvStr("HOSTEL_S3_SESSION_TOKEN", "")
	fs.IntVar(&c.AutoPackFileThreshold, "store-auto-pack-file-threshold", osx.EnvInt("HOSTEL_STORE_AUTO_PACK_FILE_THRESHOLD", defaultAutoPackFileThreshold), "auto store: switch CAS to pack above this persistable file count, 0=disabled")
	persist := fs.Duration("persist-interval", osx.EnvDuration("HOSTEL_PERSIST_INTERVAL", 0), "periodic snapshot interval, 0=lifecycle boundaries only")
	fs.Int64Var(&c.LuggageHighBytes, "luggage-high-bytes", osx.EnvInt64("HOSTEL_LUGGAGE_HIGH_BYTES", 0), "luggage disk high watermark in bytes, 0=no luggage GC")
	fs.Int64Var(&c.LuggageLowBytes, "luggage-low-bytes", osx.EnvInt64("HOSTEL_LUGGAGE_LOW_BYTES", 0), "luggage GC target in bytes (default 80% of high)")
	fs.StringVar(&c.ChromiumPath, "chromium-path", osx.EnvStr("HOSTEL_CHROMIUM_PATH", ""), "chromium binary for the browser amenity (empty = probe PATH)")
	fs.StringVar(&c.ChromiumCDPURL, "chromium-cdp-url", osx.EnvStr("HOSTEL_CHROMIUM_CDP_URL", ""), "attach to an existing Chromium CDP endpoint instead of launching")
	idleStop := fs.Duration("chromium-idle-stop", osx.EnvDuration("HOSTEL_CHROMIUM_IDLE_STOP", 5*time.Minute), "stop a launched Chromium this long after its last tenant, 0=never")
	fs.IntVar(&c.ChromiumDebugPort, "chromium-debug-port", osx.EnvInt("HOSTEL_CHROMIUM_DEBUG_PORT", 9222), "fixed remote-debugging-port for a launched Chromium so the per-bed CDP proxy has a stable upstream, 0=disable proxy")
	// Ignore parse errors for unknown flags in tests; flag prints usage itself.
	_ = fs.Parse(args)
	c.BedIdleTTL = *idle
	c.PersistInterval = *persist
	c.ChromiumIdleStop = *idleStop
	// Low defaults to 80% of high so a bare --luggage-high-bytes works; a low
	// above high would make GC loop uselessly, so clamp it.
	if c.LuggageHighBytes > 0 && (c.LuggageLowBytes <= 0 || c.LuggageLowBytes > c.LuggageHighBytes) {
		c.LuggageLowBytes = c.LuggageHighBytes * 8 / 10
	}
	return c
}

// ParseProjectedPaths converts the deployment string into Hostel's generic
// filesystem projection model. The whole set is validated together so startup
// cannot accept ambiguous nested mappings.
func ParseProjectedPaths(raw string) ([]bedfs.PathProjection, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	projections := make([]bedfs.PathProjection, 0, len(parts))
	for _, part := range parts {
		bedPath, processPath, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || strings.TrimSpace(bedPath) == "" || strings.TrimSpace(processPath) == "" {
			return nil, fmt.Errorf("invalid path projection %q: expected BED_PATH=PROCESS_PATH", part)
		}
		projection, err := bedfs.NewPathProjection(bedPath, processPath)
		if err != nil {
			return nil, fmt.Errorf("invalid path projection %q: %w", part, err)
		}
		projections = append(projections, projection)
	}
	if err := bedfs.ValidatePathProjections(projections); err != nil {
		return nil, fmt.Errorf("invalid path projections: %w", err)
	}
	return projections, nil
}

// ParsePersistedPaths validates the business-neutral BedFS durability allowlist.
// Root would make every caller-created path durable again, so it is rejected.
func ParsePersistedPaths(raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		persistPath := path.Clean(strings.TrimSpace(part))
		if !path.IsAbs(persistPath) || persistPath == "/" {
			return nil, fmt.Errorf("persist path %q must be an absolute non-root BedFS path", part)
		}
		for _, previous := range paths {
			if persistPath == previous || strings.HasPrefix(persistPath, previous+"/") || strings.HasPrefix(previous, persistPath+"/") {
				return nil, fmt.Errorf("persist paths %q and %q overlap", previous, persistPath)
			}
		}
		paths = append(paths, persistPath)
	}
	return paths, nil
}
