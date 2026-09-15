//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/url"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/privilege"
)

func TestPreferredBedUser(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Skip("Linux process credential contract")
	}
	for _, backend := range []string{"local", "supervisor"} {
		t.Run(backend, func(t *testing.T) {
			options := restrictedOptions()
			options.Bed.Privilege = privilege.Options{UID: value(1200), GID: value(1300)}
			c := startTarget(t, targetOptions{isolation: "dorm", executor: backend, config: &options}).client
			var status struct {
				Components struct{ Privilege privilege.Status }
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			response, err := c.json(ctx, "GET", "/v1/status", "", nil, &status)
			cancel()
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "identity status", response)
			p := status.Components.Privilege
			if p.BedUser.Strategy != "fixed" || !p.PreconditionsSatisfied {
				t.Fatalf("invalid identity status: %+v", p)
			}
			if p.BedUser.UID != 1200 || p.BedUser.GID != 1300 {
				if p.BedUser.UID != p.Daemon.UID || p.BedUser.GID != p.Daemon.GID || !strings.Contains(p.Selection.Reason, "using daemon identity") {
					t.Fatalf("unexplained identity fallback: %+v", p)
				}
			}
			t.Logf("preferred=1200:1300 actual=%d:%d reason=%s", p.BedUser.UID, p.BedUser.GID, p.Selection.Reason)
			const bed = "preferred-user"
			must2xx(t, "upload as selected user", c.upload(t, bed, "/workspace/seed.txt", []byte("seed")))
			command := "test \"$(cat seed.txt)\" = seed && printf identity-ok > identity.txt && id -u && id -g && grep -E '^(CapInh|CapPrm|CapEff|CapBnd|CapAmb|NoNewPrivs):' /proc/self/status"
			check := func(result commandResult) {
				t.Helper()
				assertCommandExit(t, result, 0)
				lines := strings.Split(strings.TrimSpace(result.Stdout), "\n")
				if len(lines) != 8 || lines[0] != fmt.Sprint(p.BedUser.UID) || lines[1] != fmt.Sprint(p.BedUser.GID) {
					t.Fatalf("process identity disagrees with status: %q", result.Stdout)
				}
				for _, line := range lines[2:] {
					fields := strings.Fields(line)
					if len(fields) != 2 {
						t.Fatalf("invalid process status: %q", line)
					}
					if fields[0] == "CapBnd:" && p.BedUser.UID != 0 {
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
