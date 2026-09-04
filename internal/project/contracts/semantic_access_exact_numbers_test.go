package contracts_test

import (
	"encoding/json"
	"testing"

	contracts "github.com/flidai/leapview/internal/project/contracts"
	configschema "github.com/flidai/leapview/internal/project/schema"
	"github.com/flidai/leapview/internal/semanticvalue"
)

func TestSemanticAccessAllowedValuesPreserveExactNumbersThroughResourceDecode(t *testing.T) {
	document := []byte(`{
  "apiVersion": "leapview.dev/v1",
  "kind": "SemanticModel",
  "metadata": {"id": "semantic-model:sales", "name": "sales"},
  "spec": {
    "accessGrants": {
      "numbers": {
        "userAttribute": "accountNumber",
        "allowedValues": [7, 9007199254740993, 1.2300]
      }
    },
    "datasets": {"orders": {"model": "orders_model"}},
    "metrics": {}
  }
}`)

	var model contracts.SemanticModel
	if err := configschema.DecodeResource(configschema.KindSemanticModel, "semantic-model.json", document, &model); err != nil {
		t.Fatalf("DecodeResource: %v", err)
	}
	grant := (*model.Spec.AccessGrants)["numbers"]
	wantTokens := []string{"7", "9007199254740993", "1.23"}
	if len(grant.AllowedValues) != len(wantTokens) {
		t.Fatalf("allowed value count = %d, want %d", len(grant.AllowedValues), len(wantTokens))
	}
	for index, want := range wantTokens {
		number, ok := grant.AllowedValues[index].(json.Number)
		if !ok || number.String() != want {
			t.Fatalf("allowedValues[%d] = %#v (%T), want json.Number(%q)", index, grant.AllowedValues[index], grant.AllowedValues[index], want)
		}
	}

	checks := []struct {
		index     int
		typeName  semanticvalue.Type
		canonical string
	}{
		{index: 0, typeName: semanticvalue.TypeInteger, canonical: "7"},
		{index: 1, typeName: semanticvalue.TypeInteger, canonical: "9007199254740993"},
		{index: 2, typeName: semanticvalue.TypeDecimal, canonical: "1.23"},
	}
	for _, check := range checks {
		value, err := semanticvalue.Canonicalize(check.typeName, grant.AllowedValues[check.index])
		if err != nil {
			t.Fatalf("canonicalize allowedValues[%d]: %v", check.index, err)
		}
		if value.Canonical() != check.canonical {
			t.Fatalf("canonical allowedValues[%d] = %q, want %q", check.index, value.Canonical(), check.canonical)
		}
	}

	encoded, err := json.Marshal(grant.AllowedValues)
	if err != nil {
		t.Fatalf("marshal exact allowed values: %v", err)
	}
	if got, want := string(encoded), `[7,9007199254740993,1.23]`; got != want {
		t.Fatalf("encoded allowed values = %s, want %s", got, want)
	}
}
