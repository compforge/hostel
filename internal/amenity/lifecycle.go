package amenity

import (
	"context"

	"github.com/qiankunli/hostel/internal/lifecycle"
)

type Binding struct {
	lifecycle.Noop
	registry *Registry
	id       string
}

var _ lifecycle.Component[map[string]string] = (*Binding)(nil)

func Bind(registry *Registry, id string) *Binding { return &Binding{registry: registry, id: id} }
func (b *Binding) Release(context.Context) error  { return b.registry.ReleaseAll(b.id) }
func (r *Registry) Diagnostics() map[string]string {
	report := make(map[string]string)
	for _, item := range r.List() {
		report[item.Name()] = item.State()
	}
	return report
}
func (b *Binding) Diagnostics() map[string]string { return b.registry.Diagnostics() }
