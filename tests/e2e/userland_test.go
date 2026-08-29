//go:build e2e

package e2e_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func TestPyPIInstallPersistsAcrossExecutions(t *testing.T) {
	requireUserland(t)
	target := startTarget(t, targetOptions{isolation: "dorm"})
	const load = `python -c 'import cowsay; print("HOSTEL_PIP_PATH=" + cowsay.__file__)'`
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
	target := startTarget(t, targetOptions{isolation: "dorm"})
	const load = `node -e 'const root = require("child_process").execFileSync("npm", ["root", "-g"], {encoding: "utf8"}).trim(); const modulePath = root + "/is-number"; const isNumber = require(modulePath); if (!isNumber("42")) process.exit(1); console.log("HOSTEL_NPM_PATH=" + require.resolve(modulePath));'`
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

func TestChromiumOperationsAndArtifact(t *testing.T) {
	requireUserland(t)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `<!doctype html><html><head><title>Hostel E2E</title></head><body>
<input id="name"><button id="apply" onclick="const r=document.getElementById('result');r.textContent=document.getElementById('name').value;r.className='ready'">apply</button>
<main id="result">chrome-ready</main></body></html>`)
	}))
	defer page.Close()

	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client
	bedID := "chromium"
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
		t.Fatalf("%s=1 requires %s", userlandEnv, imageEnv)
	}
}

func assertSoftwareResult(t *testing.T, step string, result commandResult, expected string) {
	t.Helper()
	assertCommandExit(t, result, 0)
	if !strings.Contains(result.Stdout, expected) {
		t.Fatalf("%s did not load package from shared /usr/local: stdout=%q stderr=%q", step, result.Stdout, result.Stderr)
	}
}
