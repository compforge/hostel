package amenity

import (
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

func TestAttachedChromiumClosePreservesExternalBrowser(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	owner := NewChromium(ChromiumConfig{DebugPort: port})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := owner.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if owner.State() == StateUnavailable {
		t.Skip("no Chromium available")
	}
	t.Cleanup(func() { _ = owner.Close(context.Background()) })
	handle, err := owner.NewTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	original := handle.(Browser)
	workspace := t.TempDir()
	if _, _, err := original.Goto(ctx, workspace, "data:text/html,<body>external-owner</body>"); err != nil {
		t.Fatal(err)
	}
	attached := NewChromium(ChromiumConfig{CDPURL: "http://127.0.0.1:" + strconv.Itoa(port)})
	if err := attached.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = attached.Close(context.Background()) })
	if attached.State() == StateUnavailable {
		t.Fatal(attached.Status())
	}
	other, err := attached.NewTenant(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := other.(Browser).Goto(ctx, t.TempDir(), "data:text/html,<body>attached-tenant</body>"); err != nil {
		t.Fatal(err)
	}
	// Exercise facility shutdown with a live tenant, not only an empty connection.
	if err := attached.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if state := other.Status().(BrowserTenantStatus); !state.Closed {
		t.Fatalf("tenant remains open: %+v", state)
	}
	if _, err := owner.upstreamWSURL(ctx); err != nil {
		t.Fatalf("external browser stopped: %v", err)
	}
	text, err := original.Text(ctx, workspace)
	if err != nil || text != "external-owner" {
		t.Fatalf("external tenant damaged: %q %v", text, err)
	}
}
