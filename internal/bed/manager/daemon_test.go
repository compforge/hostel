package manager

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
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
	m := newTestManager(t)
	root := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(root, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	m.root = root
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

// +case:id=bed_root_outside_common_writes,desc=`A beds-root alias resolves into a writable public directory`,expect=`startup rejects the overlap before any subsystem starts`
func TestStartRejectsWritableCommonDirectoryAlias(t *testing.T) {
	for _, access := range bedfs.CommonDirectories() {
		if access.ReadOnly {
			continue
		}
		t.Run(access.Path, func(t *testing.T) {
			source, err := filepath.EvalSymlinks(access.Path)
			if os.IsNotExist(err) {
				t.Skip("common source is absent on this host")
			}
			if err != nil {
				t.Fatal(err)
			}
			alias := filepath.Join(t.TempDir(), "beds")
			if err := os.Symlink(source, alias); err != nil {
				t.Fatal(err)
			}
			m := newTestManager(t)
			m.root = alias
			err = m.Start(t.Context())
			if err == nil || !strings.Contains(err.Error(), "overlaps writable common directory") {
				t.Fatalf("public/private overlap accepted: %v", err)
			}
			if len(m.subsystems) != 0 {
				t.Fatal("started subsystems before rejecting common directory overlap")
			}
		})
	}
}
