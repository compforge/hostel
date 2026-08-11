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
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/amenity"
	"github.com/qiankunli/hostel/internal/executor"
	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/store"
)

type closeTestAmenity struct {
	released []string
	revoked  []string
}

func (*closeTestAmenity) Name() string  { return "close-test" }
func (*closeTestAmenity) State() string { return amenity.StateIdle }
func (*closeTestAmenity) AcquireTenant(string, string) (amenity.Tenant, error) {
	return nil, nil
}
func (a *closeTestAmenity) ReleaseTenant(bedID string) error {
	a.released = append(a.released, bedID)
	return nil
}
func (a *closeTestAmenity) RevokeBedSecrets(bedID string) {
	a.revoked = append(a.revoked, bedID)
}

func newTestManager(t *testing.T) *Manager {
	t.Helper()
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestManagerFallsBackToShWhenBashMissing(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", filepath.Join(root, "missing-bash"), isolation.New("dorm", root), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if filepath.Base(m.shellPath) != "sh" {
		t.Fatalf("shellPath=%q, want sh fallback", m.shellPath)
	}
	b, err := m.Ensure(context.Background(), "default")
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	result, err := m.RunForeground(context.Background(), b, "printf fallback", "", nil, 0, func(output ExecutionOutput) { out.WriteString(output.Text) })
	if err != nil || result.Process.Kind != executor.ProcessExited || result.Process.ExitCode != 0 || strings.TrimSpace(out.String()) != "fallback" {
		t.Fatalf("fallback exec: result=%+v err=%v output=%q", result, err, out.String())
	}
}

func TestResolveDefaultBedAndValidation(t *testing.T) {
	m := newTestManager(t)

	b, err := m.Ensure(context.Background(), "") // empty → default
	if err != nil || b.ID != "default" {
		t.Fatalf("Resolve(\"\") = %v, %v", b, err)
	}
	if _, err := m.Ensure(context.Background(), "bad id!"); err == nil {
		t.Fatal("Resolve invalid id: want error")
	}
	b2, _ := m.Ensure(context.Background(), "conv-123")
	if b2.ID != "conv-123" || b2.Workspace() == b.Workspace() {
		t.Fatalf("distinct bed expected, got %+v", b2)
	}
	if got := m.ResidentBedCount(); got != 2 {
		t.Fatalf("ResidentBedCount = %d, want 2", got)
	}
}

func TestManagerCloseReleasesBedAmenityState(t *testing.T) {
	root := t.TempDir()
	registry := amenity.NewRegistry()
	facility := &closeTestAmenity{}
	registry.Register(facility)
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), registry, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), "close-amenity"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(facility.released) != 1 || facility.released[0] != "close-amenity" {
		t.Fatalf("released tenants = %v", facility.released)
	}
	if len(facility.revoked) != 1 || facility.revoked[0] != "close-amenity" {
		t.Fatalf("revoked secrets = %v", facility.revoked)
	}
}

func TestResidentBedCountDoesNotWaitForManagerLock(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Ensure(context.Background(), "conv-atomic"); err != nil {
		t.Fatal(err)
	}

	m.mu.Lock()
	done := make(chan int64, 1)
	go func() { done <- m.ResidentBedCount() }()
	select {
	case got := <-done:
		if got != 1 {
			t.Fatalf("ResidentBedCount = %d, want 1", got)
		}
	case <-time.After(time.Second):
		t.Fatal("ResidentBedCount waited for manager lock")
	}
	m.mu.Unlock()
}

func TestResidentBedCountTracksRemoval(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.Ensure(context.Background(), "evict-me"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Ensure(context.Background(), "purge-me"); err != nil {
		t.Fatal(err)
	}
	if got := m.ResidentBedCount(); got != 2 {
		t.Fatalf("ResidentBedCount before removal = %d, want 2", got)
	}
	if evicted, err := m.Evict(context.Background(), "evict-me"); err != nil || !evicted {
		t.Fatalf("Evict: evicted=%v err=%v", evicted, err)
	}
	if err := m.Purge(context.Background(), "purge-me"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if got := m.ResidentBedCount(); got != 0 {
		t.Fatalf("ResidentBedCount after removal = %d, want 0", got)
	}
}

func TestLifecycleObservations(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	var logs bytes.Buffer
	previousWriter := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(previousWriter) })

	b, err := m.Ensure(context.Background(), "observed")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	lifecycle := b.Lifecycle()
	if lifecycle.LastInitialization == nil || lifecycle.LastInitialization.Result != "success" || lifecycle.LastInitialization.Source != "fresh" {
		t.Fatalf("LastInitialization = %+v", lifecycle.LastInitialization)
	}
	if got := lifecycleStageNames(lifecycle.LastInitialization); got != "stage_in_bedfs,prepare_bedfs,prepare_resident" {
		t.Fatalf("initialization stages = %q", got)
	}

	if err := m.Checkpoint(context.Background(), b.ID); err != nil {
		t.Fatalf("Checkpoint: %v", err)
	}
	lifecycle = b.Lifecycle()
	if lifecycle.LastPersist == nil || lifecycle.LastPersist.Result != "success" || lifecycle.LastPersist.Trigger != "checkpoint" {
		t.Fatalf("LastPersist success = %+v", lifecycle.LastPersist)
	}
	if got := lifecycleStageNames(lifecycle.LastPersist); got != "wait_persist_lock,prepare_snapshot,persist_store,commit_watermark" {
		t.Fatalf("persist stages = %q", got)
	}

	fs.fail = true
	if err := m.Checkpoint(context.Background(), b.ID); err == nil {
		t.Fatal("Checkpoint failure: want error")
	}
	lifecycle = b.Lifecycle()
	if lifecycle.LastPersist == nil || lifecycle.LastPersist.Result != "error" || lifecycle.LastPersist.FailedStage != "persist_store" {
		t.Fatalf("LastPersist failure = %+v", lifecycle.LastPersist)
	}
	fs.fail = false
	if ok, err := m.Evict(context.Background(), b.ID); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}
	if !strings.Contains(logs.String(), "action=initialize stage=stage_in_bedfs event=start") ||
		!strings.Contains(logs.String(), "action=persist result=error") ||
		!strings.Contains(logs.String(), "action=evict result=success") {
		t.Fatalf("lifecycle logs missing structured context:\n%s", logs.String())
	}
}

func lifecycleStageNames(record *LifecycleRecord) string {
	names := make([]string, 0, len(record.Stages))
	for _, stage := range record.Stages {
		names = append(names, stage.Name)
	}
	return strings.Join(names, ",")
}

func TestForegroundShellPersistsState(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "default")

	sh, err := m.ForegroundShell(b)
	if err != nil {
		t.Fatalf("ForegroundShell: %v", err)
	}
	// Same shell returned on second call (state persistence).
	if sh2, _ := m.ForegroundShell(b); sh2 != sh {
		t.Fatal("ForegroundShell should reuse the same session")
	}

	ctx := context.Background()
	if _, err := sh.Run(ctx, "export HOSTEL_TEST=42", nil); err != nil {
		t.Fatalf("Run export: %v", err)
	}
	var out strings.Builder
	res, err := sh.Run(ctx, "echo v=$HOSTEL_TEST", func(l string) { out.WriteString(l) })
	if err != nil {
		t.Fatalf("Run echo: %v", err)
	}
	if res.ExitCode != 0 || !strings.Contains(out.String(), "v=42") {
		t.Fatalf("state not preserved: exit=%d out=%q", res.ExitCode, out.String())
	}
}

