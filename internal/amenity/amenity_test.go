// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package amenity

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestChromiumEndToEnd runs against a real browser when one is present
// (macOS dev machines usually have Chrome; CI without one skips).
func TestChromiumEndToEnd(t *testing.T) {
	br := NewChromium(ChromiumConfig{IdleStop: 200 * time.Millisecond, ActionTimeout: 20 * time.Second})
	if err := br.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if br.State() == StateUnavailable {
		t.Skip("no chromium/chrome available on this host")
	}
	c := br
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	a, _ := c.NewTenant(t.Context())
	b, _ := c.NewTenant(t.Context())
	last, _ := c.NewTenant(t.Context())
	ctx := context.Background()
	wsA, wsB := t.TempDir(), t.TempDir()

	// Lifecycle: idle until first demand.
	if c.State() != StateIdle {
		t.Fatalf("initial state = %s", c.State())
	}

	title, _, err := br.Goto(ctx, a.ID().String(), wsA, `data:text/html,<title>hello-a</title><body>alpha content</body>`)
	if err != nil {
		t.Fatalf("Goto A: %v", err)
	}
	if title != "hello-a" {
		t.Fatalf("title = %q", title)
	}
	if c.State() != StateRunning {
		t.Fatalf("state after demand = %s", c.State())
	}

	text, err := br.Text(ctx, a.ID().String(), wsA)
	if err != nil || !strings.Contains(text, "alpha content") {
		t.Fatalf("Text A: %q err=%v", text, err)
	}

	// Second bed gets its own context — its page is independent.
	if _, _, err := br.Goto(ctx, b.ID().String(), wsB, `data:text/html,<body>beta content</body>`); err != nil {
		t.Fatalf("Goto B: %v", err)
	}
	textB, _ := br.Text(ctx, b.ID().String(), wsB)
	if !strings.Contains(textB, "beta") || strings.Contains(textB, "alpha") {
		t.Fatalf("bed contexts not independent: %q", textB)
	}
	if len(c.contexts) != 2 {
		t.Fatalf("tenants = %d, want 2", len(c.contexts))
	}

	// Screenshot lands in the right bed's workspace, virtual path returned.
	saved, err := br.Screenshot(ctx, a.ID().String(), wsA, "")
	if err != nil {
		t.Fatalf("Screenshot: %v", err)
	}
	if !strings.HasPrefix(saved, "/workspace/screenshots/") {
		t.Fatalf("virtual path = %q", saved)
	}
	onDisk := filepath.Join(wsA, strings.TrimPrefix(saved, "/workspace/"))
	if fi, err := os.Stat(onDisk); err != nil || fi.Size() == 0 {
		t.Fatalf("screenshot file: %v", err)
	}
	// Escaping paths refused.
	if _, err := br.Screenshot(ctx, a.ID().String(), wsA, "../evil.png"); err == nil {
		t.Fatal("escaping screenshot path not rejected")
	}

	// Interaction verbs on a small form page (bedA).
	form := `data:text/html,` +
		`<input id="in"><button id="btn" onclick="document.getElementById('out').innerText=document.getElementById('in').value">go</button>` +
		`<div id="out"></div><div id="late" style="display:none">shown</div>` +
		`<script>document.getElementById('in').addEventListener('keydown',e=>{if(e.key==='Enter')document.getElementById('out').innerText='ENTER:'+e.target.value})</script>`
	if _, _, err := br.Goto(ctx, a.ID().String(), wsA, form); err != nil {
		t.Fatalf("Goto form: %v", err)
	}
	if err := br.Wait(ctx, a.ID().String(), wsA, "#in"); err != nil {
		t.Fatalf("Wait #in: %v", err)
	}
	if err := br.Type(ctx, a.ID().String(), wsA, "#in", "hello", true); err != nil {
		t.Fatalf("Type: %v", err)
	}
	if err := br.Click(ctx, a.ID().String(), wsA, "#btn"); err != nil {
		t.Fatalf("Click: %v", err)
	}
	out, _ := br.Text(ctx, a.ID().String(), wsA)
	if !strings.Contains(out, "hello") {
		t.Fatalf("after type+click, out = %q", out)
	}
	// Press Enter in the focused input triggers the keydown handler.
	if err := br.Type(ctx, a.ID().String(), wsA, "#in", "world", true); err != nil {
		t.Fatalf("Type 2: %v", err)
	}
	if err := br.Press(ctx, a.ID().String(), wsA, "Enter"); err != nil {
		t.Fatalf("Press Enter: %v", err)
	}
	out2, _ := br.Text(ctx, a.ID().String(), wsA)
	if !strings.Contains(out2, "ENTER:world") || strings.Contains(out2, "helloworld") {
		t.Fatalf("after Enter, out = %q", out2)
	}
	// Scroll doesn't error on a short page.
	if err := br.Scroll(ctx, a.ID().String(), wsA, 0, 100); err != nil {
		t.Fatalf("Scroll: %v", err)
	}

	// Release both tenants → idle-stop kicks in.
	_ = br.ReleaseTenant(a.ID().String())
	_ = br.ReleaseTenant(b.ID().String())
	deadline := time.Now().Add(5 * time.Second)
	for c.State() != StateIdle && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if c.State() != StateIdle {
		t.Fatalf("idle-stop did not fire: state=%s", c.State())
	}

	// And the facility restarts on new demand.
	if _, _, err := br.Goto(ctx, last.ID().String(), t.TempDir(), `data:text/html,<title>again</title>`); err != nil {
		t.Fatalf("Goto after idle-stop: %v", err)
	}
	_ = br.ReleaseTenant(last.ID().String())
	if err := c.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
}
