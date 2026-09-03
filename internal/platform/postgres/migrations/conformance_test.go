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

// TestAccessAuthorityCompatibilityPostgreSQL18 proves that the additive
// access revision can be introduced after the first six platform revisions
// without rewriting the identities already present in the baseline tables.
// The test is skip-gated by postgrestest.Start like the other PostgreSQL
// conformance tests, so local environments without Docker remain useful.
func TestAccessAuthorityCompatibilityPostgreSQL18(t *testing.T) {
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime"})
	h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly"})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "leapview_control_compat")
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

	// Install revisions 001-006 and record their exact metadata so Apply sees
	// the same upgrade state as a production database before it introduces 007.
	firstSix := []struct {
		revision int64
		id       string
		sql      string
		checksum string
	}{
		{BaselineRevision, BaselineMigrationID, BaselineSQL(), BaselineChecksum()},
		{IdentityLedgerRevision, IdentityLedgerMigrationID, IdentityLedgerSQL(), IdentityLedgerChecksum()},
		{ContractPublicationRevision, ContractPublicationMigrationID, ContractPublicationSQL(), ContractPublicationChecksum()},
		{ActivationTransitionJournalRevision, ActivationTransitionJournalMigrationID, ActivationTransitionJournalSQL(), ActivationTransitionJournalChecksum()},
		{ActivationTransitionReferencesRevision, ActivationTransitionReferencesMigrationID, ActivationTransitionReferencesSQL(), ActivationTransitionReferencesChecksum()},
		{IdentityRestoreTransitionRevision, IdentityRestoreTransitionMigrationID, IdentityRestoreTransitionSQL(), IdentityRestoreTransitionChecksum()},
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, migration := range firstSix {
		if _, err := tx.Exec(ctx, migration.sql); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("install pre-007 migration: %v", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO platform.schema_revision (revision, migration_id, checksum)
			VALUES ($1, $2, $3)`, migration.revision, migration.id, migration.checksum); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("record pre-007 migration %d: %v", migration.revision, err)
		}
	}
	const legacyPrincipal = "00000000-0000-0000-0000-000000000101"
	const legacyGroup = "00000000-0000-0000-0000-000000000102"
	const legacyDisabledPrincipal = "00000000-0000-0000-0000-000000000103"
	const legacySession = "00000000-0000-0000-0000-000000000104"
	const legacySessionTwo = "00000000-0000-0000-0000-000000000105"
	const canonicalAuditEvent = "00000000-0000-0000-0000-000000000106"
	if _, err := tx.Exec(ctx, `
		INSERT INTO access.principal(id, principal_type, status, external_subject, display_name)
		VALUES ($1::uuid, 'user', 'active', 'legacy-subject', 'Legacy User')`, legacyPrincipal); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert legacy principal: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO access.principal(id, principal_type, status, external_subject, display_name)
		VALUES ($1::uuid, 'user', 'disabled', 'legacy-disabled-subject', 'Legacy Disabled User')`, legacyDisabledPrincipal); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert legacy disabled principal: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO access.access_group(id, name, provider, external_id)
		VALUES ($1::uuid, 'Legacy Group', 'legacy', 'legacy-group')`, legacyGroup); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert legacy group: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO access.principal_group(principal_id, group_id)
		VALUES ($1::uuid, $2::uuid)`, legacyPrincipal, legacyGroup); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert legacy membership: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO access.session(id, principal_id, token_fingerprint, expires_at)
		VALUES ($1::uuid, $2::uuid, decode(repeat('ab', 32), 'hex'), clock_timestamp() + interval '1 hour')`, legacySession, legacyPrincipal); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert legacy session: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO access.session(id, principal_id, token_fingerprint, expires_at)
		VALUES ($1::uuid, $2::uuid, decode(repeat('cd', 32), 'hex'), clock_timestamp() + interval '2 hours')`, legacySessionTwo, legacyPrincipal); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("insert second legacy session: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		tx, err = conn.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if err := Apply(ctx, tx); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply PostgreSQL migrations: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	}

	var principalID, groupID, membershipPrincipal string
	if err := db.QueryRow(ctx, `SELECT id::text FROM access.principal WHERE id=$1::uuid`, legacyPrincipal).Scan(&principalID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT id::text FROM access.access_group WHERE id=$1::uuid`, legacyGroup).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT principal_id::text FROM access.principal_group WHERE principal_id=$1::uuid AND group_id=$2::uuid`, legacyPrincipal, legacyGroup).Scan(&membershipPrincipal); err != nil {
		t.Fatal(err)
	}
	var sessions, revoked, sealed int
	if err := db.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE revoked_at IS NOT NULL),
		       count(*) FILTER (WHERE octet_length(verifier) = 32)
		FROM access.session WHERE id IN ($1::uuid, $2::uuid)`, legacySession, legacySessionTwo).
		Scan(&sessions, &revoked, &sealed); err != nil {
		t.Fatal(err)
	}
	if principalID != legacyPrincipal || groupID != legacyGroup || membershipPrincipal != legacyPrincipal {
		t.Fatalf("legacy identities changed: principal=%s group=%s membership principal=%s", principalID, groupID, membershipPrincipal)
	}
	if sessions != 2 || revoked != 2 || sealed != 2 {
		t.Fatalf("legacy session migration = sessions:%d revoked:%d sealed:%d, want 2/2/2", sessions, revoked, sealed)
	}

	var disabled, disabledAt bool
	if err := db.QueryRow(ctx, `SELECT status='disabled', disabled_at IS NOT NULL FROM access.principal WHERE id=$1::uuid`, legacyDisabledPrincipal).Scan(&disabled, &disabledAt); err != nil {
		t.Fatal(err)
	}
	if !disabled || !disabledAt {
		t.Fatalf("legacy disabled principal backfill = disabled:%t disabled_at:%t, want true/true", disabled, disabledAt)
	}
	if _, err := db.Exec(ctx, `UPDATE access.principal SET email='legacy-disabled@example.com', display_name='Migrated Disabled User', updated_at=clock_timestamp() WHERE id=$1::uuid`, legacyDisabledPrincipal); err != nil {
		t.Fatalf("update migrated disabled principal: %v", err)
	}

	if _, err := db.Exec(ctx, `
		INSERT INTO audit.audit_event
		    (audit_id, principal_id, source, operation, action, resource_kind, resource_id,
		     project_id, environment, generation_id, capability, outcome,
		     aggregate_key, aggregate_sequence, intent_digest, metadata)
		VALUES ($1::uuid, $2::uuid, 'access', 'authorization', 'project.read',
		        'semantic_model', 'model_legacy', 'project_legacy', 'production',
		        'generation_legacy', 'RESOURCE_READ', 'success', 'model_legacy', 0,
		        'sha256:' || repeat('0', 64), '{}'::jsonb)`, canonicalAuditEvent, legacyPrincipal); err != nil {
		t.Fatalf("insert canonical audit event: %v", err)
	}
	var auditProject, auditEnvironment, auditGeneration string
	if err := db.QueryRow(ctx, `SELECT project_id, environment, generation_id FROM audit.audit_event WHERE audit_id=$1::uuid`, canonicalAuditEvent).
		Scan(&auditProject, &auditEnvironment, &auditGeneration); err != nil {
		t.Fatal(err)
	}
	if auditProject != "project_legacy" || auditEnvironment != "production" || auditGeneration != "generation_legacy" {
		t.Fatalf("canonical audit scope = %q/%q/%q", auditProject, auditEnvironment, auditGeneration)
	}

	var revision int64
	var checksum string
	if err := db.QueryRow(ctx, `SELECT revision, checksum FROM platform.schema_revision WHERE migration_id=$1`, AccessAuthorityCompatibilityMigrationID).
		Scan(&revision, &checksum); err != nil {
		t.Fatal(err)
	}
	if revision != AccessAuthorityCompatibilityRevision || checksum != AccessAuthorityCompatibilityChecksum() {
		t.Fatalf("revision 007 metadata = %d/%q, want %d/%q", revision, checksum, AccessAuthorityCompatibilityRevision, AccessAuthorityCompatibilityChecksum())
	}
	// Revision 001 grants runtime DELETE and readonly SELECT on every future
	// access table.  Verify revision 007 closes those defaults for a table
	// created by the migration owner; exceptional future tables must opt in
	// explicitly in their own migration.
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_owner`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `CREATE TABLE access.access_default_privilege_probe (id integer)`); err != nil {
		t.Fatalf("create future access privilege probe: %v", err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		t.Fatal(err)
	}
	var futureRuntimeDelete, futureReadonlySelect bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'access.access_default_privilege_probe', 'DELETE'),
		       has_table_privilege('leapview_control_readonly', 'access.access_default_privilege_probe', 'SELECT')`).
		Scan(&futureRuntimeDelete, &futureReadonlySelect); err != nil {
		t.Fatal(err)
	}
	if futureRuntimeDelete || futureReadonlySelect {
		t.Fatalf("future access defaults remain broad: runtime DELETE=%t readonly SELECT=%t", futureRuntimeDelete, futureReadonlySelect)
	}

	var runtimeCredentialSelect, runtimeCredentialInsert, runtimeCredentialUpdate bool
	var runtimeGrantSelect, runtimeGrantInsert, runtimeGrantUpdate bool
	var readonlyCredentialSelect, readonlyGrantSelect bool
	if err := db.QueryRow(ctx, `
		SELECT has_table_privilege('leapview_control_runtime', 'access.credential', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'access.credential', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'access.credential', 'UPDATE'),
		       has_table_privilege('leapview_control_runtime', 'access.access_grant', 'SELECT'),
		       has_table_privilege('leapview_control_runtime', 'access.access_grant', 'INSERT'),
		       has_table_privilege('leapview_control_runtime', 'access.access_grant', 'UPDATE'),
		       has_table_privilege('leapview_control_readonly', 'access.credential', 'SELECT'),
		       has_table_privilege('leapview_control_readonly', 'access.access_grant', 'SELECT')`).
		Scan(&runtimeCredentialSelect, &runtimeCredentialInsert, &runtimeCredentialUpdate,
			&runtimeGrantSelect, &runtimeGrantInsert, &runtimeGrantUpdate,
			&readonlyCredentialSelect, &readonlyGrantSelect); err != nil {
		t.Fatal(err)
	}
	if runtimeCredentialSelect || runtimeCredentialInsert || runtimeCredentialUpdate ||
		runtimeGrantSelect || runtimeGrantInsert || runtimeGrantUpdate ||
		readonlyCredentialSelect || readonlyGrantSelect {
		t.Fatalf("legacy table privileges: runtime credential=%t/%t/%t grant=%t/%t/%t readonly=%t/%t",
			runtimeCredentialSelect, runtimeCredentialInsert, runtimeCredentialUpdate,
			runtimeGrantSelect, runtimeGrantInsert, runtimeGrantUpdate,
			readonlyCredentialSelect, readonlyGrantSelect)
	}

	var oauthTables, roleTriggers int
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema='access' AND table_name = ANY($1::text[])`,
		[]string{"oauth_client", "oauth_session", "oauth_client_assertion", "authoring_session", "authoring_credential"}).Scan(&oauthTables); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `
		SELECT count(*) FROM pg_trigger
		WHERE tgrelid = 'access.principal'::regclass
		  AND tgname = 'principal_identity_immutable'`).Scan(&roleTriggers); err != nil {
		t.Fatal(err)
	}
	if oauthTables != 5 || roleTriggers != 1 {
		t.Fatalf("post-007 access contract = oauth tables:%d principal identity triggers:%d", oauthTables, roleTriggers)
	}
}