// TestBedFileIsolation is a Linux-safe end-to-end check of the bed contract:
// commands in one bed share a writable filesystem, while another bed cannot
// observe it (each bed is rooted at its own data directory).
func TestBedFileIsolation(t *testing.T) {
	m := newTestManager(t)
	a, _ := m.Ensure(context.Background(), "session-a")
	b, _ := m.Ensure(context.Background(), "session-b")
	ctx := context.Background()
	if result, err := m.RunForeground(ctx, a, "printf alpha > shared.txt", "", nil, 0, nil); err != nil || result.Process.ExitCode != 0 {
		t.Fatalf("write in session-a: result=%+v err=%v", result, err)
	}
	var out strings.Builder
	if result, err := m.RunForeground(ctx, a, "cat shared.txt", "", nil, 0, func(output ExecutionOutput) { out.WriteString(output.Text) }); err != nil || result.Process.ExitCode != 0 || strings.TrimSpace(out.String()) != "alpha" {
		t.Fatalf("read back in session-a: result=%+v err=%v out=%q", result, err, out.String())
	}
	// A missing file is the observable cross-session isolation guarantee.
	if result, err := m.RunForeground(ctx, b, "test ! -e shared.txt", "", nil, 0, nil); err != nil || result.Process.ExitCode != 0 {
		t.Fatalf("session-b observed session-a file: result=%+v err=%v", result, err)
	}
}

func TestShellExitCode(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "default")
	sh, _ := m.ForegroundShell(b)
	// Use a subshell so a non-zero exit doesn't kill the persistent session.
	res, err := sh.Run(context.Background(), `sh -c "exit 7"`, nil)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7", res.ExitCode)
	}
	// Session survives and still works afterward.
	if r2, err := sh.Run(context.Background(), "true", nil); err != nil || r2.ExitCode != 0 {
		t.Fatalf("session dead after non-zero exit: %v %+v", err, r2)
	}
}

// TestRunForegroundIsolatesFailure locks in the fresh-process foreground model:
// a one-shot command that carries `set -e` + a failing step (the shape of AS
// skill batch-sync) must return its exit code and leave the bed fully usable.
// Before the fix this ran in the shared foreground shell, where set -e / exit
// tore the whole session down ("shell: session exited during run").
func TestRunForegroundIsolatesFailure(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "default")
	ctx := context.Background()

	result, err := m.RunForeground(ctx, b, "set -euo pipefail\nfalse\necho unreached", "", nil, 0, nil)
	if err != nil {
		t.Fatalf("RunForeground: %v", err)
	}
	if result.Process.Kind != executor.ProcessExited || result.Process.ExitCode == 0 {
		t.Fatal("want non-zero exit from set -e failure, got 0")
	}

	// The bed is still fully usable for the next command (no session was killed).
	var out strings.Builder
	result2, err := m.RunForeground(ctx, b, "echo alive", "", nil, 0, func(output ExecutionOutput) { out.WriteString(output.Text) })
	if err != nil || result2.Process.ExitCode != 0 || !strings.Contains(out.String(), "alive") {
		t.Fatalf("bed unusable after set -e command: result=%+v err=%v out=%q", result2, err, out.String())
	}

	// Explicit exit code propagates.
	if result3, err := m.RunForeground(ctx, b, "exit 7", "", nil, 0, nil); err != nil || result3.Process.ExitCode != 7 {
		t.Fatalf("exit code not propagated: result=%+v err=%v", result3, err)
	}
}

// TestEvictProtectsAndTeardownKillsInflightForeground locks in Executor
// ownership of an in-flight process tree.
// Explicit eviction is cooperative and refuses active work. The teardown path
// still kills every process once lifecycle ownership has been claimed.
func TestEvictProtectsAndTeardownKillsInflightForeground(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "conv-kill")

	started := make(chan struct{})
	done := make(chan ExecutionResult, 1)
	go func() {
		result, _ := m.RunForeground(context.Background(), b, `sleep 30 & child=$!; echo up; wait "$child"`, "", nil, 0, func(ExecutionOutput) {
			select {
			case <-started:
			default:
				close(started)
			}
		})
		done <- result
	}()
	select {
	case <-started: // the sleep is running
	case <-time.After(5 * time.Second):
		t.Fatal("foreground command did not start")
	}
	if got := b.Inflight(); got != 1 {
		t.Fatalf("active operations = %d, want 1", got)
	}

	if ok, err := m.Evict(context.Background(), "conv-kill"); err != nil || ok {
		t.Fatalf("Evict active bed: ok=%v err=%v", ok, err)
	}
	m.teardown(b)
	select {
	case result := <-done:
		if result.Process.Kind != executor.ProcessSignaled || result.Cause != CauseBedTeardown {
			t.Fatalf("killed command result = %+v", result)
		}
		if got := b.Inflight(); got != 0 {
			t.Fatalf("active operations after teardown = %d, want 0", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("in-flight foreground command escaped bed teardown")
	}
}

func TestBackgroundCommandAndLogs(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "default")

	execution, err := m.StartExecution(nil, b, ExecutionBackground, "printf 'a\\nb\\nc\\n'", "", nil, 0, nil, nil)
	if err != nil {
		t.Fatalf("StartExecution: %v", err)
	}
	result := execution.Wait()
	st := execution.Status()
	if st.Running || st.Result == nil || result.Process.ExitCode != 0 {
		t.Fatalf("status after wait: %+v", st)
	}
	output, next, running, truncated := execution.Logs(-1)
	if running || truncated {
		t.Fatalf("Logs: running=%v truncated=%v", running, truncated)
	}
	if executionOutputText(output) != "a\nb\nc\n" || next != 2 {
		t.Fatalf("Logs output=%+v next=%d", output, next)
	}
	// Incremental read from cursor 0 → lines after line 0.
	inc, _, _, _ := execution.Logs(0)
	if executionOutputText(inc) != "b\nc\n" {
		t.Fatalf("incremental Logs = %+v", inc)
	}
}

func executionOutputText(output []ExecutionOutput) string {
	var text strings.Builder
	for _, item := range output {
		text.WriteString(item.Text)
	}
	return text.String()
}

func TestDeleteBedReleasesAndRemoves(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "conv-x")
	_, _ = m.ForegroundShell(b)
	if ok, err := m.Evict(context.Background(), "conv-x"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}
	if _, ok := m.Get("conv-x"); ok {
		t.Fatal("bed should be gone after Delete")
	}
}

func TestCollectExpiredSkipsDefault(t *testing.T) {
	m := newTestManager(t)
	m.SetBedIdleTTL(time.Millisecond)
	_, _ = m.Ensure(context.Background(), "default")
	_, _ = m.Ensure(context.Background(), "conv-idle")
	time.Sleep(10 * time.Millisecond)
	reaped := m.CollectExpired(context.Background(), time.Now())
	if len(reaped) != 1 || reaped[0] != "conv-idle" {
		t.Fatalf("CollectExpired reaped %v, want [conv-idle]", reaped)
	}
	if _, ok := m.Get("default"); !ok {
		t.Fatal("default bed must never be reaped")
	}
}

