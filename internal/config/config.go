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
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/osx"
)

// Config is the hostel runtime configuration. hostel is a generic sandbox
// data-plane manager: it can run standalone on a laptop/VM, but is primarily
// meant to run inside a pod, serving one or many beds (isolation units).
// DefaultAddr is the default HTTP listen address.
const DefaultAddr = ":8872"

const defaultBedEnvPassthrough = "PATH,LANG,LC_ALL,LC_CTYPE,TZ,TERM,COLORTERM,SSL_CERT_FILE,SSL_CERT_DIR,PYTHONUSERBASE,NPM_CONFIG_PREFIX,UV_TOOL_DIR,UV_TOOL_BIN_DIR"

const defaultAdmissionThresholdPercent = 90

type Config struct {
	ShowVersion bool
	HealthCheck bool
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
	// MaxActiveBeds caps tenant beds with at least one in-flight operation.
	// Zero inherits MaxBeds (and is unlimited when MaxBeds is also zero). A
	// finite MaxBeds is always the effective ceiling. The default bed is exempt.
	MaxActiveBeds int
	// AdmissionCPUThreshold / AdmissionMemoryThreshold reject an idle tenant
	// bed's first operation when aggregate carrier usage reaches the configured
	// percentage. Zero disables that resource dimension.
	AdmissionCPUThreshold    int
	AdmissionMemoryThreshold int
	// BedInit selects the process spawner: "auto" (default) probes the per-bed
	// init (docs/kernel.md 〈进程树〉) at boot and falls back to in-process
	// forking where it can't serve; "off" forces in-process.
	BedInit string

	// Workspace persistence (docs/persistence.md). Backend "auto" (default)
	// resolves to "s3" when a bucket is configured and "noop" otherwise.
	// "s3" stores content-addressed chunks under <prefix>/cas/ and transfers
	// incrementally, at lifecycle boundaries (evict/checkpoint/interval);
	// credentials resolve via the standard AWS SDK chain.
	StoreBackend string
	S3Bucket     string
	S3Prefix     string
	S3Endpoint   string // S3-compatible endpoint (MinIO/TOS/Ceph); "" = AWS
	S3PathStyle  bool   // force path-style bucket addressing (for example MinIO)
	// PersistInterval is the periodic snapshot safety net (0 = only at
	// lifecycle boundaries). Bounds how much work a crash can lose.
	PersistInterval time.Duration
	// LuggageHighBytes / LuggageLowBytes are the disk watermarks for luggage
	// (evicted beds' local dirs kept as warm cache): past high, luggage GC
	// deletes cold copies until under low. High 0 disables GC (luggage
	// accumulates — fine when workspace-root is on disposable/ample disk).
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
	// BedEnvPassthrough names standard carrier-software variables selected from
	// the daemon environment. HOSTEL_* and BED_* are reserved namespaces and
	// cannot be passed through.
	BedEnvPassthrough []string
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
	fs.StringVar(&c.WorkspaceRoot, "workspace-root", osx.EnvStr("HOSTEL_WORKSPACE_ROOT", "/workspace"), "parent dir for per-bed workspaces")
	fs.StringVar(&c.IsolationMode, "isolation", osx.EnvStr("HOSTEL_ISOLATION", "auto"), "data-isolation level: dorm | room | suite | auto (auto=env ceiling)")
	fs.StringVar(&c.DefaultBed, "default-bed", osx.EnvStr("HOSTEL_DEFAULT_BED", "default"), "bed id used when a request omits one")
	fs.StringVar(&c.ShellPath, "shell", osx.EnvStr("HOSTEL_SHELL", "/bin/bash"), "shell for bed sessions")
	bedEnvPassthrough := fs.String("bed-env-passthrough", osx.EnvStr("HOSTEL_BED_ENV_PASSTHROUGH", defaultBedEnvPassthrough), "comma-separated carrier env names exposed to bed processes")
	idle := fs.Duration("bed-idle-timeout", osx.EnvDuration("HOSTEL_BED_IDLE_TIMEOUT", 30*time.Minute), "reap a bed after this idle duration (0=never)")
	fs.IntVar(&c.MaxBeds, "max-beds", osx.EnvInt("HOSTEL_MAX_BEDS", 0), "max concurrent beds, 0=unlimited (default bed exempt)")
	fs.IntVar(&c.MaxActiveBeds, "max-active-beds", osx.EnvInt("HOSTEL_MAX_ACTIVE_BEDS", 0), "max active beds, 0=inherit max-beds (default bed exempt)")
	fs.IntVar(&c.AdmissionCPUThreshold, "admission-cpu-threshold", osx.EnvInt("HOSTEL_ADMISSION_CPU_THRESHOLD", defaultAdmissionThresholdPercent), "reject new active beds at this carrier CPU usage percent, 0=disabled")
	fs.IntVar(&c.AdmissionMemoryThreshold, "admission-memory-threshold", osx.EnvInt("HOSTEL_ADMISSION_MEMORY_THRESHOLD", defaultAdmissionThresholdPercent), "reject new active beds at this carrier memory usage percent, 0=disabled")
	fs.StringVar(&c.BedInit, "bed-init", osx.EnvStr("HOSTEL_BED_INIT", "auto"), "per-bed init spawner: auto (probe at boot, fall back in-process) | off")
	fs.StringVar(&c.StoreBackend, "store", osx.EnvStr("HOSTEL_STORE", "auto"), "workspace persistence backend: auto (s3 when --s3-bucket is set, else noop) | noop | s3")
	fs.StringVar(&c.S3Bucket, "s3-bucket", osx.EnvStr("HOSTEL_S3_BUCKET", ""), "S3 bucket for bed snapshots")
	fs.StringVar(&c.S3Prefix, "s3-prefix", osx.EnvStr("HOSTEL_S3_PREFIX", "hostel"), "key prefix for bed snapshots")
	fs.StringVar(&c.S3Endpoint, "s3-endpoint", osx.EnvStr("HOSTEL_S3_ENDPOINT", ""), "S3-compatible endpoint (empty = AWS)")
	fs.BoolVar(&c.S3PathStyle, "s3-path-style", osx.EnvBool("HOSTEL_S3_PATH_STYLE", false), "use path-style S3 bucket addressing (default virtual-hosted style)")
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
	c.BedEnvPassthrough = splitCommaList(*bedEnvPassthrough)
	// Low defaults to 80% of high so a bare --luggage-high-bytes works; a low
	// above high would make GC loop uselessly, so clamp it.
	if c.LuggageHighBytes > 0 && (c.LuggageLowBytes <= 0 || c.LuggageLowBytes > c.LuggageHighBytes) {
		c.LuggageLowBytes = c.LuggageHighBytes * 8 / 10
	}
	return c
}

func splitCommaList(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	seen := make(map[string]struct{})
	items := make([]string, 0, strings.Count(value, ",")+1)
	for _, raw := range strings.Split(value, ",") {
		item := strings.TrimSpace(raw)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		items = append(items, item)
	}
	return items
}
