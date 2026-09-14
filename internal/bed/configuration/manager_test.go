package configuration

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

func source(t *testing.T, values map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, value := range values {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestResolveConfiguration(t *testing.T) {
	first := source(t, map[string]string{"VALUE": "first", "FIRST": "one"})
	second := source(t, map[string]string{"VALUE": "second", "EMPTY": "", "MULTILINE": "a\nb\n"})
	m, err := NewManager(Config{Sources: map[string]string{"first": first, "second": second}})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		config bed.Configuration
		want   map[string]string
	}{
		{"unused", bed.Configuration{}, map[string]string{}},
		{"ordered imports", bed.Configuration{EnvFrom: []string{"first", "second"}}, map[string]string{"VALUE": "second", "FIRST": "one", "EMPTY": "", "MULTILINE": "a\nb"}},
		{"explicit overrides", bed.Configuration{EnvFrom: []string{"first"}, Env: map[string]string{"VALUE": ""}}, map[string]string{"VALUE": "", "FIRST": "one"}},
		{"renamed key", bed.Configuration{EnvValueFrom: map[string]bed.ConfigurationKeyRef{"RENAMED": {Source: "first", Key: "VALUE"}}}, map[string]string{"RENAMED": "first"}},
		{"file", bed.Configuration{EnvFiles: map[string]string{"VALUE": filepath.Join(second, "MULTILINE")}}, map[string]string{"VALUE": "a\nb"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Resolve(tc.config)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("unexpected keys: %v", got)
			}
			for k, v := range tc.want {
				if actual, ok := got[k]; !ok || actual != v {
					t.Fatalf("key %s mismatch", k)
				}
			}
		})
	}
}

func TestUnavailableUnusedSourcesAndHostStatus(t *testing.T) {
	dir := source(t, map[string]string{"VALUE": "private-value"})
	m, err := NewManager(Config{Sources: map[string]string{"available": dir, "missing": filepath.Join(t.TempDir(), "missing")}})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	status := m.Status()
	if !status.Sources[0].DirectoryReadable || status.Sources[1].DirectoryReadable || status.Sources[1].ObservedAt.IsZero() {
		t.Fatalf("host evidence = %+v", status)
	}
	if _, err := m.Resolve(bed.Configuration{}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(bed.Configuration{EnvFrom: []string{"missing"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("missing source: %v", err)
	}
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "private-value") || strings.Contains(string(raw), dir) {
		t.Fatal("source contents/path leaked into status")
	}
}

func TestRejectInvalidConfiguration(t *testing.T) {
	dir := source(t, map[string]string{"VALUE": "do-not-report-this", "NUL": "\x00", "LARGE": strings.Repeat("v", maxValueBytes+1)})
	m, _ := NewManager(Config{Sources: map[string]string{"source": dir}})
	for _, tc := range []bed.Configuration{
		{Env: map[string]string{"BED_ID": "x"}},
		{Env: map[string]string{"VALUE": "x"}, EnvFiles: map[string]string{"VALUE": filepath.Join(dir, "VALUE")}},
		{EnvValueFrom: map[string]bed.ConfigurationKeyRef{"VALUE": {Source: "source", Key: "../VALUE"}}},
		{EnvFrom: []string{"unknown"}},
		{EnvValueFrom: map[string]bed.ConfigurationKeyRef{"VALUE": {Source: "source", Key: "absent"}}},
		{EnvValueFrom: map[string]bed.ConfigurationKeyRef{"VALUE": {Source: "source", Key: "NUL"}}},
		{EnvFiles: map[string]string{"VALUE": filepath.Join(dir, "LARGE")}},
	} {
		_, err := m.Resolve(tc)
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("unexpected error: %v", err)
		}
		if strings.Contains(err.Error(), "do-not-report-this") {
			t.Fatal("value leaked")
		}
	}
}

func TestAtomicProjectionAndSourceConfinement(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "..generation"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "..generation", "TOKEN"), []byte("projected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..generation", filepath.Join(dir, "..data")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..data/TOKEN", filepath.Join(dir, "TOKEN")); err != nil {
		t.Fatal(err)
	}
	m, _ := NewManager(Config{Sources: map[string]string{"source": dir}})
	got, err := m.Resolve(bed.Configuration{EnvFrom: []string{"source"}})
	if err != nil || got["TOKEN"] != "projected" {
		t.Fatalf("atomic projection: %v", err)
	}
	outside := source(t, map[string]string{"TOKEN": "private"})
	if err := os.Symlink(filepath.Join(outside, "TOKEN"), filepath.Join(dir, "ESCAPE")); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve(bed.Configuration{EnvFrom: []string{"source"}}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source escape accepted: %v", err)
	}
}

func TestPreparedConfigurationLifetime(t *testing.T) {
	dir := source(t, map[string]string{"TOKEN": "initial"})
	m, _ := NewManager(Config{Sources: map[string]string{"source": dir}})
	b := bed.New("one", "", bed.Spec{EnvFrom: []string{"source"}})
	other := bed.New("two", "", bed.Spec{})
	if err := m.Prepare(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if err := m.Prepare(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "TOKEN"), []byte("updated"), 0600); err != nil {
		t.Fatal(err)
	}
	first, _ := m.Main(b)
	if first["TOKEN"] != "initial" {
		t.Fatal("main changed without reinitialization")
	}
	first["TOKEN"] = "caller-mutated"
	again, _ := m.Main(b)
	if again["TOKEN"] != "initial" {
		t.Fatal("main config leaked mutable state")
	}
	unused, _ := m.Main(other)
	if len(unused) != 0 {
		t.Fatal("configuration crossed Bed boundary")
	}
	service, err := m.Resolve(bed.Configuration{EnvFrom: []string{"source"}})
	if err != nil || service["TOKEN"] != "updated" {
		t.Fatal("new service launch did not refresh")
	}
	if err := m.Release(t.Context(), b); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Main(b); err == nil {
		t.Fatal("released configuration remained usable")
	}
	raw, _ := json.Marshal(b.Spec())
	if strings.Contains(string(raw), "initial") || strings.Contains(string(raw), "updated") {
		t.Fatal("resolved values entered Spec")
	}
}
