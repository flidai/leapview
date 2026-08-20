package document

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

// This fixture is deliberately assembled from the generated DTOs. It keeps
// the generated package live in the Go build and proves the public JSON names
// and all-origin union envelopes survive a canonical round trip. YAML
// decoding remains owned by configschema.DecodeResource (LEA-426); generated
// unions must not grow handwritten yaml.v3 unmarshallers here.
func TestDashboardDocumentCanonicalJSONRoundTrip(t *testing.T) {
	filterValue := DashboardFilterValue{Value: &StringDashboardFilterValue{
		Type:  "string",
		Value: "CA",
	}}
	document := DashboardDocument{
		APIVersion: DashboardApiVersionLeapviewDevV1,
		Kind:       DashboardResourceKindDashboard,
		Metadata: DashboardMetadata{
			ID:          "dashboard:sales",
			Name:        "sales",
			DisplayName: ptr("Sales"),
		},
		Spec: DashboardSpec{
			SemanticModel: "sales",
			Filters: []DashboardFilter{
				{
					ID:             "state",
					Label:          "State",
					Dimension:      "state",
					Control:        DashboardFilterControl{Value: &MultiSelectDashboardFilterControl{Type: "multiSelect"}},
					Operators:      ptr([]DashboardFilterOperator{DashboardFilterOperatorIn}),
					Default:        &DashboardFilterExpression{Value: &SetDashboardFilterExpression{Type: "set", Operator: DashboardFilterOperatorIn, Values: []DashboardFilterValue{filterValue}}},
					Required:       ptr(false),
					ReaderEditable: ptr(true),
					URLParameter:   ptr("state"),
				},
			},
			Visuals: map[string]DashboardVisual{
				"revenue": {
					Type:         DashboardVisualTypeBar,
					Title:        ptr("Revenue"),
					Query:        DashboardQuery{Value: &AggregateDashboardQuery{Type: "aggregate", Dimensions: []DashboardDimensionSelection{dimension("month")}, Metrics: []DashboardMetricSelection{metric("revenue")}}},
					Presentation: DashboardPresentation{Value: &CartesianDashboardPresentation{Type: "cartesian"}},
				},
			},
			Pages: []DashboardPage{{
				ID:    "overview",
				Title: "Overview",
				Components: []DashboardPageComponent{{
					Value: &VisualDashboardPageComponent{DashboardPageComponentBase: DashboardPageComponentBase{ID: "revenue-component", Type: "visual", Placement: DashboardPlacement{Column: 1, Row: 1, ColumnSpan: 12, RowSpan: 6}}, Type: "visual", Visual: "revenue"},
				}},
			}},
		},
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("marshal canonical document: %v", err)
	}
	if bytes.Contains(encoded, []byte("semantic_model")) || bytes.Contains(encoded, []byte("column_span")) {
		t.Fatalf("canonical document emitted snake_case field: %s", encoded)
	}
	var decoded DashboardDocument
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("unmarshal canonical document: %v", err)
	}
	reencoded, err := json.Marshal(decoded)
	if err != nil {
		t.Fatalf("marshal decoded canonical document: %v", err)
	}
	if string(reencoded) != string(encoded) {
		t.Fatalf("canonical JSON changed across round trip:\n%s\n%s", encoded, reencoded)
	}
}

func TestDashboardDocumentRejectsUnknownUnionDiscriminator(t *testing.T) {
	var query DashboardQuery
	if err := json.Unmarshal([]byte(`{"type":"sql"}`), &query); err == nil {
		t.Fatal("unknown query type decoded successfully")
	}
	var value DashboardFilterValue
	if err := json.Unmarshal([]byte(`{"type":"number","value":1}`), &value); err == nil {
		t.Fatal("unknown filter value type decoded successfully")
	}
	var dimensionSelection DashboardDimensionSelection
	if err := json.Unmarshal([]byte(`true`), &dimensionSelection); err == nil {
		t.Fatal("boolean decoded as compact dimension reference")
	}
	if err := json.Unmarshal([]byte(`{"dimension":"month","unexpected":true}`), &dimensionSelection); err == nil {
		t.Fatal("unknown dimension reference property decoded successfully")
	}
	if err := json.Unmarshal([]byte(`{"alias":"month"}`), &dimensionSelection); err == nil {
		t.Fatal("dimension reference missing required member decoded successfully")
	}
}

func TestCanonicalYAMLFixtureUsesGeneratedJSONContract(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "canonical.yaml"))
	if err != nil {
		t.Fatalf("read canonical YAML fixture: %v", err)
	}
	var value map[string]any
	if err := yaml.Unmarshal(content, &value); err != nil {
		t.Fatalf("decode canonical YAML fixture: %v", err)
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("convert canonical YAML fixture to JSON: %v", err)
	}
	var document DashboardDocument
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode canonical YAML fixture through generated JSON DTO: %v", err)
	}
	if document.Spec.SemanticModel != "sales" || len(document.Spec.Pages) != 1 {
		t.Fatalf("canonical fixture decoded unexpected document: %#v", document)
	}
}

