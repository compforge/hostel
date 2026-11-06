package pillow

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/qiankunli/hostel/internal/bed/network"
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
// not prerequisites for running a pillow in the Bed's chosen environment.
func (m *Manager) readinessCheck(g *group, r *record, proc Process, mappings []*network.PortMapping) func(context.Context) (readinessResult, error) {
	warned := false
	return func(ctx context.Context) (readinessResult, error) {
		probe := r.spec.ReadinessProbe
		if probe == nil {
			return readinessResult{Ready: true}, nil
		}
		mapping, err := probeMapping(r.spec, mappings)
		if err != nil {
			return readinessResult{}, err
		}
		address := mapping.ProbeAddress()
		ctx, cancel := context.WithTimeout(ctx, time.Duration(probe.TimeoutSeconds)*time.Second)
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
			log.Printf("hostel pillow listener conflict: bed=%s id=%s pillow=%s execution=%s address=%s", g.bed.Name, g.bed.ID, r.spec.Name, proc.ExecutionID(), address)
			return readinessResult{}, &portConflictError{name: mapping.Status().Name}
		case hostnetwork.ListenerAbsent:
			return readinessResult{Reason: "ListenerAbsent"}, nil
		case hostnetwork.ListenerUnavailable:
			if !warned {
				log.Printf("hostel pillow listener inspection unavailable: bed=%s id=%s pillow=%s execution=%s method=%s reason=%s readiness=process_and_probe", g.bed.Name, g.bed.ID, r.spec.Name, proc.ExecutionID(), inspection.Method, inspection.Reason)
				warned = true
			}
		case hostnetwork.ListenerOwned:
		default:
			return readinessResult{}, fmt.Errorf("invalid listener inspection state %q", inspection.State)
		}
		// Probe success proves availability, not application authentication or ownership.
		if probe.HTTPGet != nil {
			return probeHTTP(ctx, address, probe.HTTPGet.Path), nil
		}
		return probeTCP(ctx, address), nil
	}
}

// Shared transports and bounded probe concurrency avoid one client/pool per Pillow.
var readinessClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, MaxIdleConns: 32, MaxIdleConnsPerHost: 1, IdleConnTimeout: 10 * time.Second},
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}
var probeSlots = make(chan struct{}, 16)

func probeHTTP(ctx context.Context, address, path string) readinessResult {
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		return readinessResult{Reason: "ReadinessProbeUnavailable"}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		return readinessResult{Reason: "ReadinessProbeInvalid"}
	}
	resp, err := readinessClient.Do(req)
	if err != nil {
		return readinessResult{Reason: "ReadinessProbeFailed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return readinessResult{Reason: fmt.Sprintf("ReadinessHTTPStatus%d", resp.StatusCode)}
	}
	return readinessResult{Ready: true}
}

func probeTCP(ctx context.Context, address string) readinessResult {
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		return readinessResult{Reason: "ReadinessProbeUnavailable"}
	}
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", address)
	if err != nil {
		return readinessResult{Reason: "ReadinessTCPFailed"}
	}
	conn.Close()
	return readinessResult{Ready: true}
}
