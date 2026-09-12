package executor

import (
	"context"
	"errors"
	"github.com/qiankunli/hostel/internal/bed"
	"testing"
)

type stalledExecutor struct {
	Executor
	err error
}

func (*stalledExecutor) ID() string                       { return "executor-test" }
func (*stalledExecutor) Backend() string                  { return "test" }
func (*stalledExecutor) State() State                     { return StateLost }
func (e *stalledExecutor) Shutdown(context.Context) error { return e.err }

type replacementFactory struct {
	Factory
	created int
	next    Executor
}

func (f *replacementFactory) Create(context.Context, string) (Executor, error) {
	f.created++
	return f.next, nil
}
func TestExecutorReplacementWaitsForCleanup(t *testing.T) {
	old := &stalledExecutor{err: errors.New("cleanup busy")}
	next := &stalledExecutor{}
	b := bed.New("replacement", "", bed.Spec{})
	factory := &replacementFactory{next: next}
	m := NewManager(factory, bed.NewOwners().Executor)
	if err := m.Prepare(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	m.realms[b].executor = old
	if _, err := m.For(context.Background(), b); !errors.Is(err, old.err) || factory.created != 0 {
		t.Fatalf("replaced before cleanup: %v creates=%d", err, factory.created)
	}
	old.err = nil
	got, err := m.For(context.Background(), b)
	if err != nil || got != next || factory.created != 1 {
		t.Fatalf("retry replacement: %v creates=%d", err, factory.created)
	}
}

func (*stalledExecutor) Done() <-chan struct{} { done := make(chan struct{}); close(done); return done }
func (*stalledExecutor) Exit() Exit            { return Exit{State: StateLost} }
