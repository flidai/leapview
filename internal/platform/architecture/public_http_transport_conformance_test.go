package architecture

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestPublicHTTPAPISurfacesDoNotReferenceEventTransport keeps the public
// request/response boundary independent from the event transport.  The
// scanner intentionally visits authored handler/API Go files only: capability
// adapters and persistence packages are allowed to depend on their internal
// event implementation, while a route handler may only see the domain event
// contract.
func TestPublicHTTPAPISurfacesDoNotReferenceEventTransport(t *testing.T) {
	root := repoRoot(t)
	references, err := observePublicHTTPAPITransportReferences(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(references) == 0 {
		return
	}
	sort.Slice(references, func(i, j int) bool {
		if references[i].Path != references[j].Path {
			return references[i].Path < references[j].Path
		}
		if references[i].Line != references[j].Line {
			return references[i].Line < references[j].Line
		}
		return references[i].Symbol < references[j].Symbol
	})
	var message strings.Builder
	fmt.Fprintf(&message, "public HTTP/API source references event transport authority (%d):", len(references))
	for _, reference := range references {
		fmt.Fprintf(&message, "\n  - %s:%d %s", reference.Path, reference.Line, reference.Symbol)
	}
	t.Fatal(message.String())
}

type publicHTTPAPITransportReference struct {
	Path   string
	Line   int
	Symbol string
}

// These are canonical transport schema/framework spellings, not generic
// domain words such as "attempt" or "delivery" that legitimate product APIs
// may use.
var forbiddenEventTransportLiterals = []string{
	"event_delivery",
	"claim_generation",
	"claimed_by",
	"claimed_until",
}

func observePublicHTTPAPITransportReferences(root string) ([]publicHTTPAPITransportReference, error) {
	internalRoot := filepath.Join(root, "internal")
	references := make([]publicHTTPAPITransportReference, 0)
	err := filepath.WalkDir(internalRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if !isPublicHTTPAPIProductionGoPath(rel) {
			return nil
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return fmt.Errorf("parse %s: %w", rel, err)
		}
		for _, imported := range parsed.Imports {
			pathValue := strings.Trim(imported.Path.Value, `"`)
			if isForbiddenEventTransportImport(pathValue) {
				references = append(references, publicHTTPAPITransportReference{Path: rel, Line: fset.Position(imported.Pos()).Line, Symbol: "import " + pathValue})
			}
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.BasicLit:
				if value.Kind == token.STRING {
					literal := strings.Trim(value.Value, "`\"")
					for _, symbol := range forbiddenEventTransportLiterals {
						if strings.Contains(literal, symbol) {
							references = append(references, publicHTTPAPITransportReference{Path: rel, Line: fset.Position(value.Pos()).Line, Symbol: symbol})
						}
					}
				}
			}
			return true
		})
		return nil
	})
	return references, err
}

func isForbiddenEventTransportImport(path string) bool {
	for _, prefix := range []string{
		"github.com/flidai/leapview/internal/platform/events/postgres",
	} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func isPublicHTTPAPIProductionGoPath(path string) bool {
	if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".gen.go") || strings.HasSuffix(path, "_gen.go") {
		return false
	}
	parts := strings.Split(path, "/")
	for _, part := range parts {
		if part == "http" || part == "api" || part == "semanticapi" {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(path))
	if strings.HasPrefix(base, "api") || base == "handler.go" || base == "routes.go" || base == "router.go" || base == "public_http.go" {
		return true
	}
	return strings.HasPrefix(path, "internal/app/") && (base == "runtime_router.go" || base == "router.go")
}
