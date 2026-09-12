// Package lifecycle defines allocation-bound hooks. The Bed manager owns their
// order and the conditions for publishing or forgetting a local identity.
package lifecycle

import (
	"context"
	"fmt"
)

// Lifecycle belongs to one allocation, never a lookup by a reusable Bed ID.
// Hooks may leave partial resources on failure and must support retry.
// Forget is called only after all processes, resources and local data are gone.
// +spec=`Prepare completes before Ready; Stop precedes Release; Forget requires complete local identity cleanup.`
type Lifecycle interface {
	Recover(context.Context) error
	Prepare(context.Context) error
	Stop(context.Context) error
	Release(context.Context) error
	Forget(context.Context) error
}

type Component[R any] interface {
	Lifecycle
	// Diagnostics returns a read-only snapshot without host probes or remote I/O.
	Diagnostics() R
}

// Noop supplies only hooks a component does not participate in. A component
// must still explicitly provide Diagnostics to satisfy Component.
type Noop struct{}

func (Noop) Recover(context.Context) error { return nil }
func (Noop) Prepare(context.Context) error { return nil }
func (Noop) Stop(context.Context) error    { return nil }
func (Noop) Release(context.Context) error { return nil }
func (Noop) Forget(context.Context) error  { return nil }

type Phase string

const (
	Recover Phase = "recover"
	Prepare Phase = "prepare"
	Stop    Phase = "stop"
	Release Phase = "release"
	Forget  Phase = "forget"
)

type Participant struct {
	Name      string
	Lifecycle Lifecycle
}

// Sequence keeps completion at hook granularity. Its owner serializes Run;
// successful hooks are not replayed when a later hook fails. Participants and
// their order are fixed at construction, rather than inferred from registration.
type Sequence struct {
	phase        Phase
	participants []Participant
	next         int
}

func NewSequence(phase Phase, participants ...Participant) *Sequence {
	return &Sequence{phase: phase, participants: append([]Participant(nil), participants...)}
}
func (s *Sequence) Run(ctx context.Context) error {
	for s.next < len(s.participants) {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := s.participants[s.next]
		var err error
		switch s.phase {
		case Recover:
			err = p.Lifecycle.Recover(ctx)
		case Prepare:
			err = p.Lifecycle.Prepare(ctx)
		case Stop:
			err = p.Lifecycle.Stop(ctx)
		case Release:
			err = p.Lifecycle.Release(ctx)
		case Forget:
			err = p.Lifecycle.Forget(ctx)
		default:
			return fmt.Errorf("lifecycle: unknown phase %q", s.phase)
		}
		if err != nil {
			return fmt.Errorf("lifecycle %s %s: %w", s.phase, p.Name, err)
		}
		s.next++
	}
	return nil
}
