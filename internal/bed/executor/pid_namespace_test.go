package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/tool"
)

func TestLocalExecutorRejectsPrivatePIDNamespace(t *testing.T) {
	factory, err := ResolveFactory(context.Background(), Config{Backend: "local", PIDNS: tool.Required}, nil)
	if err == nil || factory != nil || !strings.Contains(err.Error(), "PID namespace") {
		t.Fatalf("factory=%v err=%v", factory, err)
	}
}

func TestLocalPIDNSPolicyEvidence(t *testing.T) {
	for _, policy := range []tool.Policy{tool.Off, tool.Auto} {
		f, err := ResolveFactory(t.Context(), Config{Backend: "local", PIDNS: policy}, nil)
		if err != nil {
			t.Fatal(err)
		}
		status := f.Status().Tools["pidns"]
		if status.Policy != policy || status.Selected || status.Probe != "not_probed" || status.Reason == "" {
			t.Fatalf("status=%+v", status)
		}
		f.Close()
	}
}
