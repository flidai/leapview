package dataquery

import "testing"

func TestModelRowsUsesCanonicalQueryKind(t *testing.T) {
	query := ModelRows("sales", "orders", []string{"order_id"}, nil, 0, 100, false)
	if query.Kind != KindModelRows {
		t.Fatalf("ModelRows kind = %q, want %q", query.Kind, KindModelRows)
	}
	if string(query.Kind) != "model_rows" {
		t.Fatalf("ModelRows kind = %q, want canonical model_rows", query.Kind)
	}
}

func TestQueryValidateAllowsSemanticCountOnlyAndRequiresRawTargets(t *testing.T) {
	if err := (Query{ModelID: "sales", Kind: KindSemanticRows, IncludeTotal: true}).Validate(); err != nil {
		t.Fatalf("semantic count-only query validate error = %v", err)
	}
	if err := (Query{ModelID: "sales", Kind: Kind("source_rows"), Target: "orders"}).Validate(); err == nil {
		t.Fatal("removed source query kind error = nil")
	}
	if err := (Query{ModelID: "sales", Kind: KindModelRows, Target: "orders", Sort: []Sort{{Field: "status", Direction: "sideways"}}}).Validate(); err == nil {
		t.Fatal("invalid sort direction error = nil")
	}
}
