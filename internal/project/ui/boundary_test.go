package ui

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUIPackageDoesNotImportHeadlessAPIContract(t *testing.T) {
	assertNoForbiddenImports(t, ".", map[string]bool{
		"github.com/flidai/leapview/internal/app/api/gen": true,
	})
}

func assertNoForbiddenImports(t *testing.T, dir string, forbidden map[string]bool) {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("glob %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		info, err := os.Stat(file)
		if err != nil {
			t.Fatalf("stat %s: %v", file, err)
		}
		if info.IsDir() {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatalf("parse imports in %s: %v", file, err)
		}
		for _, imported := range parsed.Imports {
			path := strings.Trim(imported.Path.Value, "\"")
			if forbidden[path] {
				t.Fatalf("%s imports forbidden package %s", file, path)
			}
		}
	}
}
