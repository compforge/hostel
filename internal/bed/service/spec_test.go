package service

import (
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

func TestNormalizePinsCompleteServiceSpec(t *testing.T) {
	input := []bed.ServiceSpec{
		{Name: "b", Command: []string{"worker", "--label=b"}, Env: map[string]string{"LABEL": "two"}},
		{Name: "a", Command: []string{"worker"}},
	}
	resolved, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Name != "a" || resolved[0].SpecDigest == "" || resolved[0].Directory != "/workspace" || resolved[0].Restart != "never" {
		t.Fatalf("unexpected normalized spec: %+v", resolved[0])
	}
	input[0].Command[0] = "changed"
	input[0].Env["LABEL"] = "changed"
	if resolved[1].Command[0] != "worker" || resolved[1].Env["LABEL"] != "two" {
		t.Fatal("normalized specs share caller-owned storage")
	}
	recovered, err := Normalize(resolved)
	if err != nil || recovered[0].SpecDigest != resolved[0].SpecDigest {
		t.Fatalf("stable recovery failed: %+v %v", recovered, err)
	}
	changed := bed.CloneServices(resolved)
	changed[0].Command = []string{"different"}
	if _, err := Normalize(changed); err == nil || !strings.Contains(err.Error(), "definition changed") {
		t.Fatalf("changed pinned definition accepted: %v", err)
	}
}

func TestNormalizeRejectsInvalidServiceSpecs(t *testing.T) {
	tests := []bed.ServiceSpec{
		{Name: "", Command: []string{"worker"}},
		{Name: "worker"},
		{Name: "worker", Command: []string{"worker"}, Env: map[string]string{"HOSTEL_ADDR": "bad"}},
		{Name: "worker", Command: []string{"worker"}, EnvFiles: map[string]string{"SECRET": "relative"}},
		{Name: "worker", Command: []string{"worker"}, HTTP: &bed.ServiceHTTPSpec{ReadyPath: "ready"}},
	}
	for _, spec := range tests {
		if _, err := Normalize([]bed.ServiceSpec{spec}); err == nil {
			t.Fatalf("accepted invalid spec: %+v", spec)
		}
	}
	if _, err := Normalize([]bed.ServiceSpec{{Name: "worker", Command: []string{"one"}}, {Name: "worker", Command: []string{"two"}}}); err == nil {
		t.Fatal("accepted duplicate service names")
	}
}

func TestNormalizeAuthentication(t *testing.T) {
	for _, tc := range []struct {
		name  string
		auth  *bed.Authentication
		env   map[string]string
		files map[string]string
		valid bool
	}{
		{name: "none", valid: true},
		{name: "generated", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceGenerated, TokenEnv: "TOKEN"}, valid: true},
		{name: "environment", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceEnvironment, TokenEnv: "TOKEN"}, env: map[string]string{"TOKEN": "test-token"}, valid: true},
		{name: "file", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceEnvironment, TokenEnv: "TOKEN"}, files: map[string]string{"TOKEN": "/run/secrets/token"}, valid: true},
		{name: "empty authentication", auth: &bed.Authentication{}},
		{name: "unknown scheme", auth: &bed.Authentication{Scheme: "basic", TokenSource: bed.TokenSourceGenerated, TokenEnv: "TOKEN"}},
		{name: "missing source", auth: &bed.Authentication{Scheme: "bearer", TokenEnv: "TOKEN"}},
		{name: "unknown source", auth: &bed.Authentication{Scheme: "bearer", TokenSource: "none", TokenEnv: "TOKEN"}},
		{name: "missing env name", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceGenerated}},
		{name: "reserved env", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceGenerated, TokenEnv: "HOSTEL_TOKEN"}},
		{name: "generated env conflict", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceGenerated, TokenEnv: "TOKEN"}, env: map[string]string{"TOKEN": ""}},
		{name: "generated file conflict", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceGenerated, TokenEnv: "TOKEN"}, files: map[string]string{"TOKEN": "/run/secrets/token"}},
		{name: "missing environment", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceEnvironment, TokenEnv: "TOKEN"}},
		{name: "empty environment", auth: &bed.Authentication{Scheme: "bearer", TokenSource: bed.TokenSourceEnvironment, TokenEnv: "TOKEN"}, env: map[string]string{"TOKEN": ""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := []bed.ServiceSpec{{Name: "web", PortMapping: "http", Command: []string{"server"}, Env: tc.env, EnvFiles: tc.files, HTTP: &bed.ServiceHTTPSpec{ReadyPath: "/ready", Authentication: tc.auth}}}
			resolved, err := Normalize(input)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%t, err=%v", tc.valid, err)
			}
			if err != nil || tc.auth == nil {
				return
			}
			cloned := bed.CloneServices(resolved)
			cloned[0].HTTP.Authentication.TokenEnv = "OTHER_TOKEN"
			if resolved[0].HTTP.Authentication.TokenEnv != "TOKEN" || input[0].HTTP.Authentication.TokenEnv != "TOKEN" {
				t.Fatal("authentication aliases caller-owned storage")
			}
			if _, err := Normalize(cloned); err == nil {
				t.Fatal("changed pinned authentication accepted")
			}
		})
	}
}

func TestResolvePortMappingConsumers(t *testing.T) {
	m, _ := httpTestManager(t)
	mappings := []bed.PortMappingSpec{{Name: "http", Protocol: "tcp", Publish: true}}
	spec := bed.ServiceSpec{Name: "web", PortMapping: "http", Command: []string{"server"}, HTTP: &bed.ServiceHTTPSpec{ReadyPath: "/"}}
	if _, err := m.Resolve([]bed.ServiceSpec{spec}, nil); err == nil {
		t.Fatal("undeclared mapping accepted")
	}
	other := spec
	other.Name = "other"
	if _, err := m.Resolve([]bed.ServiceSpec{spec, other}, mappings); err == nil {
		t.Fatal("shared consumer ownership accepted")
	}
	if _, err := m.Resolve([]bed.ServiceSpec{spec}, mappings); err != nil {
		t.Fatal(err)
	}
	spec.PortMapping = ""
	if _, err := Normalize([]bed.ServiceSpec{spec}); err == nil {
		t.Fatal("HTTP without mapping accepted")
	}
}
func TestNonHTTPPortMapping(t *testing.T) {
	m, _ := httpTestManager(t)
	b := bed.New("tcp", "", bed.Spec{PortMappings: []bed.PortMappingSpec{{Name: "tcp", Protocol: "tcp"}}, Services: []bed.ServiceSpec{{Name: "tcp", PortMapping: "tcp", Command: []string{"worker"}, Env: map[string]string{"LISTEN": "${LISTEN_ADDR}"}, Required: true}}})
	runtime := &fakeRuntime{}
	if err := m.PrepareBed(t.Context(), b, runtime); err != nil {
		t.Fatal(err)
	}
	if !m.Status(b)[0].Ready {
		t.Fatal("TCP process not ready")
	}
}
