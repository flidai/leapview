package postgresbaseline

import "testing"

func TestProductBaselineComponentOrder(t *testing.T) {
	want := []string{"platform.operation", "platform.cursor_signing", "project", "access", "deployment", "event", "ducklake", "jobs", "lineage", "cache", "queryaudit"}
	components := Components()
	if len(components) != len(want) {
		t.Fatalf("component count = %d, want %d", len(components), len(want))
	}
	for index, component := range components {
		if component.Name != want[index] || component.SQL == "" {
			t.Fatalf("component[%d] = %#v, want name %q with SQL", index, component, want[index])
		}
	}
	if Checksum() == "" {
		t.Fatal("baseline checksum is empty")
	}
}
