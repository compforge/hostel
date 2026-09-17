package service

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/network"
)

var readinessClient = &http.Client{
	Transport:     &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: time.Second}).DialContext, MaxIdleConns: 32, MaxIdleConnsPerHost: 1, IdleConnTimeout: 10 * time.Second, ResponseHeaderTimeout: time.Second},
	Timeout:       2 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse },
}

var errBindingConflict = errors.New("service port occupied by another process")

var probeSlots = make(chan struct{}, 16)

func (m *Manager) supervise(ctx context.Context, g *group, r *record) {
	defer close(r.done)
	restarts := 0
	for {
		m.update(g, r, func(s *Status) { s.Phase = "starting"; s.Reason = ""; s.Restarts = restarts })
		var explicit bool
		var outcome executor.ProcessOutcome
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			explicit, outcome, err = m.run(ctx, g, r, attempt > 0)
			if !errors.Is(err, errBindingConflict) || requiredPort(g.bed, r.spec.PortMapping) {
				break
			}
		}
		if ctx.Err() != nil {
			m.update(g, r, func(s *Status) { s.Phase = "stopped" })
			return
		}
		failure := err != nil || outcome.Kind != executor.ProcessExited || outcome.ExitCode != 0
		automatic := r.spec.Restart == "always" || (r.spec.Restart == "on-failure" && failure) || outcome.Kind == executor.ProcessLost
		if explicit {
			restarts = 0
			continue
		}
		if automatic && restarts < r.spec.MaxRestarts {
			restarts++
			failureReason := "process did not exit successfully"
			if err != nil {
				failureReason = err.Error()
			}
			log.Printf("hostel service restart scheduled: bed=%s id=%s service=%s restart=%d reason=%q outcome=%s exit_code=%d signal=%d", g.bed.Name, g.bed.ID, r.spec.Name, restarts, failureReason, outcome.Kind, outcome.ExitCode, outcome.Signal)
			m.update(g, r, func(s *Status) {
				s.Phase = "backoff"
				s.Restarts = restarts
				s.Reason = "RestartScheduled"
			})
			timer := time.NewTimer(min(time.Duration(1<<min(restarts-1, 5))*time.Second, 30*time.Second))
			select {
			case <-ctx.Done():
				timer.Stop()
				continue
			case <-r.restart:
				timer.Stop()
				restarts = 0
			case <-timer.C:
			}
			continue
		}
		m.update(g, r, func(s *Status) {
			s.Phase = "failed"
			s.Reason = "ProcessExited"
			if err != nil {
				s.Reason = err.Error()
			}
			s.Outcome = &outcome
		})
		select {
		case <-ctx.Done():
			m.update(g, r, func(s *Status) { s.Phase = "stopped" })
			return
		case <-r.restart:
			restarts = 0
		}
	}
}

