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
		{Name: "worker", Command: []string{"worker"}, HTTP: &bed.ServiceHTTPSpec{ReadyPath: "ready", TokenEnv: "TOKEN"}},
		{Name: "worker", Command: []string{"worker"}, Env: map[string]string{"TOKEN": "caller"}, HTTP: &bed.ServiceHTTPSpec{ReadyPath: "/ready", TokenEnv: "TOKEN"}},
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
