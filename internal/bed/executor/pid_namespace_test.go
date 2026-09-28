package executor

import (
	"context"
	"github.com/qiankunli/hostel/internal/bed/tool"
	"strings"
	"testing"
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
	// A previously selected private domain must not silently turn into local.
	selected := pidnsStatus(tool.Auto, true, true, "")
	if _, err := ResolveFactory(t.Context(), (Config{Backend: "local", PIDNS: tool.Auto}).WithPIDNSSelection(selected), nil); err == nil {
		t.Fatal("frozen private namespace downgraded")
	}
}
