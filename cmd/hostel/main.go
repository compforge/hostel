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
	"syscall"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/store"
	"github.com/qiankunli/hostel/internal/config"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	"github.com/qiankunli/hostel/internal/supervisor"
	"github.com/qiankunli/hostel/internal/tracing"
	"github.com/qiankunli/hostel/internal/web"
	"github.com/qiankunli/hostel/pkg/mcpproxy"
)

// version is stamped at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// The Landlock re-exec confiner must run before flag parsing because its argv
	// is `hostel <subcmd> ... -- <cmd>...`. The daemon keeps its privileges; only
	// this child applies the Bed file boundary before exec-ing the command.
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case isolation.ConfineArg: // landlock: __confine <dataDir> -- <cmd>...
			os.Exit(runConfine(os.Args[2:]))
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

	pathProjections, err := config.ParseProjectedPaths(cfg.ProjectedPaths)
	if err != nil {
		log.Fatalf("hostel: configure path projections: %v", err)
	}
	persistedPaths, err := config.ParsePersistedPaths(cfg.PersistedPaths)
	if err != nil {
		log.Fatalf("hostel: configure persisted paths: %v", err)
	}

	// New resolves the requested level against the environment ceiling and
	// logs the outcome; the returned isolator is always usable.
	host := hostfacts.Collect()
	iso := isolation.New(host, cfg.IsolationMode, cfg.WorkspaceRoot,
		isolation.WithPathProjections(pathProjections),
	)
	bedUser, err := privilege.NewBedUser(cfg.BedUID, cfg.BedGID)
	if err != nil {
		log.Fatalf("hostel: configure bed user: %v", err)
	}

	// Facilities remain registered when unavailable; Start reports the reason.
	amenities := amenity.NewManager(host)
	if err := amenities.Register(amenity.NewMCP(mcpproxy.Options{})); err != nil {
		log.Fatal(err)
	}
	if err := amenities.Register(amenity.NewChromium(amenity.ChromiumConfig{
		ExecPath: cfg.ChromiumPath, CDPURL: cfg.ChromiumCDPURL,
		IdleStop: cfg.ChromiumIdleStop, DebugPort: cfg.ChromiumDebugPort,
	})); err != nil {
		log.Fatal(err)
	}

	// Fail fast on a misconfigured store: booting with silent noop while the
	// operator believes snapshots are on would be quiet data loss.
	st, err := store.NewManager(context.Background(), store.Config{
		Sync:                  cfg.StoreSync,
		ResticBinary:          cfg.ResticBinary,
		ResticPassword:        cfg.ResticPassword,
		Bucket:                cfg.S3Bucket,
		Prefix:                cfg.S3Prefix,
		Endpoint:              cfg.S3Endpoint,
		PathStyle:             cfg.S3PathStyle,
		Region:                cfg.S3Region,
		AccessKeyID:           cfg.S3AccessKeyID,
		SecretAccessKey:       cfg.S3SecretAccessKey,
		SessionToken:          cfg.S3SessionToken,
		AutoPackFileThreshold: cfg.AutoPackFileThreshold,
		PersistedPaths:        persistedPaths,
	})
	if err != nil {
		log.Fatalf("hostel: init store: %v", err)
	}

	mgr, err := bed.NewManager(host, cfg.WorkspaceRoot, cfg.DefaultBed, cfg.ShellPath, iso, amenities, cfg.MaxBeds, st,
		bed.WithBedUser(bedUser),
	)
	if err != nil {
		log.Fatalf("hostel: init bed manager: %v", err)
	}
	if err := mgr.SetMaxPinnedBeds(cfg.MaxPinnedBeds); err != nil {
		log.Fatalf("hostel: configure pinned bed reference: %v", err)
	}
	if err := mgr.SetBedPressureThresholdPercent(cfg.BedPressureThresholdPercent); err != nil {
		log.Fatalf("hostel: configure bed pressure threshold: %v", err)
	}
	if filtered := mgr.SetCarrierEnvironment(os.Environ()); len(filtered) > 0 {
		log.Printf("hostel: filtered reserved carrier environment from bed processes: keys=%v", filtered)
	}

	networks := network.New(context.Background())
	mgr.SetNetworkManager(networks)
	resources := resource.New()
	mgr.SetResourceTracker(resources)
	admissionCtx, stopAdmission := context.WithCancel(context.Background())
	defer stopAdmission()
	resourceAdmission, err := resource.NewAdmission(admissionCtx, resource.NewCarrier(), resource.AdmissionConfig{
		CPUThresholdPercent:    cfg.AdmissionCPUThreshold,
		MemoryThresholdPercent: cfg.AdmissionMemoryThreshold,
	})
	if err != nil {
		log.Fatalf("hostel: configure resource admission: %v", err)
	}
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

	if err := amenities.Start(context.Background()); err != nil {
		log.Fatalf("hostel: start amenities: %v", err)
	}
	if err := mgr.Start(context.Background()); err != nil {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = amenities.Close(cleanup)
		cancel()
		log.Fatalf("hostel: start bed manager: %v", err)
	}
	resourceReport := resources.Report()
	if resourceReport.Available {
		log.Printf("hostel: per-bed resource accounting enabled (backend=%s)", resourceReport.Backend)
	} else {
		log.Printf("hostel: per-bed resource accounting unavailable (backend=%s reason=%s)",
			resourceReport.Backend, resourceReport.Reason)
	}
	if err := mgr.RetryLocalCleanups(context.Background()); err != nil {
		log.Printf("hostel: startup local cleanup pending: %v", err)
	}

	// Individual backend probes cannot prove that privilege ordering composes.
	probeCtx, cancelEnvironmentProbe := context.WithTimeout(context.Background(), 30*time.Second)
	environmentErr := mgr.ProbeEnvironment(probeCtx)
	cancelEnvironmentProbe()
	if environmentErr != nil {
		closeCtx, cancelClose := context.WithTimeout(context.Background(), 10*time.Second)
		_ = mgr.Close(closeCtx)
		cancelClose()
		log.Fatalf("hostel: execution environment unavailable: %v", environmentErr)
	}
	userReport := mgr.BedUserReport()
	log.Printf("hostel: execution environment verified (file=%s network=%s executor=%s bed_user_strategy=%s bed_uid=%d bed_gid=%d bed_uid_min=%d bed_uid_max=%d)",
		iso.Name(), networks.Status().Backend, mgr.ExecutorBackend(), userReport.Strategy,
		userReport.UID, userReport.GID, userReport.UIDMin, userReport.UIDMax)

	// Carrier pressure gates tenant work, not the startup capability probe.
	mgr.SetResourceAdmission(resourceAdmission)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The composite owns loop startup, cancellation and join. Domain managers
	// retain their own cadence and retries; main only drives daemon lifetime.
	mgr.SetPersistInterval(cfg.PersistInterval)
	workerDone := make(chan error, 1)
	go func() { workerDone <- mgr.Run(ctx) }()

	srv := &http.Server{Addr: cfg.Addr, Handler: web.NewServer(
		mgr,
		web.WithTracing(cfg.EnableTracing),
		web.WithDormReadFallbackRoot(cfg.DormReadFallbackRoot),
	).Handler()}
	serverDone := make(chan error, 1)
	go func() {
		log.Printf("hostel: listening on %s (isolation=%s, workspace-root=%s, default-bed=%s)",
			cfg.Addr, iso.Name(), cfg.WorkspaceRoot, cfg.DefaultBed)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverDone <- err
		}
	}()

	select {
	case <-ctx.Done():
	case err := <-workerDone:
		if err != nil {
			log.Printf("hostel: background controller failed: %v", err)
		}
	case err := <-serverDone:
		log.Printf("hostel: server failed: %v", err)
	}
	stop()
	log.Printf("hostel: shutting down")
	httpShutdownCtx, cancelHTTPShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	_ = srv.Shutdown(httpShutdownCtx)
	cancelHTTPShutdown()
	executorShutdownCtx, cancelExecutorShutdown := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelExecutorShutdown()
	if err := mgr.Close(executorShutdownCtx); err != nil {
		log.Printf("hostel: bed manager shutdown: %v", err)
	}
	if err := amenities.Close(executorShutdownCtx); err != nil {
		log.Printf("hostel: amenity shutdown: %v", err)
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
