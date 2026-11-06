package pillow

import (
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

func TestNormalizePinsCompletePillowSpec(t *testing.T) {
	input := []bed.PillowSpec{
		{Name: "b", Command: []string{"worker", "--label=b"}, Env: map[string]string{"LABEL": "two"}},
		{Name: "a", Command: []string{"worker"}},
	}
	resolved, err := Normalize(input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Name != "b" || resolved[0].SpecDigest == "" || resolved[0].Workdir != "/workspace" || resolved[0].RestartPolicy != "never" {
		t.Fatalf("unexpected normalized spec: %+v", resolved[0])
	}
	input[0].Command[0] = "changed"
	input[0].Env["LABEL"] = "changed"
	if resolved[0].Command[0] != "worker" || resolved[0].Env["LABEL"] != "two" {
		t.Fatal("normalized specs share caller-owned storage")
	}
	recovered, err := Normalize(resolved)
	if err != nil || recovered[0].SpecDigest != resolved[0].SpecDigest {
		t.Fatalf("stable recovery failed: %+v %v", recovered, err)
	}
	changed := bed.ClonePillows(resolved)
	changed[0].Command = []string{"different"}
	if _, err := Normalize(changed); err == nil || !strings.Contains(err.Error(), "definition changed") {
		t.Fatalf("changed pinned definition accepted: %v", err)
	}
}

func TestNormalizeRejectsInvalidPillowSpecs(t *testing.T) {
	for _, specs := range [][]bed.PillowSpec{nil, {}} {
		if _, err := Normalize(specs); err == nil {
			t.Fatal("accepted missing Pillows")
		}
	}
	tests := []bed.PillowSpec{
		{Name: "", Command: []string{"worker"}},
		{Name: "worker", Args: []string{"orphan"}},
		{Name: "worker", Command: []string{"worker"}, Env: map[string]string{"HOSTEL_ADDR": "bad"}},
		{Name: "worker", Command: []string{"worker"}, EnvFiles: map[string]string{"SECRET": "relative"}},
		{Name: "worker", Command: []string{"worker"}, ReadinessProbe: &bed.Probe{HTTPGet: &bed.HTTPGetAction{Path: "ready", Port: "http"}, PeriodSeconds: 1, FailureThreshold: 1}},
	}
	for _, spec := range tests {
		if _, err := Normalize([]bed.PillowSpec{spec}); err == nil {
			t.Fatalf("accepted invalid spec: %+v", spec)
		}
	}
	if _, err := Normalize([]bed.PillowSpec{{Name: "worker", Command: []string{"one"}}, {Name: "worker", Command: []string{"two"}}}); err == nil {
		t.Fatal("accepted duplicate pillow names")
	}
}

func TestResolvePortOwners(t *testing.T) {
	m, _ := httpTestManager(t)
	spec := bed.PillowSpec{Name: "web", Command: []string{"server"}, Ports: []bed.PillowPort{{Name: "http", Dynamic: true}}}
	other := spec
	other.Name = "other"
	if _, err := m.Resolve([]bed.PillowSpec{spec, other}); err == nil {
		t.Fatal("duplicate Bed port name accepted")
	}
	if _, err := m.Resolve([]bed.PillowSpec{spec}); err != nil {
		t.Fatal(err)
	}
}

func TestNonHTTPPort(t *testing.T) {
	m, _ := httpTestManager(t)
	b := bed.New("tcp", "", bed.Spec{Pillows: []bed.PillowSpec{{Name: "tcp", Command: []string{"worker"}, Ports: []bed.PillowPort{{Name: "tcp", Dynamic: true}}, Required: true}}})
	if err := m.PrepareBed(t.Context(), b, &fakeRuntime{}); err != nil {
		t.Fatal(err)
	}
	access, err := m.Access(b, "tcp")
	if err != nil || len(access.Ports) != 1 || access.Ports[0].HostPort != 0 {
		t.Fatalf("TCP access: %+v %v", access, err)
	}
}
