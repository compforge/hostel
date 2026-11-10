//go:build e2e

package e2e_test

import (
	"context"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// +case:id=daemon_file_api_scope,desc=`Two Beds use native and Execd file APIs with daemon authority`,expect=`private paths remain independent; implicit Carrier, neighboring Bed and escaping symlink reads fail; explicit mounts remain available`
func TestDaemonFileAPIScope(t *testing.T) {
	requireTestBinary(t)
	backends := []string{"local"}
	if runtime.GOOS == "linux" {
		backends = append(backends, "supervisor")
	}
	for _, backend := range backends {
		t.Run(backend, func(t *testing.T) {
			options := restrictedOptions()
			target := startTarget(t, targetOptions{executor: backend, config: &options})
			c := target.client
			for _, name := range []string{"scope-a", "scope-b"} {
				must2xx(t, "create Bed", c.createBed(t, name))
				must2xx(t, "write private file", c.upload(t, name, "/workspace/marker", []byte(name)))
				result, response := c.command(t, name, map[string]any{"command": "printf '' >> marker", "timeout": 5000})
				must2xx(t, "write API-created file from Bed", response)
				assertCommandExit(t, result, 0)
			}
			// Injection only creates the escaping reference; all assertions use public APIs.
			secret := filepath.Join(target.bedsRoot, "scope-b", "volumes", "workspace", "marker")
			link := filepath.Join(target.bedsRoot, "scope-a", "volumes", "workspace", "escape")
			if err := os.Symlink(secret, link); err != nil {
				t.Fatal(err)
			}
			fifo := filepath.Join(target.bedsRoot, "scope-a", "volumes", "workspace", "fifo")
			if err := unix.Mkfifo(fifo, 0600); err != nil {
				t.Fatal(err)
			}
			for _, endpoint := range []string{"", "/execd", "/v1/beds/scope-a/execd"} {
				read := func(path string) httpResult {
					t.Helper()
					ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
					defer cancel()
					response, err := c.request(ctx, "GET", endpoint+"/files/download?path="+url.QueryEscape(path), "scope-a", nil, "")
					if err != nil {
						t.Fatal(err)
					}
					return response
				}
				own := read("/workspace/marker")
				must2xx(t, "read own data", own)
				if string(own.Body) != "scope-a" {
					t.Fatalf("wrong Bed data: %q", own.Body)
				}
				for _, path := range []string{"/etc/hosts", secret, "/workspace/escape", "/workspace/fifo"} {
					if response := read(path); response.Status < 400 {
						t.Fatalf("%s exposed %s: %s", endpoint, path, response.Body)
					}
				}
			}
			if response := c.upload(t, "scope-a", "/workspace/escape", []byte("overwrite")); response.Status < 400 {
				t.Fatalf("escaped symlink accepted upload: %s", response.Body)
			}
			neighbor := c.download(t, "scope-b", "/workspace/marker")
			must2xx(t, "read neighboring Bed", neighbor)
			if string(neighbor.Body) != "scope-b" {
				t.Fatalf("escaped upload changed neighboring data: %q", neighbor.Body)
			}
			// A system-looking client path belongs to this Bed unless explicitly mounted.
			logical := "/usr/local/hostel-file-api-marker"
			must2xx(t, "write Bed-local system path", c.upload(t, "scope-a", logical, []byte("private")))
			if got := c.download(t, "scope-b", logical); got.Status != http.StatusNotFound {
				t.Fatalf("cross-Bed system path: %d %s", got.Status, got.Body)
			}
			if got, err := os.ReadFile(filepath.Join(target.bedsRoot, "scope-a", "rootfs", logical)); err != nil || string(got) != "private" {
				t.Fatalf("private root projection: %q %v", got, err)
			}
			for _, name := range []string{"scope-a", "scope-b"} {
				response, err := c.json(t.Context(), "DELETE", "/v1/beds/"+name+"?purge=true", "", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				must2xx(t, "purge Bed", response)
			}
		})
	}
}

// +case:id=file_request_lifecycle,desc=`An upload is held open while eviction is requested`,expect=`the active request prevents eviction; completion releases the operation and the next eviction succeeds`
func TestFileAPIUploadHoldsBed(t *testing.T) {
	requireTestBinary(t)
	options := restrictedOptions()
	c := startTarget(t, targetOptions{executor: "local", config: &options}).client
	must2xx(t, "create upload Bed", c.createBed(t, "upload-hold"))
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	form := multipart.NewWriter(writer)
	type outcome struct {
		response httpResult
		err      error
	}
	done := make(chan outcome, 1)
	go func() {
		r, err := c.request(ctx, "POST", "/files/upload", "upload-hold", reader, form.FormDataContentType())
		done <- outcome{r, err}
	}()
	if err := form.WriteField("metadata", `{"path":"/workspace/upload"}`); err != nil {
		t.Fatal(err)
	}
	part, err := form.CreateFormFile("file", "payload")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("retained")); err != nil {
		t.Fatal(err)
	}
	c.waitInventory(t, "upload operation active", func(got inventoryView) bool { return bedMatches(got, "upload-hold", "resident", "active") })
	busy, err := c.json(ctx, "DELETE", "/v1/beds/upload-hold", "", nil, nil)
	if err != nil || busy.Status != http.StatusConflict {
		t.Fatalf("evict uploading Bed: status=%d err=%v body=%s", busy.Status, err, busy.Body)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		must2xx(t, "finish upload", result.response)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	downloaded := c.download(t, "upload-hold", "/workspace/upload")
	must2xx(t, "read completed upload", downloaded)
	if string(downloaded.Body) != "retained" {
		t.Fatalf("upload data: %q", downloaded.Body)
	}
	c.waitInventory(t, "file operation released", func(got inventoryView) bool { return bedMatches(got, "upload-hold", "resident", "idle") })
	evicted, err := c.json(ctx, "DELETE", "/v1/beds/upload-hold", "", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	must2xx(t, "evict completed upload", evicted)
	if missing := c.download(t, "upload-hold", "/workspace/upload"); missing.Status != http.StatusNotFound {
		t.Fatalf("request recreated evicted Bed: %d %s", missing.Status, missing.Body)
	}
}
