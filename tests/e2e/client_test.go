//go:build e2e

package e2e_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

type apiClient struct {
	baseURL string
	http    *http.Client
}

type httpResult struct {
	Status int
	Header http.Header
	Body   []byte
}

type healthView struct {
	OK              bool   `json:"ok"`
	ExecutorBackend string `json:"executor_backend"`
	WorkspaceMount  bool   `json:"workspace_mount"`
	WorkspaceView   struct {
		Mode      string `json:"mode"`
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
	} `json:"workspace_view"`
	MaxBeds   int `json:"max_beds"`
	Isolation struct {
		Level     string `json:"level"`
		Mechanism string `json:"mechanism"`
		Requested string `json:"requested"`
		Effective string `json:"effective"`
		Ceiling   string `json:"ceiling"`
	} `json:"isolation"`
	Amenities map[string]string `json:"amenities"`
}

type readinessView struct {
	Ready   bool   `json:"status"`
	Reason  string `json:"reason"`
	Message string `json:"message"`
}

type bedView struct {
	ID        string `json:"id"`
	Workspace string `json:"workspace"`
	Status    struct {
		Phase     string        `json:"phase"`
		Activity  string        `json:"activity"`
		Readiness readinessView `json:"readiness"`
	} `json:"status"`
}

type inventoryView struct {
	Instance struct {
		Status        string         `json:"status"`
		Isolation     string         `json:"isolation"`
		MaxBeds       int            `json:"max_beds"`
		PhaseCounts   map[string]int `json:"phase_counts"`
		ActivityCount map[string]int `json:"activity_counts"`
	} `json:"instance"`
	Beds []bedView `json:"beds"`
}

type processView struct {
	Kind     string `json:"kind"`
	ExitCode *int   `json:"exit_code"`
	Signal   *int   `json:"signal"`
}

type executionView struct {
	ExecutionID     string      `json:"execution_id"`
	BedID           string      `json:"bed_id"`
	ExecutorBackend string      `json:"executor_backend"`
	Process         processView `json:"process"`
	Cause           string      `json:"termination_cause"`
}

type streamEvent struct {
	Type        string         `json:"type"`
	ExecutionID string         `json:"execution_id"`
	Text        string         `json:"text"`
	Result      *executionView `json:"result"`
}

type commandResult struct {
	ExecutionID string
	Stdout      string
	Stderr      string
	Result      *executionView
}

type executionStatusView struct {
	ExecutionID string         `json:"execution_id"`
	BedID       string         `json:"bed_id"`
	Mode        string         `json:"mode"`
	Running     bool           `json:"running"`
	Result      *executionView `json:"result"`
}

type executionOutputView struct {
	Sequence int64  `json:"sequence"`
	Stream   string `json:"stream"`
	Text     string `json:"text"`
}

type executionLogsView struct {
	ExecutionID string                `json:"execution_id"`
	Output      []executionOutputView `json:"output"`
	NextCursor  int64                 `json:"next_cursor"`
	Running     bool                  `json:"running"`
	Truncated   bool                  `json:"truncated"`
}

type fileInfoView struct {
	Path  string `json:"path"`
	Type  string `json:"type"`
	Size  int64  `json:"size"`
	Mode  int    `json:"mode"`
	Owner string `json:"owner"`
	Group string `json:"group"`
}

func newAPIClient(baseURL string) *apiClient {
	return &apiClient{
		baseURL: strings.TrimSuffix(baseURL, "/"),
		http:    &http.Client{Timeout: 4 * time.Minute},
	}
}

func (c *apiClient) request(ctx context.Context, method, path, bed string, body io.Reader, contentType string) (httpResult, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return httpResult{}, err
	}
	if bed != "" {
		req.Header.Set("X-Hostel-Bed", bed)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return httpResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return httpResult{}, err
	}
	return httpResult{Status: resp.StatusCode, Header: resp.Header.Clone(), Body: raw}, nil
}

func (c *apiClient) json(ctx context.Context, method, path, bed string, body, out any) (httpResult, error) {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return httpResult{}, err
		}
		input = bytes.NewReader(raw)
	}
	result, err := c.request(ctx, method, path, bed, input, "application/json")
	if err != nil {
		return result, err
	}
	if result.Status >= 200 && result.Status < 300 && out != nil && len(result.Body) > 0 {
		if err := json.Unmarshal(result.Body, out); err != nil {
			return result, fmt.Errorf("decode %s %s response: %w (body=%s)", method, path, err, result.Body)
		}
	}
	return result, nil
}

func (c *apiClient) command(t *testing.T, bed string, request map[string]any) (commandResult, httpResult) {
	t.Helper()
	return c.stream(t, "/command", bed, request)
}

