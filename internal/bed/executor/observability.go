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

	"github.com/qiankunli/hostel/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	oteltrace "go.opentelemetry.io/otel/trace"
)

func (e *supervisedExecutor) recordTransportFailure(
	ctx context.Context,
	operation, processID string,
	attempt, maxAttempts int,
	willRetry bool,
	err error,
) {
	if ctx == nil {
		ctx = context.Background()
	}
	errorMessage := ""
	if err != nil {
		errorMessage = err.Error()
	}
	oteltrace.SpanFromContext(ctx).AddEvent("executor.transport.failure", oteltrace.WithAttributes(
		attribute.String("hostel.bed.id", e.bedID),
		attribute.String("hostel.executor.id", e.id),
		attribute.String("hostel.executor.backend", e.Backend()),
		attribute.String("hostel.process.id", processID),
		attribute.String("hostel.executor.transport.operation", operation),
		attribute.Int("hostel.executor.transport.attempt", attempt),
		attribute.Int("hostel.executor.transport.max_attempts", maxAttempts),
		attribute.Bool("hostel.executor.transport.will_retry", willRetry),
		attribute.String("hostel.executor.transport.error", errorMessage),
	))
	tracing.WarnContext(ctx, "hostel executor transport failure",
		"bed", e.bedID,
		"executor_id", e.id,
		"executor_backend", e.Backend(),
		"process_id", processID,
		"operation", operation,
		"attempt", attempt,
		"max_attempts", maxAttempts,
		"will_retry", willRetry,
		"error", errorMessage,
	)
}

func (e *supervisedExecutor) recordTransportRecovered(ctx context.Context, operation, processID string, attempt int) {
	if ctx == nil {
		ctx = context.Background()
	}
	oteltrace.SpanFromContext(ctx).AddEvent("executor.transport.recovered", oteltrace.WithAttributes(
		attribute.String("hostel.bed.id", e.bedID),
		attribute.String("hostel.executor.id", e.id),
		attribute.String("hostel.executor.backend", e.Backend()),
		attribute.String("hostel.process.id", processID),
		attribute.String("hostel.executor.transport.operation", operation),
		attribute.Int("hostel.executor.transport.attempt", attempt),
	))
	tracing.InfoContext(ctx, "hostel executor transport recovered",
		"bed", e.bedID,
		"executor_id", e.id,
		"executor_backend", e.Backend(),
		"process_id", processID,
		"operation", operation,
		"attempt", attempt,
	)
}
