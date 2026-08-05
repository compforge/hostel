// Copyright 2026 Li Qiankun
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package bed

import (
	"errors"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/qiankunli/hostel/internal/isolation"
	"github.com/qiankunli/hostel/internal/resource"
)

func TestBedProcessEnvHasExplicitOwnership(t *testing.T) {
	root := t.TempDir()
	m, err := NewManager(root, "default", "/bin/bash", isolation.New("dorm", root), nil, 0, nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	host := []string{
		"PATH=/carrier/bin",
		"LANG=C.UTF-8",
		"HOSTEL_STORE=s3",
		"AWS_SECRET_ACCESS_KEY=carrier-secret",
	}
	if err := m.SetBedEnvPassthrough(host, []string{"PATH", "LANG"}); err != nil {
		t.Fatalf("SetBedEnvPassthrough: %v", err)
	}
	b, err := m.Ensure("alice")
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	cmd, err := m.buildCommand(b, "true", "", map[string]string{
		"PATH":            "/request/bin",
		"REQUEST_API_KEY": "explicit-secret",
	})
	if err != nil {
		t.Fatalf("buildCommand: %v", err)
	}
	env := envMap(cmd.Env)
	for name, want := range map[string]string{
		"BED_ID":          "alice",
		"HOME":            b.Workspace,
		"LANG":            "C.UTF-8",
		"PATH":            "/request/bin",
		"REQUEST_API_KEY": "explicit-secret",
		"TMPDIR":          "/tmp",
	} {
		if got := env[name]; got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	for _, name := range []string{"HOSTEL_STORE", "HOSTEL_BED_ID", "AWS_SECRET_ACCESS_KEY"} {
		if _, ok := env[name]; ok {
			t.Errorf("daemon variable %s leaked into bed env", name)
		}
	}
	if !slices.IsSorted(cmd.Env) {
		t.Fatalf("environment is not deterministic: %v", cmd.Env)
	}
}

func TestSpawnerRejectsImplicitDaemonEnvironment(t *testing.T) {
	spawner := newInProcSpawner(resource.Noop("test"))
	if _, err := spawner.Start("alice", exec.Command("true")); err == nil || !strings.Contains(err.Error(), "environment must be explicit") {
		t.Fatalf("Start with nil env error = %v", err)
	}
}

func TestBedEnvNamespacesAreReserved(t *testing.T) {
	for _, name := range []string{"HOSTEL_STORE", "BED_ID", "PLAYWRIGHT_MCP_CDP_ENDPOINT", "bad-name"} {
		err := ValidateRequestEnv(map[string]string{name: "x"})
		if !errors.Is(err, ErrInvalidEnvironment) {
			t.Errorf("request variable %q error = %v", name, err)
		}
	}
	if err := ValidateRequestEnv(map[string]string{"OK": "contains\x00nul"}); !errors.Is(err, ErrInvalidEnvironment) {
		t.Fatalf("NUL value error = %v", err)
	}

	for _, name := range []string{"HOSTEL_ADDR", "BED_ID", "bad-name"} {
		if _, err := newProcessEnv([]string{name + "=x"}, []string{name}); !errors.Is(err, ErrInvalidEnvironment) {
			t.Errorf("passthrough variable %q error = %v", name, err)
		}
	}
}

func envMap(environ []string) map[string]string {
	env := make(map[string]string, len(environ))
	for _, entry := range environ {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			env[name] = value
		}
	}
	return env
}
