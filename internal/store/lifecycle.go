package store

import (
	"context"

	"github.com/qiankunli/hostel/internal/lifecycle"
)

// Binding carries this initialization's input and result, not a shared mutable
// "current Bed" on the Store manager. Persist and Delete remain domain actions.
type Binding struct {
	lifecycle.Noop
	manager *Manager
	kind    SyncKind
	request StageInRequest
	result  StageInResult
}

var _ lifecycle.Component[Report] = (*Binding)(nil)

func (m *Manager) Bind(kind SyncKind, request StageInRequest) *Binding {
	return &Binding{manager: m, kind: kind, request: request}
}
func (b *Binding) Prepare(ctx context.Context) error {
	result, err := b.manager.StageInBedFS(ctx, b.kind, b.request)
	b.result = result
	return err
}
func (b *Binding) Result() StageInResult { return b.result }
func (b *Binding) Stop(ctx context.Context) error {
	return b.manager.StopTransfers(ctx, b.request.BedID)
}
func (b *Binding) Diagnostics() Report { return b.manager.Diagnostics() }
