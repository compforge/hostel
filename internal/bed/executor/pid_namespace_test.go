package executor

import (
	"context"
	"strings"
	"testing"
)

func TestLocalExecutorRejectsPrivatePIDNamespace(t *testing.T) {
	factory, err := ResolveFactory(context.Background(), Config{Backend: "local", PrivatePIDNamespace: true}, nil)
	if err == nil || factory != nil || !strings.Contains(err.Error(), "PID namespace") {
		t.Fatalf("factory=%v err=%v", factory, err)
	}
}
