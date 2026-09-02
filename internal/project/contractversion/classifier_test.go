package contractversion

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestUnifiedClassifierStructuralAndSemanticChanges(t *testing.T) {
	baseFields := map[string]any{
		"id": map[string]any{"datatype": "String", "nullable": false},
	}
	tests := []struct {
		name      string
		candidate map[string]any
		wantClass Class
		wantMajor bool
		wantPath  string
	}{
		{
			name: "nullable field addition is compatible",
			candidate: map[string]any{
				"id":   map[string]any{"datatype": "String", "nullable": false},
				"note": map[string]any{"datatype": "String", "nullable": true},
			},
			wantClass: Compatible, wantPath: "contract.schema.fields.note",
		},
		{
			name:      "field removal is breaking",
			candidate: map[string]any{},
			wantClass: Breaking, wantMajor: true, wantPath: "contract.schema.fields.id",
		},
		{
			name: "datatype change is breaking",
			candidate: map[string]any{
				"id": map[string]any{"datatype": "Integer", "nullable": false},
			},
			wantClass: Breaking, wantMajor: true, wantPath: "contract.schema.fields.id.datatype",
		},
		{
			name: "nullable widening is breaking",
			candidate: map[string]any{
				"id": map[string]any{"datatype": "String", "nullable": true},
			},
			wantClass: Breaking, wantMajor: true, wantPath: "contract.schema.fields.id.nullable",
		},
		{
			name: "nullability guarantee removal is breaking",
			candidate: map[string]any{
				"id": map[string]any{"datatype": "String"},
			},
			wantClass: Breaking, wantMajor: true, wantPath: "contract.schema.fields.id.nullable",
		},
		{
			name: "governance metadata is warning",
			candidate: map[string]any{
				"id": map[string]any{"datatype": "String", "nullable": false, "criticalDataElement": true},
			},
			wantClass: Warning, wantPath: "contract.schema.fields.id.criticalDataElement",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result, err := Classify(sourceDocument("1.0.0", baseFields), sourceDocument("2.0.0", test.candidate))
			if err != nil {
				t.Fatal(err)
			}
			if result.Class != test.wantClass || result.RequiresMajor != test.wantMajor || len(result.Changes) != 1 || result.Changes[0].Path != test.wantPath {
				t.Fatalf("classification = %#v", result)
			}
		})
	}

	nullableBase := map[string]any{"id": map[string]any{"datatype": "String", "nullable": true}}
	result, err := Classify(sourceDocument("1.0.0", nullableBase), sourceDocument("1.1.0", baseFields))
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Compatible || result.RequiresMajor {
		t.Fatalf("nullable tightening classification = %#v", result)
	}
}

func TestUnifiedClassifierSecurityChanges(t *testing.T) {
	base := semanticDocument("1.0.0", []string{"region_access"}, []any{"emea"})

	tightened := semanticDocument("2.0.0", []string{"region_access", "department_access"}, []any{"emea"})
	result, err := Classify(base, tightened)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != SecuritySensitive || result.SecurityImpact != SecurityTightening || !result.RequiresMajor || result.RequiresSecurityApproval {
		t.Fatalf("tightening classification = %#v", result)
	}

	widened := semanticDocument("1.1.0", []string{"region_access"}, []any{"emea", "amer"})
	result, err = Classify(base, widened)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != SecuritySensitive || result.SecurityImpact != SecurityWidening || !result.RequiresSecurityApproval || result.RequiresMajor {
		t.Fatalf("widening classification = %#v", result)
	}
}

