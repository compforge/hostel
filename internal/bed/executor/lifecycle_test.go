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

func (*replacementFactory) Status() Status { return Status{} }
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

type bindingExecutor struct {
	Executor
	id         string
	done       chan struct{}
	cleanupErr error
}

func (e *bindingExecutor) ID() string                     { return e.id }
func (*bindingExecutor) Backend() string                  { return "test" }
func (*bindingExecutor) State() State                     { return StateReady }
func (e *bindingExecutor) Done() <-chan struct{}          { return e.done }
func (*bindingExecutor) Exit() Exit                       { return Exit{State: StateStopped} }
func (e *bindingExecutor) Shutdown(context.Context) error { return e.cleanupErr }

func TestFailedBindingNeverPublishesRawExecutor(t *testing.T) {
	old := &bindingExecutor{id: "old", done: make(chan struct{}), cleanupErr: errors.New("cleanup pending")}
	next := &bindingExecutor{id: "next", done: make(chan struct{})}
	factory := &replacementFactory{next: old}
	b := bed.New("binding", "", bed.Spec{})
	m := NewManager(factory, bed.NewOwners().Executor)
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	calls := 0
	if err := m.Bind(b, func(_ context.Context, e Executor) (Executor, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("view preparation failed")
		}
		return e, nil
	}); err != nil {
		t.Fatal(err)
	}
	if got, err := m.For(t.Context(), b); got != nil || err == nil {
		t.Fatalf("unprepared executor returned: %v %v", got, err)
	}
	factory.next = next
	if _, err := m.For(t.Context(), b); !errors.Is(err, old.cleanupErr) || factory.created != 1 {
		t.Fatalf("replaced before cleanup: %v creates=%d", err, factory.created)
	}
	old.cleanupErr = nil
	got, err := m.For(t.Context(), b)
	if err != nil || got != next || calls != 2 {
		t.Fatalf("replacement binding: %v %v calls=%d", got, err, calls)
	}
	close(next.done)
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
}
