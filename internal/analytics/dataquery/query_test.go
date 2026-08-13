package dataquery

import "testing"

func TestQueryValidateAllowsSemanticCountOnlyAndRequiresRawTargets(t *testing.T) {
	if err := (Query{ModelID: "sales", Kind: KindSemanticRows, IncludeTotal: true}).Validate(); err != nil {
		t.Fatalf("semantic count-only query validate error = %v", err)
	}
	if err := (Query{ModelID: "sales", Kind: Kind("source_rows"), Target: "orders"}).Validate(); err == nil {
		t.Fatal("removed source query kind error = nil")
	}
	if err := (Query{ModelID: "sales", Kind: KindModelTableRows, Target: "orders", Sort: []Sort{{Field: "status", Direction: "sideways"}}}).Validate(); err == nil {
		t.Fatal("invalid sort direction error = nil")
	}
}
