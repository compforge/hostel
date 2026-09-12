package network

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	creates       int
	err           error
	endpoint      *fakeEndpoint
	createStarted chan struct{}
	createRelease chan struct{}
}

func (b *fakeBackend) Create(context.Context) (endpoint, error) {
	b.creates++
	if b.createStarted != nil {
		close(b.createStarted)
		<-b.createRelease
	}
	if b.err != nil {
		return nil, b.err
	}
	return b.endpoint, nil
}
func (b *fakeBackend) Close(context.Context) error { return nil }

type fakeEndpoint struct {
	closed       int
	err          error
	closeStarted chan struct{}
	closeRelease chan struct{}
	closeOnce    sync.Once
}

func (e *fakeEndpoint) Wrap(cmd *exec.Cmd) { cmd.Args = append([]string{"network"}, cmd.Args...) }
func (e *fakeEndpoint) Gateway() string    { return "198.18.0.1" }
func (e *fakeEndpoint) Close(context.Context) error {
	if e.closeStarted != nil {
		e.closeOnce.Do(func() { close(e.closeStarted) })
		<-e.closeRelease
	}
	e.closed++
	return e.err
}
func testPool(b backend) *Pool {
	return &Pool{report: Status{Available: true, Backend: "netns"}, backend: b, allocations: make(map[string]*attachment), pending: make(map[string]*acquisition)}
}

func TestDisabledDoesNotChangeCommands(t *testing.T) {
	var m *Pool
	cmd := exec.Command("/bin/sh", "-c", "echo hi")
	before := cmd.String()
	lease, err := m.Acquire(context.Background(), "a")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disabled pool: %v", err)
	}
	if lease != nil {
		t.Fatal("disabled manager returned an allocation")
	}
	if cmd.String() != before || m.Status().Available {
		t.Fatal("disabled manager changed execution")
	}
}
func TestAcquireSingleFlightAndNoSilentFallback(t *testing.T) {
	b := &fakeBackend{endpoint: &fakeEndpoint{}}
	m := testPool(b)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.Acquire(context.Background(), "allocation"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if b.creates != 1 {
		t.Fatalf("created %d networks", b.creates)
	}
	lease := m.allocations["allocation"]
	if err := lease.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := lease.Enter(exec.Command("true")); err == nil {
		t.Fatal("released network reused")
	}
	b.err = errors.New("permission revoked")
	if _, err := m.Acquire(context.Background(), "allocation"); !errors.Is(err, b.err) {
		t.Fatalf("runtime failure: %v", err)
	}
	if !m.Status().Available {
		t.Fatal("runtime failure silently disabled manager")
	}
}
func TestCleanupFailureRetainsOwnership(t *testing.T) {
	ep := &fakeEndpoint{err: errors.New("busy")}
	m := testPool(&fakeBackend{endpoint: ep})
	if _, err := m.Acquire(context.Background(), "allocation"); err != nil {
		t.Fatal(err)
	}
	lease := m.allocations["allocation"]
	if err := lease.Close(context.Background()); err == nil {
		t.Fatal("lost cleanup error")
	}
	if err := lease.Enter(exec.Command("true")); err == nil {
		t.Fatal("partially released network reused")
	}
	if _, err := m.Acquire(context.Background(), "allocation"); err == nil {
		t.Fatal("failed cleanup accepted as usable")
	}
	ep.err = nil
	if err := m.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ep.closed != 3 {
		t.Fatalf("cleanup not retried: %d", ep.closed)
	}
	if _, err := m.Acquire(context.Background(), "allocation"); err == nil {
		t.Fatal("closed manager accepted network")
	}
}

func TestOldAttachmentCannotReleaseReplacement(t *testing.T) {
	b := &fakeBackend{endpoint: &fakeEndpoint{}}
	m := testPool(b)
	old, _ := m.Acquire(context.Background(), "allocation")
	if err := old.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	next := &fakeEndpoint{}
	b.endpoint = next
	fresh, err := m.Acquire(context.Background(), "allocation")
	if err != nil {
		t.Fatal(err)
	}
	if err := old.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if next.closed != 0 {
		t.Fatal("old handle cleaned new endpoint")
	}
	if err := old.Enter(exec.Command("true")); err == nil {
		t.Fatal("old handle admitted execution")
	}
	if err := fresh.Enter(exec.Command("true")); err != nil {
		t.Fatal(err)
	}
}

func TestSlowCreateDoesNotBlockExistingAttachment(t *testing.T) {
	b := &fakeBackend{endpoint: &fakeEndpoint{}}
	m := testPool(b)
	existing, err := m.Acquire(context.Background(), "existing")
	if err != nil {
		t.Fatal(err)
	}
	b.createStarted = make(chan struct{})
	b.createRelease = make(chan struct{})
	created := make(chan error, 1)
	go func() {
		_, err := m.Acquire(context.Background(), "slow")
		created <- err
	}()
	<-b.createStarted

	entered := make(chan error, 1)
	go func() { entered <- existing.Enter(exec.Command("true")) }()
	select {
	case err := <-entered:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("existing attachment was blocked by another allocation's create")
	}
	close(b.createRelease)
	if err := <-created; err != nil {
		t.Fatal(err)
	}
}

func TestManagerCloseRetriesFailedAttachmentCleanup(t *testing.T) {
	ep := &fakeEndpoint{err: errors.New("busy")}
	m := testPool(&fakeBackend{endpoint: ep})
	if _, err := m.Acquire(context.Background(), "allocation"); err != nil {
		t.Fatal(err)
	}
	if err := m.Close(context.Background()); err == nil {
		t.Fatal("expected first close to report cleanup failure")
	}
	ep.err = nil
	if err := m.Close(context.Background()); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if len(m.allocations) != 0 {
		t.Fatalf("retry left %d attachments", len(m.allocations))
	}
}

func TestManagerCloseWaitsForPendingCleanup(t *testing.T) {
	ep := &fakeEndpoint{closeStarted: make(chan struct{}), closeRelease: make(chan struct{})}
	b := &fakeBackend{
		endpoint:      ep,
		createStarted: make(chan struct{}),
		createRelease: make(chan struct{}),
	}
	m := testPool(b)
	acquired := make(chan error, 1)
	go func() {
		_, err := m.Acquire(context.Background(), "allocation")
		acquired <- err
	}()
	<-b.createStarted

	closeCtx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	if err := m.Close(closeCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first close = %v, want deadline", err)
	}
	cancel()
	close(b.createRelease)
	<-ep.closeStarted

	retried := make(chan error, 1)
	go func() { retried <- m.Close(context.Background()) }()
	select {
	case err := <-retried:
		t.Fatalf("retry returned before pending endpoint cleanup: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(ep.closeRelease)
	if err := <-acquired; err == nil {
		t.Fatal("acquire succeeded after manager close")
	}
	if err := <-retried; err != nil {
		t.Fatalf("retry close: %v", err)
	}
}
