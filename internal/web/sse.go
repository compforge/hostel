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

package web

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// StreamEventType matches execd's ServerStreamEvent.type enum.
type StreamEventType string

const (
	EventInit     StreamEventType = "init"
	EventStatus   StreamEventType = "status"
	EventError    StreamEventType = "error"
	EventStdout   StreamEventType = "stdout"
	EventStderr   StreamEventType = "stderr"
	EventComplete StreamEventType = "execution_complete"
	EventPing     StreamEventType = "ping"
)

const ssePingInterval = 3 * time.Second

// StreamEvent is one SSE frame payload (JSON), shaped like execd's
// ServerStreamEvent so SDK stream parsers work unchanged.
type StreamEvent struct {
	Type          StreamEventType `json:"type,omitempty"`
	Text          string          `json:"text,omitempty"`
	ExecutionTime int64           `json:"execution_time,omitempty"`
	Timestamp     int64           `json:"timestamp,omitempty"`
	Error         string          `json:"error,omitempty"`
	ExitCode      *int            `json:"exit_code,omitempty"`
}

// sseStream owns an SSE response: sets headers once, writes framed events.
type sseStream struct {
	c       *gin.Context
	started bool
	mu      sync.Mutex
}

func newSSE(c *gin.Context) *sseStream { return &sseStream{c: c} }

var sseHeaders = map[string]string{
	"Content-Type":      "text/event-stream",
	"Cache-Control":     "no-cache",
	"Connection":        "keep-alive",
	"X-Accel-Buffering": "no",
}

func (s *sseStream) setup() {
	if s.started {
		return
	}
	for k, v := range sseHeaders {
		s.c.Writer.Header().Set(k, v)
	}
	s.c.Writer.WriteHeader(http.StatusOK)
	s.flush()
	s.started = true
}

// send writes one event as `<json>\n\n` and flushes.
func (s *sseStream) send(ev StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().UnixMilli()
	}
	b, _ := json.Marshal(ev)
	b = append(b, '\n', '\n')
	_, _ = s.c.Writer.Write(b)
	s.flush()
}

// start matches execd's stream lifecycle: commit an init frame, then keep a
// silent execution observable until the request ends. The returned cleanup
// waits for the writer goroutine so it cannot race with net/http closing the
// response after the handler returns.
func (s *sseStream) start(ctx context.Context, initText string, interval time.Duration) func() {
	s.send(StreamEvent{Type: EventInit, Text: initText})
	s.send(StreamEvent{Type: EventPing, Text: "pong"})

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.send(StreamEvent{Type: EventPing, Text: "pong"})
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *sseStream) flush() {
	flush(s.c.Writer)
}

// started reports whether any SSE header/frame has been committed — callers use
// it to decide between a JSON error (nothing sent yet) and an error event.
func (s *sseStream) hasStarted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.started
}

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
