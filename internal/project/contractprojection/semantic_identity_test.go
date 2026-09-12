package contractprojection

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
	graph "github.com/flidai/leapview/internal/project/graph"
)

func semanticIdentityInput(t *testing.T, values string) contracts.SemanticModel {
	t.Helper()
	var input contracts.SemanticModel
	raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders_semantic"},"spec":{
"datasets":{"orders":{"model":"orders_model","requiredAccessGrants":["region"],"accessFilters":[{"field":"region","userAttribute":"region"}],"metrics":{"orders":{"type":"simple","agg":"count","field":"id","requiredAccessGrants":["region"]}}}},
"accessGrants":{"region":{"userAttribute":"region","allowedValues":` + values + `}},
"dimensions":{"region":{"datatype":"String","bindings":{"orders":{"field":"orders.region"}},"requiredAccessGrants":["region"]}},
"filters":{"selected":{"field":"orders.region","operator":"in","value":["west","east"]}}
}}`
	if err := json.Unmarshal([]byte(raw), &input); err != nil {
		t.Fatal(err)
	}
	return input
}

func semanticIdentityContext(t *testing.T) ReferenceContext {
	t.Helper()
	g, err := graph.NewProjectGraph([]graph.Resource{{ID: "model:stable-orders", Name: "orders_model", Kind: graph.KindModel}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, err := NewReferenceContext(g)
	if err != nil {
		t.Fatal(err)
	}
	return ctx
}

func TestDatasetLocalMembersReachCanonicalSemanticProjection(t *testing.T) {
	ctx := semanticIdentityContext(t)
	contract := Contract{Version: "1.0.0", Compatibility: "backward"}
	var previous []byte
	for _, field := range []string{"", `,"field":"amount"`} {
		var authored contracts.SemanticModel
		raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders_semantic"},"spec":{"datasets":{"orders":{"model":"orders_model","dimensions":{"region":{"field":"region","datatype":"String"}},"metrics":{"amount":{"type":"simple","agg":"sum"` + field + `}}}}}}`
		if err := json.Unmarshal([]byte(raw), &authored); err != nil {
			t.Fatal(err)
		}
		projection, err := ProjectSemanticModel(authored, contract, ctx)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := CanonicalBytes(projection)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(canonical, []byte(`"aggregation":"sum"`)) || !bytes.Contains(canonical, []byte(`"field":"orders.amount"`)) || !bytes.Contains(canonical, []byte(`"field":"orders.region"`)) {
			t.Fatalf("local members omitted from projection: %s", canonical)
		}
		if _, err := DecodeSemanticModelPublication(canonical); err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, canonical) {
			t.Fatalf("explicit field default changed identity:\n%s\n%s", previous, canonical)
		}
		previous = canonical
	}
}

func TestDatasetLocalProjectionResolvesDatatypeBeforeIdentity(t *testing.T) {
	ctx, err := semanticIdentityContext(t).WithModelFieldTypes("orders_model", map[string]string{"region": "String"})
	if err != nil {
		t.Fatal(err)
	}
	var previous []byte
	for _, assertion := range []string{"", `"datatype":"String"`} {
		var authored contracts.SemanticModel
		raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders_semantic"},"spec":{"datasets":{"orders":{"model":"orders_model","dimensions":{"region":{` + assertion + `}},"metrics":{"orders":{"type":"simple","agg":"count","field":"id"}}}}}}`
		if err := json.Unmarshal([]byte(raw), &authored); err != nil {
			t.Fatal(err)
		}
		projection, err := ProjectSemanticModel(authored, Contract{Version: "1.0.0", Compatibility: "backward"}, ctx)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := CanonicalBytes(projection)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(canonical, []byte(`"field":"orders.region"`)) || !bytes.Contains(canonical, []byte(`"datatype":"String"`)) {
			t.Fatalf("inferred dimension projected incorrectly: %s", canonical)
		}
		if _, err := DecodeSemanticModelPublication(canonical); err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, canonical) {
			t.Fatalf("explicit datatype assertion changed identity:\n%s\n%s", previous, canonical)
		}
		previous = canonical
	}
	var incompatible contracts.SemanticModel
	raw := `{"apiVersion":"leapview.dev/v1","kind":"SemanticModel","metadata":{"id":"semantic:orders","name":"orders_semantic"},"spec":{"datasets":{"orders":{"model":"orders_model","dimensions":{"region":{"datatype":"Integer"}},"metrics":{"orders":{"type":"simple","agg":"count","field":"id"}}}}}}`
	if err := json.Unmarshal([]byte(raw), &incompatible); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectSemanticModel(incompatible, Contract{Version: "1.0.0", Compatibility: "backward"}, ctx); err == nil || !strings.Contains(err.Error(), "disagrees with resolved logical datatype") {
		t.Fatalf("incompatible assertion accepted: %v", err)
	}
}

