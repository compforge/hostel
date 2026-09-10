package bed

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/network"
	"github.com/qiankunli/hostel/internal/store"
)

// Embed the nil-safe manager for unused command methods; these tests exercise
// the Bed lifecycle without requiring Linux namespace privileges.
type initializationNetwork struct {
	*network.Manager
	afterAcquire func(context.Context)
	releasing    chan struct{}
	resume       chan struct{}
	active       atomic.Bool
	acquired     atomic.Int32
	released     atomic.Int32
}

func (n *initializationNetwork) Report() network.Report {
	return network.Report{Enabled: true}
}

func (n *initializationNetwork) Acquire(ctx context.Context, _ string) error {
	if n.active.Swap(true) {
		return errors.New("reused network before rollback finished")
	}
	if n.acquired.Add(1) == 1 {
		n.afterAcquire(ctx)
	}
	return nil
}

func (n *initializationNetwork) Release(ctx context.Context, _ string) error {
	if n.released.Add(1) == 1 {
		close(n.releasing)
		<-n.resume
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	n.active.Store(false)
	return nil
}

func TestInitializationRollsBackNetworkBeforeCompletion(t *testing.T) {
	for _, failure := range []string{"cancel", "timeout", "publication"} {
		t.Run(failure, func(t *testing.T) {
			m := newTestManager(t)
			ctx, cancel := context.WithCancel(context.Background())
			if failure == "timeout" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			}
			defer cancel()
			initialization := &bedInitialization{
				status: InitializationStatus{ID: "rollback", Store: store.KindNoop, BedStatus: BedStatus{Phase: PhaseInitializing}},
				done:   make(chan struct{}), cancel: cancel,
			}
			m.initializations["rollback"] = initialization
			n := &initializationNetwork{releasing: make(chan struct{}), resume: make(chan struct{})}
			n.afterAcquire = func(ctx context.Context) {
				switch failure {
				case "cancel":
					cancel()
				case "timeout":
					<-ctx.Done()
				case "publication":
					// Model an initializer superseded before resident publication.
					m.mu.Lock()
					delete(m.initializations, "rollback")
					m.mu.Unlock()
				}
			}
			m.network = n
			go m.runInitialization(ctx, initialization)
			t.Cleanup(func() { close(n.resume); <-initialization.done })
			select {
			case <-n.releasing:
			case <-initialization.done:
				t.Fatal("initialization completed without rolling back its network")
			case <-time.After(5 * time.Second):
				t.Fatal("network rollback did not start")
			}
			select {
			case <-initialization.done:
				t.Fatal("waiters notified before network cleanup completed")
			default:
			}
			if failure != "publication" {
				current, resident, err := m.beginInitialization(context.Background(), "rollback", "")
				if err != nil || current != initialization || resident != nil {
					t.Fatalf("identity released during rollback: current=%p resident=%v err=%v", current, resident, err)
				}
			}
			// Permit cleanup without closing the channel; Cleanup closes it on
			// assertion failure too, so the initializer cannot outlive the test.
			n.resume <- struct{}{}
			<-initialization.done
			want := context.Canceled
			if failure == "timeout" {
				want = context.DeadlineExceeded
			}
			if !errors.Is(initialization.err, want) || initialization.bed != nil || n.active.Load() {
				t.Fatalf("rollback: err=%v bed=%v active=%t", initialization.err, initialization.bed, n.active.Load())
			}
			retryCtx, retryCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer retryCancel()
			resident, err := m.Ensure(retryCtx, "rollback")
			if err != nil {
				t.Fatalf("reinitialize: %v", err)
			}
			if n.acquired.Load() != 2 || n.released.Load() != 1 || !n.active.Load() {
				t.Fatal("successful reinitialization did not retain its fresh network")
			}
			m.teardown(resident)
		})
	}
}
