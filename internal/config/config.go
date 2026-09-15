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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/osx"
	"github.com/qiankunli/hostel/internal/bed"
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
	ServiceAdvertiseHost string
	PortRangeStart       int
	PortRangeEnd         int
	Bed                  BedConfig
	ShowVersion          bool
	HealthCheck          bool
	// EnableTracing exports W3C-propagated HTTP and domain traces over OTLP.
	// gRPC wins when both endpoints are set.
	EnableTracing          bool
	OTLPTracesGRPCEndpoint string
	OTLPTracesHTTPEndpoint string
	// Addr is the HTTP listen address.
	Addr string
	// BedsRoot is the carrier parent directory for per-Bed identity and data
	// (<root>/<bed name>). It is independent of the Bed workdir.
	BedsRoot string
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
	var configurationSources string
	fs.StringVar(&configurationSources, "configuration-sources", osx.EnvStr("HOSTEL_CONFIGURATION_SOURCES", "{}"), "JSON object of named configuration directories")
	bedUID, bedGID := os.Geteuid(), os.Getegid()
	if bedUID == 0 {
		bedUID, bedGID = 1000, 1000
	}
	fs.StringVar(&c.Addr, "addr", osx.EnvStr("HOSTEL_ADDR", DefaultAddr), "HTTP listen address")
	fs.StringVar(&c.ServiceAdvertiseHost, "service-advertise-host", osx.EnvStr("HOSTEL_SERVICE_ADVERTISE_HOST", "127.0.0.1"), "reachable service IP or hostname (Pod IP in Kubernetes)")
	fs.IntVar(&c.PortRangeStart, "port-range-start", osx.EnvInt("HOSTEL_PORT_RANGE_START", 20000), "first dynamic TCP service port")
	fs.IntVar(&c.PortRangeEnd, "port-range-end", osx.EnvInt("HOSTEL_PORT_RANGE_END", 29999), "last dynamic TCP service port")
	// Preflight flags handled by main (used by the image HEALTHCHECK); real
	// flags so addr resolution stays identical to the running server.
	fs.BoolVar(&c.ShowVersion, "version", false, "print version and exit")
	fs.BoolVar(&c.HealthCheck, "health", false, "GET local /healthz and exit (0=ok)")
	fs.BoolVar(&c.EnableTracing, "enable-tracing", osx.EnvBool("HOSTEL_ENABLE_TRACING", false), "export OpenTelemetry traces")
	fs.StringVar(&c.OTLPTracesGRPCEndpoint, "otel-traces-grpc-endpoint", osx.EnvStr("HOSTEL_OTEL_TRACES_GRPC_ENDPOINT", ""), "OTLP gRPC traces endpoint")
	fs.StringVar(&c.OTLPTracesHTTPEndpoint, "otel-traces-http-endpoint", osx.EnvStr("HOSTEL_OTEL_TRACES_HTTP_ENDPOINT", ""), "OTLP HTTP traces endpoint")
	fs.StringVar(&c.BedsRoot, "beds-root", osx.EnvStr("HOSTEL_BEDS_ROOT", "/workspace"), "carrier parent dir for per-bed data")
	fs.StringVar(&c.Bed.RoomType, "isolation", osx.EnvStr("HOSTEL_ISOLATION", "auto"), "Bed room type: dorm | room | suite | auto (auto=highest available profile)")
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
	fs.StringVar(&c.Bed.Store.Sync, "sync", osx.EnvStr("HOSTEL_SYNC", "auto"), "Bed data synchronization policy: auto (per-bed detection) | noop | cas | pack | tar | restic")
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
	if explicit.Bed.ConfigurationSources == nil {
		if err := json.Unmarshal([]byte(configurationSources), &c.Bed.Configuration.Sources); err != nil {
			return nil, fmt.Errorf("configuration sources must be a JSON object of directory paths")
		}
	}
	explicit.apply(c)
	c.Bed.Privilege.Explicit = explicit.Bed.Privilege.UID != nil || explicit.Bed.Privilege.GID != nil
	for _, key := range []string{"HOSTEL_BED_UID", "HOSTEL_BED_GID"} {
		if _, exists := os.LookupEnv(key); exists {
			c.Bed.Privilege.Explicit = true
		}
	}
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "bed-uid" || f.Name == "bed-gid" {
			c.Bed.Privilege.Explicit = true
		}
	})
	room, roomErr := bed.ParseRoomType(c.Bed.RoomType)
	if roomErr != nil {
		return nil, roomErr
	}
	c.Bed.Filesystem = c.Bed.Filesystem.ForRoom(room)
	c.Bed.Network = c.Bed.Network.ForRoom(room)
	for _, p := range []*feature.Policy{&c.Bed.Filesystem.Bwrap, &c.Bed.Filesystem.Landlock, &c.Bed.Filesystem.UID, &c.Bed.Filesystem.PRoot, &c.Bed.Filesystem.Pathshim, &c.Bed.Network.NetNS, &c.Bed.Resource.Cgroup} {
		*p = p.Effective()
	}
	// Low defaults to 80% of high so a bare --luggage-high-bytes works; a low
	// above high would make GC loop uselessly, so clamp it.
	if c.LuggageHighBytes > 0 && (c.LuggageLowBytes <= 0 || c.LuggageLowBytes > c.LuggageHighBytes) {
		c.LuggageLowBytes = c.LuggageHighBytes * 8 / 10
	}
	if err := c.Bed.Configuration.Validate(); err != nil {
		return nil, err
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
	if c.PortRangeStart < 1 || c.PortRangeEnd > 65535 || c.PortRangeEnd < c.PortRangeStart {
		return nil, fmt.Errorf("invalid dynamic port range")
	}
	if c.ServiceAdvertiseHost == "" || strings.ContainsAny(c.ServiceAdvertiseHost, "/?#@ \t\r\n") {
		return nil, fmt.Errorf("service advertise host must be an IP or hostname")
	}
	return c, nil
}
