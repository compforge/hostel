//go:build e2e

package e2e_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestExecutionLifecycle(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client

	background, response := c.command(t, "execution-bed", map[string]any{
		"command":    "printf 'stdout-before-stop\\n'; printf 'stderr-before-stop\\n' >&2; sleep 30",
		"background": true,
		"timeout":    60_000,
	})
	must2xx(t, "start background execution", response)
	if background.ExecutionID == "" {
		t.Fatal("background execution omitted execution id")
	}
	c.waitExecution(t, background.ExecutionID, func(status executionStatusView) bool {
		return status.Running && status.BedID == "execution-bed" && status.Mode == "background"
	}, "running in execution-bed")

	logs := waitExecutionLogs(t, c, background.ExecutionID, func(got executionLogsView) bool {
		return outputContains(got.Output, "stdout", "stdout-before-stop") &&
			outputContains(got.Output, "stderr", "stderr-before-stop")
	})
	if logs.Truncated || logs.NextCursor < 1 {
		t.Fatalf("unexpected initial logs: %+v", logs)
	}
	for i := 1; i < len(logs.Output); i++ {
		if logs.Output[i].Sequence <= logs.Output[i-1].Sequence {
			t.Fatalf("execution logs are not ordered: %+v", logs.Output)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	interrupted, err := c.json(ctx, "DELETE", "/command?id="+url.QueryEscape(background.ExecutionID), "", nil, nil)
	cancel()
	if err != nil || interrupted.Status != http.StatusOK {
		t.Fatalf("interrupt background execution: status=%d err=%v body=%s", interrupted.Status, err, interrupted.Body)
	}
	finished := c.waitExecution(t, background.ExecutionID, func(status executionStatusView) bool {
		return !status.Running && status.Result != nil
	}, "interrupted with a terminal result")
	if finished.Result.Cause != "interrupted" || finished.Result.ExecutionID != background.ExecutionID {
		t.Fatalf("interrupted result=%+v", finished.Result)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
	var tail executionLogsView
	tailResult, err := c.json(ctx, "GET", "/command/"+url.PathEscape(background.ExecutionID)+"/logs?cursor="+url.QueryEscape(strconv.FormatInt(logs.NextCursor, 10)), "", nil, &tail)
	cancel()
	if err != nil || tailResult.Status != http.StatusOK || len(tail.Output) != 0 || tail.Running {
		t.Fatalf("logs cursor tail: status=%d err=%v logs=%+v body=%s", tailResult.Status, err, tail, tailResult.Body)
	}

	timedOut, response := c.command(t, "execution-bed", map[string]any{
		"command": "sleep 30",
		"timeout": 200,
	})
	must2xx(t, "run timed-out execution", response)
	if timedOut.Result == nil || timedOut.Result.Cause != "timeout" {
		t.Fatalf("timeout result=%+v", timedOut.Result)
	}
}

func TestStatefulSessionLifecycle(t *testing.T) {
	target := startTarget(t, targetOptions{isolation: "dorm"})
	c := target.client

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	var created struct {
		SessionID string `json:"session_id"`
	}
	result, err := c.json(ctx, "POST", "/session", "session-bed", map[string]string{"cwd": "/workspace"}, &created)
	cancel()
	if err != nil || result.Status != http.StatusOK || created.SessionID == "" {
		t.Fatalf("create stateful session: status=%d err=%v body=%s", result.Status, err, result.Body)
	}

	first, response := c.stream(t, "/session/"+url.PathEscape(created.SessionID)+"/run", "session-bed", map[string]any{
		"command": "export E2E_SESSION_STATE=kept; mkdir -p stateful; cd stateful",
		"timeout": 30_000,
	})
	must2xx(t, "prime stateful session", response)
	assertCommandExit(t, first, 0)

	second, response := c.stream(t, "/session/"+url.PathEscape(created.SessionID)+"/run", "session-bed", map[string]any{
		"command": "printf '%s|%s' \"$E2E_SESSION_STATE\" \"$PWD\"",
		"timeout": 30_000,
	})
	must2xx(t, "reuse stateful session", response)
	assertCommandExit(t, second, 0)
	if !strings.Contains(second.Stdout, "kept|") || !strings.HasSuffix(strings.TrimSpace(second.Stdout), "/stateful") {
		t.Fatalf("session state was not preserved: stdout=%q stderr=%q", second.Stdout, second.Stderr)
	}

	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Second)
	deleted, err := c.json(ctx, "DELETE", "/session/"+url.PathEscape(created.SessionID), "session-bed", nil, nil)
	cancel()
	if err != nil || deleted.Status != http.StatusOK {
		t.Fatalf("delete stateful session: status=%d err=%v body=%s", deleted.Status, err, deleted.Body)
	}
	_, missing := c.stream(t, "/session/"+url.PathEscape(created.SessionID)+"/run", "session-bed", map[string]string{"command": "true"})
	if missing.Status != http.StatusNotFound {
		t.Fatalf("run deleted session: status=%d body=%s", missing.Status, missing.Body)
	}
}

func waitExecutionLogs(t *testing.T, c *apiClient, id string, predicate func(executionLogsView) bool) executionLogsView {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	var last executionLogsView
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := c.json(ctx, "GET", "/command/"+url.PathEscape(id)+"/logs", "", nil, &last)
		cancel()
		if err == nil && result.Status == http.StatusOK && predicate(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for execution %s logs; last=%+v", id, last)
	return executionLogsView{}
}

func outputContains(output []executionOutputView, stream, text string) bool {
	for _, item := range output {
		if item.Stream == stream && strings.Contains(item.Text, text) {
			return true
		}
	}
	return false
}
