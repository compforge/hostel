package service

import (
	"context"
	"fmt"
	"log"
	"time"

	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
)

// Observation failures are availability results, not reasons to terminate a
// live execution. Errors are reserved for conflicts and invalid contracts.
type readinessResult struct {
	Ready  bool
	Reason string
}

// readinessCheck keeps the diagnostic and one-time degradation log local to an
// execution. Network isolation and listener inspection are optional enhancements,
// not prerequisites for running a service in the Bed's chosen environment.
func (m *Manager) readinessCheck(g *group, r *record, proc Process, address, token string) func(context.Context) (readinessResult, error) {
	warned := false
	return func(ctx context.Context) (readinessResult, error) {
		ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		inspection, err := m.inspectListener(ctx, proc.PID(), address)
		if err == nil && inspection.State == hostnetwork.ListenerForeign {
			// The application may bind during a /proc snapshot. Require a
			// second consistent observation before replacing its allocation.
			inspection, err = m.inspectListener(ctx, proc.PID(), address)
		}
		if err != nil {
			// Do not treat an inconclusive observation as a foreign listener,
			// or reuse a previously successful observation as current proof.
			inspection.State = hostnetwork.ListenerUnavailable
			inspection.Reason = "inspection_failed"
			m.update(g, r, func(s *Status) { s.Listener = &inspection })
			return readinessResult{Reason: "ListenerInspectionFailed"}, nil
		}
		m.update(g, r, func(s *Status) { s.Listener = &inspection })
		switch inspection.State {
		case hostnetwork.ListenerForeign:
			log.Printf("hostel service listener conflict: bed=%s id=%s service=%s execution=%s address=%s", g.bed.Name, g.bed.ID, r.spec.Name, proc.ExecutionID(), address)
			return readinessResult{}, errBindingConflict
		case hostnetwork.ListenerAbsent:
			return readinessResult{Reason: "ListenerAbsent"}, nil
		case hostnetwork.ListenerUnavailable:
			if !warned {
				log.Printf("hostel service listener inspection unavailable: bed=%s id=%s service=%s execution=%s method=%s reason=%s readiness=process_and_http", g.bed.Name, g.bed.ID, r.spec.Name, proc.ExecutionID(), inspection.Method, inspection.Reason)
				warned = true
			}
		case hostnetwork.ListenerOwned:
		default:
			return readinessResult{}, fmt.Errorf("invalid listener inspection state %q", inspection.State)
		}
		// HTTP success proves availability, not token validation or ownership.
		return probeHTTP(ctx, address, r.spec.HTTP.ReadyPath, token), nil
	}
}
