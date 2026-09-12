package network

import (
	"context"
	"github.com/qiankunli/hostel/internal/lifecycle"
)

// Provider allows an allocation-bound participant to use real or test managers.
type Provider interface {
	Acquire(context.Context, string) (Attachment, error)
	Diagnostics() Report
}
type Binding struct {
	lifecycle.Noop
	provider   Provider
	id         string
	attachment Attachment
}

var _ lifecycle.Component[Report] = (*Binding)(nil)

func Bind(provider Provider, id string) *Binding { return &Binding{provider: provider, id: id} }
func (b *Binding) Prepare(ctx context.Context) error {
	attachment, err := b.provider.Acquire(ctx, b.id)
	if err == nil {
		b.attachment = attachment
	}
	return err
}
func (b *Binding) Attachment() Attachment { return b.attachment }
func (b *Binding) Release(ctx context.Context) error {
	if b.attachment == nil {
		return nil
	}
	return b.attachment.Close(ctx)
}
func (b *Binding) Diagnostics() Report { return b.provider.Diagnostics() }
