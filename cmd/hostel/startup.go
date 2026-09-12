//go:build !e2e

package main

import "github.com/qiankunli/hostel/internal/config"

func startupOptions() (config.Options, error) { return config.Options{}, nil }
