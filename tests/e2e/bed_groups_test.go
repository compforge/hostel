//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/protocol"
	"net/url"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func TestBedSoftwareGroup(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		t.Skip("Linux process credential contract")
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			options := restrictedOptions()
			c := startTarget(t, targetOptions{executor: backend, config: &options}).client
			var status struct {
				Subsystems struct{ Privilege privilege.Status }
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			response, err := c.json(ctx, "GET", "/v1/status", "", nil, &status)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "identity status", response)
			p := status.Subsystems.Privilege
			if p.BedUser.Strategy != "per_bed" || !p.PreconditionsSatisfied {
				t.Fatalf("invalid identity status: %+v", p)
			}
			const bed = "extra-groups"
			must2xx(t, "declare Bed", c.createBed(t, bed))
			c.waitBed(t, bed, func(b bedView) bool { return b.Status.Readiness.Ready }, "identity ready")
			var detail protocol.BedDetail
			ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
			response, err = c.json(ctx, "GET", "/v1/beds/"+bed, "", nil, &detail)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "Bed identity", response)
			identity := detail.Status.Subsystems.Privilege
			if identity.UID != identity.GID || identity.UID < privilege.UIDMin || len(identity.SupplementaryGIDs) != 1 || identity.SupplementaryGIDs[0] != privilege.SoftwareGID {
				t.Fatalf("identity: %+v", identity)
			}

			must2xx(t, "upload as selected user", c.upload(t, bed, "/workspace/seed.txt", []byte("seed")))
			command := "test \"$(cat seed.txt)\" = seed && /usr/bin/awk '/^Groups:/ { if (NF != 2 || $2 != SOFTWARE_GID) exit 1 }' /proc/self/status && printf identity-ok > identity.txt && id -u && id -g && grep -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' /proc/self/status"
			command = strings.ReplaceAll(command, "SOFTWARE_GID", fmt.Sprint(privilege.SoftwareGID))
			check := func(result commandResult) {
				t.Helper()
				assertCommandExit(t, result, 0)
				lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
				if len(lines) != 8 || lines[0] != fmt.Sprint(identity.UID) || lines[1] != fmt.Sprint(identity.GID) {
					t.Fatalf("process identity disagrees with status: %q", result.Stdout)
				}
				for _, line := range lines[2:] {
					fields := strings.Fields(line)
					if len(fields) != 2 {
						t.Fatalf("invalid process status: %q", line)
					}
					if fields[0] == "CapBnd:" && identity.UID != 0 {
						continue
					}
					want := "0000000000000000"
					if fields[0] == "NoNewPrivs:" {
						want = "1"
					}
					if fields[1] != want {
						t.Fatalf("unsafe child credentials: %s", line)
					}
				}
			}
			result, response := c.command(t, bed, map[string]any{"command": command, "timeout": 5000})
			must2xx(t, "command identity", response)
			check(result)
			var session struct {
				SessionID string `json:"session_id"`
			}
			ctx, cancel = context.WithTimeout(t.Context(), 5*time.Second)
			response, err = c.json(ctx, "POST", "/session", bed, map[string]string{"cwd": "/workspace"}, &session)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create identity session", response)
			if session.SessionID == "" {
				t.Fatal("missing session id")
			}
			result, response = c.stream(t, "/session/"+url.PathEscape(session.SessionID)+"/run", bed, map[string]any{"command": command, "timeout": 5000})
			must2xx(t, "session identity", response)
			check(result)
			download := c.download(t, bed, "/workspace/identity.txt")
			must2xx(t, "read selected user's file", download)
			if string(download.Body) != "identity-ok" {
				t.Fatalf("file round trip: %q", download.Body)
			}
		})
	}
}