func TestOperationExtendsExpiryAndBlocksExpiredReap(t *testing.T) {
	m := newTestManager(t)
	idleTTL := 50 * time.Millisecond
	execTimeout := 500 * time.Millisecond
	m.SetBedIdleTTL(idleTTL)
	b, err := m.Ensure(context.Background(), "conv-running")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got := b.Activity(); got != ActivityIdle {
		t.Fatalf("new bed activity = %q, want idle", got)
	}

	startedAt := time.Now()
	finishOperation, err := m.BeginOperation(b, OpExec, execTimeout)
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	retainUntil := b.RetainUntil()
	if want := startedAt.Add(execTimeout + idleTTL); retainUntil.Before(want) {
		t.Fatalf("retained_until = %s, want >= %s", retainUntil, want)
	}
	if got := b.Inflight(); got != 1 {
		t.Fatalf("active operations = %d, want 1", got)
	}
	if got := b.Activity(); got != ActivityActive {
		t.Fatalf("operation activity = %q, want active", got)
	}
	finishShortOperation, err := m.BeginOperation(b, OpExec, time.Millisecond)
	if err != nil {
		t.Fatalf("BeginOperation short: %v", err)
	}
	if got := b.RetainUntil(); got.Before(retainUntil) {
		t.Fatalf("shorter operation shortened retained_until: got %s, previous %s", got, retainUntil)
	}
	finishShortOperation()

	// Even a delayed collector must not reap a command still in flight.
	if reaped := m.CollectExpired(context.Background(), retainUntil.Add(time.Hour)); len(reaped) != 0 {
		t.Fatalf("CollectExpired reaped running bed: %v", reaped)
	}
	finishOperation()
	if got := b.Inflight(); got != 0 {
		t.Fatalf("active operations after finish = %d, want 0", got)
	}
	if got := b.Activity(); got != ActivityIdle {
		t.Fatalf("finished operation activity = %q, want idle", got)
	}
	if reaped := m.CollectExpired(context.Background(), retainUntil.Add(time.Hour)); len(reaped) != 1 || reaped[0] != b.ID {
		t.Fatalf("CollectExpired after finish = %v, want [%s]", reaped, b.ID)
	}
}

func TestMaxBedsCap(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 2, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Ensure(context.Background(), "a"); err != nil {
		t.Fatalf("bed a: %v", err)
	}
	if _, err := m.Ensure(context.Background(), "b"); err != nil {
		t.Fatalf("bed b: %v", err)
	}
	// Cap hit: a third bed is refused with the sentinel.
	if _, err := m.Ensure(context.Background(), "c"); !errors.Is(err, ErrBedLimit) {
		t.Fatalf("bed c: want ErrBedLimit, got %v", err)
	}
	// Existing beds still resolve.
	if _, err := m.Ensure(context.Background(), "a"); err != nil {
		t.Fatalf("existing bed a after cap: %v", err)
	}
	// The default bed is exempt — the single-tenant path never breaks.
	if _, err := m.Ensure(context.Background(), ""); err != nil {
		t.Fatalf("default bed exempt: %v", err)
	}
	// Evicting frees a slot.
	if ok, err := m.Evict(context.Background(), "a"); err != nil || !ok {
		t.Fatalf("evict a: ok=%v err=%v", ok, err)
	}
	if _, err := m.Ensure(context.Background(), "c"); err != nil {
		t.Fatalf("bed c after free slot: %v", err)
	}
}

func TestMaxPinnedBedsAdmission(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 3, newFakeStore())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.SetMaxPinnedBeds(1); err != nil {
		t.Fatalf("SetMaxPinnedBeds: %v", err)
	}
	oldGuest, _ := m.Ensure(context.Background(), "old-guest")
	finishInitialOldGuest, err := m.BeginOperation(oldGuest, OpExec, 0)
	if err != nil {
		t.Fatalf("initial old guest operation: %v", err)
	}
	finishInitialOldGuest() // idle but dirty: this carrier still owns the bed.
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("pinned beds after operation = %d, want 1", got)
	}

	// A dirty resident bed is an existing guest. Its operation must be admitted
	// even at pressure because its latest data is still on this carrier.
	finishOldGuest, err := m.BeginOperation(oldGuest, OpExec, 0)
	if err != nil {
		t.Fatalf("initialize resident old guest: %v", err)
	}
	finishOldGuest2, err := m.BeginOperation(oldGuest, OpFile, 0)
	if err != nil {
		t.Fatalf("second old guest operation: %v", err)
	}
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("two operations changed pinned count to %d", got)
	}

	_, err = m.Ensure(context.Background(), "new-guest")
	var insufficient *InsufficientBedError
	if !errors.Is(err, ErrInsufficientBed) || !errors.As(err, &insufficient) {
		t.Fatalf("admit new guest: want InsufficientBedError, got %v", err)
	}
	if insufficient.PinnedBeds != 1 || insufficient.MaxPinnedBeds != 1 ||
		insufficient.ResidentBeds != 1 || insufficient.MaxBeds != 3 {
		t.Fatalf("insufficient capacity = %+v", insufficient)
	}
	if !m.BedPressure() {
		t.Fatal("manager did not report bed pressure at pinned capacity")
	}
	// The default bed is outside both capacity limits.
	defaultBed, _ := m.Ensure(context.Background(), "")
	finishDefault, err := m.BeginOperation(defaultBed, OpExec, 0)
	if err != nil {
		t.Fatalf("initialize default bed: %v", err)
	}
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("default bed changed pinned count to %d", got)
	}
	finishDefault()

	finishOldGuest()
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("finishing one operation changed pinned count to %d", got)
	}
	finishOldGuest2()
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("dirty idle old guest should remain pinned, got %d", got)
	}
	if done := m.PersistDirty(context.Background()); len(done) == 0 {
		t.Fatal("dirty old guest was not persisted")
	}
	if got := m.PinnedBedCount(); got != 0 || m.BedPressure() {
		t.Fatalf("persist did not release capacity: pinned=%d pressure=%v", got, m.BedPressure())
	}
	newGuest, err := m.Ensure(context.Background(), "new-guest")
	if err != nil {
		t.Fatalf("admit new guest after release: %v", err)
	}
	finishNewGuest, err := m.BeginOperation(newGuest, OpExec, 0)
	if err != nil {
		t.Fatalf("initialize new guest after admission: %v", err)
	}
	finishNewGuest()
}

func TestMaxPinnedBedsResolution(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 3, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	for _, tc := range []struct {
		configured int
		want       int
	}{{0, 3}, {2, 2}, {3, 3}, {4, 3}} {
		if err := m.SetMaxPinnedBeds(tc.configured); err != nil || m.MaxPinnedBeds() != tc.want {
			t.Fatalf("SetMaxPinnedBeds(%d) = limit %d err %v, want %d", tc.configured, m.MaxPinnedBeds(), err, tc.want)
		}
	}
	if err := m.SetMaxPinnedBeds(-1); err == nil {
		t.Fatal("SetMaxPinnedBeds(-1): want error")
	}

	unlimitedRoot := t.TempDir()
	unlimited, err := NewManager(unlimitedRoot, "default", "/bin/bash", isolation.New("dorm", unlimitedRoot), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager unlimited: %v", err)
	}
	if err := unlimited.SetMaxPinnedBeds(0); err != nil || unlimited.MaxPinnedBeds() != 0 {
		t.Fatalf("unlimited defaults = limit %d err %v, want 0", unlimited.MaxPinnedBeds(), err)
	}
	if err := unlimited.SetMaxPinnedBeds(10); err != nil {
		t.Fatalf("finite pinned cap with unlimited resident cap: %v", err)
	}
}

