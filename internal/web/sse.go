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
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/executor"
)

type StreamEventType string

const (
	EventExecutionStart StreamEventType = "execution_start"
	EventStdout         StreamEventType = "stdout"
	EventStderr         StreamEventType = "stderr"
	EventExecutionEnd   StreamEventType = "execution_end"
	EventPing           StreamEventType = "ping"
)

const ssePingInterval = 3 * time.Second

type processOutcomePayload struct {
	Kind       executor.ProcessOutcomeKind `json:"kind"`
	ExitCode   *int                        `json:"exit_code,omitempty"`
	Signal     *int                        `json:"signal,omitempty"`
	CoreDumped bool                        `json:"core_dumped,omitempty"`
	Error      string                      `json:"error,omitempty"`
}

type executionResultPayload struct {
	ExecutionID     string                `json:"execution_id"`
	BedID           string                `json:"bed_id"`
	Mode            bed.ExecutionMode     `json:"mode"`
	ExecutorID      string                `json:"executor_id"`
	ExecutorBackend string                `json:"executor_backend"`
	StartedAt       time.Time             `json:"started_at"`
	FinishedAt      time.Time             `json:"finished_at"`
	DurationMs      int64                 `json:"duration_ms"`
	Process         processOutcomePayload `json:"process"`
	Cause           bed.TerminationCause  `json:"termination_cause"`
}

type executionOutputPayload struct {
	Sequence int64            `json:"sequence"`
	Stream   bed.OutputStream `json:"stream"`
	Text     string           `json:"text"`
}

func outputPayload(output bed.ExecutionOutput) executionOutputPayload {
	return executionOutputPayload{
		Sequence: output.Sequence,
		Stream:   output.Stream,
		Text:     output.Text,
	}
}

type StreamEvent struct {
	Type        StreamEventType         `json:"type"`
	ExecutionID string                  `json:"execution_id,omitempty"`
	Sequence    *int64                  `json:"sequence,omitempty"`
	Text        string                  `json:"text,omitempty"`
	Result      *executionResultPayload `json:"result,omitempty"`
	Timestamp   int64                   `json:"timestamp"`
}

func resultPayload(result bed.ExecutionResult) executionResultPayload {
	process := processOutcomePayload{
		Kind:       result.Process.Kind,
		CoreDumped: result.Process.CoreDumped,
		Error:      result.Process.Error,
	}
	switch result.Process.Kind {
	case executor.ProcessExited:
		process.ExitCode = &result.Process.ExitCode
	case executor.ProcessSignaled:
		process.Signal = &result.Process.Signal
	}
	return executionResultPayload{
		ExecutionID:     result.ExecutionID,
		BedID:           result.BedID,
		Mode:            result.Mode,
		ExecutorID:      result.ExecutorID,
		ExecutorBackend: result.ExecutorBackend,
		StartedAt:       result.StartedAt,
		FinishedAt:      result.FinishedAt,
		DurationMs:      result.Duration.Milliseconds(),
		Process:         process,
		Cause:           result.Cause,
	}
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

// start commits the execution identity before any output and keeps a silent
// execution observable until its terminal event.
func (s *sseStream) start(ctx context.Context, executionID string, interval time.Duration) func() {
	s.send(StreamEvent{Type: EventExecutionStart, ExecutionID: executionID})
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

func flush(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}
