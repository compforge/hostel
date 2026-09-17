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
	view := ExecutionResultFrom(result)
	encoded, err := json.Marshal(view)
	if view.Process != nil || view.Error == "" {
		t.Fatalf("wrong view: %+v", view)
	}
	if err != nil || strings.Contains(string(encoded), "secret carrier path") || strings.Contains(string(encoded), "executor_lost") || !strings.Contains(string(encoded), "preparation_failed") {
		t.Fatal(string(encoded), err)
	}
}
