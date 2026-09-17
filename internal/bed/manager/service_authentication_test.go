package manager

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/service"
)

func TestBedServiceOptionalAuthentication(t *testing.T) {
	for _, mode := range []string{"none", "environment", "file"} {
		t.Run(mode, func(t *testing.T) {
			m, specs, _ := testServiceManager(t)
			spec := &specs[0]
			spec.HTTP.Authentication = nil
			token := ""
			credential := filepath.Join(t.TempDir(), "token")
			if mode != "none" {
				// Credentials are opaque: address placeholders must survive unchanged.
				token = "credential-${PORT}-${LISTEN_ADDR}"
				spec.HTTP.Authentication = &model.Authentication{Scheme: "bearer", TokenSource: model.TokenSourceEnvironment, TokenEnv: "SERVICE_TOKEN"}
				if mode == "environment" {
					spec.Env["SERVICE_TOKEN"] = token
				} else {
					if err := os.WriteFile(credential, []byte(token+"\n"), 0600); err != nil {
						t.Fatal(err)
					}
					spec.EnvFiles["SERVICE_TOKEN"] = credential
				}
			}
			if _, err := m.InitializeBedWithOptions(t.Context(), "authentication", testServiceOptions(specs[:1])); err != nil {
				t.Fatal(err)
			}
			b, err := m.Ensure(t.Context(), "authentication")
			if err != nil {
				t.Fatal(err)
			}
			first, err := m.services.Access(b.Bed, "main")
			if err != nil {
				t.Fatal(err)
			}
			if first.Token != token {
				t.Fatal("access credential differs from configured credential")
			}
			encoded, err := json.Marshal(first)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "none" && strings.Contains(string(encoded), `"token"`) {
				t.Fatal("unauthenticated access includes token")
			}
			if mode == "file" {
				for _, body := range [][]byte{mustReadIdentity(t, m, b.Name), mustMarshalServiceStatus(t, m, b)} {
					if strings.Contains(string(body), token) {
						t.Fatal("credential leaked to metadata/status")
					}
				}
				if err := os.WriteFile(credential, []byte("rotated-token\n"), 0600); err != nil {
					t.Fatal(err)
				}
				// Changing the file must not change credentials for a running execution.
				current, err := m.services.Access(b.Bed, "main")
				if err != nil || current.Token != token {
					t.Fatal("access read credentials ahead of process restart")
				}
			}
			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, testServiceURL(first.PortMapping.HostPort)+"/ready", nil)
			if err != nil {
				t.Fatal(err)
			}
			if token != "" {
				req.Header.Set("Authorization", "Bearer "+token)
			}
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("direct access status=%d", resp.StatusCode)
			}
			if err := m.services.Restart(b.Bed, "main"); err != nil {
				t.Fatal(err)
			}
			waitService(t, m, b, "main", func(s service.Status) bool {
				return s.Phase == "running" && s.Ready && s.ExecutionID != first.PortMapping.ExecutionID
			})
			current, err := m.services.Access(b.Bed, "main")
			if err != nil {
				t.Fatal(err)
			}
			if mode == "file" {
				token = "rotated-token"
			}
			if current.Token != token {
				t.Fatal("restarted process and access credentials differ")
			}
		})
	}
}

func mustReadIdentity(t *testing.T, m *Manager, name string) []byte {
	t.Helper()
	body, err := os.ReadFile(m.identityPath(name))
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func mustMarshalServiceStatus(t *testing.T, m *Manager, b *Resident) []byte {
	t.Helper()
	body, err := json.Marshal(m.services.Status(b.Bed))
	if err != nil {
		t.Fatal(err)
	}
	return body
}
