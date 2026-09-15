package bedfs

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUnredirectedMappingReadCandidates(t *testing.T) {
	fs, source := mappedFixture(t, false)
	reader := NewReader(HostView(fs), "")
	const name = "/mnt/project/input.txt"
	if got, err := reader.Read(name); err != nil || string(got) != "input" {
		t.Fatalf("source candidate: %q %v", got, err)
	}
	// The empty Bed-local mount placeholder must not hide source contents.
	entries, err := reader.List("/mnt/project", 1)
	if err != nil || len(entries) != 1 || entries[0].Path != name {
		t.Fatalf("placeholder list=%+v %v", entries, err)
	}
	local := filepath.Join(fs.Rootfs(), "mnt/project/input.txt")
	if err := os.WriteFile(local, []byte("local\nsecond\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if got, err := reader.Read(name); err != nil || string(got) != "local\nsecond\n" {
		t.Fatalf("local candidate: %q %v", got, err)
	}
	if got, err := reader.ReadLines(name, 1, 1); err != nil || got != "second\n" {
		t.Fatalf("local lines: %q %v", got, err)
	}
	if info, err := reader.Stat(name); err != nil || info.Size != 13 || info.Path != name {
		t.Fatalf("local stat=%+v %v", info, err)
	}
	found, err := reader.Search("/mnt/project", "input.txt")
	if err != nil || len(found) != 1 || found[0].Path != name {
		t.Fatalf("local search=%+v %v", found, err)
	}
	parentEntries, err := reader.List("/", 4)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range parentEntries {
		if entry.Path == name && entry.Size != 13 {
			t.Fatalf("parent listing ignored local candidate: %+v", entry)
		}
	}
	parentMatches, err := reader.Search("/", "input.txt")
	if err != nil || len(parentMatches) != 1 || parentMatches[0].Size != 13 {
		t.Fatalf("parent search=%+v %v", parentMatches, err)
	}
	// A realized process mapping always names its source, even if older local
	// data exists beneath the target. Missing source files must stay missing.
	redirected := NewReader(RedirectedView(fs, MappingSupport{ReadWrite: true}), "")
	if got, err := redirected.Read(name); err != nil || string(got) != "input" {
		t.Fatalf("realized mapping: %q %v", got, err)
	}
	if err := os.Remove(filepath.Join(source, "input.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := redirected.Read(name); !os.IsNotExist(err) {
		t.Fatalf("unexpected local fallback: %v", err)
	}
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(name); !os.IsNotExist(err) {
		t.Fatalf("missing candidates=%v", err)
	}
}

func TestMappingCandidatesStayWithinDeclaredRoots(t *testing.T) {
	fs, _ := mappedFixture(t, false)
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(fs.Rootfs(), "mnt/project/input.txt")
	if err := os.Symlink(outside, local); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(HostView(fs), "/")
	// The local symlink cannot expose outside data; the declared source is
	// still a valid second candidate after a failed local read.
	if got, err := reader.Read("/mnt/project/input.txt"); err != nil || string(got) != "input" {
		t.Fatalf("anchored fallback read=%q %v", got, err)
	}
	if err := os.Remove(local); err != nil {
		t.Fatal(err)
	}
	fallback := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fallback, "mnt/project"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(fallback, "mnt/project/undeclared"), []byte("outside"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewReader(HostView(fs), fallback).Read("/mnt/project/undeclared"); !os.IsNotExist(err) {
		t.Fatalf("mapped request entered global fallback: %v", err)
	}
}

func TestAPIMappingReadOnlyIsIndependentOfProcessSupport(t *testing.T) {
	fs, source := mappedFixture(t, true)
	reader := NewReader(HostView(fs), "")
	if got, err := reader.Read("/mnt/project/input.txt"); err != nil || string(got) != "input" {
		t.Fatalf("read-only API mapping unavailable: %q %v", got, err)
	}
	if err := fs.Write("/mnt/project/input.txt", []byte("changed"), 0); !os.IsPermission(err) {
		t.Fatalf("read-only API mutation=%v", err)
	}
	if got, _ := os.ReadFile(filepath.Join(source, "input.txt")); string(got) != "input" {
		t.Fatal("source changed")
	}
}

func TestProcessViewMapsOnlySupportedDeclarations(t *testing.T) {
	for _, readOnly := range []bool{false, true} {
		fs, _ := mappedFixture(t, readOnly)
		host, err := fs.Resolve("/mnt/project/input.txt")
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name string
			view ProcessView
			want string
		}{
			{"carrier", HostView(fs), host},
			{"mount", RootedView(fs, MappingSupport{ReadWrite: true, ReadOnly: true}), "/mnt/project/input.txt"},
			{"helper", RedirectedView(fs, MappingSupport{ReadWrite: true}), host},
		} {
			if tc.name == "helper" && !readOnly {
				tc.want = "/mnt/project/input.txt"
			}
			if got, err := tc.view.Path(host); err != nil || got != tc.want {
				t.Fatalf("%s readOnly=%t path=%q %v want=%q", tc.name, readOnly, got, err, tc.want)
			}
		}
	}
}

func TestMappingReadCandidatesSurviveReopen(t *testing.T) {
	fs, _ := mappedFixture(t, false)
	if err := os.WriteFile(filepath.Join(fs.Rootfs(), "mnt/project/input.txt"), []byte("local"), 0644); err != nil {
		t.Fatal(err)
	}
	mappings := fs.PathMappings()
	root := fs.Rootfs()
	if err := fs.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.SetPathMappings(mappings); err != nil {
		t.Fatal(err)
	}
	got, err := NewReader(HostView(reopened), "").Read("/mnt/project/input.txt")
	if err != nil || string(got) != "local" {
		t.Fatalf("reopened mapping=%q %v", got, err)
	}
}