func TestMaxPinnedBedsConcurrentAdmission(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 3, newFakeStore())
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.SetMaxPinnedBeds(1); err != nil {
		t.Fatalf("SetMaxPinnedBeds: %v", err)
	}
	a, _ := m.Ensure(context.Background(), "a")
	b, _ := m.Ensure(context.Background(), "b")

	start := make(chan struct{})
	type result struct {
		finish func()
		err    error
	}
	results := make(chan result, 2)
	for _, candidate := range []*Bed{a, b} {
		go func(candidate *Bed) {
			<-start
			finish, err := m.BeginOperation(candidate, OpExec, 0)
			results <- result{finish: finish, err: err}
		}(candidate)
	}
	close(start)

	admitted, rejected := 0, 0
	var finishes []func()
	for range 2 {
		result := <-results
		switch {
		case result.err == nil:
			admitted++
			finishes = append(finishes, result.finish)
		case errors.Is(result.err, ErrInsufficientBed):
			rejected++
		default:
			t.Fatalf("unexpected admission error: %v", result.err)
		}
	}
	if admitted != 1 || rejected != 1 || m.PinnedBedCount() != 1 {
		t.Fatalf("concurrent admission: admitted=%d rejected=%d pinned=%d", admitted, rejected, m.PinnedBedCount())
	}
	for _, finish := range finishes {
		finish()
	}
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("dirty bed after finish = %d, want 1", got)
	}
	m.PersistDirty(context.Background())
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("pinned beds after persist = %d, want 0", got)
	}
}

func TestBedPressureStartsBeforeHardCapacity(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 12, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.SetMaxPinnedBeds(10); err != nil {
		t.Fatalf("SetMaxPinnedBeds: %v", err)
	}

	beds := make([]*Bed, 11)
	for i := range beds {
		beds[i], err = m.Ensure(context.Background(), fmt.Sprintf("bed-%d", i))
		if err != nil {
			t.Fatalf("Ensure bed %d: %v", i, err)
		}
	}
	var finishes []func()
	defer func() {
		for _, finish := range finishes {
			finish()
		}
	}()
	for i := 0; i < 8; i++ {
		finish, beginErr := m.BeginOperation(beds[i], OpExec, 0)
		if beginErr != nil {
			t.Fatalf("BeginOperation bed %d: %v", i, beginErr)
		}
		finishes = append(finishes, finish)
	}
	if !m.BedPressure() {
		t.Fatal("8/10 pinned beds should report early pressure")
	}
	for i := 8; i < 10; i++ {
		finish, beginErr := m.BeginOperation(beds[i], OpExec, 0)
		if beginErr != nil {
			t.Fatalf("reserved capacity rejected bed %d: %v", i, beginErr)
		}
		finishes = append(finishes, finish)
	}
	if _, err := m.BeginOperation(beds[10], OpExec, 0); !errors.Is(err, ErrInsufficientBed) {
		t.Fatalf("operation beyond hard capacity: want ErrInsufficientBed, got %v", err)
	}
}

func TestNoopDataDoesNotRemainPinned(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 1, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.SetMaxPinnedBeds(1); err != nil {
		t.Fatalf("SetMaxPinnedBeds: %v", err)
	}
	b, _ := m.Ensure(context.Background(), "noop-bed")
	finish, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("pinned during operation = %d, want 1", got)
	}
	finish()
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("noop data kept bed pinned after operation: %d", got)
	}
}

func TestStoreSyncTriggerReleasesPinnedBed(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 1, fs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go m.RunStoreSync(ctx, 0)

	b, _ := m.Ensure(context.Background(), "sync-trigger")
	finish, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	finish()

	deadline := time.Now().Add(time.Second)
	for m.PinnedBedCount() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("store trigger did not release pinned bed: %d", got)
	}
	if fs.generation("sync-trigger") == 0 {
		t.Fatal("store trigger did not persist bed")
	}
}

func TestPurgePinnedBedReleasesCapacity(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 2, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if err := m.SetMaxPinnedBeds(1); err != nil {
		t.Fatalf("SetMaxPinnedBeds: %v", err)
	}
	a, _ := m.Ensure(context.Background(), "a")
	finish, err := m.BeginOperation(a, OpExec, 0)
	if err != nil {
		t.Fatalf("initialize a: %v", err)
	}
	if err := m.Purge(context.Background(), "a"); err != nil {
		t.Fatalf("Purge a: %v", err)
	}
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("pinned beds after purge = %d, want 0", got)
	}
	finish() // must not double-decrement after the resident entry is gone.
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("pinned beds after late finish = %d, want 0", got)
	}
}

// fakeStore is an in-memory Store for lifecycle tests.
type fakeStore struct {
	mu                 sync.Mutex
	snaps              map[string][]byte // bedID → marker file content
	gens               map[string]int64  // bedID → generation of the stored snapshot
	fail               bool              // force Persist to fail
	lastDeleteErr      error
	lastDeleteValue    any
	lastDeleteDeadline bool
}

type blockingStore struct {
	*fakeStore
	started chan struct{}
	release chan struct{}
}

type initializationBlockingStore struct {
	*fakeStore
	started   chan struct{}
	release   chan struct{}
	statError error
	mu        sync.Mutex
	statCalls int
}

func (s *initializationBlockingStore) Stat(ctx context.Context, id string) (*store.SnapshotInfo, error) {
	s.mu.Lock()
	s.statCalls++
	s.mu.Unlock()
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.release:
	}
	if s.statError != nil {
		return nil, s.statError
	}
	return s.fakeStore.Stat(ctx, id)
}

func (s *initializationBlockingStore) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statCalls
}

func (s *blockingStore) Persist(ctx context.Context, id, dir string, generation int64) error {
	select {
	case s.started <- struct{}{}:
	default:
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.release:
		return s.fakeStore.Persist(ctx, id, dir, generation)
	}
}

func newFakeStore() *fakeStore {
	return &fakeStore{snaps: map[string][]byte{}, gens: map[string]int64{}}
}

func (f *fakeStore) Name() string { return "fake" }
func (f *fakeStore) Stat(_ context.Context, id string) (*store.SnapshotInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.snaps[id]
	if !ok {
		return nil, nil
	}
	return &store.SnapshotInfo{Generation: f.gens[id], Bytes: int64(len(data))}, nil
}
func (f *fakeStore) Restore(_ context.Context, id, dir string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(dir, "data", "workspace"), 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "data", "workspace", "restored.txt"), f.snaps[id], 0o644)
}
func (f *fakeStore) Persist(_ context.Context, id, dir string, generation int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return errors.New("fake persist failure")
	}
	// dir is the bed dir: meta.json + data/workspace/. Mimic that shape.
	data, _ := os.ReadFile(filepath.Join(dir, "data", "workspace", "data.txt"))
	f.snaps[id] = data
	f.gens[id] = generation
	return nil
}

