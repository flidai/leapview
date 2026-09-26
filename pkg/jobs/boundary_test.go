package jobs

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublicAuthorityContractsRejectPrivateImportsAndExposedTypes(t *testing.T) {
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourcePath), "..", ".."))
	for _, packagePath := range []string{"pkg/permissions", "pkg/authority", "pkg/jobs"} {
		if err := checkPublicContractPackage(filepath.Join(root, packagePath)); err != nil {
			t.Fatalf("%s: %v", packagePath, err)
		}
	}
}

func checkPublicContractPackage(packageDir string) error {
	fset := token.NewFileSet()
	return filepath.WalkDir(packageDir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		imports := make(map[string]string, len(file.Imports))
		for _, imported := range file.Imports {
			importPath := strings.Trim(imported.Path.Value, `"`)
			if isPrivateImport(importPath) {
				return fmt.Errorf("%s imports private package %q", path, importPath)
			}
			if imported.Name == nil || imported.Name.Name == "_" || imported.Name.Name == "." {
				continue
			}
			imports[imported.Name.Name] = importPath
		}
		if reference := exportedPrivateTypeReference(file, imports); reference != "" {
			return fmt.Errorf("%s exposes private package type through exported API (%s)", path, reference)
		}
		return nil
	})
}

func isPrivateImport(importPath string) bool {
	return importPath == "internal" || strings.HasPrefix(importPath, "internal/") ||
		strings.Contains(importPath, "/internal/") || strings.HasSuffix(importPath, "/internal")
}

func exportedPrivateTypeReference(file *ast.File, imports map[string]string) string {
	var reference string
	check := func(node ast.Node) {
		if reference != "" {
			return
		}
		ast.Inspect(node, func(n ast.Node) bool {
			selector, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			packageName, ok := selector.X.(*ast.Ident)
			if !ok || !isPrivateImport(imports[packageName.Name]) {
				return true
			}
			reference = packageName.Name + "." + selector.Sel.Name
			return false
		})
	}

	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.FuncDecl:
			if declaration.Name.IsExported() {
				check(declaration.Type)
			}
		case *ast.GenDecl:
			for _, rawSpec := range declaration.Specs {
				switch spec := rawSpec.(type) {
				case *ast.TypeSpec:
					if spec.Name.IsExported() {
						check(spec.Type)
					}
				case *ast.ValueSpec:
					for _, name := range spec.Names {
						if name.IsExported() {
							check(spec)
							break
						}
					}
				}
			}
		}
		if reference != "" {
			return reference
		}
	}
	return ""
}
