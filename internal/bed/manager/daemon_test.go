package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/resource"
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
	b, err := declareTestBed(m, t.Context(), "retained")
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(b.Workdir(), "keep")
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
	if _, err := declareTestBed(m, t.Context(), "new"); !errors.Is(err, ErrBedUnavailable) {
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
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	m := newTestManagerAt(t, root)
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("invalid root accepted")
	}
	if _, err := declareTestBed(m, t.Context(), "a"); err == nil {
		t.Fatal("admitted after failed startup")
	}
	if len(m.subsystems) != 0 {
		t.Fatal("failed startup retained initialized subsystems")
	}
}

type partiallyStartedNetwork struct {
	network.Provider
	closes     int
	cleanupErr error
}

func (*partiallyStartedNetwork) Start(context.Context) error   { return errors.New("partial allocation") }
func (p *partiallyStartedNetwork) Close(context.Context) error { p.closes++; return p.cleanupErr }
func TestFailedSubsystemStartRetainsCleanupOwner(t *testing.T) {
	m := newTestManager(t)
	p := &partiallyStartedNetwork{cleanupErr: errors.New("resource busy")}
	m.network = network.WithProvider(p, m.owners.Network)
	if err := m.Start(t.Context()); err == nil {
		t.Fatal("partial startup accepted")
	}
	if p.closes != 1 || len(m.subsystems) == 0 {
		t.Fatal("failed subsystem lost cleanup owner")
	}
	p.cleanupErr = nil
	if err := m.closeSubsystems(t.Context()); err != nil {
		t.Fatal(err)
	}
	if p.closes != 2 || len(m.subsystems) != 0 {
		t.Fatal("failed subsystem cleanup not retried")
	}
}

// +case:id=bed_root_outside_common_writes,desc=`A beds-root reaches shared software through a symlinked parent`,expect=`Filesystem Start rejects the overlap before later subsystems start`
func TestStartRejectsSharedSoftwareAlias(t *testing.T) {
	const sharedSoftwareRoot = "/usr/local"
	if _, err := os.Stat(sharedSoftwareRoot); errors.Is(err, os.ErrNotExist) {
		t.Skip("shared software directory is absent")
	} else if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(t.TempDir(), "software-parent")
	if err := os.Symlink(filepath.Dir(sharedSoftwareRoot), parent); err != nil {
		t.Fatal(err)
	}
	m := newTestManagerAt(t, filepath.Join(parent, filepath.Base(sharedSoftwareRoot)))
	later := &partiallyStartedNetwork{}
	m.network = network.WithProvider(later, m.owners.Network)
	if err := m.Start(t.Context()); err == nil || !strings.HasPrefix(err.Error(), "start filesystem:") || !strings.Contains(err.Error(), "overlaps shared software") {
		t.Fatalf("expected shared software overlap, got %v", err)
	}
	if later.closes != 0 || len(m.subsystems) != 0 || m.started {
		t.Fatal("filesystem failure did not stop subsystem startup cleanly")
	}
}
