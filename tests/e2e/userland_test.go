//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/protocol"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPyPIInstallPersistsAcrossExecutions(t *testing.T) {
	requireUserland(t)
	target := startTarget(t, targetOptions{})
	const load = `python -c 'import cowsay; print("HOSTEL_PIP_PATH=" + cowsay.__file__)'`
	must2xx(t, "declare Bed", target.client.createBed(t, "pypi"))
	install, response := target.client.command(t, "pypi", map[string]any{
		"command": "python -m pip install --no-cache-dir --force-reinstall cowsay==6.1 && " + load,
		"timeout": 180_000,
	})
	must2xx(t, "install PyPI package", response)
	assertSoftwareResult(t, "PyPI install", install, "HOSTEL_PIP_PATH=/usr/local/")

	loaded, response := target.client.command(t, "pypi", map[string]any{"command": load, "timeout": 30_000})
	must2xx(t, "load PyPI package in next execution", response)
	assertSoftwareResult(t, "PyPI next execution", loaded, "HOSTEL_PIP_PATH=/usr/local/")
}

func TestNPMInstallPersistsAcrossExecutions(t *testing.T) {
	requireUserland(t)
	target := startTarget(t, targetOptions{})
	const load = `node -e 'const root = require("child_process").execFileSync("npm", ["root", "-g"], {encoding: "utf8"}).trim(); const modulePath = root + "/is-number"; const isNumber = require(modulePath); if (!isNumber("42")) process.exit(1); console.log("HOSTEL_NPM_PATH=" + require.resolve(modulePath));'`
	must2xx(t, "declare Bed", target.client.createBed(t, "npm"))
	install, response := target.client.command(t, "npm", map[string]any{
		"command": "npm install --global --force --prefer-online --no-audit --no-fund is-number@7.0.0 && " + load,
		"timeout": 180_000,
	})
	must2xx(t, "install npm package", response)
	assertSoftwareResult(t, "npm install", install, "HOSTEL_NPM_PATH=/usr/local/")

	loaded, response := target.client.command(t, "npm", map[string]any{"command": load, "timeout": 30_000})
	must2xx(t, "load npm package in next execution", response)
	assertSoftwareResult(t, "npm next execution", loaded, "HOSTEL_NPM_PATH=/usr/local/")
}

// +case:id=shared_software_groups,desc=`A disposable Carrier provisions shared writable software for two distinct Bed UIDs`,expect=`pip, npm and uv installs remain usable and replaceable by the neighboring Bed`
func TestSharedSoftwareAcrossBedUsers(t *testing.T) {
	requireUserland(t)
	target := startTarget(t, targetOptions{})
	c := target.client
	for _, name := range []string{"software-a", "software-b"} {
		must2xx(t, "declare Bed", c.createBed(t, name))
	}
	run := func(t *testing.T, name, command string) string {
		t.Helper()
		result, response := c.command(t, name, map[string]any{"command": command, "timeout": 180000})
		must2xx(t, "shared software command", response)
		assertCommandExit(t, result, 0)
		return strings.TrimSpace(result.Stdout)
	}
	a, b := run(t, "software-a", "id -u"), run(t, "software-b", "id -u")
	for _, name := range []string{"software-a", "software-b"} {
		// A user namespace may display an unmapped supplementary group as
		// overflow GID. Check the Carrier-space identity and prove effective
		// access through the cross-UID package operations below.
		var detail protocol.BedDetail
		response, err := c.json(t.Context(), http.MethodGet, "/v1/beds/"+name, "", nil, &detail)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "shared software identity", response)
		u := detail.Status.Subsystems.Privilege
		if !u.Assigned || !slices.Equal(u.SupplementaryGIDs, []int{privilege.SoftwareGID}) {
			t.Fatalf("unexpected Carrier-space identity: %+v", u)
		}
	}
	if a == b {
		t.Fatalf("software sharing was tested with the same UID: %s", a)
	}
	for _, tc := range []struct{ name, install, check, remove string }{
		{"pip", "python -m pip install --no-cache-dir --force-reinstall cowsay==6.1", "python -c 'import cowsay; assert cowsay.__file__.startswith(\"/usr/local/\")'", "python -m pip uninstall -y cowsay"},
		{"npm", "npm install --global --force --no-audit --no-fund is-number@7.0.0", "node -e 'if (!require(\"/usr/local/lib/node_modules/is-number\")(\"42\")) process.exit(1)'", "npm uninstall --global --no-audit --no-fund is-number"},
		{"uv", "uv tool install --force --reinstall cowsay==6.1", "test -x /usr/local/bin/cowsay && /usr/local/bin/cowsay -t shared-software", "uv tool uninstall cowsay"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run(t, "software-a", tc.install)
			run(t, "software-b", tc.check)
			run(t, "software-b", tc.install)
			run(t, "software-a", tc.check)
			run(t, "software-b", tc.remove)
		})
	}
}

