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
	"fmt"
	"strings"
	"time"

	"github.com/qiankunli/hostel/internal/tracing"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const (
	lifecycleActivate = "activate"
	lifecyclePersist  = "persist"
	lifecycleEvict    = "evict"

	lifecycleSuccess  = "success"
	lifecycleError    = "error"
	lifecycleCanceled = "canceled"
)

// LifecycleStage is one bounded step in a bed lifecycle action.
type LifecycleStage struct {
	Name     string
	Result   string
	Duration time.Duration
}

// LifecycleRecord is one completed bed lifecycle action.
// It belongs to the bed domain; HTTP and metric adapters decide how to encode it.
type LifecycleRecord struct {
	Action      string
	Result      string
	Source      string
	Trigger     string
	StartedAt   time.Time
	FinishedAt  time.Time
	Duration    time.Duration
	Stages      []LifecycleStage
	FailedStage string
	Error       string
}

// LifecycleObservations is the bounded diagnostic state attached to a
// resident bed. It is deliberately separate from Status: Status is the
// scheduler-facing current state established by BeginOperation.
type LifecycleObservations struct {
	LastActivation *LifecycleRecord
	LastPersist    *LifecycleRecord
}

func (b *Bed) recordLifecycle(record LifecycleRecord) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch record.Action {
	case lifecycleActivate:
		b.lastActivation = cloneLifecycleRecord(&record)
	case lifecyclePersist:
		b.lastPersist = cloneLifecycleRecord(&record)
	}
}

// Lifecycle returns a deep copy so callers never share mutable stage slices.
func (b *Bed) Lifecycle() LifecycleObservations {
	b.mu.Lock()
	defer b.mu.Unlock()
	return LifecycleObservations{
		LastActivation: cloneLifecycleRecord(b.lastActivation),
		LastPersist:    cloneLifecycleRecord(b.lastPersist),
	}
}

func cloneLifecycleRecord(in *LifecycleRecord) *LifecycleRecord {
	if in == nil {
		return nil
	}
	out := *in
	out.Stages = append([]LifecycleStage(nil), in.Stages...)
	return &out
}

type lifecycleRecorder struct {
	ctx         context.Context
	span        oteltrace.Span
	bedID       string
	action      string
	source      string
	trigger     string
	startedAt   time.Time
	stages      []LifecycleStage
	failedStage string
}

func beginLifecycle(ctx context.Context, bedID, action string) *lifecycleRecorder {
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, span := tracing.Tracer().Start(ctx, "hostel.bed."+action, oteltrace.WithAttributes(
		attribute.String("hostel.bed.id", bedID),
		attribute.String("hostel.lifecycle.action", action),
	))
	return &lifecycleRecorder{
		ctx:       ctx,
		span:      span,
		bedID:     bedID,
		action:    action,
		startedAt: time.Now(),
	}
}

// stage logs entry before running fn so a hung stage remains visible even
// when no completion summary is ever emitted.
func (r *lifecycleRecorder) stage(name string, fn func() error) error {
	start := time.Now()
	r.span.AddEvent("stage.start", oteltrace.WithAttributes(attribute.String("hostel.lifecycle.stage", name)))
	tracing.Printf(r.ctx,
		"hostel bed lifecycle stage: bed=%s action=%s stage=%s event=start source=%s trigger=%s",
		r.bedID, r.action, name, r.source, r.trigger,
	)
	err := fn()
	result := lifecycleSuccess
	if err != nil {
		result = lifecycleError
		r.failedStage = name
	}
	stage := LifecycleStage{Name: name, Result: result, Duration: time.Since(start)}
	r.stages = append(r.stages, stage)
	r.span.AddEvent("stage.end", oteltrace.WithAttributes(
		attribute.String("hostel.lifecycle.stage", name),
		attribute.String("hostel.lifecycle.stage.result", result),
		attribute.Int64("hostel.lifecycle.stage.duration_ms", stage.Duration.Milliseconds()),
	))
	tracing.Printf(r.ctx,
		"hostel bed lifecycle stage: bed=%s action=%s stage=%s event=finish result=%s duration_ms=%d source=%s trigger=%s error=%q",
		r.bedID, r.action, name, result, stage.Duration.Milliseconds(), r.source, r.trigger, errorText(err),
	)
	return err
}

func (r *lifecycleRecorder) finish(result string, err error) LifecycleRecord {
	finishedAt := time.Now()
	record := LifecycleRecord{
		Action:      r.action,
		Result:      result,
		Source:      r.source,
		Trigger:     r.trigger,
		StartedAt:   r.startedAt,
		FinishedAt:  finishedAt,
		Duration:    finishedAt.Sub(r.startedAt),
		Stages:      append([]LifecycleStage(nil), r.stages...),
		FailedStage: r.failedStage,
		Error:       errorText(err),
	}
	r.span.SetAttributes(
		attribute.String("hostel.lifecycle.result", result),
		attribute.String("hostel.lifecycle.source", r.source),
		attribute.String("hostel.lifecycle.trigger", r.trigger),
		attribute.String("hostel.lifecycle.failed_stage", r.failedStage),
		attribute.Int64("hostel.lifecycle.duration_ms", record.Duration.Milliseconds()),
	)
	if err != nil {
		r.span.RecordError(err)
		r.span.SetStatus(codes.Error, err.Error())
	}
	tracing.Printf(r.ctx,
		"hostel bed lifecycle summary: bed=%s action=%s result=%s source=%s trigger=%s duration_ms=%d failed_stage=%s stages=%q error=%q",
		r.bedID, r.action, result, r.source, r.trigger, record.Duration.Milliseconds(),
		r.failedStage, formatLifecycleStages(r.stages), record.Error,
	)
	r.span.End()
	return record
}

func lifecycleResult(err error) string {
	if err != nil {
		return lifecycleError
	}
	return lifecycleSuccess
}

func formatLifecycleStages(stages []LifecycleStage) string {
	parts := make([]string, 0, len(stages))
	for _, stage := range stages {
		parts = append(parts, fmt.Sprintf("%s:%s:%dms", stage.Name, stage.Result, stage.Duration.Milliseconds()))
	}
	return strings.Join(parts, ",")
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
