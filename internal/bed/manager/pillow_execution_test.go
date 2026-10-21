package manager

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	model "github.com/qiankunli/hostel/internal/bed"
	"github.com/qiankunli/hostel/internal/bed/pillow"
)

// +case:id=pillow_execution_ownership,desc=`Run commands and a session in two Pillows, then restart the main process`,expect=`default order, env, cwd and file views agree; the session and volume survive main restart`
// +link:doc=docs/kernel.md
func TestPillowExecutionOwnership(t *testing.T) {
	m := newTestManager(t)
	defer m.Close(context.Background())
	options := CreateOptions{Volumes: []model.VolumeSpec{{Name: "shared", EmptyDir: &model.EmptyDirVolume{}}}, Pillows: []model.PillowSpec{
		{Name: "z-default", Workdir: "/work/default", Env: map[string]string{"VALUE": "default"}, Mounts: []model.VolumeMount{{Volume: "shared", Path: "/reader"}}},
		{Name: "a-worker", Workdir: "/writer", Env: map[string]string{"VALUE": "worker"}, Mounts: []model.VolumeMount{{Volume: "shared", Path: "/writer"}}, Command: []string{"/bin/sleep"}, Args: []string{"300"}, Required: true, StopSeconds: 1},
	}}
	if _, err := m.InitializeBedWithOptions(t.Context(), "pillow-ownership", options); err != nil {
		t.Fatal(err)
	}
	b, err := m.Ensure(t.Context(), "pillow-ownership")
	if err != nil {
		t.Fatal(err)
	}
	run := func(name, want string) {
		t.Helper()
		var out strings.Builder
		e, err := m.StartPillowExecution(t.Context(), b, name, ExecutionForeground, `printf '%s' "$VALUE"; printf '%s' "$VALUE" > value`, "", "", nil, time.Second, nil, func(o ExecutionOutput) { out.WriteString(o.Text) })
		if err != nil {
			t.Fatal(err)
		}
		result := e.Wait()
		if result.Process == nil || result.Process.ExitCode != 0 || out.String() != want || result.Pillow != name {
			t.Fatalf("run %s: %+v %q", name, result, out.String())
		}
	}
	run("z-default", "default")
	run("a-worker", "worker")
	if got, err := b.filesystem.Read("value"); err != nil || string(got) != "default" {
		t.Fatalf("default relative file: %q %v", got, err)
	}
	if got, err := b.filesystem.Read("/reader/value"); err != nil || string(got) != "worker" {
		t.Fatalf("shared data: %q %v", got, err)
	}
	worker, err := m.PillowFiles(b, "a-worker")
	if err != nil {
		t.Fatal(err)
	}
	if worker.Workdir() == filepath.Join(b.Spec().Dir, "data", "writer") {
		t.Fatal("mounted cwd used hidden local data")
	}
	id, err := m.CreatePillowShell(b, "a-worker", "")
	if err != nil {
		t.Fatal(err)
	}
	shell, ok := b.GetShell(id)
	if !ok || shell.Pillow != "a-worker" {
		t.Fatal("session lost Pillow binding")
	}
	old := waitPillow(t, m, b, "a-worker", func(s pillow.Status) bool { return s.Ready })
	if err := m.pillows.Restart(b.Bed, "a-worker"); err != nil {
		t.Fatal(err)
	}
	waitPillow(t, m, b, "a-worker", func(s pillow.Status) bool { return s.Ready && s.ExecutionID != old.ExecutionID })
	var out strings.Builder
	e, err := m.StartSessionExecution(t.Context(), b, shell, `printf '%s' "$VALUE"`, "", time.Second, nil, func(o ExecutionOutput) { out.WriteString(o.Text) })
	if err != nil {
		t.Fatal(err)
	}
	result := e.Wait()
	if result.Process == nil || result.Process.ExitCode != 0 || out.String() != "worker" || result.Pillow != "a-worker" {
		t.Fatalf("session after restart: %+v %q", result, out.String())
	}
	if got, err := worker.Read("value"); err != nil || string(got) != "worker" {
		t.Fatalf("restart lost volume %q %v", got, err)
	}
	options.Pillows[0], options.Pillows[1] = options.Pillows[1], options.Pillows[0]
	if _, err := m.InitializeBedWithOptions(t.Context(), b.Name, options); err == nil {
		t.Fatal("reordering silently changed default")
	}
	if _, err := m.PillowFiles(b, "missing"); err == nil {
		t.Fatal("unknown Pillow accepted")
	}
}
