package isolation

import (
	"path/filepath"
	"testing"

	"github.com/qiankunli/hostel/internal/bed/filesystem/bedfs"
)

func TestRootExecutableProjectsOnlyOwnedData(t *testing.T) {
	fs := newTestFS(t, t.TempDir())
	for _, tc := range []struct{ source, want string }{
		{filepath.Join(fs.Rootfs(), "mnt/tool"), "/mnt/tool"},
		{"/bin/sh", "/bin/sh"},
		{"/carrier/sibling/tool", "/carrier/sibling/tool"},
	} {
		if got := rootExecutable(fs, tc.source, bedfs.MappingSupport{ReadWrite: true}); got != tc.want {
			t.Fatalf("executable=%q want=%q", got, tc.want)
		}
	}
}

func TestPRootProbeRejectsWorkspaceOnlySuccess(t *testing.T) {
	helper := fakeNamedHelper(t, "proot", "#!/bin/sh\nprintf 'proot-view\\n/workspace\\n'\n")
	report := probeProot(direct{}, t.TempDir(), helper)
	if report.Succeeded() || report.Error == "" {
		t.Fatalf("workspace-only helper advertised a Bed root: %+v", report)
	}
}
