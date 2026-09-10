package document

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// Structural identifier bounds and map-key grammars are part of the generated
// contract, rather than contextual compiler rules. Keep adversarial examples
// here so a permissive scalar or unconstrained Record cannot regress silently.
func TestDashboardDocumentSchemaRejectsIdentifierViolations(t *testing.T) {
	compiled := loadDashboardDocumentSchema(t)
	fixture := loadDashboardDocumentFixture(t)

	cases := map[string]func(map[string]any){
		"resource id": func(document map[string]any) {
			document["metadata"].(map[string]any)["id"] = "dashboard invalid"
		},
		"resource name": func(document map[string]any) {
			document["metadata"].(map[string]any)["name"] = "1sales"
		},
		"resource id maximum": func(document map[string]any) {
			document["metadata"].(map[string]any)["id"] = "dashboard:" + repeatString("a", 128)
		},
		"visual map key": func(document map[string]any) {
			visuals := document["spec"].(map[string]any)["visuals"].(map[string]any)
			visual := visuals["revenue"]
			delete(visuals, "revenue")
			visuals["invalid key"] = visual
		},
		"filter id": func(document map[string]any) {
			filters := document["spec"].(map[string]any)["filters"].([]any)
			filters[0].(map[string]any)["id"] = "state id"
		},
		"page id": func(document map[string]any) {
			pages := document["spec"].(map[string]any)["pages"].([]any)
			pages[0].(map[string]any)["id"] = "overview page"
		},
		"component id": func(document map[string]any) {
			pages := document["spec"].(map[string]any)["pages"].([]any)
			components := pages[0].(map[string]any)["components"].([]any)
			components[0].(map[string]any)["id"] = "component id"
		},
		"result alias": func(document map[string]any) {
			visual := document["spec"].(map[string]any)["visuals"].(map[string]any)["revenue"].(map[string]any)
			query := visual["query"].(map[string]any)
			query["dimensions"] = []any{map[string]any{"dimension": "state", "alias": "bad.alias"}}
		},
		"qualified records field": func(document map[string]any) {
			visual := document["spec"].(map[string]any)["visuals"].(map[string]any)["revenue"].(map[string]any)
			visual["query"] = map[string]any{
				"type": "records", "dataset": "orders",
				"fields": []any{map[string]any{"field": "orders.id"}},
			}
		},
		"dataset map key": func(document map[string]any) {
			visual := document["spec"].(map[string]any)["visuals"].(map[string]any)["revenue"].(map[string]any)
			visual["datasets"] = map[string]any{
				"invalid key": map[string]any{"type": "records", "dataset": "orders", "fields": []any{map[string]any{"field": "id"}}},
			}
		},
	}

	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			document := cloneSchemaDocument(fixture)
			mutate(document)
			if err := compiled.Validate(document); err == nil {
				t.Fatal("generated dashboard schema accepted identifier violation")
			}
		})
	}
}

func TestDashboardDocumentSchemaAllowsPointAndChoroplethLabels(t *testing.T) {
	compiled := loadDashboardDocumentSchema(t)
	for _, kind := range []string{"point", "choropleth"} {
		t.Run(kind, func(t *testing.T) {
			document := geographicSchemaDocument(t, kind, true)
			if err := compiled.Validate(document); err != nil {
				t.Fatalf("schema rejected %s layer label: %v", kind, err)
			}
		})
	}
}

func TestDashboardDocumentSchemaRejectsUnsupportedGeographicLabelPaths(t *testing.T) {
	compiled := loadDashboardDocumentSchema(t)
	for _, test := range []struct {
		name string
		kind string
		path string
	}{
		{name: "map presentation", kind: "presentation", path: "/spec/visuals/revenue/presentation/labels"},
		{name: "heat layer", kind: "heat", path: "/spec/visuals/revenue/presentation/layers/0/label"},
		{name: "density layer", kind: "density", path: "/spec/visuals/revenue/presentation/layers/0/label"},
		{name: "path layer", kind: "path", path: "/spec/visuals/revenue/presentation/layers/0/label"},
		{name: "reference layer", kind: "reference", path: "/spec/visuals/revenue/presentation/layers/0/label"},
	} {
		t.Run(test.name, func(t *testing.T) {
			document := geographicSchemaDocument(t, test.kind, true)
			if err := compiled.Validate(document); err == nil || !strings.Contains(err.Error(), "at '"+test.path+"'") {
				t.Fatalf("schema accepted unsupported geographic label path %s: %v", test.name, err)
			}
		})
	}
}

