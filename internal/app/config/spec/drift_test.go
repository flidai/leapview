package configspec

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestProductionGoEnvironmentReadsAreCataloged(t *testing.T) {
	known := knownSettings()
	root := filepath.Join("..", "..", "..", "..")
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Getenv" && selector.Sel.Name != "LookupEnv") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			name, _ := strconv.Unquote(literal.Value)
			if strings.HasPrefix(name, "LEAPVIEW_") {
				if _, ok := known[name]; !ok {
					t.Errorf("%s reads uncataloged environment variable %s", path, name)
				}
			}
			return true
		})
		return nil
	})
	require.NoError(t, err)
}

func TestOperationalEnvironmentReferencesAreCataloged(t *testing.T) {
	known := knownSettings()
	pattern := regexp.MustCompile(`\bLEAPVIEW_[A-Z0-9_]+\b`)
	root := filepath.Join("..", "..", "..", "..")
	paths := []string{"README.md", "Taskfile.yml", "Dockerfile", ".env.example", "docs", "scripts", "deploy", "dashboards"}
	for _, relative := range paths {
		path := filepath.Join(root, relative)
		info, err := os.Stat(path)
		require.NoError(t, err)
		visit := func(path string) {
			body, err := os.ReadFile(path)
			if err != nil {
				t.Error(err)
				return
			}
			for _, name := range pattern.FindAllString(string(body), -1) {
				if strings.HasPrefix(name, "LEAPVIEW_TEST_") {
					continue
				}
				if _, ok := known[name]; !ok && !knownDynamicEnvironmentReference(name) && !knownEnvironmentFamilyReference(name) {
					t.Errorf("%s references uncataloged environment variable %s", path, name)
				}
			}
		}
		if !info.IsDir() {
			visit(path)
			continue
		}
		_ = filepath.WalkDir(path, func(path string, entry os.DirEntry, err error) error {
			if err == nil && entry.IsDir() && entry.Name() == "__pycache__" {
				return filepath.SkipDir
			}
			if err == nil && !entry.IsDir() && !strings.Contains(path, ".terraform/") && !strings.Contains(path, "/.local/") &&
				!strings.HasSuffix(path, ".tfstate") && !strings.HasSuffix(path, ".sqlite3") && !strings.HasSuffix(path, ".pyc") {
				visit(path)
			}
			return err
		})
	}
}

func knownEnvironmentFamilyReference(name string) bool {
	// The study rejects overrides by this literal family prefix. Individual
	// threshold names still require explicit catalog entries.
	return name == "LEAPVIEW_PERF_MAX_"
}

func knownDynamicEnvironmentReference(name string) bool {
	for _, prefix := range DynamicEnvironmentPrefixes() {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func knownSettings() map[string]struct{} {
	known := map[string]struct{}{}
	for _, setting := range Settings() {
		known[setting.Name] = struct{}{}
	}
	return known
}
