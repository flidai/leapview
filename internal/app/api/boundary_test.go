package api_test

import (
	"encoding/json"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiaggregate "github.com/flidai/leapview/internal/app/api/aggregate"
)

func TestAPIPackageStaysTransportContractOnly(t *testing.T) {
	forbidden := map[string]bool{
		"github.com/flidai/leapview/internal/app":        true,
		"github.com/go-chi/chi/v5":                       true,
		"github.com/starfederation/datastar-go/datastar": true,
		"maragu.dev/gomponents":                          true,
		"maragu.dev/gomponents-datastar":                 true,
		"net/http":                                       true,
	}
	assertPackageDoesNotImport(t, ".", forbidden)
}

func TestAgentDoesNotDependOnHeadlessAPIContract(t *testing.T) {
	assertPackageDoesNotImport(t, filepath.Join("..", "..", "agent"), map[string]bool{
		"github.com/flidai/leapview/internal/app/api/gen": true,
	})
}

func TestGeneratedAssetPayloadOpenAPIAllowsArbitraryJSON(t *testing.T) {
	spec, err := apiaggregate.GetEmbeddedOpenAPISpec()
	if err != nil {
		t.Fatalf("embedded openapi: %v", err)
	}
	components, _ := spec["components"].(map[string]any)
	schemas, _ := components["schemas"].(map[string]any)
	assetResponse, _ := schemas["AssetResponse"].(map[string]any)
	properties, _ := assetResponse["properties"].(map[string]any)
	payload, _ := properties["payload"].(map[string]any)
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload schema: %v", err)
	}
	if strings.Contains(string(raw), `"additionalProperties":{"type":"string"}`) {
		t.Fatalf("payload schema is string-only, want arbitrary JSON: %s", raw)
	}
}

func assertPackageDoesNotImport(t *testing.T, dir string, forbidden map[string]bool) {
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
