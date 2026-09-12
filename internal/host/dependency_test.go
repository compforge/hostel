package host_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Host mechanisms must remain reusable by any domain. Parse every platform's
// files so local builds cannot hide a Linux-only reverse dependency.
func TestHostDoesNotImportDomains(t *testing.T) {
	err := filepath.WalkDir(".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			dep, err := strconv.Unquote(spec.Path.Value)
			if err != nil {
				return err
			}
			for _, domain := range []string{"bed", "amenity", "instance", "web"} {
				prefix := "github.com/qiankunli/hostel/internal/" + domain
				if dep == prefix || strings.HasPrefix(dep, prefix+"/") {
					t.Errorf("%s imports domain %s", path, dep)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
