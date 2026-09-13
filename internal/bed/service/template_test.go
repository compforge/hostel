package service

import (
	"testing"

	"github.com/qiankunli/hostel/internal/bed"
)

func TestCatalogFreezesAndPinsTemplates(t *testing.T) {
	template := Template{Name: "worker", Command: []string{"worker"}, AllowEnv: []string{"LABEL"}}
	c, err := NewCatalog([]Template{template})
	if err != nil {
		t.Fatal(err)
	}
	input := []bed.ServiceSpec{{Name: "b", Template: "worker", Env: map[string]string{"LABEL": "two"}}, {Name: "a", Template: "worker"}}
	resolved, err := c.Resolve(input)
	if err != nil {
		t.Fatal(err)
	}
	if resolved[0].Name != "a" || resolved[0].TemplateDigest == "" {
		t.Fatalf("not canonical/pinned: %+v", resolved)
	}
	input[0].Env["LABEL"] = "changed"
	if resolved[1].Env["LABEL"] != "two" {
		t.Fatal("caller mutated resolved state")
	}
	template.Command[0] = "different"
	updated, _ := NewCatalog([]Template{template})
	if _, err := updated.Resolve(resolved); err == nil {
		t.Fatal("changed deployment silently accepted")
	}
	if _, err := c.Resolve(resolved); err != nil {
		t.Fatal("caller mutated original catalog", err)
	}
}

func TestCatalogRejectsUndeclaredAndCredentialOverrides(t *testing.T) {
	c, err := NewCatalog([]Template{{Name: "worker", Command: []string{"worker"}, EnvFiles: map[string]string{"SECRET": "/deployment/secret"}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, specs := range [][]bed.ServiceSpec{
		{{Name: "x", Template: "missing"}},
		{{Name: "x", Template: "worker"}, {Name: "x", Template: "worker"}},
		{{Name: "x", Template: "worker", Env: map[string]string{"SECRET": "override"}}},
	} {
		if _, err := c.Resolve(specs); err == nil {
			t.Fatalf("accepted %+v", specs)
		}
	}
	if _, err := NewCatalog([]Template{{Name: "web", Command: []string{"web"}, HTTP: &HTTPTemplate{ReadyPath: "/ready"}}}); err == nil {
		t.Fatal("HTTP service lacks per-execution authentication contract")
	}
	if out, err := (*Catalog)(nil).Resolve(nil); err != nil || len(out) != 0 {
		t.Fatal("empty declarations require no templates")
	}
}
