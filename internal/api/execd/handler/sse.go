package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/qiankunli/hostel/internal/api/execd/view"
)

type eventStream struct {
	c  *gin.Context
	mu sync.Mutex
}

func (s *eventStream) send(ev view.StreamEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.c.Header("Content-Type", "text/event-stream")
	s.c.Header("Cache-Control", "no-cache")
	s.c.Header("X-Accel-Buffering", "no")
	if ev.Timestamp == 0 {
		ev.Timestamp = time.Now().UnixMilli()
	}
	data, _ := json.Marshal(ev)
	_, _ = s.c.Writer.Write(append(append([]byte("data: "), data...), '\n', '\n'))
	if f, ok := any(s.c.Writer).(http.Flusher); ok {
		f.Flush()
	}
}
func (s *eventStream) start(ctx context.Context, id string) func() {
	s.send(view.StreamEvent{Type: "init", Text: id})
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.send(view.StreamEvent{Type: "ping", Text: "pong"})
			}
		}
	}()
	return func() { cancel(); <-done }
}
