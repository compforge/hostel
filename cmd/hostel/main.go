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

// Command hostel is a generic sandbox data-plane manager: it runs one or many
// isolated "beds" and serves an OpenSandbox-compatible HTTP API over them.
// Standalone-capable, but primarily meant to run inside a pod. See docs/kernel.md.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/config"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/resource"
	"github.com/qiankunli/hostel/internal/store"
	"github.com/qiankunli/hostel/internal/supervisor"
	"github.com/qiankunli/hostel/internal/tracing"
	"github.com/qiankunli/hostel/internal/web"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// Isolation re-exec confiners (room mechanisms): before anything else, since
	// the argv is `hostel <subcmd> ... -- <cmd>...`, not flags. The daemon must
	// keep its privileges, so both mechanisms confine a self-re-exec, not hostel.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case isolation.ConfineArg: // landlock: __confine <dataDir> -- <cmd>...
			os.Exit(runConfine(os.Args[2:]))
		case isolation.AsUserArg: // uid: __asuser <uid> <dataDir> -- <cmd>...
			os.Exit(runAsUser(os.Args[2:]))
		case supervisor.Arg: // supervised Executor: __supervisor --socket S --bed B --executor E
			os.Exit(supervisor.Run(os.Args[2:]))
		}
	}

	cfg := config.Load(os.Args[1:])

	// Preflight subcommands used by the image (no curl needed). Handled after
	// config.Load so --health probes the SAME addr the server would listen on
	// (flag > env > default), not a separately-guessed one.
	if cfg.ShowVersion {
		fmt.Println(version)
		return
	}
	if cfg.HealthCheck {
		os.Exit(healthCheck(cfg.Addr))
	}
	shutdownTracing, err := tracing.Init(context.Background(), tracing.Config{
		Enabled:      cfg.EnableTracing,
		GRPCEndpoint: cfg.OTLPTracesGRPCEndpoint,
		HTTPEndpoint: cfg.OTLPTracesHTTPEndpoint,
		Version:      version,
	})
	if err != nil {
		log.Fatalf("hostel: init tracing: %v", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTracing(shutdownCtx); err != nil {
			log.Printf("hostel: shutdown tracing: %v", err)
		}
	}()

	log.Printf("hostel %s starting", version)

	// New resolves the requested level against the environment ceiling and
	// logs the outcome; the returned isolator is always usable.
	iso := isolation.New(cfg.IsolationMode, cfg.WorkspaceRoot)

	// Amenity manager: shared facilities light up per deployment. Chromium is
	// registered when launch (binary) or attach (--chromium-cdp-url) is
	// possible; otherwise the facility is honestly absent.
	amenities := amenity.NewRegistry()
	if br, ok := amenity.NewChromium(amenity.ChromiumConfig{
		ExecPath:  cfg.ChromiumPath,
		CDPURL:    cfg.ChromiumCDPURL,
		IdleStop:  cfg.ChromiumIdleStop,
		DebugPort: cfg.ChromiumDebugPort,
	}); ok {
		amenities.Register(br.(amenity.Amenity))
		log.Printf("hostel: amenity chromium registered (attach=%v)", cfg.ChromiumCDPURL != "")
	}

	// Fail fast on a misconfigured store: booting with silent noop while the
	// operator believes snapshots are on would be quiet data loss.
	st, err := store.New(context.Background(), store.Config{
		Backend:               cfg.StoreBackend,
		Bucket:                cfg.S3Bucket,
		Prefix:                cfg.S3Prefix,
		Endpoint:              cfg.S3Endpoint,
		PathStyle:             cfg.S3PathStyle,
		AutoPackFileThreshold: cfg.AutoPackFileThreshold,
	})
	if err != nil {
		log.Fatalf("hostel: init store: %v", err)
	}

	mgr, err := bed.NewManager(cfg.WorkspaceRoot, cfg.DefaultBed, cfg.ShellPath, iso, amenities, cfg.MaxBeds, st)
	if err != nil {
		log.Fatalf("hostel: init bed manager: %v", err)
	}
	if err := mgr.SetMaxPinnedBeds(cfg.MaxPinnedBeds); err != nil {
		log.Fatalf("hostel: configure pinned bed limit: %v", err)
	}
	if err := mgr.SetBedEnvPassthrough(os.Environ(), cfg.BedEnvPassthrough); err != nil {
		log.Fatalf("hostel: configure bed environment: %v", err)
	}
	log.Printf("hostel: bed env passthrough keys=%v", cfg.BedEnvPassthrough)
	resources := resource.New()
	mgr.SetResourceTracker(resources)
	resourceReport := resources.Report()
	if resourceReport.Available {
		log.Printf("hostel: per-bed resource accounting enabled (backend=%s)", resourceReport.Backend)
	} else {
		log.Printf("hostel: per-bed resource accounting unavailable (backend=%s reason=%s)",
			resourceReport.Backend, resourceReport.Reason)
	}
	admissionCtx, stopAdmission := context.WithCancel(context.Background())
	defer stopAdmission()
	resourceAdmission, err := resource.NewAdmission(admissionCtx, resource.NewCarrier(), resource.AdmissionConfig{
		CPUThresholdPercent:    cfg.AdmissionCPUThreshold,
		MemoryThresholdPercent: cfg.AdmissionMemoryThreshold,
	})
	if err != nil {
		log.Fatalf("hostel: configure resource admission: %v", err)
	}
	mgr.SetResourceAdmission(resourceAdmission)
	admissionReport := resourceAdmission.Report()
	if admissionReport.Enabled {
		log.Printf("hostel: carrier resource admission enabled (cpu=%d%% memory=%d%% available=%v reason=%s)",
			admissionReport.CPUThresholdPercent, admissionReport.MemoryThresholdPercent,
			admissionReport.Available, admissionReport.Reason)
	} else {
		log.Printf("hostel: carrier resource admission disabled")
	}
	mgr.SetBedIdleTTL(cfg.BedIdleTTL)
	mgr.SetLuggageLimits(cfg.LuggageHighBytes, cfg.LuggageLowBytes)
	// Per-bed browser endpoint injection (PLAYWRIGHT_MCP_CDP_ENDPOINT): beds
	// reach hostel over loopback (shared pod net ns). Minting is lazy-safe, so
	// this is on whenever the browser amenity can proxy.
	if addr := loopbackAddr(cfg.Addr); addr != "" {
		mgr.SetCDPAdvertise(addr)
	}

	// Select one Executor backend before request admission. Auto is an honest
	// portability fallback; explicitly requesting supervisor fails closed.
	switch cfg.Executor {
	case "local":
		mgr.SetExecutorFactory(executor.NewLocalFactory(resources))
	case "auto", "supervisor":
		exe, executableErr := os.Executable()
		var factory *executor.SupervisorFactory
		var factoryErr error
		if executableErr == nil {
			factory, factoryErr = executor.NewSupervisorFactory(exe, resources)
		}
		probeCtx, cancelProbe := context.WithTimeout(context.Background(), 5*time.Second)
		if factoryErr == nil && executableErr == nil {
			factoryErr = factory.Probe(probeCtx)
		}
		cancelProbe()
		if factoryErr != nil || executableErr != nil {
			if factory != nil {
				_ = factory.Close()
			}
			if cfg.Executor == "supervisor" {
				log.Fatalf("hostel: supervisor executor unavailable: executable=%v probe=%v", executableErr, factoryErr)
			}
			log.Printf("hostel: supervisor executor unavailable, using local executor: executable=%v probe=%v", executableErr, factoryErr)
			mgr.SetExecutorFactory(executor.NewLocalFactory(resources))
		} else {
			mgr.SetExecutorFactory(factory)
			log.Printf("hostel: supervisor executor enabled")
		}
	default:
		log.Fatalf("hostel: invalid executor backend %q", cfg.Executor)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Idle bed reaper.
	if cfg.BedIdleTTL > 0 {
		go func() {
			t := time.NewTicker(cfg.BedIdleTTL / 2)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if reaped := mgr.CollectExpired(ctx, time.Now()); len(reaped) > 0 {
						log.Printf("hostel: reaped idle beds: %v", reaped)
					}
				}
			}
		}()
	}

	// Luggage GC: keep evicted beds' local dirs (warm cache) under the disk
	// watermarks. Fixed cadence — the watermarks, not the tick rate, decide
	// how much disk luggage may hold.
	if cfg.LuggageHighBytes > 0 {
		go func() {
			t := time.NewTicker(time.Minute)
			defer t.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-t.C:
					if reaped := mgr.CollectLuggage(ctx); len(reaped) > 0 {
						log.Printf("hostel: reaped luggage: %v", reaped)
					}
				}
			}
		}()
	}

	// Store synchronization owns lifecycle requests, periodic cadence and
	// retry/backoff. A zero interval disables only the periodic safety net.
	go mgr.RunStoreSync(ctx, cfg.PersistInterval)

	srv := &http.Server{Addr: cfg.Addr, Handler: web.NewServer(
		mgr,
		web.WithTracing(cfg.EnableTracing),
		web.WithDormReadFallbackRoot(cfg.DormReadFallbackRoot),
	).Handler()}
	go func() {
		log.Printf("hostel: listening on %s (isolation=%s, workspace-root=%s, default-bed=%s)",
			cfg.Addr, iso.Name(), cfg.WorkspaceRoot, cfg.DefaultBed)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("hostel: server error: %v", err)
		}
	}()

	<-ctx.Done()
	log.Printf("hostel: shutting down")
	httpShutdownCtx, cancelHTTPShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(httpShutdownCtx)
	cancelHTTPShutdown()
	executorShutdownCtx, cancelExecutorShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelExecutorShutdown()
	if err := mgr.Close(executorShutdownCtx); err != nil {
		log.Printf("hostel: executor shutdown: %v", err)
	}
}