func (f *fakeStore) Delete(ctx context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastDeleteErr = ctx.Err()
	f.lastDeleteValue = ctx.Value(purgeContextKey{})
	_, f.lastDeleteDeadline = ctx.Deadline()
	delete(f.snaps, id)
	delete(f.gens, id)
	return nil
}

// generation returns the stored snapshot generation (0 = no snapshot).
func (f *fakeStore) generation(id string) int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gens[id]
}

func TestInitializeBedRunsStoreWorkAsynchronouslyAndReservesCapacity(t *testing.T) {
	root := t.TempDir()
	backend := &initializationBlockingStore{
		fakeStore: newFakeStore(),
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 1, backend)
	if err != nil {
		t.Fatal(err)
	}

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	status, err := m.InitializeBed(requestCtx, "one")
	if err != nil {
		t.Fatal(err)
	}
	cancelRequest()
	if status.Phase != PhaseInitializing || status.Readiness.Ready {
		t.Fatalf("accepted status = %+v", status)
	}
	select {
	case <-backend.started:
	case <-time.After(time.Second):
		t.Fatal("Store.Stat did not start")
	}
	if _, ok := m.Get("one"); ok {
		t.Fatal("initializing bed was published as resident")
	}
	secondStatus, err := m.InitializeBed(context.Background(), "one")
	if err != nil || secondStatus.StartedAt != status.StartedAt {
		t.Fatalf("same-id initialization = %+v, %v", secondStatus, err)
	}
	if _, err := m.InitializeBed(context.Background(), "two"); !errors.Is(err, ErrBedLimit) {
		t.Fatalf("second bed during initialization = %v, want ErrBedLimit", err)
	}
	close(backend.release)

	readyCtx, cancelReady := context.WithTimeout(context.Background(), time.Second)
	defer cancelReady()
	resident, err := m.Ensure(readyCtx, "one")
	if err != nil {
		t.Fatal(err)
	}
	if resident.ID != "one" || backend.calls() != 1 || m.ResidentBedCount() != 1 {
		t.Fatalf("resident=%s stat_calls=%d resident_count=%d", resident.ID, backend.calls(), m.ResidentBedCount())
	}
	if status, ok := m.Initialization("one"); ok {
		t.Fatalf("completed initialization still visible: %+v", status)
	}
}

func TestInitializeBedRetainsFailureReason(t *testing.T) {
	root := t.TempDir()
	backend := &initializationBlockingStore{
		fakeStore: newFakeStore(),
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
		statError: errors.New("S3 Stat timeout"),
	}
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 1, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.InitializeBed(context.Background(), "failed"); err != nil {
		t.Fatal(err)
	}
	<-backend.started
	close(backend.release)

	deadline := time.Now().Add(time.Second)
	for {
		status, ok := m.Initialization("failed")
		if ok && status.Phase == PhaseFailed {
			if status.Readiness.Ready || status.Readiness.Reason != "SnapshotInspectionFailed" ||
				!strings.Contains(status.Readiness.Message, "S3 Stat timeout") {
				t.Fatalf("failed readiness = %+v", status.Readiness)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("failed initialization not observable: %+v, %v", status, ok)
		}
		time.Sleep(time.Millisecond)
	}
	if m.ResidentBedCount() != 0 {
		t.Fatalf("failed initialization published resident count %d", m.ResidentBedCount())
	}
}

func TestEvictLeavesLuggageAndWarmResume(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}

	// Write data into a bed, evict it → snapshot taken, local dir stays
	// behind as luggage.
	b, _ := m.Ensure(context.Background(), "conv-1")
	if err := os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	if ok, err := m.Evict(context.Background(), "conv-1"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "meta.json")); err != nil {
		t.Fatalf("luggage should remain after evict: %v", err)
	}
	if string(fs.snaps["conv-1"]) != "payload" {
		t.Fatalf("snapshot content = %q", fs.snaps["conv-1"])
	}
	meta, ok := loadMeta(b.Dir)
	if !ok || meta.LastActiveAt.IsZero() {
		t.Fatalf("luggage meta should carry LastActiveAt, got %+v (ok=%v)", meta, ok)
	}

	// Re-resolve the same bed id → warm start from luggage: the real file is
	// still there and Restore was never called (no marker).
	b2, err := m.Ensure(context.Background(), "conv-1")
	if err != nil {
		t.Fatalf("re-Resolve: %v", err)
	}
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("conservatively dirty warm resume is not pinned: %d", got)
	}
	if record := b2.Lifecycle().LastInitialization; record == nil || record.Source != "luggage" {
		t.Fatalf("warm initialization = %+v, want luggage", record)
	}
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "data.txt")); err != nil {
		t.Fatalf("warm resume lost workspace data: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "restored.txt")); err == nil {
		t.Fatal("fresh luggage must not be re-restored from the store")
	}
	m.PersistDirty(context.Background())
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("persisted warm resume stayed pinned: %d", got)
	}
}

// A luggage copy whose generation is behind the snapshot (the bed ran on
// another instance meanwhile) must be discarded and re-restored, never served.
func TestStaleLuggageDiscardedOnResume(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-s")
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("old"), 0o644)
	if ok, err := m.Evict(context.Background(), "conv-s"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}

	// Another hostel persisted a newer snapshot.
	fs.mu.Lock()
	fs.gens["conv-s"] = fs.gens["conv-s"] + 1
	fs.mu.Unlock()

	b2, err := m.Ensure(context.Background(), "conv-s")
	if err != nil {
		t.Fatalf("re-Resolve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "restored.txt")); err != nil {
		t.Fatalf("stale luggage should be replaced by a restore: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "data.txt")); err == nil {
		t.Fatal("stale luggage content must not survive")
	}
}

// Without luggage (cold resume on a different/cleaned instance), the snapshot
// is restored before serving.
func TestColdResumeRestoresFromSnapshot(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-c")
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("payload"), 0o644)
	if ok, err := m.Evict(context.Background(), "conv-c"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}
	// Simulate luggage GC / another instance: no local copy.
	if err := os.RemoveAll(b.Dir); err != nil {
		t.Fatal(err)
	}

	b2, err := m.Ensure(context.Background(), "conv-c")
	if err != nil {
		t.Fatalf("re-Resolve: %v", err)
	}
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "restored.txt")); err != nil {
		t.Fatalf("cold resume should restore from snapshot: %v", err)
	}
}

func TestPersistFailureAbortsDelete(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	fs.fail = true
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-2")
	if _, err := m.Evict(context.Background(), "conv-2"); err == nil {
		t.Fatal("Evict should fail when persist fails")
	}
	// Bed must survive: not deleted from the map, workspace intact.
	if _, ok := m.Get("conv-2"); !ok {
		t.Fatal("bed should still exist after aborted delete")
	}
	if _, err := os.Stat(b.Workspace()); err != nil {
		t.Fatalf("workspace should be intact: %v", err)
	}
}

func TestPersistDirty(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-3")
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("v1"), 0o644)

	// Freshly created bed: persistedAt == created time; touch to mark dirty.
	time.Sleep(5 * time.Millisecond)
	m.touchBed(b)
	done := m.PersistDirty(context.Background())
	if len(done) != 1 || done[0] != "conv-3" {
		t.Fatalf("PersistDirty = %v, want [conv-3]", done)
	}
	// Untouched since → not persisted again.
	if done := m.PersistDirty(context.Background()); len(done) != 0 {
		t.Fatalf("second PersistDirty = %v, want []", done)
	}
}

