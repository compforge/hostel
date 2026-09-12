package amenity

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
)

func (m *Manager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("amenity: manager closed")
	}
	if m.started {
		m.mu.Unlock()
		return nil
	}
	m.started = true
	facilities := append([]Amenity(nil), m.amenities...)
	m.mu.Unlock()
	for i, a := range facilities {
		if err := a.Start(ctx); err != nil {
			result := fmt.Errorf("amenity %s: start: %w", a.Name(), err)
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			for j := i; j >= 0; j-- {
				result = errors.Join(result, facilities[j].Close(cleanup))
			}
			cancel()
			m.mu.Lock()
			m.closed = true
			m.mu.Unlock()
			return result
		}
	}
	return nil
}

func (m *Manager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	m.lifecycle.Lock()
	defer m.lifecycle.Unlock()
	m.mu.Lock()
	m.closed = true
	ids := make([]bed.ID, 0, len(m.beds))
	for id := range m.beds {
		ids = append(ids, id)
	}
	m.mu.Unlock()
	var result error
	for _, id := range ids {
		result = errors.Join(result, m.ReleaseBed(ctx, id))
	}
	facilities := m.List()
	for i := len(facilities) - 1; i >= 0; i-- {
		if err := facilities[i].Close(ctx); err != nil {
			result = errors.Join(result, fmt.Errorf("amenity %s: close: %w", facilities[i].Name(), err))
		}
	}
	return result
}
