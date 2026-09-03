package postgresbaseline

import (
	"context"
	"errors"
	"strings"
	"testing"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
)

// revisionReader models the authoritative platform.schema_revision table.
// Keeping revisions keyed by number ensures Verify checks every immutable
// baseline and forward-migration identity rather than only the foundation.
type revisionReader struct {
	revisions map[int64]platformpostgres.SchemaRevision
	err       error
}

func (r revisionReader) SchemaRevision(_ context.Context, revision int64) (platformpostgres.SchemaRevision, error) {
	if r.err != nil {
		return platformpostgres.SchemaRevision{}, r.err
	}
	value, ok := r.revisions[revision]
	if !ok {
		return platformpostgres.SchemaRevision{}, errors.New("revision not found")
	}
	return value, nil
}

func TestVerifyRequiresExactBaselineAndMigrationIdentity(t *testing.T) {
	revisions := map[int64]platformpostgres.SchemaRevision{
		BaselineRevision: {Revision: BaselineRevision, MigrationID: BaselineMigrationID, Checksum: Checksum()},
	}
	for _, migration := range Migrations() {
		revisions[migration.Revision] = platformpostgres.SchemaRevision{
			Revision: migration.Revision, MigrationID: migration.MigrationID, Checksum: migration.Checksum(),
		}
	}
	if err := Verify(t.Context(), revisionReader{revisions: revisions}); err != nil {
		t.Fatalf("Verify() error = %v", err)
	}

	tests := []struct {
		name   string
		mutate func(map[int64]platformpostgres.SchemaRevision)
	}{
		{name: "baseline revision", mutate: func(values map[int64]platformpostgres.SchemaRevision) {
			value := values[BaselineRevision]
			value.Revision++
			values[BaselineRevision] = value
		}},
		{name: "baseline migration", mutate: func(values map[int64]platformpostgres.SchemaRevision) {
			value := values[BaselineRevision]
			value.MigrationID = "tampered"
			values[BaselineRevision] = value
		}},
		{name: "baseline checksum", mutate: func(values map[int64]platformpostgres.SchemaRevision) {
			value := values[BaselineRevision]
			value.Checksum = "tampered"
			values[BaselineRevision] = value
		}},
		{name: "forward migration checksum", mutate: func(values map[int64]platformpostgres.SchemaRevision) {
			for _, migration := range Migrations() {
				value := values[migration.Revision]
				value.Checksum = strings.Repeat("f", 64)
				values[migration.Revision] = value
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := make(map[int64]platformpostgres.SchemaRevision, len(revisions))
			for key, value := range revisions {
				copy[key] = value
			}
			test.mutate(copy)
			if err := Verify(t.Context(), revisionReader{revisions: copy}); err == nil {
				t.Fatal("Verify() accepted mismatched schema identity")
			}
		})
	}
	if err := Verify(t.Context(), revisionReader{err: errors.New("connection failed")}); err == nil {
		t.Fatal("Verify() accepted a revision reader error")
	}
	if err := Verify(t.Context(), nil); err == nil {
		t.Fatal("Verify(nil) unexpectedly succeeded")
	}
}

func TestProductBaselineComponentOrder(t *testing.T) {
	want := []string{"platform.bootstrap", "platform.operation", "platform.cursor_signing", "project", "access", "admin.product", "dashboard.session", "dashboard.usage", "dashboard.appearance", "dashboard.authoring", "dashboard.publication", "connection_binding", "event", "managed_data", "physical_pool", "deployment", "serving_state", "release", "ducklake", "jobs", "agent", "refresh", "recoveryset", "lineage", "cache", "queryaudit"}
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
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("role policy is missing maintenance boundary %q", required)
		}
	}
}

func TestProductRolePolicyRepairsL3CapabilitySplit(t *testing.T) {
	for _, required := range []string{
		"REVOKE ALL ON cache.cache_l3_object_fence, cache.cache_l3_gc_state FROM leapview_control_runtime",
		"cache.prepare_l3_object_gc(uuid,text,text,text,bigint) FROM leapview_control_runtime",
		"cache.acquire_l3_object_fence(uuid,text,text,text,interval)",
		"cache.admit_manifest(uuid,uuid,text,text,bigint",
		"REVOKE ALL ON cache.cache_l3_object_fence, cache.cache_l3_gc_state FROM leapview_control_maintenance",
		"cache.prepare_l3_object_gc(uuid,text,text,text,bigint) TO leapview_control_maintenance",
		"GRANT SELECT ON cache.cache_l3_object_fence, cache.cache_l3_gc_state TO leapview_control_readonly",
		"GRANT SELECT ON cache.cache_l3_object_fence, cache.cache_l3_gc_state TO leapview_control_backup",
	} {
		if !strings.Contains(rolePolicySQL, required) {
			t.Fatalf("role policy is missing L3 capability repair %q", required)
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
