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

// Package tracing owns Hostel's OpenTelemetry process setup and the small
// context-aware logging seam used by domain code. Domain facts stay in bed;
// this package only projects them into the active trace provider.
package tracing

import (
	"context"
	"fmt"
	"log"
	"log/slog"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkresource "go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	ServiceName         = "hostel"
	InstrumentationName = "github.com/qiankunli/hostel"
)

type Config struct {
	Enabled      bool
	GRPCEndpoint string
	HTTPEndpoint string
	Version      string
}

// Init installs Hostel's process-wide trace provider. Endpoint selection is
// intentionally identical to sandctl: gRPC wins when both deployment values
// exist, while enabled-without-endpoint is an explicit no-op rather than a
// carrier startup failure.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	if !cfg.Enabled {
		return func(context.Context) error { return nil }, nil
	}

	var (
		exporter *otlptrace.Exporter
		protocol string
		endpoint string
		err      error
	)
	switch {
	case cfg.GRPCEndpoint != "":
		if cfg.HTTPEndpoint != "" {
			log.Printf("tracing: both grpc and http endpoints set, preferring grpc; ignored http endpoint=%s", cfg.HTTPEndpoint)
		}
		exporter, err = otlptracegrpc.New(ctx, otlptracegrpc.WithEndpointURL(cfg.GRPCEndpoint))
		protocol, endpoint = "grpc", cfg.GRPCEndpoint
	case cfg.HTTPEndpoint != "":
		exporter, err = otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.HTTPEndpoint))
		protocol, endpoint = "http", cfg.HTTPEndpoint
	default:
		log.Printf("tracing: enabled but no endpoint configured, skipping exporter init")
		return func(context.Context) error { return nil }, nil
	}
	if err != nil {
		return nil, fmt.Errorf("create otlp trace exporter (%s): %w", protocol, err)
	}

	instanceID, _ := os.Hostname()
	attributes := []sdkresource.Option{
		sdkresource.WithAttributes(
			semconv.ServiceName(ServiceName),
			semconv.ServiceVersion(cfg.Version),
		),
	}
	if instanceID != "" {
		attributes = append(attributes, sdkresource.WithAttributes(semconv.ServiceInstanceID(instanceID)))
	}
	resource, err := sdkresource.New(ctx, attributes...)
	if err != nil {
		return nil, fmt.Errorf("build otel resource: %w", err)
	}

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource),
	)
	otel.SetTracerProvider(provider)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	log.Printf("tracing enabled: service=%s protocol=%s endpoint=%s", ServiceName, protocol, endpoint)
	return provider.Shutdown, nil
}

func Tracer() trace.Tracer {
	return otel.Tracer(InstrumentationName)
}

// InfoContext adds correlation fields without replacing the application's
// global logger. Startup and background logs without a span keep their current
// shape; only domain logs emitted with a trace context gain trace identifiers.
func InfoContext(ctx context.Context, message string, args ...any) {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		args = append(args,
			"trace_id", spanContext.TraceID().String(),
			"span_id", spanContext.SpanID().String(),
		)
	}
	slog.InfoContext(ctx, message, args...)
}

// WarnContext is the warning-level counterpart of InfoContext. It keeps
// transient failures correlated with the request or execution span even when
// a retry later recovers and no terminal error is returned to the caller.
func WarnContext(ctx context.Context, message string, args ...any) {
	spanContext := trace.SpanContextFromContext(ctx)
	if spanContext.IsValid() {
		args = append(args,
			"trace_id", spanContext.TraceID().String(),
			"span_id", spanContext.SpanID().String(),
		)
	}
	slog.WarnContext(ctx, message, args...)
}

// Printf preserves Hostel's existing text log format while adding trace
// correlation to lifecycle lines that are already consumed operationally.
func Printf(ctx context.Context, format string, args ...any) {
	spanContext := trace.SpanContextFromContext(ctx)
	if !spanContext.IsValid() {
		log.Printf(format, args...)
		return
	}
	log.Printf("trace_id=%s span_id=%s "+format,
		append([]any{spanContext.TraceID().String(), spanContext.SpanID().String()}, args...)...)
}
