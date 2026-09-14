//go:build !linux

package network

import (
	"context"
	"errors"
	"net/netip"
	"os/exec"
	"strconv"
	"strings"
)

func InspectTCPListener(ctx context.Context, group int, address string) (ListenerInspection, error) {
	result := ListenerInspection{State: ListenerUnavailable, Method: "lsof", Reason: "inspection_unavailable"}
	target, err := netip.ParseAddrPort(address)
	if err != nil {
		return result, err
	}
	out, err := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+strconv.Itoa(int(target.Port())), "-sTCP:LISTEN", "-Fpgn").Output()
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.Is(err, exec.ErrNotFound) || errors.As(err, &exit) && exit.ExitCode() == 1 {
			// lsof may omit inaccessible processes. Empty output proves neither
			// absence nor a foreign owner.
			return result, nil
		}
		return result, err
	}
	processGroup := ""
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "p") {
			processGroup = ""
		}
		if strings.HasPrefix(line, "g") {
			processGroup = strings.TrimPrefix(line, "g")
		}
		if !strings.HasPrefix(line, "n") || processGroup == "" {
			continue
		}
		local, err := netip.ParseAddrPort(strings.Replace(strings.TrimPrefix(line, "n"), "*:", "0.0.0.0:", 1))
		if err != nil || !listenerMatches(local, target) {
			continue
		}
		result.State, result.Reason = ListenerForeign, ""
		if processGroup == strconv.Itoa(group) {
			result.State = ListenerOwned
			return result, nil
		}
	}
	return result, nil
}
