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
	"sync"
	"time"

	"github.com/qiankunli/go-stdx/randx"
)

// SessionKind distinguishes the stateful holds a client can open on a bed
// (docs/lifecycle.md): the client opens and closes them explicitly — possibly
// never — so evict cannot wait them out and must be able to revoke them.
type SessionKind string

const (
	SessionKindShell SessionKind = "shell"
	SessionKindCDP   SessionKind = "cdp"
)

// sessionRevokeWait bounds how long evict waits for revoked session handlers
// to exit before persisting; a handler that ignores the cancel stalls the
// evict at most this long.
const sessionRevokeWait = 5 * time.Second

// Session is one revocable stateful hold on a bed. The shell kind has its own
// specialized machinery (b.shells, shell.go); this type holds the kinds with
// no other registry (cdp today). Lifecycle: OpenSession registers the hold,
// the handler serves until the client disconnects or evict revokes, and the
// handler must defer Close so evict's bounded wait observes the full exit.
// Unlike an operation, a session never blocks eviction and never raises the
// bed's status — an idle bed holding only sessions is still idle.
type Session struct {
	ID   string
	Kind SessionKind

	bed     *Bed
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{} // closed by Close = handler fully exited
	closeFn func()        // resource kill switch (e.g. conn.Close); must be idempotent
	touchFn func()

	revokeOnce sync.Once
	closeOnce  sync.Once
}

// OpenSession registers a stateful hold on the bed. Admission mirrors
// BeginOperation (same lock order, same residency check): a session cannot
// start on a bed whose resident entry is gone. Opening is activity and
// touches the bed.
func (m *Manager) OpenSession(b *Bed, kind SessionKind, closeFn func()) (*Session, error) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Session{
		ID:      "sess-" + randx.Hex(8),
		Kind:    kind,
		bed:     b,
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
		closeFn: closeFn,
		touchFn: func() { b.touch(m.bedIdleTTL) },
	}
	m.mu.Lock()
	b.mu.Lock()
	if current, ok := m.beds[b.ID]; !ok || current != b {
		b.mu.Unlock()
		m.mu.Unlock()
		cancel()
		return nil, ErrBedUnavailable
	}
	b.touchLocked(time.Now(), m.bedIdleTTL)
	b.sessions[s.ID] = s
	b.mu.Unlock()
	m.mu.Unlock()
	return s, nil
}

// Context is canceled when the session is revoked (evict) — handlers should
// select on it alongside client traffic.
func (s *Session) Context() context.Context { return s.ctx }

// Touch reports real traffic on the session (docs/lifecycle.md: touch is a
// side effect of use, never its own request category).
func (s *Session) Touch() { s.touchFn() }

// revoke signals the handler to exit: cancel the context and kill the
// resource. Idempotent; done stays open until the handler's Close.
func (s *Session) revoke() {
	s.revokeOnce.Do(func() {
		s.cancel()
		if s.closeFn != nil {
			s.closeFn()
		}
	})
}

// Close marks the handler fully exited and deregisters the session. Safe to
// call twice, and after revoke.
func (s *Session) Close() {
	s.revoke()
	s.closeOnce.Do(func() {
		s.bed.mu.Lock()
		delete(s.bed.sessions, s.ID)
		s.bed.mu.Unlock()
		close(s.done)
	})
}

// revokeSessions is the evict revoke stage (docs/lifecycle.md): stateful holds
// cannot be waited out, so evict actively ends them. Shells are closed first
// (their writes must not race the persist), registered sessions are revoked,
// and the wait for handlers to exit is bounded.
func (m *Manager) revokeSessions(b *Bed) {
	b.mu.Lock()
	shells := make([]*Shell, 0, len(b.shells))
	for sid, sh := range b.shells {
		shells = append(shells, sh)
		delete(b.shells, sid)
	}
	sessions := make([]*Session, 0, len(b.sessions))
	for _, s := range b.sessions {
		sessions = append(sessions, s)
	}
	b.mu.Unlock()

	for _, sh := range shells {
		sh.Close()
	}
	for _, s := range sessions {
		s.revoke()
	}
	timer := time.NewTimer(sessionRevokeWait)
	defer timer.Stop()
	for _, s := range sessions {
		select {
		case <-s.done:
		case <-timer.C:
			return
		}
	}
}
