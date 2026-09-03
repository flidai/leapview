package migrations

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestBaselinePostgreSQL18 applies the clean baseline to a real PostgreSQL 18
// server.  CI can make container availability mandatory with
// LEAPVIEW_POSTGRES_CONFORMANCE_REQUIRED=1; local runs skip when Docker is not
// available, matching the existing platform PostgreSQL conformance tests.
func TestBaselinePostgreSQL18(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "leapview_control")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("apply baseline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	// Re-running a clean baseline is safe for retries; the recorded checksum is
	// verified rather than replaced.
	tx, err = conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := Apply(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("reapply baseline: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	var schemaCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.schemata
		WHERE schema_name = ANY($1::text[])`, []string{"access", "delivery", "refresh", "event", "audit", "lineage", "cache", "agent"}).Scan(&schemaCount); err != nil {
		t.Fatal(err)
	}
	if schemaCount != 8 {
		t.Fatalf("capability schema count = %d, want 8", schemaCount)
	}

	var revision int64
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, BaselineMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != BaselineRevision {
		t.Fatalf("schema revision = %d, want %d", revision, BaselineRevision)
	}
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, IdentityLedgerMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != IdentityLedgerRevision {
		t.Fatalf("identity ledger schema revision = %d, want %d", revision, IdentityLedgerRevision)
	}
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, ContractPublicationMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != ContractPublicationRevision {
		t.Fatalf("contract publication schema revision = %d, want %d", revision, ContractPublicationRevision)
	}
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, ActivationTransitionJournalMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != ActivationTransitionJournalRevision {
		t.Fatalf("activation transition schema revision = %d, want %d", revision, ActivationTransitionJournalRevision)
	}
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, ActivationTransitionReferencesMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != ActivationTransitionReferencesRevision {
		t.Fatalf("activation transition references schema revision = %d, want %d", revision, ActivationTransitionReferencesRevision)
	}
	if err := db.QueryRow(ctx, `SELECT revision FROM platform.schema_revision WHERE migration_id = $1`, IdentityRestoreTransitionMigrationID).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != IdentityRestoreTransitionRevision {
		t.Fatalf("identity restore transition schema revision = %d, want %d", revision, IdentityRestoreTransitionRevision)
	}
	var nullable string
	if err := db.QueryRow(ctx, `
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'project'
		  AND table_name = 'identity_activation_transition'
		  AND column_name = 'durable_references_json'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" {
		t.Fatalf("durable reference evidence nullability = %q, want NO", nullable)
	}
	var durableCheckCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = 'project.identity_activation_transition'::regclass
		  AND conname = 'identity_activation_transition_durable_references_json_check'`).Scan(&durableCheckCount); err != nil {
		t.Fatal(err)
	}
	if durableCheckCount != 1 {
		t.Fatalf("durable reference evidence check constraints = %d, want 1", durableCheckCount)
	}
	if err := db.QueryRow(ctx, `
		SELECT is_nullable
		FROM information_schema.columns
		WHERE table_schema = 'project'
		  AND table_name = 'identity_activation_transition'
		  AND column_name = 'approved_authored_ids_json'`).Scan(&nullable); err != nil {
		t.Fatal(err)
	}
	if nullable != "NO" {
		t.Fatalf("approved restore evidence nullability = %q, want NO", nullable)
	}
	var operationCheckDefinition string
	if err := db.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conrelid = 'project.identity_activation_transition'::regclass
		  AND conname = 'identity_activation_transition_operation_check'`).Scan(&operationCheckDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(operationCheckDefinition, "restore") {
		t.Fatalf("identity transition operation constraint = %q, want restore", operationCheckDefinition)
	}
	var approvedCheckCount int
	if err := db.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_constraint
		WHERE conrelid = 'project.identity_activation_transition'::regclass
		  AND conname = 'identity_activation_transition_approved_authored_ids_json_check'`).Scan(&approvedCheckCount); err != nil {
		t.Fatal(err)
	}
	if approvedCheckCount != 1 {
		t.Fatalf("approved restore evidence check constraints = %d, want 1", approvedCheckCount)
	}
	var canonical, unsorted, duplicate bool
	if err := db.QueryRow(ctx, `
		SELECT project.identity_restore_authored_ids_canonical($1),
		       project.identity_restore_authored_ids_canonical($2),
		       project.identity_restore_authored_ids_canonical($3)`,
		`["alpha","zeta"]`, `["zeta","alpha"]`, `["alpha","alpha"]`).
		Scan(&canonical, &unsorted, &duplicate); err != nil {
		t.Fatal(err)
	}
	if !canonical || unsorted || duplicate {
		t.Fatalf("canonical restore ID validation = valid:%t unsorted:%t duplicate:%t", canonical, unsorted, duplicate)
	}
	var publicationIndexDefinition string
	if err := db.QueryRow(ctx, `
		SELECT indexdef
		FROM pg_indexes
		WHERE schemaname = 'project'
		  AND indexname = 'identity_activation_transition_publish_bundle_idx'`).Scan(&publicationIndexDefinition); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(publicationIndexDefinition, "restore") {
		t.Fatalf("publication transition bundle index = %q, want restore", publicationIndexDefinition)
	}
	var canUpdateAudit, canUpdateRevision, canUpdatePublication, canDeleteTransition, canExecuteCanonical bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'audit.audit_event', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'platform.schema_revision', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'project.contract_publication', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'project.identity_activation_transition', 'DELETE'),
		       has_function_privilege('leapview_control_runtime', 'project.identity_restore_authored_ids_canonical(text)', 'EXECUTE')`).
		Scan(&canUpdateAudit, &canUpdateRevision, &canUpdatePublication, &canDeleteTransition, &canExecuteCanonical); err != nil {
		t.Fatal(err)
	}
	if canUpdateAudit || canUpdateRevision || canUpdatePublication || canDeleteTransition || !canExecuteCanonical {
		t.Fatalf("runtime grants: audit update=%t revision update=%t contract publication update=%t transition delete=%t canonical execute=%t", canUpdateAudit, canUpdateRevision, canUpdatePublication, canDeleteTransition, canExecuteCanonical)
	}
	if _, err := db.Exec(ctx, `UPDATE platform.schema_revision SET migration_id = 'tampered' WHERE revision = $1`, BaselineRevision); err == nil {
		t.Fatal("schema revision append-only trigger did not reject an update")
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, source, operation, action, capability, outcome,
		     aggregate_key, aggregate_sequence, intent_digest)
		VALUES ('00000000-0000-0000-0000-000000000002', 'schema', 'test',
		        'schema.test', '', 'success', 'schema:test', 0,
		        'sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE audit.audit_event SET action = 'tampered' WHERE audit_id = '00000000-0000-0000-0000-000000000002'`); err == nil {
		t.Fatal("audit append-only trigger did not reject an update")
	}
	// Exercise the append path as the non-superuser runtime role.  Trigger
	// execution must remain available even though the trigger function itself
	// is not callable by PUBLIC.
	runtimeConn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer runtimeConn.Release()
	if _, err := runtimeConn.Exec(ctx, `SET ROLE leapview_control_runtime`); err != nil {
		t.Fatal(err)
	}
	if _, err := runtimeConn.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, source, operation, action, capability, outcome,
		     aggregate_key, aggregate_sequence, intent_digest)
		VALUES ('00000000-0000-0000-0000-000000000003', 'runtime', 'test',
		        'runtime.test', '', 'success', 'runtime:test', 0,
		        'sha256:0000000000000000000000000000000000000000000000000000000000000000')`); err != nil {
		t.Fatalf("runtime audit append: %v", err)
	}

	// PostgreSQL enforces the JSON document boundary at the persistence edge.
	_, err = db.Exec(ctx, `
		INSERT INTO access.principal (id, principal_type, status, attributes)
		VALUES ('00000000-0000-0000-0000-000000000001', 'user', 'active', $1::jsonb)`, `{"oversized":"`+strings.Repeat("x", 20000)+`"}`)
	if err == nil {
		t.Fatal("oversized principal attributes unexpectedly accepted")
	}
}
