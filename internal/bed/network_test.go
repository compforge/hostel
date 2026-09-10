package bed

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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

func (n *initializationNetwork) Acquire(ctx context.Context, _ string) (network.Attachment, error) {
	if n.active.Swap(true) {
		return nil, errors.New("reused network before rollback finished")
	}
	if n.acquired.Add(1) == 1 && n.afterAcquire != nil {
		n.afterAcquire(ctx)
	}
	return n, nil
}

func (n *initializationNetwork) Close(ctx context.Context) error {
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

func (n *initializationNetwork) Enter(*exec.Cmd) error { return nil }
func (n *initializationNetwork) Gateway() string       { return "" }

func TestEvictionFencesSameIDUntilDirectoryCleanup(t *testing.T) {
	m := newTestManager(t)
	n := &initializationNetwork{releasing: make(chan struct{}), resume: make(chan struct{})}
	m.network = n
	b, err := m.Ensure(context.Background(), "reused")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(b.Workspace(), "old"), []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := m.Evict(context.Background(), b.ID); done <- err }()
	t.Cleanup(func() { close(n.resume) })
	select {
	case <-n.releasing:
	case <-time.After(5 * time.Second):
		t.Fatal("cleanup did not start")
	}
	if _, err := m.Ensure(context.Background(), b.ID); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("same-ID init during cleanup: %v", err)
	}
	if _, err := m.BeginOperation(b, OpFile, time.Second); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("old Bed admitted work: %v", err)
	}
	if status, ok := m.Initialization(b.ID); !ok || status.Readiness.Ready || status.Phase != PhaseEvicting {
		t.Fatalf("cleanup status: %+v %t", status, ok)
	}
	n.resume <- struct{}{}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	next, err := m.Ensure(context.Background(), b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if next == b {
		t.Fatal("reused retired Bed")
	}
	if _, err := os.Stat(filepath.Join(next.Workspace(), "old")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale file survived: %v", err)
	}
	if err := m.teardown(next); err != nil {
		t.Fatal(err)
	}
}
