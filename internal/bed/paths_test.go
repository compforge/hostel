package bed

import (
	"errors"
	"testing"
)

func TestPathDeclarationValidationAndCloning(t *testing.T) {
	for _, input := range []struct {
		m []PathMapping
		p []string
	}{
		{m: []PathMapping{{HostPath: "/volume", BedPath: "/workspace/x"}}},
		{m: []PathMapping{{HostPath: "relative", BedPath: "/project"}}},
		{m: []PathMapping{{HostPath: "/volume", BedPath: "/project"}}, p: []string{"/project/x"}},
		{m: []PathMapping{{HostPath: "/a", BedPath: "/project"}, {HostPath: "/b", BedPath: "/project/sub"}}},
		{p: []string{"/"}}, {p: []string{"/data", "/data/sub"}},
	} {
		if _, _, err := NormalizePaths(input.m, input.p); !errors.Is(err, ErrInvalidPaths) {
			t.Fatalf("accepted %+v: %v", input, err)
		}
	}
	for _, paths := range [][]string{nil, {}} {
		b := New("bed", "", Spec{SyncPaths: paths})
		if (b.Spec().SyncPaths == nil) != (paths == nil) {
			t.Fatal("lost nil/empty distinction")
		}
	}
	paths := []string{"/memory", "/workspace"}
	mappings := []PathMapping{{HostPath: "/volume", BedPath: "/project"}}
	b := New("bed", "", Spec{SyncPaths: paths, PathMappings: mappings})
	paths[0] = "/changed"
	mappings[0].HostPath = "/changed"
	spec := b.Spec()
	spec.SyncPaths[0] = "/changed"
	spec.PathMappings[0].HostPath = "/changed"
	if b.Spec().SyncPaths[0] != "/memory" || b.Spec().PathMappings[0].HostPath != "/volume" {
		t.Fatal("spec aliases caller state")
	}
}