func TestEncodeYAMLEmitsBlockStyleConfiguration(t *testing.T) {
	content, err := os.ReadFile(filepath.Join("testdata", "canonical.yaml"))
	if err != nil {
		t.Fatalf("read canonical YAML fixture: %v", err)
	}
	var source map[string]any
	if err := yaml.Unmarshal(content, &source); err != nil {
		t.Fatalf("decode canonical YAML fixture: %v", err)
	}
	normalized, err := json.Marshal(source)
	if err != nil {
		t.Fatalf("normalize canonical YAML fixture: %v", err)
	}
	var dashboard DashboardDocument
	if err := json.Unmarshal(normalized, &dashboard); err != nil {
		t.Fatalf("decode canonical dashboard: %v", err)
	}

	encoded, err := EncodeYAML(dashboard)
	if err != nil {
		t.Fatalf("EncodeYAML() error = %v", err)
	}
	if json.Valid(encoded) {
		t.Fatalf("EncodeYAML() emitted JSON instead of block-style YAML:\n%s", encoded)
	}
	text := string(encoded)
	for _, fragment := range []string{"apiVersion: leapview.dev/v1\n", "kind: Dashboard\n", "spec:\n", "  semanticModel: sales\n"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("EncodeYAML() output omitted block-style fragment %q:\n%s", fragment, text)
		}
	}

	var roundTrip map[string]any
	if err := yaml.Unmarshal(encoded, &roundTrip); err != nil {
		t.Fatalf("decode encoded YAML: %v", err)
	}
	roundTripJSON, err := json.Marshal(roundTrip)
	if err != nil {
		t.Fatalf("normalize encoded YAML: %v", err)
	}
	var decoded DashboardDocument
	if err := json.Unmarshal(roundTripJSON, &decoded); err != nil {
		t.Fatalf("decode encoded dashboard: %v", err)
	}
	if !reflect.DeepEqual(decoded, dashboard) {
		t.Fatalf("block-style YAML changed dashboard semantics:\n got=%#v\nwant=%#v", decoded, dashboard)
	}
}

func TestDashboardDocumentSchemaIsCamelCaseAndSealed(t *testing.T) {
	path := filepath.Join("..", "..", "..", "schemas", "json", "dashboard-document.schema.json")
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated dashboard schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(content, &schema); err != nil {
		t.Fatalf("decode generated dashboard schema: %v", err)
	}
	if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("schema is not draft 2020-12: %v", schema["$schema"])
	}
	var walk func(any, string)
	walk = func(value any, path string) {
		switch current := value.(type) {
		case map[string]any:
			if properties, ok := current["properties"].(map[string]any); ok {
				for name, property := range properties {
					if strings.Contains(name, "_") {
						t.Fatalf("snake_case public property %q at %s", name, path)
					}
					walk(property, path+"."+name)
				}
			}
			for name, child := range current {
				if name != "properties" {
					walk(child, path+"."+name)
				}
			}
		case []any:
			for index, child := range current {
				walk(child, path+"["+string(rune('0'+index))+"]")
			}
		}
	}
	walk(schema, "$")
}

func TestDashboardDocumentSchemaRejectsUnknownVisualAndPresentationKinds(t *testing.T) {
	path := filepath.Join("..", "..", "..", "schemas", "json", "dashboard-document.schema.json")
	compiled, err := jsonschema.NewCompiler().Compile(path)
	if err != nil {
		t.Fatalf("compile generated dashboard schema: %v", err)
	}
	content, err := os.ReadFile(filepath.Join("testdata", "canonical.yaml"))
	if err != nil {
		t.Fatalf("read canonical YAML fixture: %v", err)
	}
	var document map[string]any
	if err := yaml.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode canonical YAML fixture: %v", err)
	}
	// yaml.v3's map values are normalized through JSON so the validator sees
	// the same scalar/container types as a browser or API client.
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("normalize canonical YAML fixture: %v", err)
	}
	if err := json.Unmarshal(encoded, &document); err != nil {
		t.Fatalf("decode normalized canonical YAML fixture: %v", err)
	}
	if err := compiled.Validate(document); err != nil {
		t.Fatalf("canonical fixture rejected by generated schema: %v", err)
	}

	spec := document["spec"].(map[string]any)
	visuals := spec["visuals"].(map[string]any)
	visual := visuals["revenue"].(map[string]any)
	visual["type"] = "unknown"
	if err := compiled.Validate(document); err == nil {
		t.Fatal("generated schema accepted unknown visual type")
	}
	visual["type"] = "bar"
	presentation := visual["presentation"].(map[string]any)
	presentation["type"] = "unknown"
	if err := compiled.Validate(document); err == nil {
		t.Fatal("generated schema accepted unknown presentation type")
	}
}

func ptr[T any](value T) *T { return &value }

func dimension(value string) DashboardDimensionSelection {
	return DashboardDimensionSelection{String: ptr(value)}
}

func metric(value string) DashboardMetricSelection {
	return DashboardMetricSelection{String: ptr(value)}
}
