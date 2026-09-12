package amenity

import (
	"context"
	"errors"
	"github.com/qiankunli/hostel/internal/bed"
)

// Bed hooks only release a Bed slice. The daemon owns the shared facilities.
func (r *Registry) Release(_ context.Context, b *bed.Bed) error {
	if r == nil {
		return nil
	}
	r.mu.RLock()
	owned := r.beds[b]
	r.mu.RUnlock()
	if !owned {
		return nil
	}
	if err := r.ReleaseAll(b.ID.String()); err != nil {
		return err
	}
	r.mu.Lock()
	delete(r.beds, b)
	r.mu.Unlock()
	if r != nil {
		r.status.Set(b, bed.AmenityStatus{Released: true})
	}
	return nil
}
func (r *Registry) SetStatusWriter(status bed.StatusWriter[bed.AmenityStatus]) {
	if r != nil {
		r.status = status
	}
}
func (r *Registry) Start(context.Context) error { return nil }
func (r *Registry) Close(ctx context.Context) error {
	var result error
	for _, a := range r.List() {
		if closer, ok := a.(interface{ Close(context.Context) error }); ok {
			result = errors.Join(result, closer.Close(ctx))
		}
	}
	return result
}
func (r *Registry) Status() map[string]string {
	report := make(map[string]string)
	for _, item := range r.List() {
		report[item.Name()] = item.State()
	}
	return report
}

var _ bed.Component[map[string]string] = (*Registry)(nil)

func (r *Registry) Prepare(_ context.Context, b *bed.Bed) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.beds[b] = true
	return nil
}