func TestUnifiedClassifierSemanticMetadataAndUnreferencedGrant(t *testing.T) {
	base := semanticDocument("1.0.0", []string{"region_access"}, []any{"emea"})
	candidate := mutateDocument(base, func(value map[string]any) {
		value["metadata"].(map[string]any)["contract"].(map[string]any)["version"] = "2.0.0"
		value["contract"].(map[string]any)["metrics"].(map[string]any)["order_count"].(map[string]any)["format"] = "currency"
	})
	result, err := Classify(base, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Breaking || len(result.Changes) != 1 || result.Changes[0].Domain != DomainSemantic || result.Changes[0].Path != "contract.metrics.order_count.format" {
		t.Fatalf("semantic metadata classification = %#v", result)
	}

	withoutGrant := unreferencedGrantDocument("1.0.0", false)
	withGrant := unreferencedGrantDocument("1.1.0", true)
	result, err = Classify(withoutGrant, withGrant)
	if err != nil {
		t.Fatal(err)
	}
	if result.Class != Compatible || result.SecurityImpact != SecurityNone || len(result.Changes) != 1 || result.Changes[0].Domain != DomainSecurity {
		t.Fatalf("unreferenced grant classification = %#v", result)
	}
}

func TestVersionTransitionPolicy(t *testing.T) {
	base := sourceDocument("1.2.3", map[string]any{"id": map[string]any{"datatype": "String", "nullable": false}})
	optional := map[string]any{
		"id":   map[string]any{"datatype": "String", "nullable": false},
		"note": map[string]any{"datatype": "String", "nullable": true},
	}
	if _, err := ValidateVersionTransition(base, sourceDocument("1.2.3", optional)); !errors.Is(err, ErrVersionReuseConflict) {
		t.Fatalf("same-version drift error = %v", err)
	}
	if _, err := ValidateVersionTransition(base, sourceDocument("1.2.4", optional)); !errors.Is(err, ErrVersionPolicy) {
		t.Fatalf("patch additive error = %v", err)
	}
	if _, err := ValidateVersionTransition(base, sourceDocument("1.3.0", optional)); err != nil {
		t.Fatalf("minor additive transition: %v", err)
	}

	removed := sourceDocument("1.3.0", map[string]any{})
	if _, err := ValidateVersionTransition(base, removed); !errors.Is(err, ErrVersionPolicy) {
		t.Fatalf("same-major breaking error = %v", err)
	}
	if _, err := ValidateVersionTransition(base, sourceDocument("2.0.0", map[string]any{})); err != nil {
		t.Fatalf("major breaking transition: %v", err)
	}

	if first, _ := SemverBaseline("1.2.3+build.1"); first != "v1.2.3" {
		t.Fatalf("build metadata baseline = %q", first)
	}
}

func sourceDocument(version string, fields map[string]any) []byte {
	return encodeDocument(map[string]any{
		"profile": "leapview.contract/v1", "apiVersion": "leapview.dev/v1", "kind": "Source",
		"metadata": map[string]any{"id": "source:orders", "name": "orders", "contract": map[string]any{"version": version, "compatibility": "backward"}},
		"contract": map[string]any{"schema": map[string]any{"mode": "strict", "fields": fields}},
	})
}

func semanticDocument(version string, grants []string, allowed []any) []byte {
	canonicalAllowed := make([]any, len(allowed))
	for index, value := range allowed {
		canonicalAllowed[index] = map[string]any{"type": "String", "value": value}
	}
	return encodeDocument(map[string]any{
		"profile": "leapview.contract/v1", "apiVersion": "leapview.dev/v1", "kind": "SemanticModel",
		"metadata": map[string]any{"id": "semantic-model:sales", "name": "sales", "contract": map[string]any{"version": version, "compatibility": "backward"}},
		"contract": map[string]any{
			"datasets": map[string]any{"orders": map[string]any{"model": "orders", "requiredAccessGrants": grants}},
			"accessGrants": map[string]any{
				"region_access":     map[string]any{"userAttribute": "region", "allowedValues": canonicalAllowed},
				"department_access": map[string]any{"userAttribute": "department", "allowedValues": []any{map[string]any{"type": "String", "value": "finance"}}},
			},
			"metrics": map[string]any{"order_count": map[string]any{"type": "aggregate", "dataset": "orders", "aggregation": "count"}},
		},
	})
}

func encodeDocument(value map[string]any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

func mutateDocument(encoded []byte, mutate func(map[string]any)) []byte {
	var value map[string]any
	if err := json.Unmarshal(encoded, &value); err != nil {
		panic(err)
	}
	mutate(value)
	return encodeDocument(value)
}

func unreferencedGrantDocument(version string, include bool) []byte {
	contract := map[string]any{
		"datasets": map[string]any{"orders": map[string]any{"model": "orders"}},
		"metrics":  map[string]any{"order_count": map[string]any{"type": "aggregate", "dataset": "orders", "aggregation": "count"}},
	}
	if include {
		contract["accessGrants"] = map[string]any{
			"unused": map[string]any{"userAttribute": "region", "allowedValues": []any{map[string]any{"type": "String", "value": "emea"}}},
		}
	}
	return encodeDocument(map[string]any{
		"profile": "leapview.contract/v1", "apiVersion": "leapview.dev/v1", "kind": "SemanticModel",
		"metadata": map[string]any{"id": "semantic-model:sales", "name": "sales", "contract": map[string]any{"version": version, "compatibility": "backward"}},
		"contract": contract,
	})
}
