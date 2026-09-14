package network

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func inspectProcListener(ctx context.Context, procRoot string, group int, address string) (ListenerInspection, error) {
	result := ListenerInspection{State: ListenerUnavailable, Method: "proc", Reason: "process_unavailable"}
	target, err := netip.ParseAddrPort(address)
	if err != nil {
		return result, err
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return procInspectionFailure(result, err)
	}
	inodes := make(map[string]bool)
	fds := make(map[string]bool)
	ambiguousFamily := false
	members, tables := 0, 0
	var incomplete error
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		root := filepath.Join(procRoot, entry.Name())
		stat, err := os.ReadFile(filepath.Join(root, "stat"))
		if errors.Is(err, os.ErrNotExist) {
			// A process may exit during enumeration.
			continue
		}
		if err != nil {
			incomplete = err
			continue
		}
		end := strings.LastIndexByte(string(stat), ')')
		if end < 0 {
			incomplete = fmt.Errorf("invalid process stat")
			continue
		}
		fields := strings.Fields(string(stat)[end+1:])
		if len(fields) < 3 {
			incomplete = fmt.Errorf("invalid process stat")
			continue
		}
		if fields[2] != strconv.Itoa(group) {
			continue
		}
		members++
		for _, table := range []string{"tcp", "tcp6"} {
			data, err := os.ReadFile(filepath.Join(root, "net", table))
			if err != nil {
				incomplete = err
				continue
			}
			tables++
			for _, line := range strings.Split(string(data), "\n") {
				f := strings.Fields(line)
				if len(f) < 10 || f[3] != "0A" {
					continue
				}
				local, err := procSocketAddress(f[1])
				if err != nil {
					return result, err
				}
				if listenerMatches(local, target) {
					// /proc does not expose IPV6_V6ONLY. An IPv6 wildcard
					// alone cannot prove ownership or conflict for IPv4.
					if local.Addr().Is6() && local.Addr().IsUnspecified() && target.Addr().Unmap().Is4() {
						ambiguousFamily = true
						continue
					}
					inodes["socket:["+f[9]+"]"] = true
				}
			}
		}
		files, err := os.ReadDir(filepath.Join(root, "fd"))
		if err != nil {
			incomplete = err
			continue
		}
		for _, fd := range files {
			target, err := os.Readlink(filepath.Join(root, "fd", fd.Name()))
			if err != nil {
				incomplete = err
				continue
			}
			fds[target] = true
		}
	}
	for inode := range inodes {
		if fds[inode] {
			result.State, result.Reason = ListenerOwned, ""
			return result, nil
		}
	}
	// Negative evidence requires a complete inspection. setuid/userns may make
	// fd readlink fail even when readdir succeeds; that is not a foreign owner.
	if incomplete != nil {
		return procInspectionFailure(result, incomplete)
	}
	if members == 0 || tables == 0 {
		return result, nil
	}
	result.State, result.Reason = ListenerAbsent, ""
	if len(inodes) != 0 {
		result.State = ListenerForeign
	} else if ambiguousFamily {
		result.State, result.Reason = ListenerUnavailable, "address_family_unverified"
	}
	return result, nil
}

func procInspectionFailure(result ListenerInspection, err error) (ListenerInspection, error) {
	switch {
	case errors.Is(err, os.ErrPermission):
		result.Reason = "permission_denied"
	case errors.Is(err, os.ErrNotExist):
		result.Reason = "process_or_table_unavailable"
	default:
		return result, fmt.Errorf("inspect service listener: %w", err)
	}
	return result, nil
}

func procSocketAddress(raw string) (netip.AddrPort, error) {
	host, port, ok := strings.Cut(raw, ":")
	if !ok {
		return netip.AddrPort{}, fmt.Errorf("invalid proc socket address")
	}
	b, err := hex.DecodeString(host)
	if err != nil || (len(b) != 4 && len(b) != 16) {
		return netip.AddrPort{}, fmt.Errorf("invalid proc socket IP")
	}
	// Supported Linux targets (amd64/arm64) expose native little-endian words.
	for i := 0; i < len(b); i += 4 {
		b[i], b[i+3] = b[i+3], b[i]
		b[i+1], b[i+2] = b[i+2], b[i+1]
	}
	ip, _ := netip.AddrFromSlice(b)
	n, err := strconv.ParseUint(port, 16, 16)
	if err != nil {
		return netip.AddrPort{}, err
	}
	return netip.AddrPortFrom(ip, uint16(n)), nil
}