// +spec=`Readiness loss changes admission only: preserve the process, Execution ID, token, port allocations, forwarder and existing connections until this run actually ends.`
func (m *Manager) run(ctx context.Context, g *group, r *record, avoidPreferred bool) (explicit bool, outcome executor.ProcessOutcome, retErr error) {
	if err := ctx.Err(); err != nil {
		return false, outcome, err
	}
	m.update(g, r, func(s *Status) { s.Phase = "starting"; s.Listener = nil })
	spec := r.spec
	env, err := m.configurations.Resolve(spec.Configuration())
	if err != nil {
		return false, outcome, err
	}
	var mapping *network.PortMapping
	var address, token string
	if spec.HTTP != nil && spec.HTTP.Authentication != nil {
		auth := spec.HTTP.Authentication
		switch auth.TokenSource {
		case bed.TokenSourceGenerated:
			if _, exists := env[auth.TokenEnv]; exists {
				return false, outcome, fmt.Errorf("service %s generated token conflicts with resolved configuration", spec.Name)
			}
			token = randx.Hex(32)
			env[auth.TokenEnv] = token
		case bed.TokenSourceEnvironment:
			// Resolve once per execution so probes and discovery match the process,
			// even if the credential file changes while it is running.
			token = env[auth.TokenEnv]
			if token == "" {
				return false, outcome, fmt.Errorf("service %s token env %s is empty", spec.Name, auth.TokenEnv)
			}
		}
	}
	if spec.PortMapping != "" {
		scope, host, err := g.runtime.Network()
		if err != nil {
			return false, outcome, err
		}
		mapping, err = m.mappings.Reserve(g.bed, spec.PortMapping, spec.Name, scope, host, avoidPreferred)
		if err != nil {
			return false, outcome, err
		}
		g.mu.Lock()
		r.mapping = mapping
		g.mu.Unlock()
		defer func() {
			for {
				if err := mapping.Release(); err == nil {
					break
				}
				m.update(g, r, func(s *Status) { s.Reason = "PortCleanupPending" })
				time.Sleep(time.Second)
			}
			g.mu.Lock()
			if r.mapping == mapping {
				r.mapping = nil
			}
			g.mu.Unlock()
		}()
		address = mapping.ProbeAddress()
	}

	command := append([]string(nil), spec.Command...)
	if mapping != nil {
		replace := strings.NewReplacer("${PORT}", strconv.Itoa(mapping.Port()), "${LISTEN_ADDR}", mapping.ListenAddress())
		for i, v := range command {
			command[i] = replace.Replace(v)
		}
		for k, v := range env {
			// Credentials are opaque and must not undergo address substitution.
			if spec.HTTP == nil || spec.HTTP.Authentication == nil || k != spec.HTTP.Authentication.TokenEnv {
				env[k] = replace.Replace(v)
			}
		}
	}
	proc, err := g.runtime.Start(ctx, Launch{Command: command, Directory: spec.Directory, Env: env})
	if err != nil {
		return false, outcome, fmt.Errorf("service process start failed: %w", err)
	}
	defer func() {
		// Withdraw discovery before terminating; existing forwarding connections
		// are closed before the process/environment can be reused.
		m.update(g, r, func(s *Status) { s.Phase = "stopping" })
		if mapping != nil {
			if err := mapping.Withdraw(); err != nil {
				retErr = errors.Join(retErr, err)
			}
		}
		// Keep this owner alive if cleanup fails. Stop callers retain the group
		// and can retry; never release a live process's port or filesystem.
		for {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Duration(spec.StopSeconds+5)*time.Second)
			err := proc.Stop(cleanup, time.Duration(spec.StopSeconds)*time.Second)
			cancel()
			if err == nil {
				break
			}
			m.update(g, r, func(s *Status) { s.Reason = "ProcessCleanupPending" })
			timer := time.NewTimer(time.Second)
			<-timer.C
		}
		outcome = proc.Outcome()
		m.update(g, r, func(s *Status) { s.Outcome = &outcome })
	}()
	if mapping != nil {
		mapping.Bind(proc.ExecutionID())
	}
	m.update(g, r, func(s *Status) {
		s.Phase = "running"
		s.ExecutionID = proc.ExecutionID()
		s.ExecutorID = proc.ExecutorID()
		s.Outcome = nil
	})
	startup, cancel := context.WithTimeout(ctx, time.Duration(spec.StartupSeconds)*time.Second)
	defer cancel()
	checkReadiness := m.readinessCheck(g, r, proc, address, token)
	for {
		select {
		case <-proc.Done():
			return false, proc.Outcome(), fmt.Errorf("process exited before readiness")
		default:
		}
		result := readinessResult{Ready: spec.HTTP == nil}
		if spec.HTTP != nil {
			result, err = checkReadiness(startup)
			if err != nil {
				return false, outcome, err
			}
		}
		if result.Ready {
			select {
			case <-proc.Done():
				return false, proc.Outcome(), fmt.Errorf("process exited before readiness")
			default:
			}
			break
		}
		m.update(g, r, func(s *Status) { s.Reason = result.Reason })
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-startup.Done():
			timer.Stop()
			return false, outcome, fmt.Errorf("service readiness timeout")
		case <-proc.Done():
			timer.Stop()
			return false, proc.Outcome(), fmt.Errorf("process exited before readiness")
		case <-r.restart:
			timer.Stop()
			return true, outcome, nil
		case <-timer.C:
		}
	}
	if mapping != nil {
		if err := mapping.Publish(); err != nil {
			return false, outcome, err
		}
	}

	g.mu.Lock()
	r.token = token
	g.mu.Unlock()
	m.update(g, r, func(s *Status) { s.Ready = true; s.Reason = "" })
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return false, outcome, ctx.Err()
		case <-r.restart:
			return true, outcome, nil
		case <-proc.Done():
			return false, proc.Outcome(), nil
		case <-ticker.C:
			if spec.HTTP != nil {
				result, err := checkReadiness(ctx)
				if err != nil {
					return false, outcome, err
				}
				// A completed probe must not republish an execution that stopped
				// during the check. Only this loop owns readiness transitions.
				select {
				case <-ctx.Done():
					return false, outcome, ctx.Err()
				case <-proc.Done():
					return false, proc.Outcome(), nil
				default:
				}
				m.update(g, r, func(s *Status) { s.Ready = result.Ready; s.Reason = result.Reason })
			}
		}
	}
}

func probeHTTP(ctx context.Context, address, path, token string) readinessResult {
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
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := readinessClient.Do(req)
	if err != nil {
		return readinessResult{Reason: "ReadinessProbeFailed"}
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return readinessResult{Reason: fmt.Sprintf("ReadinessHTTPStatus%d", resp.StatusCode)}
	}
	return readinessResult{Ready: true}
}

func requiredPort(b *bed.Bed, name string) bool {
	for _, p := range b.Spec().PortMappings {
		if p.Name == name {
			return p.RequireBedPort
		}
	}
	return false
}
