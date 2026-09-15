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

package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/isolation"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
	hostfacts "github.com/qiankunli/hostel/internal/host/facts"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTracingMiddlewareExtractsParentAndSkipsHealth(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previousProvider := otel.GetTracerProvider()
	previousPropagator := otel.GetTextMapPropagator()
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	t.Cleanup(func() {
		otel.SetTracerProvider(previousProvider)
		otel.SetTextMapPropagator(previousPropagator)
		_ = provider.Shutdown(context.Background())
	})

	root := t.TempDir()
	host := hostfacts.Collect()
	mgr, err := bed.NewManager(host, root, "default", "/bin/bash", isolation.New(host, "shared", root), nil, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mgr.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	s := NewServer(mgr, WithTracing(true))
	request := httptest.NewRequest(http.MethodGet, "/v1/beds", nil)
	request.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("GET /v1/beds status = %d", response.Code)
	}

	spans := recorder.Ended()
	if len(spans) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(spans))
	}
	if got := spans[0].Name(); got != "GET /v1/beds" {
		t.Fatalf("span name = %q, want route template", got)
	}
	if got := spans[0].Parent().TraceID().String(); got != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("parent trace id = %s", got)
	}
	if got := spans[0].Parent().SpanID().String(); got != "00f067aa0ba902b7" {
		t.Fatalf("parent span id = %s", got)
	}

	healthResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(healthResponse, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if got := len(recorder.Ended()); got != 1 {
		t.Fatalf("health check created a span: ended spans = %d", got)
	}
	diagnosticsResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(diagnosticsResponse, httptest.NewRequest(http.MethodGet, "/v1/status", nil))
	if got := len(recorder.Ended()); got != 1 {
		t.Fatalf("diagnostics created a span: ended spans = %d", got)
	}
}
