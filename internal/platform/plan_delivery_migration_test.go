package platform

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlanDeliveryMigrationCreatesConstrainedControlState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()

	deliveryTables := []string{
		"delivery_target_revisions", "physical_pools", "delivery_plans",
		"delivery_writer_leases", "delivery_build_attempts", "delivery_catalog_seals",
		"delivery_candidates", "delivery_generations", "delivery_publications",
		"delivery_retention_exceptions", "delivery_query_leases", "delivery_gc_cycles", "delivery_gc_delete_intents",
	}
	for _, table := range deliveryTables {
		assertTableCount(t, ctx, store, table, 1)
	}

	// The control store may retain root and closure evidence, but never an
	// authoritative duplicate of DuckLake table/file membership.
	var tables string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT group_concat(name, ',') FROM sqlite_master WHERE type = 'table' AND name LIKE 'delivery_%'`).Scan(&tables); err != nil {
		t.Fatalf("inspect delivery tables: %v", err)
	}
	for _, forbidden := range []string{"file_membership", "table_membership", "reference_counts", "data_files", "delete_files"} {
		if strings.Contains(tables, forbidden) {
			t.Fatalf("forbidden authoritative membership table %q in %s", forbidden, tables)
		}
	}
	// DuckLake remains authoritative for file/table membership. The control
	// store may retain catalog roots and deletion intents, but never per-file
	// membership or reference-count columns of its own.
	for _, table := range deliveryTables {
		rows, err := store.SQLDB().QueryContext(ctx, "PRAGMA table_info('"+strings.ReplaceAll(table, "'", "''")+"')")
		if err != nil {
			t.Fatalf("inspect columns for %s: %v", table, err)
		}
		for rows.Next() {
			var cid, notNull, primaryKey int
			var name, columnType string
			var defaultValue sql.NullString
			if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
				rows.Close()
				t.Fatalf("scan columns for %s: %v", table, err)
			}
			normalized := strings.ToLower(name)
			for _, forbidden := range []string{
				"file_membership", "table_membership", "reference_count", "reference_counts",
				"refcount", "ref_count", "file_id", "file_ids", "data_file", "data_files",
				"delete_file", "delete_files",
			} {
				if strings.Contains(normalized, forbidden) {
					rows.Close()
					t.Fatalf("forbidden authoritative membership column %q on %s", name, table)
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate columns for %s: %v", table, err)
		}
		rows.Close()
	}

	sha := "sha256:" + strings.Repeat("a", 64)
	poolID := sha
	qualificationDigest := "sha256:" + strings.Repeat("e", 64)
	provenanceJSON := `{"repository":"https://example.invalid/repo","sourceRevision":"rev-1","builder":"ci"}`
	governanceJSON := `{"policyDigest":"` + sha + `","authorizationDigest":"` + sha + `","qualificationDigest":"` + qualificationDigest + `","expiresAt":"2026-08-18T00:00:00Z","requiresApproval":false,"observedInputsAllowed":false}`
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_target_revisions (target_id, project_id, environment, created_at, updated_at)
		VALUES ('target-1', 'project-1', 'prod', '2026-08-17T00:00:00Z', '2026-08-17T00:00:00Z')`); err != nil {
		t.Fatalf("insert target revision: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pools (
		  id, identity_digest, storage_location, storage_namespace,
		  storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
		  retention_authority, retention_policy_json
		) VALUES (?, ?, 's3://warehouse', 'tenant-a', 's3',
		  'sha256-object-names-v1', 'target-1', 'target-1', 'retention-1', '{}')`, poolID, sha); err != nil {
		t.Fatalf("insert physical pool: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_plans (
		  id, target_id, project_id, environment, operation_kind, source_digest,
		  base_target_revision, execution_digest, execution_inputs_json,
		  provenance_json, governance_json, provenance_digest, governance_digest, plan_digest, expires_at, created_at
		) VALUES ('plan-1', 'target-1', 'project-1', 'prod', 'code_change', ?, 0, ?, '{}', ?, ?, ?, ?, ?,
		          '2026-08-18T00:00:00Z', '2026-08-17T00:00:00Z')`, sha, sha, provenanceJSON, governanceJSON, sha, sha, sha); err != nil {
		t.Fatalf("insert delivery plan: %v", err)
	}
	var provenanceType, governanceType string
	if err := store.SQLDB().QueryRowContext(ctx, `
		SELECT json_type(provenance_json), json_type(governance_json)
		FROM delivery_plans WHERE id = 'plan-1'`).Scan(&provenanceType, &governanceType); err != nil {
		t.Fatalf("read canonical plan evidence: %v", err)
	}
	if provenanceType != "object" || governanceType != "object" {
		t.Fatalf("canonical plan evidence types = %q/%q, want object/object", provenanceType, governanceType)
	}
	for column, invalid := range map[string]string{"provenance_json": "[]", "governance_json": "null"} {
		if _, err := store.SQLDB().ExecContext(ctx, `UPDATE delivery_plans SET `+column+` = ? WHERE id = 'plan-1'`, invalid); err == nil {
			t.Fatalf("non-object %s unexpectedly succeeded", column)
		}
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_plans (
		  id, target_id, project_id, environment, operation_kind, source_digest,
		  base_target_revision, execution_digest, execution_inputs_json,
		  provenance_json, governance_json, provenance_digest, governance_digest, plan_digest, expires_at, created_at
		) VALUES ('plan-cross-scope', 'target-1', 'other-project', 'prod', 'code_change', ?, 0, ?, '{}', ?, ?, ?, ?, ?,
		          '2026-08-18T00:00:00Z', '2026-08-17T00:00:00Z')`, sha, sha, provenanceJSON, governanceJSON, sha, sha, sha); err == nil {
		t.Fatal("plan with target/project scope mismatch unexpectedly succeeded")
	}
	catalogDigest := "sha256:" + strings.Repeat("b", 64)
	compatibilityDigest := "sha256:" + strings.Repeat("c", 64)
	closureDigest := "sha256:" + strings.Repeat("d", 64)
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_writer_leases (id, attempt_id, physical_pool_id, owner_id, epoch, expires_at, created_at)
		VALUES ('writer-1', 'attempt-1', ?, 'worker-1', 1, '2026-08-18T00:00:00Z', '2026-08-17T00:00:00Z')`, poolID); err != nil {
		t.Fatalf("insert writer lease: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_build_attempts (
		  id, plan_id, plan_digest, source_digest, execution_digest, physical_pool_id,
		  writer_lease_id, status, revision, created_at, updated_at
		) VALUES ('attempt-1', 'plan-1', ?, ?, ?, ?, 'writer-1', 'building', 1,
		          '2026-08-17T00:00:00Z', '2026-08-17T00:00:00Z')`, sha, sha, sha, poolID); err != nil {
		t.Fatalf("insert build attempt: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_catalog_seals (
		  id, attempt_id, plan_id, plan_digest, execution_digest, physical_pool_id,
		  catalog_digest, compatibility_digest, object_key, object_size,
		  closure_digest, qualification_digest, serving_artifact_id, serving_artifact_digest,
		  status, created_at, verified_at
		) VALUES ('seal-1', 'attempt-1', 'plan-1', ?, ?, ?, ?, ?,
		          'catalogs/catalog.ducklake', 1, ?, ?, 'artifact-1', ?, 'verified',
		          '2026-08-17T00:00:00Z', '2026-08-17T00:01:00Z')`, sha, sha, poolID, catalogDigest, compatibilityDigest, closureDigest, qualificationDigest, sha); err != nil {
		t.Fatalf("insert catalog seal: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE delivery_catalog_seals
		SET base_catalog_digest = ?, base_physical_pool_id = ?
		WHERE id = 'seal-1'`, compatibilityDigest, poolID); err == nil {
		t.Fatal("catalog seal with a base that differs from its build attempt unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_candidates (
		  id, plan_id, plan_digest, target_id, project_id, environment, source_digest,
		  execution_digest, base_target_revision, seal_id, catalog_digest,
		  compatibility_digest, catalog_object_key, physical_pool_id,
		  serving_artifact_id, serving_artifact_digest, serving_state_id, qualification_digest, status, created_at, ready_at
		) VALUES ('candidate-1', 'plan-1', ?, 'target-1', 'project-1', 'prod', ?, ?, 0,
		          'seal-1', ?, ?, 'catalogs/catalog.ducklake', ?, 'artifact-1', ?, 'state-1', ?, 'ready',
		          '2026-08-17T00:00:00Z', '2026-08-17T00:01:00Z')`, sha, sha, sha, catalogDigest, compatibilityDigest, poolID, sha, qualificationDigest); err != nil {
		t.Fatalf("insert ready candidate: %v", err)
	}
	for _, candidate := range []struct {
		id        string
		status    string
		retiredAt any
	}{
		{id: "candidate-unbound-ready", status: "ready"},
		{id: "candidate-unbound-retired", status: "retired", retiredAt: "2026-08-17T00:02:00Z"},
	} {
		if _, err := store.SQLDB().ExecContext(ctx, `
			INSERT INTO delivery_candidates (
			  id, plan_id, plan_digest, target_id, project_id, environment, source_digest,
			  execution_digest, base_target_revision, seal_id, catalog_digest,
			  compatibility_digest, catalog_object_key, physical_pool_id,
			  serving_artifact_id, serving_artifact_digest, qualification_digest, status, created_at, ready_at, retired_at
			) VALUES (?, 'plan-1', ?, 'target-1', 'project-1', 'prod', ?, ?, 0,
			          'seal-1', ?, ?, 'catalogs/catalog.ducklake', ?, 'artifact-1', ?, ?, ?,
			          '2026-08-17T00:00:00Z', '2026-08-17T00:01:00Z', ?)`, candidate.id, sha, sha, sha, catalogDigest, compatibilityDigest, poolID, sha, qualificationDigest, candidate.status, candidate.retiredAt); err == nil || !strings.Contains(err.Error(), "ready candidate requires serving state identity") {
			t.Fatalf("%s candidate without serving identity error = %v", candidate.status, err)
		}
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE delivery_candidates
		SET catalog_object_key = 'catalogs/other.ducklake'
		WHERE id = 'candidate-1'`); err == nil {
		t.Fatal("candidate with an artifact key different from its catalog seal unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE delivery_catalog_seals SET serving_artifact_id = '' WHERE id = 'seal-1'`); err == nil {
		t.Fatal("catalog seal serving identity could be cleared after migration")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE delivery_candidates SET serving_artifact_digest = '' WHERE id = 'candidate-1'`); err == nil {
		t.Fatal("ready candidate serving identity could be cleared after migration")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE delivery_candidates
		SET execution_digest = ?
		WHERE id = 'candidate-1'`, compatibilityDigest); err == nil {
		t.Fatal("candidate with execution inputs different from its plan unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE delivery_build_attempts SET status = 'sealed', seal_id = 'seal-1', candidate_id = 'candidate-1',
		  terminal_at = '2026-08-17T00:01:00Z', updated_at = '2026-08-17T00:01:00Z', revision = 2
		WHERE id = 'attempt-1'`); err != nil {
		t.Fatalf("seal build attempt: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_generations (
		  id, candidate_id, plan_id, plan_digest, target_id, project_id, environment,
		  catalog_digest, catalog_object_key, physical_pool_id, serving_artifact_id, serving_artifact_digest, serving_state_id, rollback_class, status, created_at
		) VALUES ('generation-1', 'candidate-1', 'plan-1', ?, 'target-1', 'project-1', 'prod',
		          ?, 'catalogs/catalog.ducklake', ?, 'artifact-1', ?, 'state-1', 'rollback_safe', 'prepared', '2026-08-17T00:02:00Z')`, sha, catalogDigest, poolID, sha); err != nil {
		t.Fatalf("insert prepared generation: %v", err)
	}
	for _, status := range []string{"prepared", "active", "retired"} {
		if _, err := store.SQLDB().ExecContext(ctx, `
			INSERT INTO delivery_generations (
			  id, candidate_id, plan_id, plan_digest, target_id, project_id, environment,
			  catalog_digest, catalog_object_key, physical_pool_id, serving_artifact_id,
			  serving_artifact_digest, rollback_class, status, created_at, activated_at, retired_at
			) VALUES (?, 'candidate-1', 'plan-1', ?, 'target-1', 'project-1', 'prod',
			          ?, 'catalogs/catalog.ducklake', ?, 'artifact-1', ?, 'rollback_safe', ?,
			          '2026-08-17T00:02:00Z', '2026-08-17T00:02:00Z', '2026-08-17T00:03:00Z')`, "generation-unbound-"+status, sha, catalogDigest, poolID, sha, status); err == nil || !strings.Contains(err.Error(), "generation requires serving state identity") {
			t.Fatalf("%s generation without serving identity error = %v", status, err)
		}
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_publications (
		  id, request_digest, target_id, project_id, environment, plan_id, plan_digest,
		  candidate_id, generation_id, expected_target_revision, status, created_at
		) VALUES ('publication-1', ?, 'target-1', 'project-1', 'prod', 'plan-1', ?,
		          'candidate-1', 'generation-1', 0, 'pending', '2026-08-17T00:03:00Z')`, sha, sha); err != nil {
		t.Fatalf("insert pending publication: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_query_leases (
		  id, holder_id, generation_id, catalog_digest, physical_pool_id, expires_at, created_at
		) VALUES ('query-1', 'reader-1', 'generation-1', ?, ?, '2026-08-18T00:00:00Z', '2026-08-17T00:04:00Z')`, catalogDigest, poolID); err != nil {
		t.Fatalf("insert exact generation lease: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_query_leases (
			id, holder_id, catalog_digest, physical_pool_id, expires_at, created_at
		) VALUES ('lease-1', 'worker-1', ?, ?, '2026-08-18T00:00:00Z', '2026-08-17T00:00:00Z')`, sha, poolID); err == nil {
		t.Fatal("query lease without candidate or generation unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_retention_exceptions (
		  id, physical_pool_id, catalog_digest, reason, expires_at, created_at
		) VALUES ('retention-1', ?, ?, 'incident hold', '2026-08-19T00:00:00Z', '2026-08-17T00:00:00Z')`, poolID, sha); err == nil {
		t.Fatal("retention exception without exactly one root unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_gc_cycles (
		  id, physical_pool_id, epoch, root_revision, status, created_at
		) VALUES ('gc-1', ?, 1, 1, 'marked', '2026-08-17T00:00:00Z')`, poolID); err == nil {
		t.Fatal("marked GC cycle without mark digest unexpectedly succeeded")
	}
}

func TestDeliveryBuildAttemptBaseGenerationAllowsFullRefresh(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "full-refresh.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()

	sha := "sha256:" + strings.Repeat("a", 64)
	baseCatalog := "sha256:" + strings.Repeat("b", 64)
	poolID := sha
	const created = "2026-08-17T00:00:00Z"
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_target_revisions (target_id, project_id, environment, created_at, updated_at)
		VALUES ('target-full-refresh', 'project-full-refresh', 'prod', ?, ?)`, created, created); err != nil {
		t.Fatalf("insert target revision: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pools (
		  id, identity_digest, storage_location, storage_namespace,
		  storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
		  retention_authority, retention_policy_json
		) VALUES (?, ?, 's3://warehouse', 'full-refresh', 's3',
		  'sha256-object-names-v1', 'target-full-refresh', 'target-full-refresh', 'retention-full-refresh', '{}')`, poolID, sha); err != nil {
		t.Fatalf("insert physical pool: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_plans (
		  id, target_id, project_id, environment, operation_kind, source_digest,
		  base_target_revision, execution_digest, execution_inputs_json,
		  provenance_json, governance_json, provenance_digest, governance_digest,
		  plan_digest, expires_at, created_at
		) VALUES ('plan-full-refresh', 'target-full-refresh', 'project-full-refresh', 'prod',
		  'code_change', ?, 0, ?, '{}', '{}', '{}', ?, ?, ?, '2026-08-18T00:00:00Z', ?)`, sha, sha, sha, sha, sha, created); err != nil {
		t.Fatalf("insert delivery plan: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_writer_leases (id, attempt_id, physical_pool_id, owner_id, epoch, expires_at, created_at)
		VALUES ('writer-full-refresh', 'attempt-full-refresh', ?, 'worker-full-refresh', 1, '2026-08-18T00:00:00Z', ?)`, poolID, created); err != nil {
		t.Fatalf("insert full-refresh writer lease: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_build_attempts (
		  id, plan_id, plan_digest, source_digest, execution_digest,
		  base_generation_id, physical_pool_id, writer_lease_id, status,
		  revision, created_at, updated_at
		) VALUES ('attempt-full-refresh', 'plan-full-refresh', ?, ?, ?,
		          'generation-full-refresh', ?, 'writer-full-refresh', 'building', 1, ?, ?)`, sha, sha, sha, poolID, created, created); err != nil {
		t.Fatalf("full-refresh attempt with an unfurnished base pair: %v", err)
	}
	var generation string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT base_generation_id FROM delivery_build_attempts WHERE id = 'attempt-full-refresh'`).Scan(&generation); err != nil {
		t.Fatalf("read full-refresh attempt: %v", err)
	}
	if generation != "generation-full-refresh" {
		t.Fatalf("base generation = %q, want generation-full-refresh", generation)
	}
	var roots int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT root_revision FROM delivery_pool_fences WHERE physical_pool_id = ?`, poolID).Scan(&roots); err != nil {
		t.Fatalf("read pool root revision after full-refresh attempt: %v", err)
	}
	if roots != 1 {
		t.Fatalf("pool root revision = %d, want 1 (rebuilt attempt trigger must remain attached)", roots)
	}

	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_writer_leases (id, attempt_id, physical_pool_id, owner_id, epoch, expires_at, created_at)
		VALUES ('writer-retained', 'attempt-retained', ?, 'worker-retained', 2, '2026-08-18T00:00:00Z', ?)`, poolID, created); err != nil {
		t.Fatalf("insert retained writer lease: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO delivery_build_attempts (
		  id, plan_id, plan_digest, source_digest, execution_digest,
		  base_generation_id, base_catalog_digest, base_physical_pool_id,
		  physical_pool_id, writer_lease_id, status, revision, created_at, updated_at
		) VALUES ('attempt-retained', 'plan-full-refresh', ?, ?, ?,
		          'generation-retained', ?, ?, ?, 'writer-retained', 'building', 1, ?, ?)`, sha, sha, sha, baseCatalog, poolID, poolID, created, created); err != nil {
		t.Fatalf("valid retained-base attempt: %v", err)
	}
	for i, test := range []struct {
		name     string
		catalog  any
		basePool any
	}{
		{name: "catalog without pool", catalog: baseCatalog},
		{name: "pool without catalog", basePool: poolID},
		{name: "malformed catalog", catalog: "not-a-digest", basePool: poolID},
		{name: "base pool mismatch", catalog: baseCatalog, basePool: "sha256:" + strings.Repeat("c", 64)},
	} {
		t.Run(test.name, func(t *testing.T) {
			attemptID := "attempt-invalid-" + strings.ReplaceAll(test.name, " ", "-")
			writerID := "writer-invalid-" + strings.ReplaceAll(test.name, " ", "-")
			if _, err := store.SQLDB().ExecContext(ctx, `
				INSERT INTO delivery_writer_leases (id, attempt_id, physical_pool_id, owner_id, epoch, expires_at, created_at)
				VALUES (?, ?, ?, ?, ?, '2026-08-18T00:00:00Z', ?)`, writerID, attemptID, poolID, writerID, 10+i, created); err != nil {
				t.Fatalf("insert invalid-case writer lease: %v", err)
			}
			_, err := store.SQLDB().ExecContext(ctx, `
				INSERT INTO delivery_build_attempts (
				  id, plan_id, plan_digest, source_digest, execution_digest,
				  base_generation_id, base_catalog_digest, base_physical_pool_id,
				  physical_pool_id, writer_lease_id, status, revision, created_at, updated_at
				) VALUES (?, 'plan-full-refresh', ?, ?, ?, 'generation-invalid', ?, ?, ?, ?, 'building', 1, ?, ?)`, attemptID, sha, sha, sha, test.catalog, test.basePool, poolID, writerID, created, created)
			if err == nil {
				t.Fatalf("invalid retained-base pair unexpectedly succeeded")
			}
		})
	}
}