func TestPersistDirtyDoesNotWaitForSessionClose(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)
	b, _ := m.Ensure(context.Background(), "session-sync")
	sess, err := m.OpenSession(b, SessionKindCDP, nil)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	defer sess.Close()

	if done := m.PersistDirty(context.Background()); len(done) != 1 || done[0] != b.ID {
		t.Fatalf("PersistDirty with open session = %v, want [%s]", done, b.ID)
	}
	status := b.Status()
	if status.Sessions[SessionKindCDP] != 1 {
		t.Fatalf("persist closed the session: %v", status.Sessions)
	}
	if !status.DataSynced || status.Pinned {
		t.Fatalf("persisted idle session bed = synced %v pinned %v, want true/false", status.DataSynced, status.Pinned)
	}

	// Later session traffic dirties and pins the bed again; another store pass
	// can persist it without waiting for the long-lived connection to close.
	sess.Touch()
	if !b.Status().Pinned {
		t.Fatal("session traffic did not pin dirty data")
	}
	if done := m.PersistDirty(context.Background()); len(done) != 1 || done[0] != b.ID {
		t.Fatalf("PersistDirty after session traffic = %v, want [%s]", done, b.ID)
	}
}

func TestPersistKeepsActivityAfterSnapshotPinned(t *testing.T) {
	root := t.TempDir()
	fs := &blockingStore{
		fakeStore: newFakeStore(),
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 1, fs)
	b, _ := m.Ensure(context.Background(), "watermark")

	finish, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("initial operation: %v", err)
	}
	finish()
	persisted := make(chan struct{})
	go func() {
		m.PersistDirty(context.Background())
		close(persisted)
	}()
	<-fs.started

	// Activity after snapshot preparation must remain newer than the committed
	// watermark even when the in-flight upload later succeeds.
	finish, err = m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("operation during persist: %v", err)
	}
	finish()
	close(fs.release)
	<-persisted

	if b.Status().DataSynced {
		t.Fatal("activity after snapshot was incorrectly marked synced")
	}
	if got := m.PinnedBedCount(); got != 1 {
		t.Fatalf("activity after snapshot released pinned capacity: %d", got)
	}
	if done := m.PersistDirty(context.Background()); len(done) != 1 {
		t.Fatalf("follow-up persist = %v, want watermark bed", done)
	}
	if got := m.PinnedBedCount(); got != 0 {
		t.Fatalf("follow-up persist did not release capacity: %d", got)
	}
}

// Regression for the devbox-found deadlock: a shell whose process dies while a
// Run is waiting for output must error out (reader closes the lines channel),
// and the manager/bed locks must stay usable from other goroutines throughout.
// Before the runMu/mu split, this hung the entire daemon including /healthz.
func TestDyingShellDoesNotDeadlock(t *testing.T) {
	m := newTestManager(t)
	b, _ := m.Ensure(context.Background(), "default")
	sh, err := m.ForegroundShell(b)
	if err != nil {
		t.Fatalf("ForegroundShell: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		// The shell kills itself: no marker line will ever arrive.
		_, err := sh.Run(context.Background(), "kill -9 $$", nil)
		done <- err
	}()

	// While that Run is in flight/dying, the full lock chain must stay live:
	// Manager.Resolve (m.mu) → touch (b.mu) → ForegroundShell (b.mu + Dead()).
	probe := make(chan struct{})
	go func() {
		_, _ = m.Ensure(context.Background(), "default")
		_, _ = m.ForegroundShell(b) // may restart the shell; must not block forever
		close(probe)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Run on self-killed shell should return an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: Run never returned after shell death")
	}
	select {
	case <-probe:
	case <-time.After(5 * time.Second):
		t.Fatal("DEADLOCK: manager locks wedged by dying shell")
	}

	// And the bed recovers: a fresh foreground shell works.
	sh2, err := m.ForegroundShell(b)
	if err != nil {
		t.Fatalf("ForegroundShell after death: %v", err)
	}
	if res, err := sh2.Run(context.Background(), "echo back", nil); err != nil || res.ExitCode != 0 {
		t.Fatalf("recovered shell run: %v %+v", err, res)
	}
}

// slowStore wraps fakeStore with a controllable persist delay, to widen the
// eviction window for the cancel-race test.
type slowStore struct {
	*fakeStore
	gate chan struct{} // Persist blocks until this closes
}

func (s *slowStore) Persist(ctx context.Context, id, dir string, generation int64) error {
	<-s.gate
	return s.fakeStore.Persist(ctx, id, dir, generation)
}

// Activity during an evict's persist window must CANCEL the eviction —
// otherwise writes landing after the snapshot are silently destroyed with the
// workspace (docs/store.md §4).
func TestEvictCanceledByActivity(t *testing.T) {
	root := t.TempDir()
	ss := &slowStore{fakeStore: newFakeStore(), gate: make(chan struct{})}
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, ss)

	b, _ := m.Ensure(context.Background(), "conv-race")
	res := make(chan struct {
		ok  bool
		err error
	}, 1)
	go func() {
		ok, err := m.Evict(context.Background(), "conv-race")
		res <- struct {
			ok  bool
			err error
		}{ok, err}
	}()

	// While persist is blocked on the gate, a new operation wins admission.
	time.Sleep(10 * time.Millisecond) // let Evict reach Persist
	if status := b.Status(); status.Phase != PhaseEvicting || status.Activity != ActivityIdle {
		t.Fatalf("status during persist = %+v, want evicting/idle", status.BedStatus)
	}
	finishOperation, err := m.BeginOperation(b, OpExec, time.Second)
	if err != nil {
		t.Fatalf("BeginOperation during eviction: %v", err)
	}
	if status := b.Status(); status.Phase != PhaseEvicting || status.Activity != ActivityActive {
		t.Fatalf("status after operation admission = %+v, want evicting/active", status.BedStatus)
	}
	close(ss.gate)

	r := <-res
	if r.err != nil || r.ok {
		t.Fatalf("Evict = (%v, %v), want canceled (false, nil)", r.ok, r.err)
	}
	finishOperation()
	// Bed survived, back to idle, still resolvable.
	if status := b.Status(); status.Phase != PhaseResident || status.Activity != ActivityIdle {
		t.Fatalf("status after canceled evict = %+v", status.BedStatus)
	}
	if _, ok := m.Get("conv-race"); !ok {
		t.Fatal("bed should still be resident after canceled evict")
	}
}

func TestPurgeEndsIdentity(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-p")
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("x"), 0o644)
	if ok, _ := m.Evict(context.Background(), "conv-p"); !ok {
		t.Fatal("evict failed")
	}
	if info, _ := fs.Stat(context.Background(), "conv-p"); info == nil {
		t.Fatal("snapshot should exist after evict (DORMANT)")
	}
	// Purge the dormant bed: snapshot AND luggage gone, resolve starts fresh.
	if err := m.Purge(context.Background(), "conv-p"); err != nil {
		t.Fatalf("Purge: %v", err)
	}
	if info, _ := fs.Stat(context.Background(), "conv-p"); info != nil {
		t.Fatal("snapshot should be deleted after purge")
	}
	if _, err := os.Stat(b.Dir); !os.IsNotExist(err) {
		t.Fatal("luggage should be removed after purge")
	}
	b2, _ := m.Ensure(context.Background(), "conv-p")
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "restored.txt")); err == nil {
		t.Fatal("purged bed must start empty, not restored")
	}
	if _, err := os.Stat(filepath.Join(b2.Workspace(), "data.txt")); err == nil {
		t.Fatal("purged bed must not resurrect old luggage data")
	}
	// Default bed is not purgeable.
	if err := m.Purge(context.Background(), "default"); err == nil {
		t.Fatal("purging the default bed must be refused")
	}
}

