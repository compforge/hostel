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

package bed

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/qiankunli/hostel/internal/executor"
)

func installSpanRecorder(t *testing.T) (*sdktrace.TracerProvider, *tracetest.SpanRecorder) {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		_ = provider.Shutdown(context.Background())
		otel.SetTracerProvider(previous)
	})
	return provider, recorder
}

func TestLifecycleSpanUsesRequestParentAndStageEvents(t *testing.T) {
	provider, recorder := installSpanRecorder(t)
	ctx, parent := provider.Tracer("test").Start(context.Background(), "request")
	m := newTestManager(t)
	if _, err := m.Ensure(ctx, "traced-bed"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	parent.End()

	span := endedSpanByName(t, recorder, "hostel.bed.initialize")
	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("initialize parent = %s, want %s", span.Parent().SpanID(), parent.SpanContext().SpanID())
	}
	if got := spanAttribute(span.Attributes(), "hostel.bed.id"); got != "traced-bed" {
		t.Fatalf("hostel.bed.id = %q", got)
	}
	if len(span.Events()) == 0 {
		t.Fatal("initialize span has no stage events")
	}
}

func TestExecutionSpanRecordsOutcomeWithoutCommandContent(t *testing.T) {
	provider, recorder := installSpanRecorder(t)
	ctx, parent := provider.Tracer("test").Start(context.Background(), "request")
	m := newTestManager(t)
	b, err := m.Ensure(ctx, "execution-bed")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	result, err := m.RunForeground(ctx, b, "printf super-secret", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	parent.End()

	span := endedSpanByName(t, recorder, "hostel.execution")
	if span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Fatalf("execution parent = %s, want %s", span.Parent().SpanID(), parent.SpanContext().SpanID())
	}
	if got := spanAttribute(span.Attributes(), "hostel.execution.process.outcome"); got != string(executor.ProcessExited) {
		t.Fatalf("process outcome = %q", got)
	}
	for _, attr := range span.Attributes() {
		if attr.Value.AsString() == "printf super-secret" || attr.Value.AsString() == "super-secret" {
			t.Fatalf("sensitive command/output leaked into span attribute %q", attr.Key)
		}
	}
	if result.Process.ExitCode != 0 {
		t.Fatalf("exit code = %d", result.Process.ExitCode)
	}
}

func endedSpanByName(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			return span
		}
	}
	t.Fatalf("ended span %q not found", name)
	return nil
}

func spanAttribute(attributes []attribute.KeyValue, key attribute.Key) string {
	for _, attr := range attributes {
		if attr.Key == key {
			return attr.Value.AsString()
		}
	}
	return ""
}
