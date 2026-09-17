package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	apiview "github.com/qiankunli/hostel/internal/api/execd/view"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

// The optional admission hook is API configuration owned by the Bed creator.
// It does not participate in the Executor model or carry command/file output.
var execdControlClient = &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
	Proxy: http.ProxyFromEnvironment, MaxIdleConns: 32, MaxIdleConnsPerHost: 16,
	MaxConnsPerHost: 32, IdleConnTimeout: 90 * time.Second,
}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func (s *Handler) prepareExecd(c *gin.Context, b *bed.Resident, command *apiview.CommandRequest) bool {
	env := b.Spec().Env
	endpoint := env["EXECD_CONTROL_URL"]
	if endpoint == "" {
		return true
	}
	body, err := json.Marshal(struct {
		Command *apiview.CommandRequest `json:"command,omitempty"`
	}{command})
	if err != nil {
		runtimeError(c, "encode execution admission")
		return false
	}
	// Use the resolved Bed identity, never X-Sandbox-ID supplied by the caller.
	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost,
		strings.TrimRight(endpoint, "/")+"/execd/v1/sandboxes/"+url.PathEscape(b.Name)+"/prepare", bytes.NewReader(body))
	if err != nil {
		runtimeError(c, "invalid execution admission endpoint")
		return false
	}
	req.Header.Set("Authorization", "Bearer "+env["EXECD_ACCESS_TOKEN"])
	req.Header.Set("Content-Type", "application/json")
	resp, err := execdControlClient.Do(req)
	if err != nil {
		respondError(c, 503, apiview.ErrServiceUnavailable, "execution admission unavailable")
		return false
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		respondError(c, 503, apiview.ErrServiceUnavailable, "execution admission rejected")
		return false
	}
	var decision struct {
		ExitCode int               `json:"exit_code"`
		Stderr   string            `json:"stderr"`
		Envs     map[string]string `json:"envs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&decision); err != nil {
		runtimeError(c, "invalid execution admission response")
		return false
	}
	if decision.ExitCode != 0 {
		respondError(c, 403, "COMMAND_DENIED", decision.Stderr)
		return false
	}
	if command != nil {
		if command.Envs == nil {
			command.Envs = map[string]string{}
		}
		for key, value := range decision.Envs {
			command.Envs[key] = value
		}
	}
	return true
}
