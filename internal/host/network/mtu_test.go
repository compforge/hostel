package network

import (
	"errors"
	"net"
	"reflect"
	"testing"
)

// +case=egress-mtu-selection
// +spec=`Only default-route egress interfaces contribute to the startup MTU; multiple usable exits use the smallest value.`
func TestMTUFromRoutes(t *testing.T) {
	interfaces := map[string]net.Interface{
		"eth0":    {MTU: 1422, Flags: net.FlagUp},
		"eth1":    {MTU: 1500, Flags: net.FlagUp},
		"eth2":    {MTU: 1300, Flags: net.FlagUp},
		"lo":      {MTU: 65536, Flags: net.FlagUp | net.FlagLoopback},
		"down":    {MTU: 1200},
		"invalid": {MTU: 0, Flags: net.FlagUp},
	}
	lookup := func(name string) (*net.Interface, error) {
		iface, ok := interfaces[name]
		if !ok {
			return nil, errors.New("interface disappeared")
		}
		return &iface, nil
	}
	for _, tc := range []struct {
		name, routes string
		value        int
		names        []string
	}{
		{"carrier MTU", `[{"dst":"default","dev":"eth0"}]`, 1422, []string{"eth0"}},
		{"normal ethernet", `[{"dst":"default","dev":"eth1"}]`, 1500, []string{"eth1"}},
		{"policy tables and duplicate interfaces", `[{"dst":"default","dev":"eth1"},{"dst":"default","dev":"eth0","table":100},{"dst":"default","dev":"eth0"}]`, 1422, []string{"eth0", "eth1"}},
		{"multipath", `[{"dst":"default","nexthops":[{"dev":"eth0"},{"dev":"eth2"}]}]`, 1300, []string{"eth0", "eth2"}},
		{"ignore non-default routes", `[{"dst":"0.0.0.0/0","dev":"eth1"},{"dst":"10.0.0.0/8","dev":"eth2"}]`, 1500, []string{"eth1"}},
		{"ignore unusable routes", `[{"dst":"default","dev":"lo"},{"dst":"default","dev":"down"},{"dst":"default","dev":"eth2","flags":["linkdown"]},{"dst":"default","type":"blackhole"},{"dst":"default","nexthops":[{"dev":"missing","flags":["dead"]},{"dev":"eth1"}]}]`, 1500, []string{"eth1"}},
		{"no default", `[]`, 0, nil},
		{"no usable interface", `[{"dst":"default","dev":"down"}]`, 0, nil},
		{"partial lookup failure", `[{"dst":"default","dev":"eth0"},{"dst":"default","dev":"missing"}]`, 0, nil},
		{"invalid interface MTU", `[{"dst":"default","dev":"invalid"}]`, 0, nil},
		{"unresolved nexthop", `[{"dst":"default","nhid":4}]`, 0, nil},
		{"malformed route output", `[`, 0, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := mtuFromRoutes([]byte(tc.routes), lookup)
			if tc.value == 0 {
				if err == nil {
					t.Fatalf("accepted incomplete discovery: %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, iface := range got.Interfaces {
				names = append(names, iface.Name)
			}
			if got.Value != tc.value || got.Source != "detected" || !reflect.DeepEqual(names, tc.names) {
				t.Fatalf("MTU = %+v, want value=%d interfaces=%v", got, tc.value, tc.names)
			}
		})
	}
}

func TestMTUDiscoveryFailureUsesFallback(t *testing.T) {
	// Linux cannot find ip; other platforms report unsupported discovery.
	t.Setenv("PATH", t.TempDir())
	for _, fallback := range []int{1422, 1300} {
		got := DetectMTU(t.Context(), fallback)
		if got.Value != fallback || got.Source != "fallback" || got.Reason == "" || len(got.Interfaces) != 0 {
			t.Fatalf("fallback lost value or attribution: %+v", got)
		}
	}
}
