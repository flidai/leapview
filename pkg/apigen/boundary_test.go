package apigen

import (
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPublicPackagesAvoidRepoPrivateImports(t *testing.T) {
	t.Helper()

	fset := token.NewFileSet()
	err := filepath.WalkDir(".", func(path string, d fs.DirEntry, walkErr error) error {
		require.NoError(t, walkErr)
		if d.IsDir() {
			if d.Name() == "testdata" || d.Name() == "examples" || d.Name() == "example" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		require.NoError(t, err)

		for _, imp := range file.Imports {
			importPath := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(importPath, "github.com/Yacobolo/toolbelt/apigen/") {
				continue
			}
			if strings.HasPrefix(importPath, "github.com/Yacobolo/toolbelt/") {
				t.Fatalf("%s imports repo-private package %q", path, importPath)
			}
			if importPath == "github.com/go-chi/chi/v5" && !strings.HasPrefix(filepath.ToSlash(path), "runtime/chi/") {
				t.Fatalf("%s imports chi directly; only apigen/runtime/chi may do that", path)
			}
			if importPath == "github.com/spf13/cobra" && !strings.HasPrefix(filepath.ToSlash(path), "runtime/cobra/") {
				t.Fatalf("%s imports cobra directly; only apigen/runtime/cobra may do that", path)
			}
		}

		return nil
	})
	require.NoError(t, err)
}
