package postgresbaseline

import (
	"strings"
	"testing"
)

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

func TestProductRolePolicyGrantsNativeDeliveryIdentityReads(t *testing.T) {
	const required = "GRANT SELECT ON ducklake.catalog_identity TO leapview_control_runtime"
	if !strings.Contains(rolePolicySQL, required) {
		t.Fatalf("native delivery role policy is missing exact identity read grant %q", required)
	}
	if strings.Contains(rolePolicySQL, "generation_binding") {
		t.Fatal("native delivery role policy references removed DuckLake generation binding authority")
	}
	for _, forbidden := range []string{
		"GRANT SELECT ON ALL TABLES IN SCHEMA ducklake TO leapview_control_runtime",
		"GRANT SELECT, INSERT, UPDATE ON ALL TABLES IN SCHEMA ducklake TO leapview_control_runtime",
	} {
		if strings.Contains(rolePolicySQL, forbidden) {
			t.Fatalf("native delivery role policy broadens DuckLake identity access through %q", forbidden)
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
		"ON audit.audit_retention_floor, audit.query_event_retention_floor",
		"FROM leapview_control_runtime",
		"GRANT EXECUTE ON FUNCTION event.prune_event_log(timestamptz, integer) TO leapview_control_maintenance",
		"GRANT EXECUTE ON FUNCTION jobs.prune(timestamptz, integer) TO leapview_control_maintenance",
		"REVOKE ALL ON FUNCTION delivery.lock_live_snapshot_retention(uuid) FROM PUBLIC",
		"GRANT EXECUTE ON FUNCTION delivery.lock_live_snapshot_retention(uuid) TO leapview_control_runtime",
		"GRANT EXECUTE ON FUNCTION delivery.lock_live_snapshot_retention(uuid) TO leapview_control_maintenance",
		"REVOKE EXECUTE ON FUNCTION delivery.lock_live_snapshot_retention(uuid) FROM leapview_control_readonly",
		"REVOKE EXECUTE ON FUNCTION delivery.lock_live_snapshot_retention(uuid) FROM leapview_control_backup",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("role policy is missing maintenance boundary %q", required)
		}
	}
}

func TestProductRolePolicyKeepsRecoveryFrontierFenced(t *testing.T) {
	for _, required := range []string{
		"GRANT SELECT ON ALL TABLES IN SCHEMA recovery TO leapview_control_runtime",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON ALL TABLES IN SCHEMA recovery FROM leapview_control_runtime",
		"GRANT SELECT, INSERT, UPDATE ON recovery.recovery_set, recovery.validation_attempt TO leapview_control_maintenance",
		"REVOKE UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON recovery.recovery_cluster_point, recovery.recovery_object_root, recovery.validation_result FROM leapview_control_maintenance",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("role policy is missing recovery frontier boundary %q", required)
		}
	}
}

func TestProductRolePolicyNamesDashboardTablesExplicitly(t *testing.T) {
	if strings.Contains(rolePolicySQL, "ALL TABLES IN SCHEMA dashboard") {
		t.Fatal("dashboard role policy must not broaden future tables through ALL TABLES")
	}
	for _, required := range []string{
		"dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_runtime",
		"dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_readonly",
		"dashboard.view_session, dashboard.view_day, dashboard.appearance_override TO leapview_control_backup",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("dashboard role policy is missing explicit grant %q", required)
		}
	}
}

func TestProductRolePolicyDoesNotBroadenDashboardProjectionMutation(t *testing.T) {
	for _, forbidden := range []string{
		"GRANT SELECT, INSERT, UPDATE ON dashboard.authoring_dashboards",
		"GRANT SELECT, INSERT, UPDATE ON dashboard.authoring_drafts",
		"GRANT SELECT, INSERT, UPDATE ON dashboard.authoring_published",
		"GRANT SELECT, INSERT ON dashboard.authoring_revisions",
		"GRANT SELECT, INSERT ON dashboard.publication_events",
		"GRANT SELECT, INSERT, UPDATE ON dashboard.publications",
		"GRANT SELECT, INSERT, UPDATE ON dashboard.publication_streams",
		"GRANT SELECT, DELETE ON dashboard.publication_streams",
	} {
		if strings.Contains(rolePolicySQL, forbidden) {
			t.Fatalf("dashboard role policy broadens component column grants through %q", forbidden)
		}
	}
	for _, required := range []string{
		"GRANT SELECT ON dashboard.authoring_dashboards, dashboard.authoring_revisions",
		"dashboard.publication_events, dashboard.publication_streams TO leapview_control_runtime",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.authoring_dashboards",
		"dashboard.publication_events, dashboard.publication_streams FROM leapview_control_runtime",
		"REVOKE INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES, TRIGGER ON dashboard.publication_streams FROM leapview_control_maintenance",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("dashboard role policy is missing guarded-mutation boundary %q", required)
		}
	}
}