func TestSemanticProtectionProjectionIdentity(t *testing.T) {
	ctx := semanticIdentityContext(t)
	contract := Contract{Version: "1.0.0", Compatibility: "backward"}
	var previous []byte
	for _, allowed := range []string{`["west","east"]`, `["east","west"]`} {
		input := semanticIdentityInput(t, allowed)
		projection, err := ProjectSemanticModel(input, contract, ctx)
		if err != nil {
			t.Fatal(err)
		}
		canonical, err := CanonicalBytes(projection)
		if err != nil {
			t.Fatal(err)
		}
		if previous != nil && !bytes.Equal(previous, canonical) {
			t.Fatalf("grant order changed identity:\n%s\n%s", previous, canonical)
		}
		previous = canonical
		if _, err := DecodeSemanticModelPublication(canonical); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{`"accessGrants"`, `"requiredAccessGrants"`, `"accessFilters"`, `"model:stable-orders"`, `"type":"String"`} {
			if !strings.Contains(string(canonical), field) {
				t.Fatalf("protection metadata omitted %s", field)
			}
		}
		input.Spec.Datasets["orders"] = contracts.SemanticDataset{Model: "other"}
		after, err := CanonicalBytes(projection)
		if err != nil || !bytes.Equal(canonical, after) {
			t.Fatal("nested authored map mutation affected sealed identity")
		}
	}
	changed, err := ProjectSemanticModel(semanticIdentityInput(t, `["north"]`), contract, ctx)
	if err != nil {
		t.Fatal(err)
	}
	changedBytes, err := CanonicalBytes(changed)
	if err != nil || bytes.Equal(previous, changedBytes) {
		t.Fatal("access grant change did not change identity")
	}
}

func TestSemanticProjectionRejectsUnusableAuthority(t *testing.T) {
	contract := Contract{Version: "1.0.0", Compatibility: "backward"}
	ctx := semanticIdentityContext(t)
	for _, allowed := range []contracts.SemanticAllowedValues{{}, {nil}, {true, "east"}} {
		input := semanticIdentityInput(t, `["east"]`)
		grant := (*input.Spec.AccessGrants)["region"]
		grant.AllowedValues = allowed
		(*input.Spec.AccessGrants)["region"] = grant
		if _, err := ProjectSemanticModel(input, contract, ctx); err == nil {
			t.Fatalf("accepted invalid grant %#v", allowed)
		}
	}
	input := semanticIdentityInput(t, `["east"]`)
	if _, err := ProjectSemanticModel(input, contract); err == nil {
		t.Fatal("missing graph authority accepted")
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	encoded = bytes.Replace(encoded, []byte(`"field":"orders.region","operator"`), []byte(`"field":"orders.unknown","operator"`), 1)
	var invalid contracts.SemanticModel
	if err := json.Unmarshal(encoded, &invalid); err != nil {
		t.Fatal(err)
	}
	if _, err := ProjectSemanticModel(invalid, contract, ctx); err == nil {
		t.Fatal("unknown semantic filter dimension accepted")
	}
}
