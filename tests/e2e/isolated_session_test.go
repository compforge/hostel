//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestIsolatedSessionCompatibility(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	var created struct {
		SessionID string    `json:"session_id"`
		CreatedAt time.Time `json:"created_at"`
	}
	create, err := c.json(ctx, "POST", "/v1/isolated/session", "", map[string]any{
		"profile":   "balanced",
		"workspace": map[string]string{"path": "/workspace", "mode": "rw"},
	}, &created)
	cancel()
	if err != nil || create.Status != http.StatusCreated || created.SessionID == "" || created.CreatedAt.IsZero() {
		t.Fatalf("create isolated session: status=%d err=%v response=%+v body=%s", create.Status, err, created, create.Body)
	}
	base := "/v1/isolated/session/" + url.PathEscape(created.SessionID)

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var state struct {
		Status    string `json:"status"`
		Profile   string `json:"profile"`
		Workspace struct {
			Path string `json:"path"`
			Mode string `json:"mode"`
		} `json:"workspace"`
		ShareNet bool `json:"share_net"`
	}
	get, err := c.json(ctx, "GET", base, "", nil, &state)
	cancel()
	if err != nil || get.Status != http.StatusOK || state.Status != "active" || state.Profile != "balanced" ||
		state.Workspace.Path != "/workspace" || state.Workspace.Mode != "rw" || !state.ShareNet {
		t.Fatalf("get isolated session: status=%d err=%v state=%+v body=%s", get.Status, err, state, get.Body)
	}

	primed, response := c.stream(t, base+"/run", "", map[string]any{
		"code":            "export ISOLATED_E2E_STATE=kept; mkdir -p stateful; cd stateful",
		"timeout_seconds": 30,
	})
	must2xx(t, "prime isolated session", response)
	assertCommandExit(t, primed, 0)
	reused, response := c.stream(t, base+"/run", "", map[string]any{
		"code":            "printf '%s|%s' \"$ISOLATED_E2E_STATE\" \"$PWD\"",
		"timeout_seconds": 30,
	})
	must2xx(t, "reuse isolated session", response)
	assertCommandExit(t, reused, 0)
	if !strings.HasPrefix(strings.TrimSpace(reused.Stdout), "kept|") || !strings.HasSuffix(strings.TrimSpace(reused.Stdout), "/stateful") {
		t.Fatalf("isolated shell state=%q stderr=%q", reused.Stdout, reused.Stderr)
	}

	upload := c.uploadAt(t, base+"/files/upload", "", "/workspace/isolated.txt", []byte("isolated-artifact"))
	must2xx(t, "upload through isolated file API", upload)
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	download, err := c.request(ctx, "GET", base+"/files/download?path="+url.QueryEscape("/workspace/isolated.txt"), "", nil, "")
	cancel()
	if err != nil || download.Status != http.StatusOK || string(download.Body) != "isolated-artifact" {
		t.Fatalf("download isolated artifact: status=%d err=%v body=%q", download.Status, err, download.Body)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	var listed struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Status    string `json:"status"`
		} `json:"sessions"`
	}
	list, err := c.json(ctx, "GET", "/v1/isolated/sessions", "", nil, &listed)
	cancel()
	if err != nil || list.Status != http.StatusOK || !containsSession(listed.Sessions, created.SessionID) {
		t.Fatalf("list isolated sessions: status=%d err=%v sessions=%+v body=%s", list.Status, err, listed.Sessions, list.Body)
	}

	for _, unsupported := range []struct {
		method string
		path   string
	}{
		{method: "GET", path: base + "/diff"},
		{method: "POST", path: base + "/commit"},
	} {
		ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
		result, requestErr := c.json(ctx, unsupported.method, unsupported.path, "", nil, nil)
		cancel()
		if requestErr != nil || result.Status != http.StatusServiceUnavailable || !strings.Contains(string(result.Body), "NOT_SUPPORTED") {
			t.Fatalf("unsupported %s %s: status=%d err=%v body=%s", unsupported.method, unsupported.path, result.Status, requestErr, result.Body)
		}
	}

	ctx, cancel = context.WithTimeout(context.Background(), 15*time.Second)
	deleted, err := c.json(ctx, "DELETE", base, "", nil, nil)
	cancel()
	if err != nil || deleted.Status != http.StatusOK {
		t.Fatalf("delete isolated session: status=%d err=%v body=%s", deleted.Status, err, deleted.Body)
	}
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	missing, err := c.json(ctx, "GET", base, "", nil, nil)
	cancel()
	if err != nil || missing.Status != http.StatusNotFound {
		t.Fatalf("get deleted isolated session: status=%d err=%v body=%s", missing.Status, err, missing.Body)
	}
}

func containsSession(sessions []struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}, id string) bool {
	for _, session := range sessions {
		if session.SessionID == id && session.Status == "active" {
			return true
		}
	}
	return false
}