func TestDeliveryGenerationServingStateIdentityRejectsScopedDuplicates(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "identity.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var indexSQL string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='index' AND name='delivery_generations_serving_state_identity_uq'`).Scan(&indexSQL); err != nil {
		t.Fatalf("serving identity unique index missing: %v", err)
	}
	if !strings.Contains(indexSQL, "WHERE serving_state_id <> ''") {
		t.Fatalf("serving identity index is not partial: %s", indexSQL)
	}
	conn, err := store.SQLDB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, `PRAGMA foreign_keys = OFF`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO delivery_generations (
	 id, candidate_id, plan_id, plan_digest, target_id, project_id, environment,
	 catalog_digest, catalog_object_key, physical_pool_id, serving_artifact_id, serving_artifact_digest, rollback_class, status, serving_state_id, created_at
	 ) VALUES (?, 'candidate-identity', 'plan-identity', ?, ?, ?, ?, ?, 'catalogs/identity.ducklake', 'pool-identity', 'artifact-identity', ?, 'rollback_safe', 'prepared', 'state-identity', '2026-08-17T00:00:00Z')`
	sha := "sha256:" + strings.Repeat("a", 64)
	if _, err := conn.ExecContext(ctx, insert, "generation-identity-1", sha, "target-identity-1", "project-identity-1", "prod", sha, sha); err != nil {
		t.Fatalf("insert first serving generation: %v", err)
	}
	if _, err := conn.ExecContext(ctx, insert, "generation-identity-2", sha, "target-identity-2", "project-identity-2", "prod", sha, sha); err == nil {
		t.Fatal("duplicate scoped serving-state generation unexpectedly succeeded")
	}
}

// A restart is part of the migration contract: migrations must be durable and
// re-openable without creating a second legacy serving path.  This test uses a
// file-backed store so the second Open exercises the same WAL/recovery boundary
// as a process restart rather than a fresh in-memory schema.
func TestPlanDeliveryMigrationRestartPreservesCanonicalServingPathAndGuards(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "restart.db")
	first, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("first open migrated store: %v", err)
	}
	var canonical, legacy int
	if err := first.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('serving_states','project_active_serving_states')`).Scan(&canonical); err != nil {
		t.Fatalf("inspect canonical serving tables: %v", err)
	}
	if err := first.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('deployments','workspace_active_deployments','deployment_artifacts')`).Scan(&legacy); err != nil {
		t.Fatalf("inspect legacy serving tables: %v", err)
	}
	if canonical != 2 || legacy != 0 {
		t.Fatalf("serving schema before restart canonical=%d legacy=%d, want 2/0", canonical, legacy)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first store: %v", err)
	}

	second, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen migrated store: %v", err)
	}
	defer second.Close()
	if err := second.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='trigger' AND name IN ('delivery_events_append_only_update','delivery_events_append_only_delete','delivery_candidates_serving_state_identity_guard_update','delivery_candidates_serving_state_identity_guard_insert','delivery_generations_serving_state_identity_guard_update','delivery_generations_serving_state_identity_guard_insert')`).Scan(&canonical); err != nil {
		t.Fatalf("inspect durable delivery triggers: %v", err)
	}
	if canonical != 6 {
		t.Fatalf("durable delivery trigger count=%d, want 6", canonical)
	}
	if err := second.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('serving_states','project_active_serving_states')`).Scan(&canonical); err != nil {
		t.Fatalf("inspect canonical serving tables after restart: %v", err)
	}
	if err := second.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name IN ('deployments','workspace_active_deployments','deployment_artifacts')`).Scan(&legacy); err != nil {
		t.Fatalf("inspect legacy serving tables after restart: %v", err)
	}
	if canonical != 2 || legacy != 0 {
		t.Fatalf("serving schema after restart canonical=%d legacy=%d, want 2/0", canonical, legacy)
	}
}
