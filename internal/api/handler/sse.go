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

package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	apiview "github.com/qiankunli/hostel/internal/api/view"

	"github.com/gin-gonic/gin"
)

const ssePingInterval = 3 * time.Second

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
func (s *sseStream) send(ev apiview.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup()
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().UnixMilli()
	}
	var payload any = ev
	if s.c.GetBool(execdContextKey) {
		payload = execdEvent(ev)
	}
	b, _ := json.Marshal(payload)
	if s.c.GetBool(execdContextKey) {
		b = append([]byte("data: "), b...)
	}
	b = append(b, '\n', '\n')
	_, _ = s.c.Writer.Write(b)
	s.flush()
}

// start commits the execution identity before any output and keeps a silent
// execution observable until its terminal event.
func (s *sseStream) start(ctx context.Context, executionID string, interval time.Duration) func() {
	s.send(apiview.StreamEvent{Type: apiview.EventExecutionStart, ExecutionID: executionID})
	s.send(apiview.StreamEvent{Type: apiview.EventPing, Text: "pong"})

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
				s.send(apiview.StreamEvent{Type: apiview.EventPing, Text: "pong"})
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

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
