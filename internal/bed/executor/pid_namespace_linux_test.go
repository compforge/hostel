//go:build linux

package executor

import (
	"os"
	"os/exec"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
	"golang.org/x/sys/unix"
)

func TestPIDNamespaceUnavailablePolicies(t *testing.T) {
	if os.Getenv("HOSTEL_TEST_PID_NAMESPACE") != "1" {
		t.Skip("requires privileged Linux policy runner")
	}
	if os.Getenv("HOSTEL_TEST_PIDNS_DENIED") != "1" {
		setpriv, err := exec.LookPath("setpriv")
		if err != nil {
			t.Fatal("setpriv is required for actual capability denial")
		}
		cmd := exec.Command(setpriv, "--bounding-set=-sys_admin", os.Args[0], "-test.run=^TestPIDNamespaceUnavailablePolicies$", "-test.v")
		cmd.Env = append(os.Environ(), "HOSTEL_TEST_PIDNS_DENIED=1")
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("capability-denied child: %v\n%s", err, output)
		}
		t.Log(string(output))
		return
	}
	if hostfacts.Collect().HasCap(unix.CAP_SYS_ADMIN) {
		t.Fatal("namespace capability denial was not established")
	}
	for _, policy := range []tool.Policy{tool.Auto, tool.Off, tool.Required} {
		factory, err := ResolveFactory(t.Context(), Config{Backend: "supervisor", PIDNS: policy}, nil)
		if policy == tool.Required {
			if err == nil {
				factory.Close()
				t.Fatal("required accepted unavailable PID namespace")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		status := factory.Status().Tools["pidns"]
		factory.Close()
		wantProbe := "unavailable"
		if policy == tool.Off {
			wantProbe = "not_probed"
		}
		if status.Policy != policy || status.Probe != wantProbe || status.Selected || status.Reason == "" {
			t.Fatalf("policy %s evidence: %+v", policy, status)
		}
	}
	// Losing privileges after selection cannot silently weaken a live factory.
	cfg := (Config{Backend: "supervisor", PIDNS: tool.Auto}).WithSelection(map[string]tool.Status{"pidns": pidnsStatus(tool.Auto, true, true, "")})
	if factory, err := ResolveFactory(t.Context(), cfg, nil); err == nil {
		factory.Close()
		t.Fatal("frozen private selection downgraded")
	}
}
