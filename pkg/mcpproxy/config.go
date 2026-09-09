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

// Package mcpproxy provides a single-sandbox remote MCP connection pool.
// Callers own sandbox identity, admission, HTTP routing and Proxy.Close.
package mcpproxy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

var (
	ErrInvalid  = errors.New("invalid MCP configuration or request")
	ErrNotFound = errors.New("MCP server is not configured")
	ErrCapacity = errors.New("MCP connection capacity reached")
	ErrClosed   = errors.New("MCP proxy is closed")
	ErrUpstream = errors.New("remote MCP request failed")
)

// Server describes a remote endpoint. Headers expand only bundle secrets;
// the carrier's environment is never a fallback credential source.
type Server struct {
	URL       string            `json:"url"`
	Transport string            `json:"transport,omitempty"`
	Headers   map[string]string `json:"headers,omitempty"`
	TimeoutMS int               `json:"timeoutMs,omitempty"`
}

// Bundle is an immutable request snapshot. Revision is caller metadata, not
// proof of equality: connection identity also hashes the effective config.
type Bundle struct {
	Revision string            `json:"revision"`
	Servers  map[string]Server `json:"servers"`
	Secrets  map[string]string `json:"secrets,omitempty"`
}

// Call selects an optional call-scoped bundle without replacing defaults.
type Call struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
	Meta      map[string]any  `json:"_meta,omitempty"`
	Bundle    *Bundle         `json:"bundle,omitempty"`
}

// Options are process-level limits. Zero values select bounded defaults.
// HTTPClient may supply deployment-specific TLS, egress and tracing policy;
// its transport must be safe for concurrent use and for long-lived SSE.
type Options struct {
	HTTPClient     *http.Client
	Timeout        time.Duration
	IdleTimeout    time.Duration
	MaxConnections int
}

type preparedBundle struct {
	revision string
	digest   string
	servers  map[string]Server
}

func prepare(bundle Bundle, max int) (preparedBundle, error) {
	if strings.TrimSpace(bundle.Revision) == "" || len(bundle.Servers) > max {
		return preparedBundle{}, fmt.Errorf("%w: revision required and server count must not exceed %d", ErrInvalid, max)
	}
	result := preparedBundle{revision: bundle.Revision, servers: make(map[string]Server, len(bundle.Servers))}
	for name, server := range bundle.Servers {
		u, err := url.Parse(server.URL)
		if strings.TrimSpace(name) == "" || err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.Fragment != "" {
			return preparedBundle{}, fmt.Errorf("%w: server name and HTTP(S) URL required", ErrInvalid)
		}
		if server.Transport == "" {
			server.Transport = "streamable_http"
		}
		if server.Transport != "sse" && server.Transport != "streamable_http" {
			return preparedBundle{}, fmt.Errorf("%w: unsupported transport", ErrInvalid)
		}
		if server.TimeoutMS < 0 {
			return preparedBundle{}, fmt.Errorf("%w: negative timeout", ErrInvalid)
		}
		headers := make(map[string]string, len(server.Headers))
		missing := false
		for key, value := range server.Headers {
			expanded := os.Expand(value, func(name string) string {
				v, ok := bundle.Secrets[name]
				if !ok {
					missing = true
				}
				return v
			})
			if strings.ContainsAny(expanded, "\r\n") || key == "" || strings.ContainsAny(key, "\r\n:") {
				return preparedBundle{}, fmt.Errorf("%w: invalid header", ErrInvalid)
			}
			canonical := http.CanonicalHeaderKey(key)
			if _, exists := headers[canonical]; exists {
				return preparedBundle{}, fmt.Errorf("%w: duplicate header", ErrInvalid)
			}
			headers[canonical] = expanded
		}
		if missing {
			return preparedBundle{}, fmt.Errorf("%w: unresolved secret placeholder", ErrInvalid)
		}
		server.Headers = headers
		result.servers[name] = server
	}
	data, err := json.Marshal(result.servers)
	if err != nil {
		return preparedBundle{}, ErrInvalid
	}
	hash := sha256.Sum256(data)
	result.digest = hex.EncodeToString(hash[:])
	return result, nil
}