type purgeContextKey struct{}

func TestPurgeCompletesStoreDeleteAfterRequestCancellation(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)
	if _, err := m.Ensure(context.Background(), "cancelled-purge"); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), purgeContextKey{}, "trace-context"))
	cancel()
	if err := m.Purge(ctx, "cancelled-purge"); err != nil {
		t.Fatalf("Purge: %v", err)
	}

	fs.mu.Lock()
	deleteErr := fs.lastDeleteErr
	deleteValue := fs.lastDeleteValue
	deleteDeadline := fs.lastDeleteDeadline
	fs.mu.Unlock()
	if deleteErr != nil {
		t.Fatalf("store delete context error = %v, want live detached context", deleteErr)
	}
	if deleteValue != "trace-context" {
		t.Fatalf("store delete context value = %v, want trace-context", deleteValue)
	}
	if !deleteDeadline {
		t.Fatal("store delete context has no cleanup deadline")
	}
}

func TestPurgeJoinsCanceledInitializationBeforeDeleting(t *testing.T) {
	root := t.TempDir()
	backend := &initializationBlockingStore{
		fakeStore: newFakeStore(),
		started:   make(chan struct{}, 1),
		release:   make(chan struct{}),
	}
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, backend)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.InitializeBed(context.Background(), "purged-initialization"); err != nil {
		t.Fatal(err)
	}
	<-backend.started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := m.Purge(ctx, "purged-initialization"); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Initialization("purged-initialization"); ok {
		t.Fatal("purged initialization remains observable")
	}
	if _, err := os.Stat(filepath.Join(root, "purged-initialization")); !os.IsNotExist(err) {
		t.Fatalf("purged initialization recreated its directory: %v", err)
	}
}

// Every successful persist bumps the generation by one, and the store's
// metadata mirrors the bed meta's counter — this is the freshness token the
// luggage warm-start (and any future fencing) compares against.
func TestGenerationMonotonicAcrossPersists(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-g")
	if err := m.Checkpoint(context.Background(), "conv-g"); err != nil {
		t.Fatalf("checkpoint 1: %v", err)
	}
	if g := fs.generation("conv-g"); g != 1 {
		t.Fatalf("generation after first persist = %d, want 1", g)
	}
	if err := m.Checkpoint(context.Background(), "conv-g"); err != nil {
		t.Fatalf("checkpoint 2: %v", err)
	}
	if g := fs.generation("conv-g"); g != 2 {
		t.Fatalf("generation after second persist = %d, want 2", g)
	}
	meta, ok := loadMeta(b.Dir)
	if !ok || meta.Generation != 2 {
		t.Fatalf("local meta generation = %+v (ok=%v), want 2", meta, ok)
	}

	// A failed upload still bumps the local counter (locally dirty, ahead of
	// the store) but never advances LastPersistedAt.
	before := meta.LastPersistedAt
	fs.fail = true
	if err := m.Checkpoint(context.Background(), "conv-g"); err == nil {
		t.Fatal("checkpoint with failing store should error")
	}
	meta, _ = loadMeta(b.Dir)
	if meta.Generation != 3 || !meta.LastPersistedAt.Equal(before) {
		t.Fatalf("after failed persist: gen=%d lastPersisted=%v, want gen=3 lastPersisted=%v",
			meta.Generation, meta.LastPersistedAt, before)
	}
	if g := fs.generation("conv-g"); g != 2 {
		t.Fatalf("store generation after failed persist = %d, want 2", g)
	}
}

// Luggage GC: over the high watermark, cold copies are deleted — stale
// generation first (pure garbage), then LRU — until under the low watermark.
func TestCollectLuggageWatermarks(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	mkLuggage := func(id string, size int) {
		b, _ := m.Ensure(context.Background(), id)
		payload := make([]byte, size)
		if err := os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), payload, 0o644); err != nil {
			t.Fatal(err)
		}
		if ok, err := m.Evict(context.Background(), id); err != nil || !ok {
			t.Fatalf("evict %s: ok=%v err=%v", id, ok, err)
		}
		time.Sleep(5 * time.Millisecond) // distinct LastActiveAt ordering
	}
	mkLuggage("conv-old", 10_000)
	mkLuggage("conv-mid", 10_000)
	mkLuggage("conv-new", 10_000)

	if got := len(m.ListLuggage()); got != 3 {
		t.Fatalf("luggage count = %d, want 3", got)
	}
	// Below the watermark: nothing reaped.
	m.SetLuggageLimits(100_000, 80_000)
	if reaped := m.CollectLuggage(context.Background()); len(reaped) != 0 {
		t.Fatalf("under watermark reaped %v, want none", reaped)
	}
	// Over the watermark: LRU order, stop under low. ~30KB total → target
	// ~15KB keeps one entry (plus meta noise).
	m.SetLuggageLimits(25_000, 15_000)
	reaped := m.CollectLuggage(context.Background())
	if len(reaped) != 2 || reaped[0] != "conv-old" || reaped[1] != "conv-mid" {
		t.Fatalf("reaped %v, want [conv-old conv-mid]", reaped)
	}
	if got := m.ListLuggage(); len(got) != 1 || got[0].BedID != "conv-new" {
		t.Fatalf("survivors = %+v, want conv-new", got)
	}
}

// A stale-generation copy is reaped before fresher-but-older ones.
func TestCollectLuggageStaleFirst(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	for _, id := range []string{"conv-a", "conv-b"} {
		b, _ := m.Ensure(context.Background(), id)
		_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), make([]byte, 10_000), 0o644)
		if ok, err := m.Evict(context.Background(), id); err != nil || !ok {
			t.Fatalf("evict %s: ok=%v err=%v", id, ok, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	// conv-b is the most recent locally, but the bed ran elsewhere since —
	// its luggage is pure garbage and must go first.
	fs.mu.Lock()
	fs.gens["conv-b"]++
	fs.mu.Unlock()

	m.SetLuggageLimits(15_000, 12_000)
	reaped := m.CollectLuggage(context.Background())
	if len(reaped) != 1 || reaped[0] != "conv-b" {
		t.Fatalf("reaped %v, want [conv-b] (stale first)", reaped)
	}
}

// Inventory reports in-memory beds and luggage with generations — the
// scheduler's placement hint.
func TestInventory(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	_, _ = m.Ensure(context.Background(), "conv-live")
	b, _ := m.Ensure(context.Background(), "conv-cold")
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("x"), 0o644)
	if ok, err := m.Evict(context.Background(), "conv-cold"); err != nil || !ok {
		t.Fatalf("evict: ok=%v err=%v", ok, err)
	}

	byID := map[string]InventoryBed{}
	for _, e := range m.Inventory() {
		byID[e.ID] = e
	}
	if e := byID["conv-live"]; e.Status.Phase != PhaseResident || e.Status.Activity != ActivityIdle || e.Generation != 0 {
		t.Fatalf("conv-live = %+v, want idle gen 0", e)
	}
	cold := byID["conv-cold"]
	if cold.Status.Phase != PhaseDormant || cold.Status.Activity != "" || cold.Generation != 1 || cold.LocalBytes == 0 || cold.LastActiveAt.IsZero() {
		t.Fatalf("conv-cold = %+v, want dormant gen 1 with bytes and last_active_at", cold)
	}
	if cold.SnapshotGeneration != 1 || cold.SnapshotBytes != 1 || cold.RestoreBytes != 0 {
		t.Fatalf("conv-cold recovery facts = %+v, want fresh one-byte snapshot", cold)
	}
}

