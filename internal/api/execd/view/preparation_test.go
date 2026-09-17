package view

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
	bed "github.com/qiankunli/hostel/internal/bed/manager"
)

func TestPreparationFailureProjection(t *testing.T) {
	result := bed.ExecutionResult{Cause: bed.CausePreparationFailed, Err: hostel.WrapError(hostel.ErrPreparationFailed, "session directory", errors.New("secret carrier path"))}
	view := Terminal(result)
	encoded, err := json.Marshal(view)
	if view.Type != "error" || view.Error == nil || view.Error.Value != "125" {
		t.Fatalf("wrong event: %+v", view)
	}
	if err != nil || strings.Contains(string(encoded), "secret carrier path") || strings.Contains(string(encoded), "executor_lost") || !strings.Contains(string(encoded), "preparation_failed") {
		t.Fatal(string(encoded), err)
	}
}