// runConfine implements `hostel __confine <dataDir> -- <cmd> <args>...`: apply
// the room (Landlock) restrictions to THIS process, then exec the real command
// so it inherits them. Returns a process exit code (it only returns on error;
// success replaces the process image).
func runConfine(args []string) int {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 1 || sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "hostel __confine: usage: __confine <dataDir> -- <cmd>...")
		return 2
	}
	dataDir := args[0]
	cmd := args[sep+1:]

	if err := isolation.ApplyConfine(dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "hostel __confine: apply landlock: %v\n", err)
		return 1
	}
	path, err := exec.LookPath(cmd[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostel __confine: %s: %v\n", cmd[0], err)
		return 127
	}
	if err := syscall.Exec(path, cmd, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "hostel __confine: exec %s: %v\n", path, err)
		return 126
	}
	return 0 // unreachable
}

// runAsUser implements `hostel __asuser <uid> <dataDir> -- <cmd> <args>...`:
// drop THIS process to the bed uid (and no_new_privs), enter the data dir, then
// exec the real command so it inherits the reduced identity. Returns a process
// exit code (only on error; success replaces the process image).
func runAsUser(args []string) int {
	sep := -1
	for i, a := range args {
		if a == "--" {
			sep = i
			break
		}
	}
	// Need at least <uid> <dataDir> before the "--", and a command after it.
	if sep < 2 || sep+1 >= len(args) {
		fmt.Fprintln(os.Stderr, "hostel __asuser: usage: __asuser <uid> <dataDir> -- <cmd>...")
		return 2
	}
	uid, err := strconv.Atoi(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostel __asuser: bad uid %q: %v\n", args[0], err)
		return 2
	}
	dataDir := args[1]
	cmd := args[sep+1:]

	if err := isolation.ApplyAsUser(uid, dataDir); err != nil {
		fmt.Fprintf(os.Stderr, "hostel __asuser: %v\n", err)
		return 1
	}
	path, err := exec.LookPath(cmd[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "hostel __asuser: %s: %v\n", cmd[0], err)
		return 127
	}
	// The bed uid has no /etc/passwd entry; give tools a sane HOME (its own
	// workspace) and USER so bash and friends don't choke on the unknown uid.
	// Filter any inherited HOME/USER/LOGNAME first: appending would leave two
	// copies, and a libc getenv() takes the FIRST (the daemon's) — bash happens
	// to be last-wins, but don't rely on the exec target being a shell.
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "HOME=") || strings.HasPrefix(kv, "USER=") || strings.HasPrefix(kv, "LOGNAME=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "HOME="+dataDir, "USER=hostel-bed", "LOGNAME=hostel-bed")
	if err := syscall.Exec(path, cmd, env); err != nil {
		fmt.Fprintf(os.Stderr, "hostel __asuser: exec %s: %v\n", path, err)
		return 126
	}
	return 0 // unreachable
}

// loopbackAddr rewrites the listen address into the loopback host:port a bed
// can dial: wildcard or empty hosts become 127.0.0.1, concrete hosts stay.
// Empty on unparseable input — callers treat that as "don't advertise".
func loopbackAddr(addr string) string {
	host, port, err := net.SplitHostPort(addr)
	if err != nil || port == "" {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}

// healthCheck GETs the local /healthz for the image HEALTHCHECK — no external
// tool required. addr is the server's resolved listen address, so the probe
// can never target the wrong port.
func healthCheck(addr string) int {
	if addr == "" {
		addr = config.DefaultAddr
	}
	if addr[0] == ':' {
		addr = "127.0.0.1" + addr
	}
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/healthz")
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
