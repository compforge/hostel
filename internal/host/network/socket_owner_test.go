package network

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestProcListenerEvidence(t *testing.T) {
	for _, tc := range []struct {
		name, socket, link string
		missingFD          bool
		want               ListenerState
	}{
		{name: "owned", socket: "0100007F:61A8", link: "socket:[42]", want: ListenerOwned},
		{name: "wildcard", socket: "00000000:61A8", link: "socket:[42]", want: ListenerOwned},
		{name: "foreign", socket: "0100007F:61A8", link: "socket:[99]", want: ListenerForeign},
		{name: "absent", want: ListenerAbsent},
		{name: "different_address", socket: "0200007F:61A8", link: "socket:[42]", want: ListenerAbsent},
		{name: "ipv6_wildcard_for_ipv4", socket: "00000000000000000000000000000000:61A8", link: "socket:[99]", want: ListenerUnavailable},
		{name: "unreadable_fd", socket: "0100007F:61A8", missingFD: true, want: ListenerUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			process := filepath.Join(root, "100")
			if err := os.MkdirAll(filepath.Join(process, "net"), 0700); err != nil {
				t.Fatal(err)
			}
			write := func(name, value string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(process, name), []byte(value), 0600); err != nil {
					t.Fatal(err)
				}
			}
			write("stat", "100 (test worker) S 1 100")
			table := "header\n"
			if tc.socket != "" {
				table += "0: " + tc.socket + " 00000000:0000 0A 0 0 0 0 0 42\n"
			}
			write("net/tcp", table)
			write("net/tcp6", "header\n")
			if !tc.missingFD {
				if err := os.Mkdir(filepath.Join(process, "fd"), 0700); err != nil {
					t.Fatal(err)
				}
				if tc.link != "" {
					if err := os.Symlink(tc.link, filepath.Join(process, "fd", "3")); err != nil {
						t.Fatal(err)
					}
				}
			}
			got, err := inspectProcListener(t.Context(), root, 100, "127.0.0.1:25000")
			if err != nil || got.State != tc.want {
				t.Fatalf("inspection=%+v err=%v", got, err)
			}
		})
	}
}

func TestInspectionPermissionAndCancellation(t *testing.T) {
	got, err := procInspectionFailure(ListenerInspection{State: ListenerUnavailable, Method: "proc"}, &os.PathError{Op: "readlink", Path: "/proc/100/fd/3", Err: os.ErrPermission})
	if err != nil || got.State != ListenerUnavailable || got.Reason != "permission_denied" {
		t.Fatalf("inspection=%+v err=%v", got, err)
	}
	if _, err := procInspectionFailure(got, errors.New("broken IO")); err == nil {
		t.Fatal("unexpected failure was degraded")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "100"), 0700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := inspectProcListener(ctx, root, 100, "127.0.0.1:25000"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}

func TestProcSocketAddresses(t *testing.T) {
	for raw, want := range map[string]string{
		"0100007F:61A8":                         "127.0.0.1:25000",
		"00000000000000000000000001000000:61A8": "[::1]:25000",
		"0000000000000000FFFF00000100007F:61A8": "[::ffff:127.0.0.1]:25000",
	} {
		got, err := procSocketAddress(raw)
		if err != nil || got.String() != want {
			t.Fatalf("%s: %v %v", raw, got, err)
		}
	}
}

func TestInspectLiveListener(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	got, err := InspectTCPListener(ctx, syscall.Getpgrp(), ln.Addr().String())
	if err != nil || got.State != ListenerOwned {
		t.Fatalf("live socket ownership=%+v err=%v", got, err)
	}
}

func TestReserveSkipsExternalListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port
	m, err := NewPortManager(port, min(port+10, 65535))
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if _, err := m.Reserve("fixed", "", net.JoinHostPort("127.0.0.1", strconv.Itoa(port))); !errors.Is(err, ErrPortsExhausted) {
		t.Fatalf("fixed occupied=%v", err)
	}
	a, err := m.Reserve("service", "", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if a.Port() == port {
		t.Fatal("reserved externally occupied port")
	}
	private, err := m.Reserve("other-scope", "bed-netns", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer private.Release()
	if len(m.Status()) != 2 {
		t.Fatalf("failed reservations leaked: %+v", m.Status())
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if len(m.Status()) != 1 {
		t.Fatal("released wrong allocation")
	}
}