func TestDashboardDocumentSchemaRejectsRemovedPathCurvature(t *testing.T) {
	compiled := loadDashboardDocumentSchema(t)
	document := geographicSchemaDocument(t, "path", false)
	layer := document["spec"].(map[string]any)["visuals"].(map[string]any)["revenue"].(map[string]any)["presentation"].(map[string]any)["layers"].([]any)[0].(map[string]any)
	layer["line"] = map[string]any{"width": 3, "curvature": 0}
	if err := compiled.Validate(document); err == nil || !strings.Contains(err.Error(), "curvature") {
		t.Fatalf("generated dashboard schema accepted removed path curvature: %v", err)
	}
}

func geographicSchemaDocument(t *testing.T, kind string, label bool) map[string]any {
	t.Helper()
	document := loadDashboardDocumentFixture(t)
	visual := document["spec"].(map[string]any)["visuals"].(map[string]any)["revenue"].(map[string]any)
	visual["type"] = "map"
	visual["presentation"] = map[string]any{"type": "geographic"}
	if kind == "presentation" {
		visual["presentation"].(map[string]any)["labels"] = map[string]any{"density": "automatic"}
		return document
	}
	layer := map[string]any{"id": "layer", "kind": kind}
	if label {
		layer["label"] = "city"
	}
	switch kind {
	case "point", "heat", "density":
		layer["latitude"], layer["longitude"] = "latitude", "longitude"
	case "choropleth":
		layer["geometryAsset"], layer["join"] = "brazil_states", "state"
	case "reference":
		layer["geometryAsset"] = "brazil_states"
	case "path":
		layer["latitude"], layer["longitude"] = "latitude", "longitude"
		layer["path"], layer["order"] = "route", "position"
	}
	visual["presentation"].(map[string]any)["layers"] = []any{layer}
	return document
}

func TestDashboardDocumentSchemaRejectsRemovedKPIThresholds(t *testing.T) {
	compiled := loadDashboardDocumentSchema(t)
	document := loadDashboardDocumentFixture(t)
	visual := document["spec"].(map[string]any)["visuals"].(map[string]any)["revenue"].(map[string]any)
	visual["type"] = "kpi"
	visual["presentation"] = map[string]any{
		"type":       "kpi",
		"thresholds": []any{map[string]any{"value": 50, "tone": "warning"}},
	}
	if err := compiled.Validate(document); err == nil || !strings.Contains(err.Error(), "thresholds") {
		t.Fatalf("generated dashboard schema accepted removed KPI thresholds: %v", err)
	}
}

func loadDashboardDocumentSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	path := filepath.Join("..", "..", "..", "schemas", "json", "dashboard-document.schema.json")
	compiled, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatalf("compile generated dashboard schema: %v", err)
	}
	return compiled
}

func loadDashboardDocumentFixture(t *testing.T) map[string]any {
	t.Helper()
	path := filepath.Join("testdata", "canonical.yaml")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read canonical dashboard fixture: %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode canonical dashboard fixture: %v", err)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("normalize canonical dashboard fixture: %v", err)
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode normalized canonical dashboard fixture: %v", err)
	}
	return document
}

func cloneSchemaDocument(document map[string]any) map[string]any {
	encoded, _ := json.Marshal(document)
	var clone map[string]any
	_ = json.Unmarshal(encoded, &clone)
	return clone
}

func repeatString(value string, count int) string {
	result := ""
	for index := 0; index < count; index++ {
		result += value
	}
	return result
}
