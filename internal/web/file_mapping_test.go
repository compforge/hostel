package web

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	"github.com/qiankunli/hostel/internal/feature"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"
)

func TestCarrierMappingAPIAndCommandPaths(t *testing.T) {
	root := t.TempDir()
	facts := hostfacts.Collect()
	iso, err := isolation.Resolve(facts, isolation.Config{Level: "shared", PRoot: feature.Off, Pathshim: feature.Off}, root)
	if err != nil {
		t.Fatal(err)
	}
	mgr, err := bed.NewManager(facts, root, "default", "/bin/bash", iso, nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := mgr.Close(ctx); err != nil {
			t.Error(err)
		}
	})
	s := NewServer(mgr)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "input.txt"), []byte("shared-source"), 0644); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two"} {
		body, _ := json.Marshal(map[string]any{
			"id":            name,
			"path_mappings": []map[string]any{{"host_path": source, "bed_path": "/project"}},
		})
		headers := map[string]string{"Content-Type": "application/json", "X-Hostel-Bed": name}
		rec := do(t, s, http.MethodPost, "/v1/beds", strings.NewReader(string(body)), headers)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("create=%d %s", rec.Code, rec.Body.String())
		}
		waitBedReady(t, s, name)
		read := func(path, want string) {
			t.Helper()
			rec := do(t, s, http.MethodGet, "/files/download?path="+path, nil, headers)
			if rec.Code != http.StatusOK || rec.Body.String() != want {
				t.Fatalf("%s read %s=%d %q want=%q", name, path, rec.Code, rec.Body.String(), want)
			}
		}
		read("/project/input.txt", "shared-source")
		// Relative paths under the Bed root can produce local artifacts even
		// when absolute /project isn't redirected in the process namespace.
		command, _ := json.Marshal(map[string]string{"command": "printf " + name + " > project/input.txt", "cwd": "/"})
		rec = do(t, s, http.MethodPost, "/command", strings.NewReader(string(command)), headers)
		if rec.Code != http.StatusOK {
			t.Fatalf("command=%d %s", rec.Code, rec.Body.String())
		}
		read("/project/input.txt", name)
		// Structured cwd can still reach a declared source by its carrier path.
		command, _ = json.Marshal(map[string]string{"command": "printf mapped > from-cwd.txt", "cwd": "/project"})
		do(t, s, http.MethodPost, "/command", strings.NewReader(string(command)), headers)
		read("/project/from-cwd.txt", "mapped")
		// Default workdir and API relative paths continue to agree.
		do(t, s, http.MethodPost, "/command", strings.NewReader(`{"command":"printf workdir > relative.txt"}`), headers)
		read("relative.txt", "workdir")
	}
	for _, name := range []string{"one", "two"} {
		rec := do(t, s, http.MethodGet, "/files/download?path=/project/input.txt", nil, map[string]string{"X-Hostel-Bed": name})
		if rec.Body.String() != name {
			t.Fatalf("Bed-local paths leaked: %s=%q", name, rec.Body.String())
		}
	}
	if got, _ := os.ReadFile(filepath.Join(source, "input.txt")); string(got) != "shared-source" {
		t.Fatalf("local write changed source: %q", got)
	}
}
