//go:build e2e

package e2e_test

import (
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
	"github.com/qiankunli/hostel/protocol"
)

// +case:id=bed_read_only_process_root,desc=`Create a Bed requesting a read-only root`,expect=`All Pillows retain writable data mounts or initialization reports an unsupported root`
// +link:doc=docs/volumes.md
func TestBedReadOnlyRootFilesystem(t *testing.T) {
	requireTestBinary(t)
	for _, mounted := range []bool{false, true} {
		backend := "carrier"
		if mounted {
			backend = "mount"
		}
		t.Run(backend, func(t *testing.T) {
			options := restrictedOptions()
			if mounted {
				if runtime.GOOS != "linux" {
					t.Skip("mount namespace requires Linux")
				}
				if _, err := exec.LookPath("bwrap"); err != nil {
					t.Skip("bwrap unavailable")
				}
				options.Bed.Filesystem.Bwrap = value(tool.Required)
			}
			c := startTarget(t, targetOptions{config: &options}).client
			const id = "read-only-root"
			req := map[string]any{"id": id, "securityContext": protocol.SecurityContext{ReadOnlyRootFilesystem: true}, "pillows": workspacePillows()}
			response, err := c.json(t.Context(), http.MethodPost, "/v1/beds", "", req, nil)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "create read-only Bed", response)
			final := c.waitBed(t, id, func(b bedView) bool { return b.Status.Phase == "resident" || b.Status.Phase == "failed" }, "terminal initialization")
			if !mounted {
				if final.Status.Phase != "failed" || final.Status.Readiness.Reason != "BedFSPreparationFailed" || !strings.Contains(final.Status.Readiness.Message, "readOnlyRootFilesystem") {
					t.Fatalf("unexpected failure: %+v", final.Status)
				}
				return
			}
			if final.Status.Phase != "resident" {
				t.Fatalf("mount preparation failed: %+v", final.Status)
			}
			var detail protocol.BedDetail
			response, err = c.json(t.Context(), http.MethodGet, "/v1/beds/"+id, "", nil, &detail)
			if err != nil {
				t.Fatal(err)
			}
			must2xx(t, "inspect read-only Bed", response)
			fs := detail.Status.Subsystems.Filesystem
			if !fs.ReadOnlyRootRequested || fs.Realization == nil || len(fs.Realization.Views) != 1 || !fs.Realization.Views[0].ReadOnlyRootFilesystem {
				t.Fatalf("read-only realization missing: %+v", fs)
			}
			result, response := c.command(t, id, map[string]any{"command": `set -eu
test ! -w /
test ! -w /usr/local
test -w /workspace
test -w /tmp
touch /workspace/root-policy /tmp/root-policy
if mkdir /unexpected-write; then exit 1; fi`})
			must2xx(t, "check read-only root", response)
			assertCommandExit(t, result, 0)
			delete(req, "securityContext")
			response, err = c.json(t.Context(), http.MethodPost, "/v1/beds", "", req, nil)
			if err != nil || response.Status != http.StatusConflict {
				t.Fatalf("conflicting root policy: %v HTTP %d", err, response.Status)
			}
		})
	}
}

// +case:id=bed_explicit_shared_install,desc=`Two ordinary Beds declare the same software volume`,expect=`An installed tool is visible in both Beds while workspace files stay private`
func TestBedExplicitSharedSoftwareVolume(t *testing.T) {
	requireTestBinary(t)
	if runtime.GOOS != "linux" {
		t.Skip("process mount requires Linux")
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		t.Skip("bwrap unavailable")
	}
	cfg := restrictedOptions()
	cfg.Bed.Filesystem.Bwrap = value(tool.Required)
	source := t.TempDir()
	if err := os.Chmod(source, 0777); err != nil {
		t.Fatal(err)
	}
	c := startTarget(t, targetOptions{config: &cfg}).client
	for _, id := range []string{"installer", "consumer"} {
		volumes := []protocol.VolumeSpec{{Name: "workspace", Managed: &protocol.ManagedVolume{Persistence: "none"}}, {Name: "software", HostPath: &protocol.HostPathVolume{Path: source}}}
		pillows := []protocol.PillowSpec{{Name: "main", Mounts: []protocol.VolumeMount{{Volume: "workspace", Path: "/workspace"}, {Volume: "software", Path: "/mnt/software"}}}}
		response, err := c.json(t.Context(), "POST", "/v1/beds", "", map[string]any{"id": id, "volumes": volumes, "pillows": pillows}, nil)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "create ordinary Bed", response)
		c.waitBed(t, id, func(b bedView) bool { return b.Status.Phase == "resident" }, "ordinary Bed ready")
	}
	result, response := c.command(t, "installer", map[string]any{"command": `set -eu
printf '#!/bin/sh\nprintf "shared-tool\\n"\n' > /mnt/software/probe
chmod 755 /mnt/software/probe
touch /workspace/private`})
	must2xx(t, "install shared tool", response)
	assertCommandExit(t, result, 0)
	result, response = c.command(t, "consumer", map[string]any{"command": `set -eu
test ! -e /workspace/private
test "$(/mnt/software/probe)" = shared-tool`})
	must2xx(t, "use shared tool", response)
	assertCommandExit(t, result, 0)
}