func (c *apiClient) stream(t *testing.T, path, bed string, request any) (commandResult, httpResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	response, err := c.request(ctx, "POST", path, bed, bytes.NewReader(raw), "application/json")
	if err != nil {
		t.Fatalf("stream %s for bed %s: %v", path, bed, err)
	}
	if response.Status < 200 || response.Status >= 300 {
		return commandResult{}, response
	}

	var result commandResult
	scanner := bufio.NewScanner(bytes.NewReader(response.Body))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event streamEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode command stream line %q: %v", line, err)
		}
		if event.ExecutionID != "" {
			result.ExecutionID = event.ExecutionID
		}
		switch event.Type {
		case "stdout":
			result.Stdout += event.Text
		case "stderr":
			result.Stderr += event.Text
		case "execution_end":
			result.Result = event.Result
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read command stream: %v", err)
	}
	return result, response
}

func (c *apiClient) waitExecution(t *testing.T, id string, predicate func(executionStatusView) bool, description string) executionStatusView {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	var last executionStatusView
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := c.json(ctx, "GET", "/command/status/"+url.PathEscape(id), "", nil, &last)
		cancel()
		if err == nil && result.Status == http.StatusOK && predicate(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for execution %s to become %s; last=%+v", id, description, last)
	return executionStatusView{}
}

func (c *apiClient) createBed(t *testing.T, id string) httpResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var bed bedView
	result, err := c.json(ctx, "POST", "/v1/beds", "", map[string]string{"id": id}, &bed)
	if err != nil {
		t.Fatalf("create bed %s: %v", id, err)
	}
	return result
}

func (c *apiClient) waitBed(t *testing.T, id string, predicate func(bedView) bool, description string) bedView {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last bedView
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		result, err := c.json(ctx, "GET", "/v1/beds/"+url.PathEscape(id), "", nil, &last)
		cancel()
		if err == nil && result.Status == http.StatusOK && predicate(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for bed %s to become %s; last=%+v", id, description, last)
	return bedView{}
}

func (c *apiClient) inventory(t *testing.T) inventoryView {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var inventory inventoryView
	result, err := c.json(ctx, "GET", "/v1/beds", "", nil, &inventory)
	if err != nil || result.Status != http.StatusOK {
		t.Fatalf("get bed inventory: status=%d err=%v body=%s", result.Status, err, result.Body)
	}
	return inventory
}

func (c *apiClient) waitInventory(t *testing.T, description string, predicate func(inventoryView) bool) inventoryView {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	var last inventoryView
	for time.Now().Before(deadline) {
		last = c.inventory(t)
		assertInventory(t, last)
		if predicate(last) {
			return last
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s; last=%+v", description, last)
	return inventoryView{}
}

func (c *apiClient) upload(t *testing.T, bed, path string, content []byte) httpResult {
	t.Helper()
	return c.uploadAt(t, "/files/upload", bed, path, content)
}

func (c *apiClient) uploadAt(t *testing.T, endpoint, bed, path string, content []byte) httpResult {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("metadata", fmt.Sprintf(`{"path":%q}`, path)); err != nil {
		t.Fatalf("write upload metadata: %v", err)
	}
	file, err := writer.CreateFormFile("file", "payload")
	if err != nil {
		t.Fatalf("create upload part: %v", err)
	}
	if _, err := file.Write(content); err != nil {
		t.Fatalf("write upload content: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close upload body: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := c.request(ctx, "POST", endpoint, bed, &body, writer.FormDataContentType())
	if err != nil {
		t.Fatalf("upload %s for bed %s: %v", path, bed, err)
	}
	return result
}

func (c *apiClient) download(t *testing.T, bed, path string) httpResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	result, err := c.request(ctx, "GET", "/files/download?path="+url.QueryEscape(path), bed, nil, "")
	if err != nil {
		t.Fatalf("download %s for bed %s: %v", path, bed, err)
	}
	return result
}

func must2xx(t *testing.T, action string, result httpResult) {
	t.Helper()
	if result.Status < 200 || result.Status >= 300 {
		t.Fatalf("%s: status=%d body=%s", action, result.Status, result.Body)
	}
}

func assertInventory(t *testing.T, inventory inventoryView) {
	t.Helper()
	phase := map[string]int{}
	activity := map[string]int{}
	for _, bed := range inventory.Beds {
		phase[bed.Status.Phase]++
		if bed.Status.Phase == "resident" || bed.Status.Phase == "evicting" {
			activity[bed.Status.Activity]++
		}
	}
	for _, name := range []string{"resident", "evicting", "purging", "dormant", "initializing", "failed"} {
		if inventory.Instance.PhaseCounts[name] != phase[name] {
			t.Fatalf("inventory phase %s=%d, bed facts=%d: %+v", name, inventory.Instance.PhaseCounts[name], phase[name], inventory)
		}
	}
	for _, name := range []string{"active", "idle"} {
		if inventory.Instance.ActivityCount[name] != activity[name] {
			t.Fatalf("inventory activity %s=%d, bed facts=%d: %+v", name, inventory.Instance.ActivityCount[name], activity[name], inventory)
		}
	}
}
