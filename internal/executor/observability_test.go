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

package executor

import (
	"context"
	"errors"
	"testing"

	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestTransportRetryAddsTraceEvents(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	ctx, span := provider.Tracer("test").Start(context.Background(), "execution")
	executor := &supervisedExecutor{id: "executor-test", bedID: "bed-test"}

	executor.recordTransportFailure(ctx, "wait", "process-test", 1, 3, true, errors.New("read: EOF"))
	executor.recordTransportRecovered(ctx, "wait", "process-test", 2)
	span.End()

	ended := recorder.Ended()
	if len(ended) != 1 {
		t.Fatalf("ended spans = %d, want 1", len(ended))
	}
	events := ended[0].Events()
	if len(events) != 2 || events[0].Name != "executor.transport.failure" || events[1].Name != "executor.transport.recovered" {
		t.Fatalf("transport events = %+v", events)
	}
}
