package pillow

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/executor"
)

func (m *Manager) supervise(ctx context.Context, g *group, r *record) {
	defer close(r.done)
	restarts := 0
	for {
		m.update(g, r, func(s *Status) { s.Phase = "starting"; s.Reason = ""; s.Restarts = restarts })
		var explicit bool
		var outcome executor.ProcessOutcome
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			explicit, outcome, err = m.run(ctx, g, r)
			if !retryPortConflict(err, r.spec) {
				break
			}
		}
		if ctx.Err() != nil {
			m.update(g, r, func(s *Status) { s.Phase = "stopped" })
			return
		}
		failure := err != nil || outcome.Kind != executor.ProcessExited || outcome.ExitCode != 0
		automatic := r.spec.RestartPolicy == "always" || (r.spec.RestartPolicy == "on-failure" && failure) || outcome.Kind == executor.ProcessLost
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
			log.Printf("hostel pillow restart scheduled: bed=%s id=%s pillow=%s restart=%d reason=%q outcome=%s exit_code=%d signal=%d", g.bed.Name, g.bed.ID, r.spec.Name, restarts, failureReason, outcome.Kind, outcome.ExitCode, outcome.Signal)
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
			if !failure {
				s.Phase = "completed"
			}
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

// +spec=`Readiness failure preserves the process, Execution ID, port allocations and existing connections; it never consumes the process restart budget.`
// +link=component://docs/pillow.md
func (m *Manager) run(ctx context.Context, g *group, r *record) (explicit bool, outcome executor.ProcessOutcome, retErr error) {
	if err := ctx.Err(); err != nil {
		return false, outcome, err
	}
	m.update(g, r, func(s *Status) { s.Phase = "starting"; s.Listener = nil })
	spec := r.spec
	env, err := m.configurations.Resolve(bed.PillowConfiguration(spec))
	if err != nil {
		return false, outcome, err
	}
	mappings, err := m.reservePorts(g, r)
	defer m.releasePorts(g, r, mappings)
	if err != nil {
		return false, outcome, err
	}
	command := portCommand(spec, mappings)
	proc, err := g.runtime.Start(ctx, Launch{Name: spec.Name, Command: command, Directory: spec.Workdir, Env: env})
	if err != nil {
		return false, outcome, fmt.Errorf("pillow process start failed: %w", err)
	}
	defer func() {
		m.update(g, r, func(s *Status) { s.Phase = "stopping" })
		for _, mapping := range mappings {
			retErr = errors.Join(retErr, mapping.Withdraw())
		}
		// Cleanup retains ownership until the whole process group has stopped.
		for {
			cleanup, cancel := context.WithTimeout(context.Background(), time.Duration(spec.StopSeconds+5)*time.Second)
			err := proc.Stop(cleanup, time.Duration(spec.StopSeconds)*time.Second)
			cancel()
			if err == nil {
				break
			}
			m.update(g, r, func(s *Status) { s.Reason = "ProcessCleanupPending" })
			time.Sleep(time.Second)
		}
		outcome = proc.Outcome()
		m.update(g, r, func(s *Status) { s.Outcome = &outcome })
	}()
	for _, mapping := range mappings {
		mapping.Bind(proc.ExecutionID())
	}
	m.update(g, r, func(s *Status) {
		s.Phase = "running"
		s.ExecutionID = proc.ExecutionID()
		s.ExecutorID = proc.ExecutorID()
		s.Outcome = nil
	})
	check := m.readinessCheck(g, r, proc, mappings)
	tracker := readinessTracker{}
	period := time.Second
	delay := time.Duration(0)
	if spec.ReadinessProbe != nil {
		period = time.Duration(spec.ReadinessProbe.PeriodSeconds) * time.Second
		delay = time.Duration(spec.ReadinessProbe.InitialDelaySeconds) * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	published := false
	for {
		select {
		case <-ctx.Done():
			return false, outcome, ctx.Err()
		case <-r.restart:
			return true, outcome, nil
		case <-proc.Done():
			return false, proc.Outcome(), nil
		case <-timer.C:
			result, err := check(ctx)
			if err != nil {
				return false, outcome, err
			}
			// A probe completing after process exit must never republish that execution.
			select {
			case <-ctx.Done():
				return false, outcome, ctx.Err()
			case <-proc.Done():
				return false, proc.Outcome(), nil
			default:
			}
			result = tracker.observe(result, spec.ReadinessProbe)
			if result.Ready && !published {
				for _, mapping := range mappings {
					if err := mapping.Publish(); err != nil {
						return false, outcome, err
					}
				}
				published = true
			}
			m.update(g, r, func(s *Status) { s.Ready = result.Ready; s.Reason = result.Reason })
			timer.Reset(period)
		}
	}
}

// Threshold counters belong to one execution and reset on an opposite result.
type readinessTracker struct {
	ready     bool
	successes int
	failures  int
}

func (s *readinessTracker) observe(result readinessResult, probe *bed.Probe) readinessResult {
	success, failure := 1, 1
	if probe != nil {
		success, failure = probe.SuccessThreshold, probe.FailureThreshold
	}
	if result.Ready {
		s.failures = 0
		s.successes = min(s.successes+1, success)
		if s.successes >= success {
			s.ready = true
		}
	} else {
		s.successes = 0
		s.failures = min(s.failures+1, failure)
		if s.failures >= failure {
			s.ready = false
		}
	}
	if s.ready {
		return readinessResult{Ready: true}
	}
	if result.Ready {
		return readinessResult{Reason: "ReadinessSuccessThresholdPending"}
	}
	return result
}
