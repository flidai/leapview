package postgresbaseline

import (
	"strings"
	"testing"
)

func TestProductBaselineComponentOrder(t *testing.T) {
	want := []string{"platform.operation", "platform.cursor_signing", "project", "access", "managed_data", "deployment", "event", "ducklake", "jobs", "refresh", "lineage", "cache", "queryaudit"}
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

func TestProductRolePolicyDoesNotRestoreDuckLakeMigrationAuthority(t *testing.T) {
	for _, forbidden := range []string{
		"ALL TABLES IN SCHEMA delivery, ducklake",
		"ALL TABLES IN SCHEMA ducklake TO leapview_control_runtime",
		"ALL TABLES IN SCHEMA delivery, jobs, cache",
		"ALL TABLES IN SCHEMA cache TO leapview_control_runtime",
	} {
		if strings.Contains(rolePolicySQL, forbidden) {
			t.Fatalf("role policy restores DuckLake runtime authority through %q", forbidden)
		}
	}
	for _, required := range []string{
		"REVOKE INSERT, UPDATE, DELETE ON ducklake.catalog_runtime_compatibility",
		"ducklake.snapshot_requalification FROM leapview_control_runtime",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("role policy is missing DuckLake upgrade boundary %q", required)
		}
	}
}

func TestProductRolePolicyKeepsRetentionOutOfRuntime(t *testing.T) {
	for _, forbidden := range []string{
		"GRANT EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) TO leapview_control_runtime",
		"GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_runtime",
	} {
		if strings.Contains(rolePolicySQL, forbidden) {
			t.Fatalf("role policy restores runtime retention authority through %q", forbidden)
		}
	}
	for _, required := range []string{
		"REVOKE EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) FROM leapview_control_runtime",
		"REVOKE EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) FROM leapview_control_runtime",
		"GRANT EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) TO leapview_control_maintenance",
		"GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_maintenance",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("role policy is missing maintenance boundary %q", required)
		}
	}
}
