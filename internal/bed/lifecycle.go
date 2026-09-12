// Bed lifecycle hooks receive an allocation. The Bed manager owns their
// order and the conditions for publishing or forgetting a local identity.
package bed

import (
	"context"
	"fmt"
)

// Lifecycle belongs to one allocation, never a lookup by a reusable Bed ID.
// Hooks may leave partial resources on failure and must support retry.
// Forget is called only after all processes, resources and local data are gone.
// +spec=`Prepare completes before Ready; Stop precedes Release; Forget requires complete local identity cleanup.`
type BedLifecycle interface {
	Recover(context.Context, *Bed) error
	Prepare(context.Context, *Bed) error
	Stop(context.Context, *Bed) error
	Release(context.Context, *Bed) error
	Forget(context.Context, *Bed) error
}

type Component[R any] interface {
	DaemonLifecycle
	BedLifecycle
	// Status returns a read-only snapshot without host probes or remote I/O.
	Status() R
}

// Noop supplies only hooks a component does not participate in. A component
// must still explicitly provide Status to satisfy Component.
type Noop struct{}

func (Noop) Recover(context.Context, *Bed) error { return nil }
func (Noop) Prepare(context.Context, *Bed) error { return nil }
func (Noop) Stop(context.Context, *Bed) error    { return nil }
func (Noop) Release(context.Context, *Bed) error { return nil }
func (Noop) Forget(context.Context, *Bed) error  { return nil }

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
	Lifecycle BedLifecycle
}

// Sequence keeps completion at hook granularity. Its owner serializes Run;
// successful hooks are not replayed when a later hook fails. Participants and
// their order are fixed at construction, rather than inferred from registration.
type Sequence struct {
	phase        Phase
	participants []Participant
	next         int
	bed          *Bed
}

func NewSequence(phase Phase, participants ...Participant) *Sequence {
	return &Sequence{phase: phase, participants: append([]Participant(nil), participants...)}
}
func (s *Sequence) Run(ctx context.Context, b *Bed) error {
	if s.bed != nil && s.bed != b {
		return fmt.Errorf("lifecycle: sequence belongs to a different Bed")
	}
	s.bed = b
	for s.next < len(s.participants) {
		if err := ctx.Err(); err != nil {
			return err
		}
		p := s.participants[s.next]
		var err error
		switch s.phase {
		case Recover:
			err = p.Lifecycle.Recover(ctx, b)
		case Prepare:
			err = p.Lifecycle.Prepare(ctx, b)
		case Stop:
			err = p.Lifecycle.Stop(ctx, b)
		case Release:
			err = p.Lifecycle.Release(ctx, b)
		case Forget:
			err = p.Lifecycle.Forget(ctx, b)
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

// DaemonLifecycle is driven once by the composite. Start initializes and returns.
type DaemonLifecycle interface {
	Start(context.Context) error
	Close(context.Context) error
}

// Runnable owns a blocking background loop until cancellation or terminal error.
type Runnable interface{ Run(context.Context) error }

func (Noop) Start(context.Context) error { return nil }
func (Noop) Close(context.Context) error { return nil }
