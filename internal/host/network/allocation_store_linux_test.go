//go:build linux

package network

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestAllocationStoreRecoveryRetainsFailedCleanup(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAllocationStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if second, err := OpenAllocationStore(t.Context(), root); err == nil {
		second.Close()
		t.Fatal("live store lock was taken over")
	}
	record, err := store.Add(allocationRecord{Kind: "uid", Key: "bed-owner", Name: "hu_0123456789abcdef", UID: 4294967294})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	t.Setenv("PATH", tools)
	nft := filepath.Join(tools, "nft")
	if err := os.WriteFile(nft, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if recovered, err := OpenAllocationStore(t.Context(), root); err == nil {
		recovered.Close()
		t.Fatal("failed reconciliation opened admission")
	}
	path := filepath.Join(root, record.Name+".json")
	if _, err := os.Stat(path); err != nil {
		t.Fatal("failed cleanup lost ownership:", err)
	}
	// Retry through a fresh store: its preceding failed Open released the lock,
	// but kept the exact allocation for the next attempt.
	logPath := filepath.Join(tools, "calls")
	t.Setenv("NFT_CALLS", logPath)
	if err := os.WriteFile(nft, []byte("#!/bin/sh\nwhile IFS= read -r line; do printf '%s\\n' \"$line\" >> \"$NFT_CALLS\"; done\n"), 0700); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenAllocationStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("completed cleanup retained record: %v", err)
	}
	raw, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(raw), "delete table inet "+record.Name) || !strings.Contains(string(raw), "delete table ip "+record.Name) {
		t.Fatalf("both table families were not reconciled: %s %v", raw, err)
	}
	if _, err := store.Add(*record); err == nil {
		t.Fatal("closed owner accepted a new allocation")
	}
}

func TestAllocationStoreCarrierChangeAndMalformedRecord(t *testing.T) {
	root := t.TempDir()
	store, err := OpenAllocationStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Add(allocationRecord{Kind: "uid", Key: "bed-owner", Name: "hu_0123456789abcdef", UID: 4294967294})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	record.Boot = "previous-boot"
	raw, _ := json.Marshal(record)
	path := filepath.Join(root, record.Name+".json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	// A changed carrier must not require nft or mutate the current namespace.
	t.Setenv("PATH", t.TempDir())
	recovered, err := OpenAllocationStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	if err := recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stale carrier record remains: %v", err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"kind":"uid","owner":"bed","name":"../../foreign"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if recovered, err := OpenAllocationStore(context.Background(), root); err == nil {
		recovered.Close()
		t.Fatal("malformed ownership record was ignored")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("malformed record was discarded:", err)
	}
}

func TestAllocationStoreRetainsEnforcementForLiveUID(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("requires a disposable root runner with credential switching")
	}
	child := exec.Command("/bin/sleep", "30")
	child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 200123, Gid: 200123}}
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	root := t.TempDir()
	store, err := OpenAllocationStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.Add(allocationRecord{Kind: "uid", Key: "bed-live", Name: "hu_fedcba9876543210", UID: 200123})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	tools := t.TempDir()
	t.Setenv("PATH", tools)
	logPath := filepath.Join(tools, "calls")
	t.Setenv("NFT_CALLS", logPath)
	if err := os.WriteFile(filepath.Join(tools, "nft"), []byte("#!/bin/sh\nprintf called >> \"$NFT_CALLS\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if recovered, err := OpenAllocationStore(t.Context(), root); err == nil {
		recovered.Close()
		t.Fatal("live UID enforcement was removed")
	} else if !strings.Contains(err.Error(), "still has a live process") {
		t.Fatal(err)
	}
	if _, err := os.Stat(logPath); !os.IsNotExist(err) {
		t.Fatalf("firewall mutated while process alive: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, record.Name+".json")); err != nil {
		t.Fatal("live allocation lost its record:", err)
	}
	if err := child.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
	recovered, err := OpenAllocationStore(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := os.Stat(logPath); err != nil {
		t.Fatal("quiescent allocation not cleaned:", err)
	}
}
