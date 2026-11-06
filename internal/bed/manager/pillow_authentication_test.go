package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/pillow"
)

// +case:id=pillow_application_auth,desc=`A process receives an opaque token through Carrier env, explicit env or a file`,expect=`The process enforces authentication; readiness and access neither discover nor return credentials`
func TestPillowApplicationOwnsAuthentication(t *testing.T) {
	for _, mode := range []string{"carrier", "environment", "file"} {
		t.Run(mode, func(t *testing.T) {
			m, specs, _ := testPillowManager(t)
			token := "credential-${PORT}-${LISTEN_ADDR}"
			spec := &specs[0]
			credential := filepath.Join(t.TempDir(), "token")
			switch mode {
			case "carrier":
				m.SetCarrierEnvironment(append(os.Environ(), "SERVICE_TOKEN="+token))
			case "environment":
				spec.Env["SERVICE_TOKEN"] = token
			case "file":
				if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
					t.Fatal(err)
				}
				spec.EnvFiles["SERVICE_TOKEN"] = credential
			}
			if _, err := m.InitializeBed(t.Context(), "auth", testPillowOptions(specs[:1])); err != nil {
				t.Fatal(err)
			}
			b, err := m.Ensure(t.Context(), "auth")
			if err != nil {
				t.Fatal(err)
			}
			access, err := m.pillows.Access(b.Bed, "main")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(access)
			if strings.Contains(string(body), token) || strings.Contains(string(body), `"token"`) {
				t.Fatal("access discovered business credentials")
			}
			check := func(port int, credential string, want int) {
				t.Helper()
				req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, testPillowURL(port)+"/private", nil)
				if credential != "" {
					req.Header.Set("Authorization", "Bearer "+credential)
				}
				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				resp.Body.Close()
				if resp.StatusCode != want {
					t.Fatalf("business response=%d, want %d", resp.StatusCode, want)
				}
			}
			check(access.Ports[0].HostPort, "", http.StatusUnauthorized)
			check(access.Ports[0].HostPort, token, http.StatusOK)
			if mode == "file" {
				identity, _ := os.ReadFile(m.identityPath(b.Name))
				if strings.Contains(string(identity), token) {
					t.Fatal("resolved value leaked to identity")
				}
				if err := os.WriteFile(credential, []byte("rotated-token\n"), 0600); err != nil {
					t.Fatal(err)
				}
				check(access.Ports[0].HostPort, token, http.StatusOK)
			}
			if err := m.pillows.Restart(b.Bed, "main"); err != nil {
				t.Fatal(err)
			}
			waitPillow(t, m, b, "main", func(s pillow.Status) bool { return s.Ready && s.ExecutionID != access.Ports[0].ExecutionID })
			next, err := m.pillows.Access(b.Bed, "main")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "file" {
				token = "rotated-token"
			}
			check(next.Ports[0].HostPort, token, http.StatusOK)
		})
	}
}
