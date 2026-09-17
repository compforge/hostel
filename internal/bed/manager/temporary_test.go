package manager

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestTemporaryDirectorySurvivesExecutorReplacement(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	b, err := m.Ensure(t.Context(), "temporary")
	if err != nil {
		t.Fatal(err)
	}
	first, err := m.RunForeground(t.Context(), b, `printf retained > "$TMPDIR/retained"`, "", nil, time.Second, nil)
	if err != nil || first.Process.ExitCode != 0 {
		t.Fatalf("write: %+v %v", first, err)
	}
	host, err := b.BedFS().Resolve("/tmp/retained")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.executorManager.Current(b.Bed).Shutdown(t.Context()); err != nil {
		t.Fatal(err)
	}
	second, err := m.RunForeground(t.Context(), b, `test "$(cat "$TMPDIR/retained")" = retained`, "", nil, time.Second, nil)
	if err != nil || second.Process.ExitCode != 0 || first.ExecutorID == second.ExecutorID {
		t.Fatalf("replacement: %+v %v", second, err)
	}
	if data, err := b.BedFS().Read("/tmp/retained"); err != nil || string(data) != "retained" {
		t.Fatalf("API read: %q %v", data, err)
	}
	if err := m.Purge(t.Context(), b.Name); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(host); !os.IsNotExist(err) {
		t.Fatalf("temporary file survived purge: %v", err)
	}
}
