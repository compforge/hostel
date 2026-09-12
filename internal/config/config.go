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
	"os"
	"path"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/osx"
	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
	"github.com/qiankunli/hostel/internal/feature"
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
	Bed         BedConfig
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

// Load resolves explicit options > CLI flags > HOSTEL_* environment > defaults.
// Components receive this snapshot and never re-read the startup environment.
func Load(args []string, explicit Options) (*Config, error) {
	fs := flag.NewFlagSet("hostel", flag.ContinueOnError)
	c := &Config{}
	var persistedPaths string
	bedUID, bedGID := os.Geteuid(), os.Getegid()
	if bedUID == 0 {
		bedUID, bedGID = 1000, 1000
	}
	fs.StringVar(&c.Addr, "addr", osx.EnvStr("HOSTEL_ADDR", DefaultAddr), "HTTP listen address")
	// Preflight flags handled by main (used by the image HEALTHCHECK); real
	// flags so addr resolution stays identical to the running server.
	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&c.HealthCheck, "health", false, "GET local /healthz and exit (0=ok)")
	fs.BoolVar(&c.EnableTracing, "enable-tracing", osx.EnvBool("HOSTEL_ENABLE_TRACING", false), "export OpenTelemetry traces")
	fs.StringVar(&c.OTLPTracesGRPCEndpoint, "otel-traces-grpc-endpoint", osx.EnvStr("HOSTEL_OTEL_TRACES_GRPC_ENDPOINT", ""), "OTLP gRPC traces endpoint")
	fs.StringVar(&c.OTLPTracesHTTPEndpoint, "otel-traces-http-endpoint", osx.EnvStr("HOSTEL_OTEL_TRACES_HTTP_ENDPOINT", ""), "OTLP HTTP traces endpoint")
	fs.StringVar(&c.WorkspaceRoot, "workspace-root", osx.EnvStr("HOSTEL_WORKSPACE_ROOT", "/workspace"), "parent dir for per-bed workspaces")
	fs.StringVar(&c.Bed.Filesystem.Level, "isolation", osx.EnvStr("HOSTEL_ISOLATION", "auto"), "data-isolation level: dorm | room | suite | auto (auto=env ceiling)")
	fs.StringVar(&c.Bed.Filesystem.ProjectedPaths, "projected-paths", osx.EnvStr("HOSTEL_PROJECTED_PATHS", ""), "comma-separated additional BedFS-to-process path projections; /workspace=/workspace is built in")
	fs.StringVar(&persistedPaths, "persisted-paths", osx.EnvStr("HOSTEL_PERSISTED_PATHS", "/workspace"), "comma-separated BedFS paths included in Store snapshots")
	fs.StringVar(&c.Bed.Filesystem.DormReadFallbackRoot, "dorm-read-fallback-root", osx.EnvStr("HOSTEL_DORM_READ_FALLBACK_ROOT", ""), "exclusive dorm process root used only for read fallback (empty=disabled)")
	fs.StringVar(&c.DefaultBed, "default-bed", osx.EnvStr("HOSTEL_DEFAULT_BED", "default"), "bed id used when a request omits one")
	fs.StringVar(&c.ShellPath, "shell", osx.EnvStr("HOSTEL_SHELL", "/bin/bash"), "shell for bed sessions")
	idle := fs.Duration("bed-idle-timeout", osx.EnvDuration("HOSTEL_BED_IDLE_TIMEOUT", 30*time.Minute), "reap a bed after this idle duration (0=never)")
	fs.IntVar(&c.MaxBeds, "max-beds", osx.EnvInt("HOSTEL_MAX_BEDS", 0), "max concurrent beds, 0=unlimited (default bed exempt)")
	fs.IntVar(&c.MaxPinnedBeds, "max-pinned-beds", osx.EnvInt("HOSTEL_MAX_PINNED_BEDS", 0), "pinned-bed pressure reference, 0=inherit max-beds (default bed exempt)")
	fs.IntVar(&c.BedPressureThresholdPercent, "bed-pressure-threshold-percent", osx.EnvInt("HOSTEL_BED_PRESSURE_THRESHOLD_PERCENT", defaultBedPressureThresholdPercent), "occupied/pinned bed pressure threshold percent, 0=disabled")
	fs.IntVar(&c.Bed.Resource.Admission.CPUThresholdPercent, "admission-cpu-threshold", osx.EnvInt("HOSTEL_ADMISSION_CPU_THRESHOLD", defaultAdmissionThresholdPercent), "reject new active beds at this carrier CPU usage percent, 0=disabled")
	fs.IntVar(&c.Bed.Resource.Admission.MemoryThresholdPercent, "admission-memory-threshold", osx.EnvInt("HOSTEL_ADMISSION_MEMORY_THRESHOLD", defaultAdmissionThresholdPercent), "reject new active beds at this carrier memory usage percent, 0=disabled")
	fs.StringVar(&c.Bed.Executor.Backend, "executor", osx.EnvStr("HOSTEL_EXECUTOR", "auto"), "executor backend: auto | supervisor | local")
	fs.IntVar(&c.Bed.Privilege.UID, "bed-uid", osx.EnvInt("HOSTEL_BED_UID", bedUID), "fixed non-root uid for Bed processes")
	fs.IntVar(&c.Bed.Privilege.GID, "bed-gid", osx.EnvInt("HOSTEL_BED_GID", bedGID), "fixed non-root gid for Bed processes")
	fs.StringVar(&c.Bed.Store.Sync, "sync", osx.EnvStr("HOSTEL_SYNC", "auto"), "workspace synchronization policy: auto (per-bed detection) | noop | cas | pack | tar | restic")
	fs.StringVar(&c.Bed.Store.ResticBinary, "restic-binary", osx.EnvStr("HOSTEL_RESTIC_BINARY", "restic"), "restic binary (requires 0.19.1)")
	c.Bed.Store.ResticPassword = osx.EnvStr("HOSTEL_RESTIC_PASSWORD", "")
	fs.StringVar(&c.Bed.Store.Bucket, "s3-bucket", osx.EnvStr("HOSTEL_S3_BUCKET", ""), "S3 bucket for bed snapshots")
	fs.StringVar(&c.Bed.Store.Prefix, "s3-prefix", osx.EnvStr("HOSTEL_S3_PREFIX", "hostel"), "key prefix for bed snapshots")
	fs.StringVar(&c.Bed.Store.Endpoint, "s3-endpoint", osx.EnvStr("HOSTEL_S3_ENDPOINT", ""), "S3-compatible endpoint (empty = AWS)")
	fs.BoolVar(&c.Bed.Store.PathStyle, "s3-path-style", osx.EnvBool("HOSTEL_S3_PATH_STYLE", false), "use path-style S3 bucket addressing (default virtual-hosted style)")
	fs.StringVar(&c.Bed.Store.Region, "s3-region", osx.EnvStr("HOSTEL_S3_REGION", ""), "S3 region")
	c.Bed.Store.AccessKeyID = osx.EnvStr("HOSTEL_S3_ACCESS_KEY_ID", "")
	c.Bed.Store.SecretAccessKey = osx.EnvStr("HOSTEL_S3_SECRET_ACCESS_KEY", "")
	c.Bed.Store.SessionToken = osx.EnvStr("HOSTEL_S3_SESSION_TOKEN", "")
	fs.IntVar(&c.Bed.Store.AutoPackFileThreshold, "sync-auto-pack-file-threshold", osx.EnvInt("HOSTEL_SYNC_AUTO_PACK_FILE_THRESHOLD", defaultAutoPackFileThreshold), "auto store: switch CAS to pack above this persistable file count, 0=disabled")
	persist := fs.Duration("persist-interval", osx.EnvDuration("HOSTEL_PERSIST_INTERVAL", 0), "periodic snapshot interval, 0=lifecycle boundaries only")
	fs.Int64Var(&c.LuggageHighBytes, "luggage-high-bytes", osx.EnvInt64("HOSTEL_LUGGAGE_HIGH_BYTES", 0), "luggage disk high watermark in bytes, 0=no luggage GC")
	fs.Int64Var(&c.LuggageLowBytes, "luggage-low-bytes", osx.EnvInt64("HOSTEL_LUGGAGE_LOW_BYTES", 0), "luggage GC target in bytes (default 80% of high)")
	fs.StringVar(&c.ChromiumPath, "chromium-path", osx.EnvStr("HOSTEL_CHROMIUM_PATH", ""), "chromium binary for the browser amenity (empty = probe PATH)")
	fs.StringVar(&c.ChromiumCDPURL, "chromium-cdp-url", osx.EnvStr("HOSTEL_CHROMIUM_CDP_URL", ""), "attach to an existing Chromium CDP endpoint instead of launching")
	idleStop := fs.Duration("chromium-idle-stop", osx.EnvDuration("HOSTEL_CHROMIUM_IDLE_STOP", 5*time.Minute), "stop a launched Chromium this long after its last tenant, 0=never")
	fs.IntVar(&c.ChromiumDebugPort, "chromium-debug-port", osx.EnvInt("HOSTEL_CHROMIUM_DEBUG_PORT", 9222), "fixed remote-debugging-port for a launched Chromium so the per-bed CDP proxy has a stable upstream, 0=disable proxy")
	if err := fs.Parse(args); err != nil {
		return nil, err
	}
	if fs.NArg() != 0 {
		return nil, fmt.Errorf("unexpected arguments: %v", fs.Args())
	}
	c.BedIdleTTL = *idle
	c.PersistInterval = *persist
	c.ChromiumIdleStop = *idleStop
	explicit.apply(c)
	var paths []string
	if explicit.Bed.Store.PersistedPaths != nil {
		paths = *explicit.Bed.Store.PersistedPaths
	} else {
		paths = strings.Split(persistedPaths, ",")
	}
	var err error
	c.Bed.Store.PersistedPaths, err = normalizePersistedPaths(paths)
	if err != nil {
		return nil, err
	}

	for _, p := range []*feature.Policy{&c.Bed.Filesystem.Bwrap, &c.Bed.Filesystem.Landlock, &c.Bed.Filesystem.UID, &c.Bed.Filesystem.PRoot, &c.Bed.Filesystem.Pathshim, &c.Bed.Network.NetNS, &c.Bed.Resource.Cgroup} {
		*p = p.Effective()
	}
	// Low defaults to 80% of high so a bare --luggage-high-bytes works; a low
	// above high would make GC loop uselessly, so clamp it.
	if c.LuggageHighBytes > 0 && (c.LuggageLowBytes <= 0 || c.LuggageLowBytes > c.LuggageHighBytes) {
		c.LuggageLowBytes = c.LuggageHighBytes * 8 / 10
	}
	if err := c.Bed.Filesystem.Validate(); err != nil {
		return nil, err
	}
	if err := c.Bed.Network.Validate(); err != nil {
		return nil, fmt.Errorf("network: %w", err)
	}
	if err := c.Bed.Resource.Validate(); err != nil {
		return nil, fmt.Errorf("resource: %w", err)
	}
	return c, nil
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
	return normalizePersistedPaths(strings.Split(raw, ","))
}
func normalizePersistedPaths(parts []string) ([]string, error) {
	if len(parts) == 0 {
		return nil, fmt.Errorf("store persisted paths must contain at least one path")
	}
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
