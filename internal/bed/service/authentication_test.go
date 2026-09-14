package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

func TestUnavailableTokenFilePreventsProcessStart(t *testing.T) {
	for _, mode := range []string{"missing", "empty"} {
		t.Run(mode, func(t *testing.T) {
			m, ports := httpTestManager(t)
			path := filepath.Join(t.TempDir(), "token")
			if mode == "empty" {
				if err := os.WriteFile(path, []byte("\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			b := bed.New("token-file", "", bed.Spec{Services: []bed.ServiceSpec{{
				Name: "web", Command: []string{"server"}, Required: true,
				EnvFiles: map[string]string{"TOKEN": path},
				HTTP:     &bed.ServiceHTTPSpec{ReadyPath: "/ready", Authentication: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceEnvironment, TokenEnv: "TOKEN"}},
			}}})
			runtime := &fakeRuntime{}
			if err := m.PrepareBed(t.Context(), b, runtime); err == nil {
				t.Fatal("missing credential accepted")
			}
			runtime.mu.Lock()
			started := len(runtime.processes)
			runtime.mu.Unlock()
			if started != 0 || len(ports.Status()) != 0 {
				t.Fatal("credential failure started a process or reserved a port")
			}
			status := m.Status(b)[0]
			if status.Phase != "failed" || !strings.Contains(status.Reason, "TOKEN") {
				t.Fatalf("missing failure context: %+v", status)
			}
			if _, err := m.Access(b, "web"); err == nil {
				t.Fatal("credential failure published access")
			}
		})
	}
}
