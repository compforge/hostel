package executor

import (
	"context"

	"github.com/qiankunli/hostel/internal/lifecycle"
)

type Report struct {
	Backend string `json:"backend"`
}

// Binding stops the current realm through its allocation owner's serialized
// shutdown entry. Prepare is empty: creating/replacing a realm remains lazy.
type Binding struct {
	lifecycle.Noop
	factory  Factory
	shutdown func(context.Context) error
}

var _ lifecycle.Component[Report] = (*Binding)(nil)

func Bind(factory Factory, shutdown func(context.Context) error) *Binding {
	return &Binding{factory: factory, shutdown: shutdown}
}
func (b *Binding) Stop(ctx context.Context) error { return b.shutdown(ctx) }
func Describe(factory Factory) Report             { return Report{Backend: factory.Backend()} }
func (b *Binding) Diagnostics() Report            { return Describe(b.factory) }
