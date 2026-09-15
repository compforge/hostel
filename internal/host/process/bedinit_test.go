package process

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBedInitKeepsEnvironmentInertBeforeFinalStage(t *testing.T) {
	cmd := exec.Command("/bin/true")
	cmd.Env = []string{"LD_PRELOAD=/untrusted/plugin.so", "GODEBUG=inittrace=1", "PATH=/untrusted", "TOKEN=private\nvalue=1"}
	want := append([]string(nil), cmd.Env...)
	WrapBedInit(cmd, Executable(), "/")
	for _, env := range cmd.Env {
		if env != "PATH=/usr/bin:/bin" && !strings.HasPrefix(env, workloadEnvPrefix) {
			t.Fatalf("workload variable active before entry: %q", env)
		}
	}
	for i, env := range want {
		_, payload, _ := strings.Cut(cmd.Env[i+1], "=")
		if payload != env {
			t.Fatalf("payload changed: %q", payload)
		}
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "private") {
			t.Fatal("credential leaked into argv")
		}
	}
}

func TestBedInitResolvesExecutableWithOwnCwdAndPath(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "workload-tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\nprintf '%s' \"$MARKER\"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{tool, "./workload-tool", "workload-tool"} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(name)
			cmd.Env = []string{"PATH=" + dir, "MARKER=literal $() `value`\nsecond=line"}
			WrapBedInit(cmd, Executable(), dir)
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != "literal $() `value`\nsecond=line" {
				t.Fatalf("workload result: %q %v", out, err)
			}
		})
	}
}
