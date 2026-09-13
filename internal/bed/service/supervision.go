package service

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/qiankunli/go-stdx/randx"
	"github.com/qiankunli/hostel/internal/bed/executor"
	hostnetwork "github.com/qiankunli/hostel/internal/host/network"
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
		m.update(g, r, func(s *Status) { s.Phase = "starting"; s.Endpoint = ""; s.Reason = ""; s.Restarts = restarts })
		var explicit bool
		var outcome executor.ProcessOutcome
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			explicit, outcome, err = m.run(ctx, g, r)
			if !errors.Is(err, errBindingConflict) {
				break
			}
		}
		if ctx.Err() != nil {
			m.update(g, r, func(s *Status) { s.Phase = "stopped"; s.Endpoint = "" })
			return
		}
		failure := err != nil || outcome.Kind != executor.ProcessExited || outcome.ExitCode != 0
		automatic := r.template.Restart == "always" || (r.template.Restart == "on-failure" && failure) || outcome.Kind == executor.ProcessLost
		if explicit {
			restarts = 0
			continue
		}
		if automatic && restarts < r.template.MaxRestarts {
			restarts++
			m.update(g, r, func(s *Status) {
				s.Phase = "backoff"
				s.Endpoint = ""
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
			s.Endpoint = ""
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

func (m *Manager) run(ctx context.Context, g *group, r *record) (explicit bool, outcome executor.ProcessOutcome, retErr error) {
	if err := ctx.Err(); err != nil {
		return false, outcome, err
	}
	m.update(g, r, func(s *Status) { s.Phase = "starting"; s.Endpoint = "" })
	t := r.template
	env := maps.Clone(t.Env)
	if env == nil {
		env = make(map[string]string)
	}
	maps.Copy(env, r.spec.Env)
	for k, file := range t.EnvFiles {
		value, err := os.ReadFile(file)
		if err != nil {
			return false, outcome, fmt.Errorf("credential source unavailable for %s", k)
		}
		if len(value) > 65536 || strings.ContainsRune(string(value), 0) {
			return false, outcome, fmt.Errorf("invalid credential value for %s", k)
		}
		env[k] = strings.TrimSuffix(string(value), "\n")
	}
	var allocation *hostnetwork.PortAllocation
	var scope, host, address, token string
	var forward *hostnetwork.TCPForwarder
	if t.HTTP != nil {
		var err error
		scope, host, err = g.runtime.Network()
		if err != nil {
			return false, outcome, err
		}
		allocation, err = m.ports.Reserve(g.bed.ID.String()+"/service/"+r.spec.Name, scope, "0.0.0.0:0")
		if err != nil {
			return false, outcome, err
		}
		defer allocation.Release()
		address = net.JoinHostPort(host, strconv.Itoa(allocation.Port()))
		token = randx.Hex(32)
		env[t.HTTP.TokenEnv] = token
	}
	command := append([]string(nil), t.Command...)
	if allocation != nil {
		replace := strings.NewReplacer("${PORT}", strconv.Itoa(allocation.Port()), "${LISTEN_ADDR}", allocation.Address())
		for i, v := range command {
			command[i] = replace.Replace(v)
		}
		for k, v := range env {
			if k != t.HTTP.TokenEnv {
				env[k] = replace.Replace(v)
			}
		}
	}
	proc, err := g.runtime.Start(ctx, Launch{Command: command, Directory: t.Directory, Env: env})
	if err != nil {
		return false, outcome, fmt.Errorf("service process start failed: %w", err)
	}
	defer func() {
		// Withdraw discovery before terminating; existing forwarding connections
		// are closed before the process/environment can be reused.
		m.update(g, r, func(s *Status) { s.Endpoint = ""; s.Phase = "stopping" })
		if forward != nil {
			_ = forward.Close()
		}
		// Keep this owner alive if cleanup fails. Stop callers retain the group
		// and can retry; never release a live process's port or filesystem.
		for {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Duration(t.StopSeconds+5)*time.Second)
			err := proc.Stop(cleanup, time.Duration(t.StopSeconds)*time.Second)
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
	m.update(g, r, func(s *Status) { s.ExecutionID = proc.ExecutionID(); s.ExecutorID = proc.ExecutorID(); s.Outcome = nil })
	startup, cancel := context.WithTimeout(ctx, time.Duration(t.StartupSeconds)*time.Second)
	defer cancel()
	for {
		select {
		case <-proc.Done():
			return false, proc.Outcome(), startupExitError(startup, address)
		default:
		}
		ready := t.HTTP == nil
		if t.HTTP != nil {
			owned, err := hostnetwork.OwnsTCPListener(startup, proc.PID(), allocation.Port())
			if err != nil {
				return false, outcome, fmt.Errorf("socket ownership verification failed")
			}
			if owned {
				ready = probeHTTP(startup, address, t.HTTP.ReadyPath, token)
			}
			if !owned {
				conn, dialErr := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(startup, "tcp", address)
				if dialErr == nil {
					conn.Close()
					// Recheck after dial: the service may have bound during the
					// inspection window. Only a foreign listener means conflict.
					owned, err = hostnetwork.OwnsTCPListener(startup, proc.PID(), allocation.Port())
					if err == nil && !owned {
						return false, outcome, errBindingConflict
					}
				}
			}
		}
		if ready {
			break
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-startup.Done():
			timer.Stop()
			return false, outcome, fmt.Errorf("service readiness timeout")
		case <-proc.Done():
			timer.Stop()
			return false, proc.Outcome(), startupExitError(startup, address)
		case <-r.restart:
			timer.Stop()
			return true, outcome, nil
		case <-timer.C:
		}
	}
	endpoint := ""
	if allocation != nil {
		if err := allocation.Confirm(); err != nil {
			return false, outcome, err
		}
		publishedPort := allocation.Port()
		if scope != "" {
			var err error
			forward, err = hostnetwork.NewTCPForwarder(m.ports, g.bed.ID.String()+"/service/"+r.spec.Name+"/publish", address)
			if err != nil {
				return false, outcome, err
			}
			publishedPort = forward.Port()
		}
		endpoint = "http://" + net.JoinHostPort(m.advertise, strconv.Itoa(publishedPort))
	}
	g.mu.Lock()
	r.token = token
	g.mu.Unlock()
	m.update(g, r, func(s *Status) { s.Phase = "ready"; s.Endpoint = endpoint; s.Reason = "" })
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
			if t.HTTP != nil {
				owned, err := hostnetwork.OwnsTCPListener(ctx, proc.PID(), allocation.Port())
				if err != nil || !owned || !probeHTTP(ctx, address, t.HTTP.ReadyPath, token) {
					return false, outcome, fmt.Errorf("service lost readiness")
				}
			}
		}
	}
}

func probeHTTP(ctx context.Context, address, path, token string) bool {
	select {
	case probeSlots <- struct{}{}:
		defer func() { <-probeSlots }()
	case <-ctx.Done():
		return false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+path, nil)
	if err != nil {
		return false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := readinessClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.StatusCode >= 200 && resp.StatusCode < 300
}

func startupExitError(ctx context.Context, address string) error {
	if address != "" {
		conn, err := (&net.Dialer{Timeout: 100 * time.Millisecond}).DialContext(ctx, "tcp", address)
		if err == nil {
			conn.Close()
			return errBindingConflict
		}
	}
	return fmt.Errorf("process exited before readiness")
}
