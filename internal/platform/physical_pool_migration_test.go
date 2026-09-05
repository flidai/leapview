package platform

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhysicalPoolMigrationPersistsControlMetadataOnly(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open migrated store: %v", err)
	}
	defer store.Close()

	for _, table := range []string{"physical_pools", "physical_pool_admissions", "physical_catalog_bindings"} {
		assertTableCount(t, ctx, store, table, 1)
	}
	for _, forbidden := range []string{"table_membership_json", "file_membership_json", "secret_value", "credential_secret"} {
		var count int
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('physical_pools') WHERE name = ?`, forbidden).Scan(&count); err != nil {
			t.Fatalf("inspect forbidden column %s: %v", forbidden, err)
		}
		if count != 0 {
			t.Fatalf("forbidden column %s exists", forbidden)
		}
	}

	poolID := "sha256:" + strings.Repeat("a", 64)
	identityDigest := poolID
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pools (
		  id, identity_digest, storage_location, storage_namespace,
		  storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
		  retention_authority, retention_policy_json
		) VALUES ('pool-1', 'sha256:bad', 's3://invalid', 'tenant-a', 's3',
		  'uuidv7:v1', 'target-a', 'target-a', 'retention-a', '{}')`); err == nil {
		t.Fatal("non-canonical physical pool digest unexpectedly accepted")
	}
	_, err = store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pools (
		  id, identity_digest, storage_location, storage_namespace,
		  storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
		  retention_authority, retention_policy_json
		  ) VALUES (?, ?, 's3://warehouse', 'tenant-a', 's3',
		  'uuidv7:v1', 'target-a', 'target-a', 'target-a-retention', '{}')`, poolID, identityDigest)
	if err != nil {
		t.Fatalf("insert physical pool: %v", err)
	}
	conflictID := "sha256:" + strings.Repeat("e", 64)
	conflictDigest := conflictID
	_, err = store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pools (
		  id, identity_digest, storage_location, storage_namespace,
		  storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
		  retention_authority, retention_policy_json
		) VALUES (?, ?, 's3://warehouse', 'tenant-a', 's3',
		  'uuidv7:v1', 'target-b', 'target-b', 'independent-collector', '{}')`, conflictID, conflictDigest)
	if err != nil {
		// The same deletable namespace must remain globally owned, regardless
		// of target boundary, tenant, or retention authority.
		if !strings.Contains(err.Error(), "UNIQUE") {
			t.Fatalf("insert conflicting physical pool: %v", err)
		}
	} else {
		t.Fatal("independently governed collector namespace unexpectedly registered")
	}
	for _, candidate := range []struct {
		id, digest, tenant, isolation, authority string
	}{
		{"sha256:" + strings.Repeat("1", 64), "sha256:" + strings.Repeat("1", 64), "tenant-b", "target-a", "target-a-retention"},
		{"sha256:" + strings.Repeat("3", 64), "sha256:" + strings.Repeat("3", 64), "tenant-a", "target-b", "target-a-retention"},
		{"sha256:" + strings.Repeat("5", 64), "sha256:" + strings.Repeat("5", 64), "tenant-a", "target-a", "independent-collector"},
	} {
		if _, err := store.SQLDB().ExecContext(ctx, `
			INSERT INTO physical_pools (
			  id, identity_digest, storage_location, storage_namespace,
			  storage_implementation, object_naming_contract, encryption_domain, isolation_boundary,
			  tenant, retention_authority, retention_policy_json
			) VALUES (?, ?, 's3://warehouse', 'tenant-a', 's3',
			  'uuidv7:v1', ?, ?, ?, ?, '{}')`, candidate.id, candidate.digest, candidate.isolation, candidate.isolation, candidate.tenant, candidate.authority); err == nil {
			t.Fatalf("namespace variant tenant=%q isolation=%q authority=%q unexpectedly registered", candidate.tenant, candidate.isolation, candidate.authority)
		}
	}
	evidenceDigest := "sha256:" + strings.Repeat("c", 64)
	compatibilityDigest := "sha256:" + strings.Repeat("d", 64)
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pool_admissions (
		  pool_id, compatibility_json, evidence_json, evidence_digest, compatibility_digest,
		  conformance_version
		) VALUES (?, '{"duckdb_runtime":"duckdb:1.5.4"}',
		  '{"compatibility":{"duckdb_runtime":"duckdb:1.5.4"},"conformance_version":"lea-405/v1","checks":[{"id":"migration","passed":true}],"digest":"sha256:' || printf('%064d', 1) || '"}',
		  ?, ?, 'lea-405/v1')`, poolID, evidenceDigest, compatibilityDigest); err != nil {
		t.Fatalf("insert admission: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE physical_pool_admissions SET conformance_version = 'tampered' WHERE pool_id = ?`, poolID); err == nil {
		t.Fatal("physical pool admission update unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `DELETE FROM physical_pool_admissions WHERE pool_id = ?`, poolID); err == nil {
		t.Fatal("physical pool admission delete unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_pool_admissions (
		  pool_id, compatibility_json, evidence_json, evidence_digest, compatibility_digest,
		  conformance_version
		) VALUES (?, '{"duckdb_runtime":"duckdb:1.6.0"}',
		  '{"compatibility":{"duckdb_runtime":"duckdb:1.6.0"},"conformance_version":"lea-405/v2","checks":[{"id":"migration","passed":true}],"digest":"sha256:' || printf('%064d', 2) || '"}',
		  'sha256:' || printf('%064d', 2), 'sha256:' || printf('%064d', 3), 'lea-405/v2')`, poolID); err != nil {
		t.Fatalf("append upgraded admission: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_catalog_bindings (
		  id, physical_pool_id, catalog_digest, object_key, size_bytes,
		  compatibility_digest, catalog_format, evidence_digest, state
		) VALUES ('binding-unadmitted', ?, 'sha256:' || printf('%064d', 5),
		  'catalogs/sha256/unadmitted.ducklake', 1,
		  'sha256:' || printf('%064d', 6), 'ducklake-v1',
		  'sha256:' || printf('%064d', 7), 'working')`, poolID); err == nil {
		t.Fatal("catalog binding without matching admission unexpectedly succeeded")
	}
	_, err = store.SQLDB().ExecContext(ctx, `
		INSERT INTO physical_catalog_bindings (
		  id, physical_pool_id, catalog_digest, object_key, size_bytes,
		  compatibility_digest, catalog_format, evidence_digest, state, sealed_at
		) VALUES ('binding-1', ?, 'sha256:' || printf('%064d', 4),
		  'catalogs/sha256/catalog.ducklake', 1, ?, 'ducklake-v1',
		  ?, 'sealed', '2026-08-17T00:00:00Z')`, poolID, compatibilityDigest, evidenceDigest)
	if err != nil {
		t.Fatalf("insert physical catalog binding: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE physical_catalog_bindings SET physical_pool_id = ? WHERE id = 'binding-1'`, conflictID); err == nil {
		t.Fatal("sealed catalog binding update unexpectedly succeeded")
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE physical_pools SET storage_namespace = 'other' WHERE id = ?`, poolID); err == nil {
		t.Fatal("physical pool identity update unexpectedly succeeded")
	}
}
