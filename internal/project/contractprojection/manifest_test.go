package contractprojection

import "testing"

func TestExclusionManifestIsCompleteAndQueryAble(t *testing.T) {
	manifest, err := LoadExclusionManifest()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		kind string
		path string
	}{
		{"Source", "spec.connection"},
		{"Source", "spec.schema.fields.customer_id.description"},
		{"Model", "spec.fields.amount.aiContext"},
		{"SemanticModel", "spec.filters.active.all.0.aiContext"},
		{"SemanticModel", "spec.metrics.revenue.hidden"},
	} {
		if !manifest.Excludes(test.kind, test.path) {
			t.Errorf("%s %s is not covered by a reviewed exclusion", test.kind, test.path)
		}
	}
	for _, test := range []struct {
		kind string
		path string
	}{
		{"Source", "spec.schema.fields.customer_id.datatype"},
		{"Model", "spec.grain.entity"},
		{"SemanticModel", "spec.datasets.orders.requiredAccessGrants"},
	} {
		if manifest.Excludes(test.kind, test.path) {
			t.Errorf("included field %s %s is unexpectedly excluded", test.kind, test.path)
		}
	}
}