// +case:id=shared_software_provided_permissions,desc=`A disposable Carrier provides writable shared software before Hostel starts`,expect=`distinct Bed UIDs can update shared files without Hostel claiming permission preparation`
func TestSharedSoftwareProvidedPermissions(t *testing.T) {
	requireUserland(t)
	started := time.Now()
	target := startTarget(t, targetOptions{})
	t.Logf("Carrier startup completed in %s", time.Since(started).Round(time.Millisecond))
	c := target.client
	for _, name := range []string{"permissions-a", "permissions-b"} {
		must2xx(t, "declare Bed", c.createBed(t, name))
	}
	run := func(name, command string) string {
		t.Helper()
		result, response := c.command(t, name, map[string]any{"command": command, "timeout": 30000})
		must2xx(t, "shared software permissions", response)
		assertCommandExit(t, result, 0)
		return strings.TrimSpace(result.Stdout)
	}
	if run("permissions-a", "id -u") == run("permissions-b", "id -u") {
		t.Fatal("distinct UID prerequisites were not realized")
	}
	for _, name := range []string{"permissions-a", "permissions-b"} {
		var detail protocol.BedDetail
		response, err := c.json(t.Context(), "GET", "/v1/beds/"+name, "", nil, &detail)
		if err != nil {
			t.Fatal(err)
		}
		must2xx(t, "Bed software realization", response)
		actual := detail.Status.Subsystems.Filesystem.Realization
		if actual == nil || actual.State != "prepared" {
			t.Fatalf("missing realization: %+v", actual)
		}
		if len(actual.Software) != 0 {
			t.Fatalf("filesystem claimed Carrier permission preparation: %+v", actual.Software)
		}
	}
	dir := fmt.Sprintf("/usr/local/hostel-permissions-%d", time.Now().UnixNano())
	run("permissions-a", fmt.Sprintf("umask 022; mkdir -p %s/nested; printf first > %s/nested/package", dir, dir))
	run("permissions-b", fmt.Sprintf("printf second >> %s/nested/package; mv %s/nested/package %s/nested/replaced; umask 022; printf replacement > %s/nested/package", dir, dir, dir, dir))
	must2xx(t, "private API upload", c.upload(t, "permissions-a", dir+"/api-package", []byte("api")))
	run("permissions-b", fmt.Sprintf(`test ! -e %s/api-package`, dir))
	if got := c.download(t, "permissions-b", dir+"/api-package"); got.Status != http.StatusNotFound {
		t.Fatalf("implicit shared API data: %d", got.Status)
	}
	run("permissions-a", fmt.Sprintf("printf third >> %s/nested/package; rm -r %s", dir, dir))
}

func TestChromiumOperationsAndArtifact(t *testing.T) {
	requireUserland(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Hostel E2E</title></head><body>
<input id="name"><button id="apply" onclick="const r=document.getElementById('result');r.textContent=document.getElementById('name').value;r.className='ready'">apply</button>
<main id="result">chrome-ready</main></body></html>`)
	}))
	defer page.Close()

	target := startTarget(t, targetOptions{})
	c := target.client
	bedID := "chromium"
	must2xx(t, "declare Bed", c.createBed(t, bedID))

	post := func(action string, body any, out any) httpResult {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		result, err := c.json(ctx, "POST", "/v1/beds/"+bedID+"/browser/"+action, "", body, out)
		if err != nil {
			t.Fatalf("browser %s: %v", action, err)
		}
		return result
	}

	var gotoResult struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	}
	must2xx(t, "browser goto", post("goto", map[string]string{"url": page.URL}, &gotoResult))
	if gotoResult.Title != "Hostel E2E" || gotoResult.URL != page.URL+"/" {
		t.Fatalf("browser goto=%+v", gotoResult)
	}
	must2xx(t, "browser type", post("type", map[string]any{"selector": "#name", "text": "typed-by-hostel", "clear": true}, nil))
	must2xx(t, "browser click", post("click", map[string]string{"selector": "#apply"}, nil))
	must2xx(t, "browser wait", post("wait", map[string]string{"selector": "#result.ready"}, nil))
	var textResult struct {
		Text string `json:"text"`
	}
	must2xx(t, "browser text", post("text", nil, &textResult))
	if !strings.Contains(textResult.Text, "typed-by-hostel") {
		t.Fatalf("browser text=%q", textResult.Text)
	}

	var screenshot struct {
		Path string `json:"path"`
	}
	must2xx(t, "browser screenshot", post("screenshot", map[string]string{"path": "screenshots/e2e.png"}, &screenshot))
	if screenshot.Path != "/workspace/screenshots/e2e.png" {
		t.Fatalf("screenshot path=%q", screenshot.Path)
	}
	artifact := c.download(t, bedID, screenshot.Path)
	if artifact.Status != http.StatusOK || len(artifact.Body) < 1024 || !strings.HasPrefix(string(artifact.Body[:min(len(artifact.Body), 8)]), "\x89PNG\r\n\x1a\n") {
		t.Fatalf("screenshot artifact: status=%d size=%d prefix=%x", artifact.Status, len(artifact.Body), artifact.Body[:min(len(artifact.Body), 8)])
	}
	must2xx(t, "browser close", post("close", nil, nil))
}

func requireUserland(t *testing.T) {
	t.Helper()
	if os.Getenv(userlandEnv) != "1" {
		t.Skip("set HOSTEL_E2E_USERLAND=1 to run PyPI/npm/Chromium image scenarios")
	}
	if strings.TrimSpace(os.Getenv(imageEnv)) == "" {
		// An on-target runner may already be inside the disposable userland
		// image. Keep this explicit so a developer's workstation is not used
		// accidentally for global package mutations.
		if strings.TrimSpace(os.Getenv(binaryEnv)) != "" && os.Getenv("HOSTEL_E2E_ON_TARGET") == "1" && os.Getenv("HOSTEL_E2E_DISPOSABLE") == "1" {
			return
		}
		t.Fatalf("%s=1 requires %s or an on-target binary with HOSTEL_E2E_DISPOSABLE=1", userlandEnv, imageEnv)
	}
}

func assertSoftwareResult(t *testing.T, step string, result commandResult, expected string) {
	t.Helper()
	assertCommandExit(t, result, 0)
	if !strings.Contains(result.Stdout, expected) {
		t.Fatalf("%s did not load package from shared /usr/local: stdout=%q stderr=%q", step, result.Stdout, result.Stderr)
	}
}
