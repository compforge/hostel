package network

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"slices"
	"time"

	"github.com/qiankunli/hostel/protocol"
)

// FallbackMTU preserves the value chosen for carriers with a 1422-byte egress.
// It is not the kernel veth default or a guarantee about the end-to-end path.
const FallbackMTU = 1422

// DetectMTU reads the carrier's default IPv4 egress interfaces without sending packets.
// +spec=`Use the smallest observed default-route interface MTU; if discovery fails, use the configured fallback and disclose the reason.`
// +link=component://docs/network.md
func DetectMTU(ctx context.Context, fallback int) protocol.NetworkMTUStatus {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	routes, err := defaultRoutes(ctx)
	var report protocol.NetworkMTUStatus
	if err == nil {
		report, err = mtuFromRoutes(routes, net.InterfaceByName)
	}
	if err != nil {
		report = protocol.NetworkMTUStatus{Value: fallback, Source: "fallback", Reason: err.Error()}
	}
	log.Printf("network: mtu=%d source=%s interfaces=%v reason=%q", report.Value, report.Source, report.Interfaces, report.Reason)
	return report
}

type mtuNextHop struct {
	Dev   string   `json:"dev"`
	Flags []string `json:"flags"`
}

type mtuRoute struct {
	mtuNextHop
	Dst      string       `json:"dst"`
	Type     string       `json:"type"`
	NextHops []mtuNextHop `json:"nexthops"`
}

func mtuFromRoutes(data []byte, lookup func(string) (*net.Interface, error)) (protocol.NetworkMTUStatus, error) {
	var routes []mtuRoute
	if err := json.Unmarshal(data, &routes); err != nil {
		return protocol.NetworkMTUStatus{}, fmt.Errorf("decode default IPv4 routes: %w", err)
	}
	var names []string
	for _, route := range routes {
		if (route.Dst != "default" && route.Dst != "0.0.0.0/0") ||
			(route.Type != "" && route.Type != "unicast") || inactiveMTURoute(route.Flags) {
			continue
		}
		hops := route.NextHops
		if len(hops) == 0 {
			hops = []mtuNextHop{route.mtuNextHop}
		}
		for _, hop := range hops {
			if inactiveMTURoute(hop.Flags) {
				continue
			}
			if hop.Dev == "" {
				return protocol.NetworkMTUStatus{}, fmt.Errorf("default IPv4 route has no resolved egress interface")
			}
			names = append(names, hop.Dev)
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	report := protocol.NetworkMTUStatus{Source: "detected"}
	for _, name := range names {
		iface, err := lookup(name)
		if err != nil {
			return protocol.NetworkMTUStatus{}, fmt.Errorf("read egress interface %s: %w", name, err)
		}
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		if iface.MTU < 68 || iface.MTU > 65535 {
			return protocol.NetworkMTUStatus{}, fmt.Errorf("egress interface %s has invalid MTU %d", name, iface.MTU)
		}
		report.Interfaces = append(report.Interfaces, protocol.NetworkMTUInterface{Name: name, MTU: iface.MTU})
		if report.Value == 0 || iface.MTU < report.Value {
			report.Value = iface.MTU
		}
	}
	if report.Value == 0 {
		return protocol.NetworkMTUStatus{}, fmt.Errorf("no usable default IPv4 egress interface")
	}
	return report, nil
}

func inactiveMTURoute(flags []string) bool {
	return slices.Contains(flags, "dead") || slices.Contains(flags, "linkdown")
}
