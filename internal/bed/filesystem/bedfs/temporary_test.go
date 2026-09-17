package bedfs

import (
	model "github.com/qiankunli/hostel/internal/bed"
	"os"
	"path/filepath"
	"testing"
)

func TestTemporaryDirectoryViews(t *testing.T) {
	home := filepath.Join(t.TempDir(), "beds", "one", "data")
	if err := os.MkdirAll(home, 0755); err != nil {
		t.Fatal(err)
	}
	fs := newTestFS(t, home)
	if err := fs.PrepareTempdir(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(fs.Rootfs(), "tmp"))
	if err != nil || info.Mode()&(os.ModeSticky|0777) != os.ModeSticky|0777 {
		t.Fatalf("temporary permissions: %v %v", info, err)
	}
	if err := fs.Write("/tmp/retained", []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := fs.PrepareTempdir(); err != nil {
		t.Fatal(err)
	}
	if data, err := fs.Read("/tmp/retained"); err != nil || string(data) != "retained" {
		t.Fatalf("preparation discarded data: %q %v", data, err)
	}
	for _, tc := range []struct {
		name string
		view ProcessView
		want string
	}{
		{"carrier", HostView(fs), filepath.Join(fs.Rootfs(), "tmp")},
		{"pathshim", RedirectedView(fs, MappingSupport{ReadWrite: true}), "/tmp"},
		{"rooted", RootedView(fs, MappingSupport{ReadWrite: true}), "/tmp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.view.Tempdir()
			if err != nil || got != tc.want {
				t.Fatalf("temporary path=%q %v want=%q", got, err, tc.want)
			}
			host, err := fs.Resolve("/tmp/retained")
			if err != nil {
				t.Fatal(err)
			}
			if got, err := tc.view.Path(host); err != nil || got != filepath.Join(tc.want, "retained") {
				t.Fatalf("file path=%q %v", got, err)
			}
		})
	}
	external, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(external)
	if err := fs.SetPathMappings([]model.PathMapping{{HostPath: external, BedPath: "/tmp"}}); err != nil {
		t.Fatal(err)
	}
	if err := fs.PrepareTempdir(); err != nil {
		t.Fatal(err)
	}
	after, _ := os.Stat(external)
	if before.Mode() != after.Mode() {
		t.Fatal("changed caller-owned temporary directory permissions")
	}
	if got, err := HostView(fs).Tempdir(); err != nil || got != external {
		t.Fatalf("mapped carrier temporary path=%q %v", got, err)
	}
	if got, err := RedirectedView(fs, MappingSupport{ReadWrite: true}).Tempdir(); err != nil || got != "/tmp" {
		t.Fatalf("mapped temporary path=%q %v", got, err)
	}
}
