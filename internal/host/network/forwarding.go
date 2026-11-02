package network

import (
	"fmt"
	"os"
	"strings"

	hostel "github.com/qiankunli/hostel/internal"
)

// checkForwarding only observes carrier state; Hostel never mutates its sysctls.
func checkForwarding(path string) error {
	value, err := os.ReadFile(path)
	if err != nil {
		return hostel.WrapError(hostel.ErrUnavailable, "carrier IPv4 forwarding", hostel.CauseCarrier, err)
	}
	if strings.TrimSpace(string(value)) != "1" {
		return hostel.WrapError(hostel.ErrUnavailable, "carrier IPv4 forwarding", hostel.CauseCarrier,
			fmt.Errorf("IPv4 forwarding is unavailable; Hostel does not change carrier-wide sysctls"))
	}
	return nil
}
