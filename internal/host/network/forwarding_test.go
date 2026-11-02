package network

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	hostel "github.com/qiankunli/hostel/internal"
)

// +case:id=carrier_forwarding_attribution,desc=`Read disabled, enabled and unavailable carrier forwarding`,expect=`Disabled or unreadable is carrier; never modifies sysctl`
func TestCarrierForwardingAttribution(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ip_forward")
	for _, value := range []string{"0\n", "1\n"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		err := checkForwarding(path)
		if value == "0\n" && (!errors.Is(err, hostel.ErrUnavailable) || hostel.CauseOf(err) != hostel.CauseCarrier) {
			t.Fatalf("%v", err)
		}
		if value == "1\n" && err != nil {
			t.Fatal(err)
		}
		got, readErr := os.ReadFile(path)
		if readErr != nil || string(got) != value {
			t.Fatal("probe mutated carrier setting")
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	err := checkForwarding(path)
	if !errors.Is(err, os.ErrNotExist) || hostel.CauseOf(err) != hostel.CauseCarrier {
		t.Fatalf("%v", err)
	}
}
