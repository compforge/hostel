package bedfs

import (
	"path/filepath"
	"testing"
)

func TestRootedViewPreservesNativeDataPaths(t *testing.T) {
	for _, name := range []string{"one", "two"} {
		t.Run(name, func(t *testing.T) {
			fs := newTestFS(t, t.TempDir())
			view := RootedView(fs, MappingSupport{ReadWrite: true, ReadOnly: true})
			for _, client := range []string{"/", "/workspace", "/mnt/skills/a", "/tmp/a", "/session-cache/a"} {
				host, err := fs.Resolve(client)
				if err != nil {
					t.Fatal(err)
				}
				if got, err := view.Path(host); err != nil || got != client {
					t.Fatalf("Path(%s)=%q,%v want %q", host, got, err, client)
				}
			}
			if _, err := view.Path(filepath.Dir(fs.Rootfs())); err == nil {
				t.Fatal("root view accepted a sibling carrier path")
			}
		})
	}
}
