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
	"sync/atomic"
	"testing"
	"time"
)

// A session is a revocable stateful hold: it shows up in Status.Sessions by
// kind, never raises the bed's State, and deregisters on Close.
func TestSessionStatusAndClose(t *testing.T) {
	m := newTestManager(t)
	m.SetBedIdleTTL(time.Minute)
	b, err := m.Ensure(context.Background(), "sess-bed")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	sess, err := m.OpenSession(b, SessionKindCDP, nil)
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	status := b.Status()
	if status.Sessions[SessionKindCDP] != 1 {
		t.Fatalf("sessions by kind = %v, want cdp:1", status.Sessions)
	}
	if status.State != StateIdle {
		t.Fatalf("state = %s, want idle — a session must not raise the bed's state", status.State)
	}
	if status.LastActiveAt.IsZero() {
		t.Fatal("OpenSession should touch the bed")
	}

	sess.Touch()
	if got := b.LastActiveAt(); got.IsZero() {
		t.Fatal("Touch should refresh last_active_at")
	}

	sess.Close()
	sess.Close() // idempotent
	if status := b.Status(); len(status.Sessions) != 0 {
		t.Fatalf("sessions after Close = %v, want empty", status.Sessions)
	}
}

// OpenSession mirrors BeginOperation admission: a bed whose resident entry is
// gone refuses new sessions.
func TestOpenSessionOnEvictedBedRefused(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(context.Background(), "sess-gone")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	evicted, err := m.Evict(context.Background(), b.ID)
	if err != nil || !evicted {
		t.Fatalf("Evict = %v, %v", evicted, err)
	}
	if _, err := m.OpenSession(b, SessionKindCDP, nil); err != ErrBedUnavailable {
		t.Fatalf("OpenSession on evicted bed = %v, want ErrBedUnavailable", err)
	}
}

// Evict cannot wait sessions out, so it revokes them BEFORE persisting: the
// session's context is canceled and its kill switch fired.
func TestEvictRevokesSessions(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(context.Background(), "sess-evict")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	var closed atomic.Int32
	sess, err := m.OpenSession(b, SessionKindCDP, func() { closed.Add(1) })
	if err != nil {
		t.Fatalf("OpenSession: %v", err)
	}

	evicted, err := m.Evict(context.Background(), b.ID)
	if err != nil || !evicted {
		t.Fatalf("Evict = %v, %v — an idle session must not block eviction", evicted, err)
	}
	select {
	case <-sess.Context().Done():
	default:
		t.Fatal("evict should cancel the session context")
	}
	if closed.Load() != 1 {
		t.Fatalf("closeFn called %d times, want 1", closed.Load())
	}
}

// Operation timeouts are system-bounded: non-positive gets the default, beyond
// the max is clamped — never rejected, never unbounded.
func TestBeginOperationTimeoutClamp(t *testing.T) {
	m := newTestManager(t)
	m.SetBedIdleTTL(time.Minute)
	b, err := m.Ensure(context.Background(), "clamp-bed")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	finish, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	want := time.Now().Add(time.Minute).Add(DefaultOperationTimeout)
	if got := b.RetainUntil(); got.Before(want.Add(-time.Second)) {
		t.Fatalf("default timeout: retained_until = %s, want >= %s", got, want)
	}
	finish()

	finish, err = m.BeginOperation(b, OpExec, 100*time.Hour)
	if err != nil {
		t.Fatalf("BeginOperation: %v", err)
	}
	defer finish()
	limit := time.Now().Add(time.Minute).Add(MaxOperationTimeout)
	if got := b.RetainUntil(); got.After(limit.Add(time.Second)) {
		t.Fatalf("clamped timeout: retained_until = %s, want <= %s", got, limit)
	}
}

func TestSessionExecutionEnforcesTimeout(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(context.Background(), "session-timeout")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	sessionID, err := m.CreateShell(b, "")
	if err != nil {
		t.Fatalf("CreateShell: %v", err)
	}
	shell, ok := b.GetShell(sessionID)
	if !ok {
		t.Fatalf("shell %s not found", sessionID)
	}

	execution, err := m.StartSessionExecution(context.Background(), b, shell, "sleep 30", 30*time.Millisecond, nil, nil)
	if err != nil {
		t.Fatalf("StartSessionExecution: %v", err)
	}
	result := execution.Wait()
	if result.Cause != CauseTimeout {
		t.Fatalf("termination cause = %s, want %s", result.Cause, CauseTimeout)
	}
}

// Status.Operations breaks inflight down by kind and drops zeroed kinds.
func TestStatusOperationsByKind(t *testing.T) {
	m := newTestManager(t)
	b, err := m.Ensure(context.Background(), "kind-bed")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	finishExec, err := m.BeginOperation(b, OpExec, 0)
	if err != nil {
		t.Fatalf("BeginOperation exec: %v", err)
	}
	finishFile, err := m.BeginOperation(b, OpFile, 0)
	if err != nil {
		t.Fatalf("BeginOperation file: %v", err)
	}

	status := b.Status()
	if status.Inflight != 2 || status.Operations[OpExec] != 1 || status.Operations[OpFile] != 1 {
		t.Fatalf("status = inflight %d operations %v, want 2 and exec:1 file:1", status.Inflight, status.Operations)
	}
	if status.State != StateActive {
		t.Fatalf("state = %s, want active with inflight operations", status.State)
	}

	finishExec()
	if status := b.Status(); status.Operations[OpExec] != 0 || status.Operations[OpFile] != 1 {
		t.Fatalf("operations after finishing exec = %v, want file:1 only", status.Operations)
	}
	finishFile()
	if status := b.Status(); status.Inflight != 0 || len(status.Operations) != 0 || status.State != StateIdle {
		t.Fatalf("status after finishing all = inflight %d operations %v state %s", status.Inflight, status.Operations, status.State)
	}
}
