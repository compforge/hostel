package bed

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type testParticipant struct {
	Noop
	name string
	log  *[]string
	fail bool
}

func (p *testParticipant) Release(context.Context, *Bed) error {
	*p.log = append(*p.log, p.name)
	if p.fail {
		return errors.New("busy")
	}
	return nil
}
func TestSequenceRetriesOnlyUnfinishedHooks(t *testing.T) {
	var log []string
	first := &testParticipant{name: "first", log: &log}
	second := &testParticipant{name: "second", log: &log, fail: true}
	last := &testParticipant{name: "last", log: &log}
	sequence := NewSequence(Release, Participant{"first", first}, Participant{"second", second}, Participant{"last", last})
	if err := sequence.Run(context.Background(), New("test", 0, Spec{})); err == nil {
		t.Fatal("expected failure")
	}
	if !reflect.DeepEqual(log, []string{"first", "second"}) {
		t.Fatal(log)
	}
	second.fail = false
	if err := sequence.Run(context.Background(), New("test", 0, Spec{})); err != nil {
		t.Fatal(err)
	}
	if err := sequence.Run(context.Background(), New("test", 0, Spec{})); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(log, []string{"first", "second", "second", "last"}) {
		t.Fatal(log)
	}
}
func TestSequenceCancellationDoesNotAdvance(t *testing.T) {
	var log []string
	p := &testParticipant{name: "one", log: &log}
	sequence := NewSequence(Release, Participant{"one", p})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := sequence.Run(ctx, New("test", 0, Spec{})); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(log) != 0 {
		t.Fatal(log)
	}
	if err := sequence.Run(context.Background(), New("test", 0, Spec{})); err != nil {
		t.Fatal(err)
	}
	if len(log) != 1 {
		t.Fatal(log)
	}
}
