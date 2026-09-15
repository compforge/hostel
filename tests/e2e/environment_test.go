//go:build e2e

package e2e_test

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/quality-harness/sdks/go/e2e/caserun"
	"github.com/compforge/quality-harness/sdks/go/e2e/core"
	"github.com/compforge/quality-harness/sdks/go/e2e/environment"
	"github.com/compforge/quality-harness/sdks/go/e2e/matrix"
	"github.com/compforge/quality-harness/sdks/go/e2e/testrun"
	hostops "github.com/compforge/quality-harness/sdks/go/toolbox/host"
	hostfs "github.com/qiankunli/hostel/internal/host/filesystem"
)

// TestMain keeps the existing suite and testing.T cleanup ownership. The
// environment adapter surrounds m.Run so fixture cleanup completes before the
// suite result is recorded. Network probe subprocesses are not separate suites.
func TestMain(m *testing.M) {
	path := os.Getenv("HOSTEL_E2E_ENVIRONMENT_FILE")
	if path == "" || os.Getenv("E2E_NETWORK_PROBE") != "" {
		os.Exit(m.Run())
	}
	flag.Parse()
	if flag.Lookup("test.list").Value.String() != "" {
		os.Exit(m.Run())
	}
	if _, err := os.Stat(path); err != nil {
		fmt.Fprintln(os.Stderr, "load E2E environment:", err)
		os.Exit(1)
	}
	cfg, err := core.LoadConfig(path)
	if err != nil {
		fmt.Fprintln(os.Stderr, "load E2E environment:", err)
		os.Exit(1)
	}
	os.Exit(runInEnvironment(cfg, m.Run))
}

// runInEnvironment takes the environment as data; the selected runner must
// already be on the target Host or in the target Pod. Deployment/SSH launchers
// consume Environment.Host outside this function.
func runInEnvironment(cfg *core.E2EConfig, run func() int) int {
	env := cfg.Service.Environment
	snapshot := common.EnvironmentSnapshot{Name: env.Name, Kind: env.ResolvedKind(), Profile: cfg.Profile, Revision: os.Getenv("HOSTEL_E2E_REVISION")}
	if env.Host != nil {
		snapshot.HostName = env.Host.Name
	}
	state := environment.State[int]{Environment: snapshot}
	required := map[string]string{}
	for key, value := range cfg.Custom {
		if strings.HasPrefix(key, "require.") {
			required[strings.TrimPrefix(key, "require.")] = value
		}
	}
	selection := flag.Lookup("test.run").Value.String()
	if selection == "" {
		selection = "all"
	}
	// A passing filtered run must remain distinguishable from the full suite.
	variant := matrix.Variant{"test_filter": selection}
	result := environment.Run(context.Background(), caserun.Ref("hostel-runtime", "suite"), variant, &state, required,
		caserun.Definition[environment.State[int]]{
			Prepare: func(ctx context.Context, s *environment.State[int]) error {
				if os.Getenv(binaryEnv) == "" || os.Getenv(imageEnv) != "" {
					return fmt.Errorf("environment suite requires a binary colocated with the runner; use the same Pod for Kubernetes")
				}
				if (env.Host != nil && env.Host.ResolvedTransport() == "ssh") || env.ResolvedKind() == "kubernetes" {
					if os.Getenv("HOSTEL_E2E_ON_TARGET") != "1" {
						return fmt.Errorf("remote environment requires an on-target runner (HOSTEL_E2E_ON_TARGET=1)")
					}
				}
				facts, err := hostops.ObserveLocal(ctx)
				s.Environment.Runner = facts
				s.Environment.Target = facts
				s.Environment.Target.Source = "colocated-binary:" + facts.Source
				if err != nil {
					return err
				}
				// These are carrier process observations. They do not claim that
				// every Bed has the same effective capabilities or filesystem view.
				observeProcessConditions(&s.Environment.Target)
				if env.ResolvedKind() == "kubernetes" {
					if os.Getenv("HOSTEL_E2E_POD_UID") == "" {
						return fmt.Errorf("Kubernetes runner requires HOSTEL_E2E_POD_UID from the downward API")
					}
					s.Environment.Target.Values["pod_uid"] = os.Getenv("HOSTEL_E2E_POD_UID")
				} else {
					hostFacts := s.Environment.Target
					s.Environment.Host = &hostFacts
				}
				return nil
			},
			Execute: func(_ context.Context, s *environment.State[int]) error {
				s.Value = run()
				if s.Value != 0 {
					return caserun.Fail(fmt.Sprintf("native go test suite exited %d; see test output", s.Value))
				}
				return nil
			},
			// m.Run has completed testing.T cleanups before returning. No
			// background process is owned by this adapter.
			Budgets: caserun.Budgets{Prepare: 30 * time.Second, Execute: 30 * time.Minute},
		})
	options := []testrun.Option{}
	if path := os.Getenv("HOSTEL_E2E_RUNS_DIR"); path != "" {
		options = append(options, testrun.WithRunsDir(path))
	}
	evidence := testrun.New("hostel-runtime", options...)
	evidence.Record(result)
	// Pod files disappear with the Job. Keep the same environment evidence in
	// the suite log so operators can collect it before removing the workload.
	if data, err := json.Marshal(result.Environment); err == nil {
		fmt.Fprintln(os.Stderr, "E2E_ENVIRONMENT_JSON", string(data))
	}
	return evidence.Main(func() int {
		if result.Status != "pass" {
			fmt.Fprintln(os.Stderr, "E2E environment:", result.Reason)
			if state.Value != 0 {
				return state.Value
			}
			return 1
		}
		return 0
	})
}

func observeProcessConditions(facts *common.EnvironmentFacts) {
	if runtime.GOOS != "linux" {
		return
	}
	probe := hostfs.ProbePtrace()
	switch {
	case probe.Succeeded():
		facts.Values["ptrace_probe"] = "passed"
	case probe.Attempted:
		facts.Values["ptrace_probe"] = "failed"
	default:
		facts.Values["ptrace_probe"] = "unavailable"
	}
	if probe.Error != "" {
		facts.Values["ptrace_probe_error"] = probe.Error
	}
	if data, err := os.ReadFile("/proc/self/status"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			key, value, ok := strings.Cut(line, ":")
			if ok {
				switch key {
				case "Seccomp", "NoNewPrivs", "CapEff", "CapBnd":
					facts.Values[key] = strings.TrimSpace(value)
				}
			}
		}
	}
	if data, err := os.ReadFile("/proc/self/attr/apparmor/current"); err == nil {
		facts.Values["apparmor"] = strings.TrimSpace(string(data))
	}
}
