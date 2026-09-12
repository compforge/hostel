//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"github.com/qiankunli/hostel/internal/config"
	"io"
	"os"
)

// Only the test build accepts internal startup configuration. Helpers still
// enter __confine/__supervisor before this hook, so they never start another daemon.
func startupOptions() (config.Options, error) {
	var options config.Options
	path := os.Getenv("HOSTEL_E2E_CONFIG")
	if path == "" {
		return options, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return options, err
	}
	defer f.Close()
	d := json.NewDecoder(f)
	d.DisallowUnknownFields()
	if err := d.Decode(&options); err != nil {
		return options, err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return options, fmt.Errorf("expected one startup config object")
	}
	return options, nil
}
