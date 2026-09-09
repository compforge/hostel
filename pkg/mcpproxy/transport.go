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

package mcpproxy

import (
	"context"
	"net"
	"net/http"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

func defaultHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		Proxy:        http.ProxyFromEnvironment,
		DialContext:  (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConns: 64, MaxIdleConnsPerHost: 8, MaxConnsPerHost: 32,
		IdleConnTimeout: 90 * time.Second, TLSHandshakeTimeout: 10 * time.Second, ResponseHeaderTimeout: 15 * time.Second,
	}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func (p *Proxy) connect(ctx context.Context, server Server) (*mcpclient.Client, context.CancelFunc, error) {
	var client *mcpclient.Client
	var err error
	// SDK transport logs can contain remote payloads. Request-level outcomes
	// are observed by the caller, without exposing those payloads.
	logger := quietLogger{}
	if server.Transport == "sse" {
		client, err = mcpclient.NewSSEMCPClient(server.URL, transport.WithHeaders(server.Headers), transport.WithHTTPClient(p.opts.HTTPClient), transport.WithSSELogger(logger))
	} else {
		client, err = mcpclient.NewStreamableHttpClient(server.URL, transport.WithHTTPHeaders(server.Headers), transport.WithHTTPBasicClient(p.opts.HTTPClient), transport.WithHTTPLogger(logger))
	}
	if err != nil {
		return nil, nil, err
	}
	// SSE's Start context owns the stream. Tie it to the sandbox lifetime,
	// while allowing this request's deadline to abort an unfinished handshake.
	streamCtx, streamCancel := context.WithCancel(p.ctx)
	stop := context.AfterFunc(ctx, streamCancel)
	if err = client.Start(streamCtx); err == nil {
		request := mcp.InitializeRequest{}
		request.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
		request.Params.ClientInfo = mcp.Implementation{Name: "hostel-mcp-proxy", Version: "1"}
		_, err = client.Initialize(ctx, request)
	}
	stopped := stop()
	if err != nil || !stopped || ctx.Err() != nil {
		streamCancel()
		_ = client.Close()
		if err == nil {
			err = ctx.Err()
		}
		if err == nil {
			err = context.Canceled
		}
		return nil, nil, err
	}
	return client, streamCancel, nil
}

type quietLogger struct{}

func (quietLogger) Infof(string, ...any)  {}
func (quietLogger) Errorf(string, ...any) {}
