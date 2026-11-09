//go:build linux

package network

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

// OpenAllocationStore reconciles even allocations with no surviving Bed tree.
// A nonblocking process-lifetime lock prevents takeover of a live instance.
func OpenAllocationStore(ctx context.Context, directory string) (*AllocationStore, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(directory, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(directory, "owner.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lock.Close()
		return nil, fmt.Errorf("network state directory already owned: %w", err)
	}
	s := &AllocationStore{directory: directory, lock: lock}
	fail := func(err error) (*AllocationStore, error) { _ = s.Close(); return nil, err }
	s.carrier, err = os.Readlink("/proc/self/ns/net")
	if err != nil {
		return fail(err)
	}
	boot, err := os.ReadFile("/proc/sys/kernel/random/boot_id")
	if err != nil {
		return fail(err)
	}
	s.boot = strings.TrimSpace(string(boot))
	entries, err := os.ReadDir(directory)
	if err != nil {
		return fail(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		if err != nil {
			return fail(err)
		}
		var record allocationRecord
		if err := json.Unmarshal(raw, &record); err != nil {
			return fail(err)
		}
		if err := record.validate(); err != nil {
			return fail(err)
		}
		if entry.Name() != record.Name+".json" {
			return fail(fmt.Errorf("allocation filename does not match identity: %s", entry.Name()))
		}
		if record.Carrier == s.carrier && record.Boot == s.boot {
			cleanupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			err = cleanupAllocation(cleanupCtx, record)
			cancel()
			if err != nil {
				return fail(fmt.Errorf("recover network owner=%s allocation=%s: %w", record.Key, record.Name, err))
			}
			log.Printf("network: recovered allocation owner=%s kind=%s allocation=%s uid=%d", record.Key, record.Kind, record.Name, record.UID)
		} else {
			log.Printf("network: discarded allocation record owner=%s allocation=%s reason=carrier_changed", record.Key, record.Name)
		}
		if err := s.Remove(&record); err != nil {
			return fail(err)
		}
	}
	return s, nil
}

func deleteTables(name string) string {
	return fmt.Sprintf("add table inet %s\ndelete table inet %s\nadd table ip %s\ndelete table ip %s\n", name, name, name, name)
}

func cleanupAllocation(ctx context.Context, r allocationRecord) error {
	// Do not remove enforcement under a still-running workload. PID reuse is not
	// an ownership signal; inspect the currently realized UID/netns instead.
	if r.Kind == "uid" {
		paths, err := filepath.Glob("/proc/[0-9]*/status")
		if err != nil {
			return err
		}
		for _, path := range paths {
			raw, err := os.ReadFile(path)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			zombie := false
			owned := false
			for _, line := range strings.Split(string(raw), "\n") {
				if strings.HasPrefix(line, "State:") {
					f := strings.Fields(line)
					zombie = len(f) > 1 && f[1] == "Z"
				}
				if strings.HasPrefix(line, "Uid:") {
					for _, u := range strings.Fields(line)[1:] {
						if u == strconv.FormatUint(uint64(r.UID), 10) {
							owned = true
						}
					}
				}
			}
			if owned && !zombie {
				return fmt.Errorf("UID %d still has a live process (%s); enforcement retained", r.UID, path)
			}
		}
	} else {
		// /run/netns pins named namespaces even when their last process exits.
		if _, err := os.Stat(filepath.Join("/run/netns", r.Name)); err == nil {
			out, err := run(ctx, "", "ip", "netns", "pids", r.Name)
			if err != nil {
				return err
			}
			if strings.TrimSpace(string(out)) != "" {
				return fmt.Errorf("namespace %s still has processes: %s", r.Name, out)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
	}
	if _, err := run(ctx, deleteTables(r.Name), "nft", "-f", "-"); err != nil {
		return err
	}
	if r.Kind == "netns" {
		// Only exact names from our locked write-ahead records are touched.
		for _, link := range []string{r.Link, r.Peer} {
			// The peer can remain in the carrier if creation stopped before its move.
			out, err := run(ctx, "", "ip", "-j", "link", "show")
			if err != nil {
				return err
			}
			var links []struct {
				Ifname string `json:"ifname"`
			}
			if err := json.Unmarshal(out, &links); err != nil {
				return err
			}
			for _, candidate := range links {
				if candidate.Ifname == link {
					if _, err := run(ctx, "", "ip", "link", "del", link); err != nil {
						return err
					}
					break
				}
			}
		}
		if _, err := os.Stat(filepath.Join("/run/netns", r.Name)); err == nil {
			if _, err := run(ctx, "", "ip", "netns", "del", r.Name); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		if err := os.RemoveAll(filepath.Join("/etc/netns", r.Name)); err != nil {
			return err
		}
	}
	return nil
}

// cleanupRecorded uses the same idempotent implementation after partial
// preparation and during crash recovery, including lost kernel acknowledgments.
func (s *AllocationStore) cleanupRecorded(ctx context.Context, r *allocationRecord) error {
	if s == nil || r == nil {
		return errors.New("network allocation has no recovery record")
	}
	if err := cleanupAllocation(ctx, *r); err != nil {
		return err
	}
	return s.Remove(r)
}
