package contractprojection

import (
	"os"
	"strings"
	"testing"
)

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
		{"Model", "spec.fields.amount.aiContext.instructions"},
		{"SemanticModel", "spec.filters.active.all.0.aiContext.instructions"},
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

func TestExclusionManifestRejectsTampering(t *testing.T) {
	raw, err := os.ReadFile("exclusions.json")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		edit func(string) string
		want string
	}{
		{
			name: "unknown schema field",
			edit: func(value string) string {
				return strings.Replace(value, `"profile":`, `"unreviewed":true,"profile":`, 1)
			},
			want: "unknown field",
		},
		{
			name: "schema version",
			edit: func(value string) string {
				return strings.Replace(value, `"schemaVersion": 1`, `"schemaVersion": 2`, 1)
			},
			want: "schemaVersion",
		},
		{
			name: "profile",
			edit: func(value string) string {
				return strings.Replace(value, `"profile": "leapview.contract/v1"`, `"profile": "other/v1"`, 1)
			},
			want: "profile",
		},
		{
			name: "source authority",
			edit: func(value string) string {
				return strings.Replace(value, `"sourceAuthority": "api/gen/data-resources-ir.json"`, `"sourceAuthority": "untrusted.json"`, 1)
			},
			want: "sourceAuthority",
		},
		{
			name: "unknown kind",
			edit: func(value string) string { return strings.Replace(value, `"Model": [`, `"Unknown": [`, 1) },
			want: "Model exclusions are required",
		},
		{
			name: "duplicate path",
			edit: func(value string) string {
				return strings.Replace(value, `"metadata.description"`, `"metadata.displayName"`, 1)
			},
			want: "duplicate Source path",
		},
		{
			name: "duplicate object member",
			edit: func(value string) string {
				return strings.Replace(value, `"profile": "leapview.contract/v1",`, `"profile": "leapview.contract/v1","profile": "leapview.contract/v1",`, 1)
			},
			want: "duplicate object member",
		},
		{
			name: "invalid path shape",
			edit: func(value string) string {
				return strings.Replace(value, `"metadata.description"`, `"metadata..description"`, 1)
			},
			want: "invalid Source path",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseExclusionManifest([]byte(test.edit(string(raw))))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseExclusionManifest error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestExclusionManifestDoesNotTreatParentsAsLeaves(t *testing.T) {
	manifest, err := LoadExclusionManifest()
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Excludes("Source", "spec.location") {
		t.Fatal("parent exclusion unexpectedly covers future location descendants")
	}
	if manifest.Excludes("Model", "spec.fields.amount.aiContext") {
		t.Fatal("parent exclusion unexpectedly covers future AI context descendants")
	}
}
