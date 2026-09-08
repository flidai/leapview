package contractprojection

import (
	"bytes"
	"encoding/json"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
	graph "github.com/flidai/leapview/internal/project/graph"
)

func TestModelCheckDefaultsAndSetOrder(t *testing.T) {
	g, err := graph.NewProjectGraph([]graph.Resource{{ID: "source:orders", Name: "orders", Kind: graph.KindSource}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := NewReferenceContext(g)
	if err != nil {
		t.Fatal(err)
	}
	var previous []byte
	for _, check := range []string{`{"id":"row_unique","type":"unique","fields":["b","a"]}`, `{"id":"row_unique","type":"unique","fields":["a","b"],"severity":"error"}`} {
		var input contracts.Model
		raw := `{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders_model"},"spec":{"definition":{"type":"direct","source":"orders"},"entities":{"row":{"type":"primary","fields":["a","b"]}},"grain":{"entity":"row"},"fields":{"a":{"datatype":"Integer"},"b":{"datatype":"Integer"}},"checks":[` + check + `]}}`
		if err := json.Unmarshal([]byte(raw), &input); err != nil {
			t.Fatal(err)
		}
		projection, err := ProjectModel(input, Contract{Version: "1.0.0", Compatibility: "backward"}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := CanonicalBytes(projection)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := DecodeModelPublication(canonical); err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, canonical) {
			t.Fatalf("equivalent check defaults differ:\n%s\n%s", previous, canonical)
		}
		previous = canonical
		if _, err := ProjectModel(input, Contract{Version: "1.0.0", Compatibility: "backward"}); err == nil {
			t.Fatal("direct model accepted without reference context")
		}
	}
	if _, err := canonicalModelSQL("SELECT id FROM source.orders", nil); err == nil {
		t.Fatal("SQL accepted without reference context")
	}
	if _, err := projectUniqueCheckFields([]string{"a", "a"}); err == nil {
		t.Fatal("duplicate unique-check fields silently collapsed")
	}
	var duplicateInput contracts.Model
	duplicateRaw := `{"apiVersion":"leapview.dev/v1","kind":"Model","metadata":{"id":"model:orders","name":"orders_model"},"spec":{"definition":{"type":"direct","source":"orders"},"entities":{"row":{"type":"primary","fields":["a"]}},"grain":{"entity":"row"},"fields":{"a":{"datatype":"Integer"}},"checks":[{"id":"same_check","type":"unique","fields":["a"]},{"id":"same_check","type":"non_null","field":"a"}]}}`
	if err := json.Unmarshal([]byte(duplicateRaw), &duplicateInput); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectModel(duplicateInput, Contract{Version: "1.0.0", Compatibility: "backward"}, ctx); err == nil {
		t.Fatal("duplicate model check IDs accepted")
	}
}

func TestSemanticTimeDefaultsMaterialize(t *testing.T) {
	gregorian, utc, empty := "gregorian", "UTC", ""
	var previous []byte
	for _, input := range []*SemanticTime{
		{NativeGrain: "day", Grains: []string{"month", "day"}},
		{NativeGrain: "day", Grains: []string{"day", "month"}, Calendar: &gregorian, Timezone: &utc},
		{NativeGrain: "day", Grains: []string{"day", "month"}, Calendar: &empty, Timezone: &empty},
	} {
		value, err := projectSemanticTime(input)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, encoded) {
			t.Fatalf("equivalent time defaults differ: %s %s", previous, encoded)
		}
		previous = encoded
	}
}

func TestSemanticFilterUsesFullBindingNotDimensionName(t *testing.T) {
	input := semanticIdentityInput(t, `["east"]`)
	dimension := (*input.Spec.Dimensions)["region"]
	delete(*input.Spec.Dimensions, "region")
	(*input.Spec.Dimensions)["display_region"] = dimension
	contract := Contract{Version: "1.0.0", Compatibility: "backward"}
	ctx := semanticIdentityContext(t)
	if _, err := ProjectSemanticModel(input, contract, ctx); err != nil {
		t.Fatalf("valid conformed binding rejected: %v", err)
	}
	conflicting := dimension
	conflicting.Datatype = "Integer"
	(*input.Spec.Dimensions)["region"] = conflicting
	if _, err := ProjectSemanticModel(input, contract, ctx); err == nil {
		t.Fatal("conflicting field types accepted")
	}
	conflicting.Bindings = map[string]contracts.SemanticDimensionBinding{"orders": {Field: "orders.unrelated"}}
	(*input.Spec.Dimensions)["region"] = conflicting
	if _, err := ProjectSemanticModel(input, contract, ctx); err != nil {
		t.Fatalf("unrelated suffix-named dimension changed filter typing: %v", err)
	}
}

func TestAggregateEmptyDefaultsMatchExplicitValues(t *testing.T) {
	ctx := semanticIdentityContext(t)
	contract := Contract{Version: "1.0.0", Compatibility: "backward"}
	for _, pair := range [][2]string{{"count", "zero"}, {"count_distinct", "zero"}, {"sum", "null"}, {"avg", "null"}} {
		t.Run(pair[0], func(t *testing.T) {
			input := semanticIdentityInput(t, `["east"]`)
			metric := input.Spec.Metrics["orders"].Value.(*contracts.SemanticMetricAggregateVariant)
			metric.Aggregation = pair[0]
			implicit, err := ProjectSemanticModel(input, contract, ctx)
			if err != nil {
				t.Fatal(err)
			}
			metric.Empty = &pair[1]
			explicit, err := ProjectSemanticModel(input, contract, ctx)
			if err != nil {
				t.Fatal(err)
			}
			left, err := Digest(implicit)
			if err != nil {
				t.Fatal(err)
			}
			right, err := Digest(explicit)
			if err != nil || left != right {
				t.Fatalf("default changed identity: %s %s %v", left, right, err)
			}
		})
	}
}
