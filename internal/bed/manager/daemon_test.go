package manager

import (
	"context"
	"errors"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type joiningAdmission struct {
	resource.Admitter
	entered  chan struct{}
	canceled chan struct{}
	finish   chan struct{}
}

func (a *joiningAdmission) Run(ctx context.Context) error {
	close(a.entered)
	<-ctx.Done()
	close(a.canceled)
	<-a.finish
	return nil
}
func TestCloseJoinsBackgroundBeforeReleasingBeds(t *testing.T) {
	m := newTestManager(t)
	a := &joiningAdmission{Admitter: resource.NoopAdmission("test"), entered: make(chan struct{}), canceled: make(chan struct{}), finish: make(chan struct{})}
	m.SetResourceAdmission(a)
	b, err := m.Ensure(t.Context(), "retained")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(b.Workspace(), "keep")
	if err := os.WriteFile(marker, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	runDone := make(chan error, 1)
	go func() { runDone <- m.Run(context.Background()) }()
	select {
	case <-a.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("controller not started")
	}
	closeDone := make(chan error, 1)
	go func() { closeDone <- m.Close(context.Background()) }()
	<-a.canceled
	if !b.Bed.Status().Filesystem.Prepared {
		t.Fatal("resources released before background controller exited")
	}
	if _, err := m.Ensure(t.Context(), "new"); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("new work accepted during shutdown: %v", err)
	}
	if _, err := m.BeginOperation(b, OpFile, time.Second); !errors.Is(err, ErrBedUnavailable) {
		t.Fatalf("operation accepted during shutdown: %v", err)
	}
	close(a.finish)
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if err := <-runDone; err != nil {
		t.Fatal(err)
	}
	if b.Bed.Status().Filesystem.Prepared {
		t.Fatal("filesystem still prepared")
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "data" {
		t.Fatalf("daemon close removed local identity: %q %v", data, err)
	}
	if err := m.Close(t.Context()); err != nil {
		t.Fatalf("repeated close: %v", err)
	}
}
func TestStartFailureDoesNotAdmitBeds(t *testing.T) {
	m := newTestManager(t)
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	m.root = root
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("invalid root accepted")
	}
	if _, err := m.Ensure(t.Context(), "a"); err == nil {
		t.Fatal("admitted after failed startup")
	}
	if len(m.components) != 0 {
		t.Fatal("failed startup retained initialized components")
	}
}
