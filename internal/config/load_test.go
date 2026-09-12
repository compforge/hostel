package config

import "testing"

func mustLoad(t *testing.T, args []string) *Config {
	t.Helper()
	c, err := Load(args, Options{})
	if err != nil {
		t.Fatal(err)
	}
	return c
}
