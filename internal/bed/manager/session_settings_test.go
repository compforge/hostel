package manager

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	hostel "github.com/qiankunli/hostel/internal"
	"github.com/qiankunli/hostel/internal/bed/executor"
)

func TestSessionPreparationFailurePreservesRuntime(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	b, err := m.Ensure(t.Context(), "default")
	if err != nil {
		t.Fatal(err)
	}
	id, err := m.CreateShell(b, "")
	if err != nil {
		t.Fatal(err)
	}
	sh, _ := b.GetShell(id)
	for _, directory := range []string{"/missing-review-cwd", "$(touch should-not-run)", "$UNDEFINED_REVIEW_CWD"} {
		e, err := m.StartConfiguredSessionExecution(t.Context(), b, sh, "touch should-not-run", SessionSettings{Directory: directory}, time.Second, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		failed := e.Wait()
		if failed.Process != nil || failed.Cause != CausePreparationFailed || !errors.Is(failed.Err, hostel.ErrPreparationFailed) {
			t.Fatalf("wrong preparation result: %+v", failed)
		}
		if directory == "/missing-review-cwd" && !errors.Is(failed.Err, os.ErrNotExist) {
			t.Fatal("filesystem cause lost", failed.Err)
		}
		if _, err := b.BedFS().Stat("should-not-run"); !os.IsNotExist(err) {
			t.Fatal("command ran despite failed preparation", err)
		}
		next, err := m.StartConfiguredSessionExecution(t.Context(), b, sh, "true", SessionSettings{}, time.Second, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result := next.Wait(); result.Process == nil || result.Process.ExitCode != 0 || result.Cause != CauseNatural {
			t.Fatal(result)
		}
	}
	sh.Close()
	e, err := m.StartConfiguredSessionExecution(t.Context(), b, sh, "true", SessionSettings{}, time.Second, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if lost := e.Wait(); lost.Process == nil || lost.Process.Kind != executor.ProcessLost || lost.Cause != CauseExecutorLost {
		t.Fatal("runtime loss misclassified", lost)
	}
}
