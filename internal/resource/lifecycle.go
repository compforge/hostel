package resource

import (
	"context"

	"github.com/qiankunli/hostel/internal/lifecycle"
)

type Binding struct {
	lifecycle.Noop
	tracker Tracker
	id      string
}

var _ lifecycle.Component[Report] = (*Binding)(nil)

func Bind(tracker Tracker, id string) *Binding { return &Binding{tracker: tracker, id: id} }
func (b *Binding) Prepare(context.Context) error {
	group, err := b.tracker.OpenGroup(b.id)
	if err != nil {
		return err
	}
	if group != nil {
		return group.Close()
	}
	return nil
}
func (b *Binding) Release(context.Context) error { return b.tracker.Release(b.id) }
func (b *Binding) Diagnostics() Report           { return b.tracker.Report() }