func TestEstimatedRestoreBytesUsesFullSnapshotForStaleCopy(t *testing.T) {
	tests := []struct {
		name                                          string
		generation, snapshotGeneration, snapshotBytes int64
		dataSynced                                    bool
		want                                          int64
	}{
		{name: "fresh local", generation: 4, snapshotGeneration: 4, snapshotBytes: 1024, dataSynced: true, want: 0},
		{name: "stale local", generation: 3, snapshotGeneration: 4, snapshotBytes: 1024, dataSynced: true, want: 1024},
		{name: "dirty resident", generation: 4, snapshotGeneration: 4, snapshotBytes: 1024, dataSynced: false, want: 1024},
		{name: "unknown size", generation: 0, snapshotGeneration: 0, snapshotBytes: 0, dataSynced: false, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := estimatedRestoreBytes(tt.generation, tt.snapshotGeneration, tt.snapshotBytes, tt.dataSynced); got != tt.want {
				t.Fatalf("estimatedRestoreBytes() = %d, want %d", got, tt.want)
			}
		})
	}
}

// Usage counters accumulate in memory, flush into meta at persist, and keep
// accumulating after evict → warm resume (the meta round-trip).
func TestProfileAccumulatesAndSurvivesEvict(t *testing.T) {
	root := t.TempDir()
	fs := newFakeStore()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, fs)

	b, _ := m.Ensure(context.Background(), "conv-prof")
	b.RecordCommand(1500 * time.Millisecond)
	b.RecordCommand(500 * time.Millisecond)
	if p := b.Usage(); p.CmdCount != 2 || p.CmdTotalMs != 2000 {
		t.Fatalf("profile = %+v, want 2 cmds / 2000ms", p)
	}
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("x"), 0o644)
	if ok, err := m.Evict(context.Background(), "conv-prof"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}

	// The luggage meta (what the snapshot packs) carries the counters, and the
	// inventory's luggage row exposes them.
	meta, ok := loadMeta(filepath.Join(root, "conv-prof"))
	if !ok || meta.Usage.CmdCount != 2 || meta.Usage.CmdTotalMs != 2000 {
		t.Fatalf("luggage meta profile = %+v (ok=%v)", meta.Usage, ok)
	}
	for _, e := range m.Inventory() {
		if e.ID == "conv-prof" && e.Usage.CmdCount != 2 {
			t.Fatalf("inventory usage = %+v", e.Usage)
		}
	}

	// Warm resume seeds from meta and keeps counting on top.
	b2, err := m.Ensure(context.Background(), "conv-prof")
	if err != nil {
		t.Fatalf("re-Resolve: %v", err)
	}
	b2.RecordCommand(1000 * time.Millisecond)
	if p := b2.Usage(); p.CmdCount != 3 || p.CmdTotalMs != 3000 {
		t.Fatalf("profile after resume = %+v, want 3 cmds / 3000ms", p)
	}
}

// sleepyStore makes persist/restore take measurable wall time, so the
// node-specific migration-cost fields have something to record.
type sleepyStore struct{ *fakeStore }

func (s sleepyStore) Persist(ctx context.Context, id, dir string, generation int64) error {
	time.Sleep(20 * time.Millisecond)
	return s.fakeStore.Persist(ctx, id, dir, generation)
}
func (s sleepyStore) Restore(ctx context.Context, id, dir string) error {
	time.Sleep(20 * time.Millisecond)
	return s.fakeStore.Restore(ctx, id, dir)
}

// Last{Persist,Restore}Ms are measured where the work actually happens: the
// persist duration lands in the luggage meta after evict, the restore duration
// lands in the resumed bed's in-memory profile after a cold resume.
func TestProfileRecordsMigrationCost(t *testing.T) {
	root := t.TempDir()
	ss := sleepyStore{fakeStore: newFakeStore()}
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, ss)

	b, _ := m.Ensure(context.Background(), "conv-cost")
	_ = os.WriteFile(filepath.Join(b.Workspace(), "data.txt"), []byte("x"), 0o644)
	if ok, err := m.Evict(context.Background(), "conv-cost"); err != nil || !ok {
		t.Fatalf("Evict: ok=%v err=%v", ok, err)
	}
	meta, ok := loadMeta(filepath.Join(root, "conv-cost"))
	if !ok || meta.Usage.LastPersistMs < 10 {
		t.Fatalf("LastPersistMs = %d (ok=%v), want >= 10", meta.Usage.LastPersistMs, ok)
	}

	// Cold resume (luggage gone) → the restore is timed on this host.
	if err := os.RemoveAll(filepath.Join(root, "conv-cost")); err != nil {
		t.Fatal(err)
	}
	b2, err := m.Ensure(context.Background(), "conv-cost")
	if err != nil {
		t.Fatalf("re-Resolve: %v", err)
	}
	if record := b2.Lifecycle().LastInitialization; record == nil || record.Source != "snapshot" ||
		!strings.Contains(lifecycleStageNames(record), "stage_in_bedfs") {
		t.Fatalf("cold initialization = %+v, want snapshot stage-in", record)
	}
	if p := b2.Usage(); p.LastRestoreMs < 10 {
		t.Fatalf("LastRestoreMs = %d, want >= 10", p.LastRestoreMs)
	}
}

func TestBedDirLayoutAndMetaAcrossRestart(t *testing.T) {
	root := t.TempDir()
	m, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)

	b, _ := m.Ensure(context.Background(), "default")
	// Layout: {root}/default/{meta.json,data/workspace}; Home is the bed_home
	// root (data), Workspace the real subdir below it.
	if b.Home() != filepath.Join(root, "default", "data") {
		t.Fatalf("Home = %s", b.Home())
	}
	if b.Workspace() != filepath.Join(root, "default", "data", "workspace") {
		t.Fatalf("Workspace = %s", b.Workspace())
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "meta.json")); err != nil {
		t.Fatalf("meta.json missing: %v", err)
	}
	created := b.CreatedAt

	// "Restart": a new Manager over the same root sees the same identity.
	time.Sleep(5 * time.Millisecond)
	m2, _ := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)
	b2, _ := m2.Ensure(context.Background(), "default")
	if !b2.CreatedAt.Equal(created) {
		t.Fatalf("CreatedAt not preserved across restart: %v vs %v", b2.CreatedAt, created)
	}
}

func TestShortID(t *testing.T) {
	cases := map[string]string{
		"default": "default",
		"sandbox-019f3cab442f7435abe5a27f3436859a": "…3436859a",
	}
	for in, want := range cases {
		if got := ShortID(in); got != want {
			t.Errorf("ShortID(%q) = %q, want %q", in, got, want)
		}
	}
}
