package migrations

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type recordingTx struct {
	sqls      []string
	revisions map[int64]recordingRow
	queryErr  error
}

func (r *recordingTx) Exec(_ context.Context, sql string, _ ...any) (pgconn.CommandTag, error) {
	r.sqls = append(r.sqls, sql)
	return pgconn.CommandTag{}, nil
}

func (r *recordingTx) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	if r.queryErr != nil {
		return recordingRow{err: r.queryErr}
	}
	revision, _ := args[0].(int64)
	return r.revisions[revision]
}

type recordingRow struct {
	revision    int64
	migrationID string
	checksum    string
	err         error
}

func (r recordingRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != 3 {
		return errors.New("unexpected destination count")
	}
	*dest[0].(*int64) = r.revision
	*dest[1].(*string) = r.migrationID
	*dest[2].(*string) = r.checksum
	return nil
}

func TestBaselineMetadata(t *testing.T) {
	if BaselineRevision != 1 || BaselineMigrationID != "001_control_plane" {
		t.Fatalf("baseline metadata = revision %d, id %q", BaselineRevision, BaselineMigrationID)
	}
	sql := BaselineSQL()
	for _, schema := range []string{"access", "delivery", "refresh", "event", "audit", "lineage", "cache", "agent"} {
		if !strings.Contains(sql, "CREATE SCHEMA IF NOT EXISTS "+schema) {
			t.Errorf("baseline does not create %s capability schema", schema)
		}
	}
	for _, role := range []string{"leapview_control_owner", "leapview_control_migrator", "leapview_control_runtime", "leapview_control_readonly"} {
		if !strings.Contains(sql, role) {
			t.Errorf("baseline does not declare/grant %s", role)
		}
	}
	for _, marker := range []string{
		"platform.schema_revision",
		"platform.operation",
		"event.event_aggregate",
		"event.event_retention_root",
		"delivery.delivery_snapshot_retention",
		"octet_length(payload::text) <= 65536",
		"octet_length(properties::text) <= 16384",
		"octet_length(metadata::text) <= 16384",
		"audit.reject_audit_mutation",
		"REVOKE UPDATE, DELETE ON audit.audit_event",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("baseline missing required contract marker %q", marker)
		}
	}
	if strings.Contains(sql, "-- +goose") {
		t.Fatal("PostgreSQL baseline must use its own migration mechanism")
	}
	if strings.Contains(sql, "repeat('0', 64)") {
		t.Fatal("baseline must not seed a fake schema checksum")
	}
}

func TestApplyUsesCallerOwnedTransaction(t *testing.T) {
	recorder := &recordingTx{revisions: validRevisions()}
	if err := Apply(context.Background(), recorder); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if len(recorder.sqls) != 14 || recorder.sqls[0] != BaselineSQL() || recorder.sqls[2] != IdentityLedgerSQL() || recorder.sqls[4] != ContractPublicationSQL() || recorder.sqls[6] != ActivationTransitionJournalSQL() || recorder.sqls[8] != ActivationTransitionReferencesSQL() || recorder.sqls[10] != IdentityRestoreTransitionSQL() || recorder.sqls[12] != AccessAuthorityCompatibilitySQL() {
		t.Fatal("Apply() did not execute the authored migrations in order")
	}
	if err := Apply(context.Background(), nil); err == nil {
		t.Fatal("Apply(nil) unexpectedly succeeded")
	}
}

func TestContractPublicationMigrationIsImmutableAndIdentityQualified(t *testing.T) {
	sql := ContractPublicationSQL()
	for _, marker := range []string{
		"PRIMARY KEY (instance_id, authored_id, resource_kind, version_baseline)",
		"REFERENCES project.resource_identity(instance_id, authored_id, resource_kind)",
		"projection_profile = 'leapview.contract/v1'",
		"canonical_bytes",
		"canonical_digest",
		"validation_evidence_json",
		"contract_publication_immutable",
		"contract_publication_no_truncate",
		"BEFORE UPDATE OR DELETE",
		"BEFORE TRUNCATE",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("contract publication migration missing %q", marker)
		}
	}
	for _, forbidden := range []string{"resource_uid", "publication_id", "gen_random_uuid", "uuid_generate"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("contract publication migration introduces forbidden identity %q", forbidden)
		}
	}
}

func TestApplyRejectsRevisionChecksumMismatch(t *testing.T) {
	revisions := validRevisions()
	revisions[IdentityLedgerRevision] = recordingRow{
		revision: IdentityLedgerRevision, migrationID: IdentityLedgerMigrationID, checksum: strings.Repeat("f", 64),
	}
	recorder := &recordingTx{revisions: revisions}
	if err := Apply(context.Background(), recorder); err == nil {
		t.Fatal("Apply() accepted a mismatched recorded checksum")
	}
}

func TestIdentityLedgerMigrationHasCompositeIdentity(t *testing.T) {
	sql := IdentityLedgerSQL()
	for _, marker := range []string{
		"PRIMARY KEY (instance_id, authored_id)",
		"resource_identity_kind_immutable",
		"resource_identity_no_delete",
		"resource_identity_history_append_only",
		"source_bundle_one_active_idx",
		"project.durable_resource_reference",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("identity ledger migration missing %q", marker)
		}
	}
	for _, forbidden := range []string{"resource_uid", "uuid_generate", "gen_random_uuid"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("identity ledger migration introduces forbidden surrogate identity %q", forbidden)
		}
	}
}

func TestActivationTransitionJournalMigrationIsImmutableAndFenced(t *testing.T) {
	sql := ActivationTransitionJournalSQL()
	for _, marker := range []string{
		"project.identity_activation_transition",
		"PRIMARY KEY (instance_id, transition_id)",
		"candidate_id",
		"bundle_id",
		"expected_bundle_id",
		"authored_resources_json",
		"graph_digest",
		"identity_pending",
		"identity_active",
		"delivery_active",
		"identity_activation_transition_guard",
		"activation transition parameters and evidence are immutable",
		"completed activation transition is immutable",
		"activation transition phase must move forward",
		"identity_activation_transition_no_delete",
		"identity_activation_transition_cutover_owner_idx",
		"identity_activation_transition_publish_bundle_idx",
		"BEFORE TRUNCATE",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("activation transition journal migration missing %q", marker)
		}
	}
	for _, forbidden := range []string{"gen_random_uuid", "uuid_generate", "resource_uid"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("activation transition journal introduces forbidden surrogate identity %q", forbidden)
		}
	}
}

func TestActivationTransitionReferencesMigrationAddsEvidenceAndInsertFence(t *testing.T) {
	sql := ActivationTransitionReferencesSQL()
	for _, marker := range []string{
		"ADD COLUMN IF NOT EXISTS durable_references_json text",
		"SET durable_references_json = '[]'",
		"ALTER COLUMN durable_references_json SET NOT NULL",
		"identity_activation_transition_durable_references_json_check",
		"BEFORE INSERT OR UPDATE",
		"activation transition must be inserted in prepared state",
		"NEW.durable_references_json IS DISTINCT FROM OLD.durable_references_json",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("activation transition references migration missing %q", marker)
		}
	}
}

func TestIdentityRestoreTransitionMigrationAddsApprovedEvidence(t *testing.T) {
	sql := IdentityRestoreTransitionSQL()
	for _, marker := range []string{
		"ADD COLUMN IF NOT EXISTS approved_authored_ids_json text",
		"SET approved_authored_ids_json = '[]'",
		"ALTER COLUMN approved_authored_ids_json SET NOT NULL",
		"identity_activation_transition_approved_authored_ids_json_check",
		"operation IN ('publish', 'rollback', 'restore')",
		"identity_activation_transition_restore_ids_check",
		"identity_restore_authored_ids_canonical",
		"identity_activation_transition_approved_authored_ids_canonical_check",
		"jsonb_typeof(item) <> 'string'",
		`authored_id COLLATE "C" <= previous_id COLLATE "C"`,
		"identity_activation_transition_publish_bundle_idx",
		"WHERE operation IN ('publish', 'restore')",
		"NEW.approved_authored_ids_json IS DISTINCT FROM OLD.approved_authored_ids_json",
		"activation transition must be inserted in prepared state",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("identity restore transition migration missing %q", marker)
		}
	}
}

func TestAccessAuthorityCompatibilityMigrationPreservesLegacyRowsAndAddsRepositoryContracts(t *testing.T) {
	sql := AccessAuthorityCompatibilitySQL()
	for _, marker := range []string{
		"ADD COLUMN IF NOT EXISTS project_id text",
		"ADD COLUMN IF NOT EXISTS environment text",
		"ADD COLUMN IF NOT EXISTS generation_id text",
		"audit_event_project_id_compatibility_check",
		"audit_event_environment_compatibility_check",
		"audit_event_generation_id_compatibility_check",
		"ALTER TABLE access.principal",
		"ADD COLUMN IF NOT EXISTS email text",
		"UPDATE access.principal",
		"SET disabled_at = COALESCE(disabled_at, clock_timestamp())",
		"principal_email_length_compatibility_check",
		"CHECK (length(email) <= 320)",
		"ADD COLUMN IF NOT EXISTS revoked_at timestamptz",
		"ALTER TABLE access.principal_group",
		"membership_id uuid DEFAULT uuidv7()",
		"DROP CONSTRAINT IF EXISTS principal_group_pkey",
		"UPDATE access.session",
		"revoked_at = COALESCE(revoked_at, clock_timestamp())",
		"decode(repeat('00', 32), 'hex')",
		"ALTER COLUMN verifier SET NOT NULL",
		"CREATE TABLE IF NOT EXISTS access.oauth_client",
		"CREATE TABLE IF NOT EXISTS access.oauth_session",
		"CREATE TABLE IF NOT EXISTS access.oauth_client_assertion",
		"CREATE TABLE IF NOT EXISTS access.authoring_session",
		"CREATE TABLE IF NOT EXISTS access.authorization_snapshot",
		"CREATE UNIQUE INDEX IF NOT EXISTS principal_email_active_key",
		"CREATE UNIQUE INDEX IF NOT EXISTS principal_group_active_key",
		"CREATE INDEX IF NOT EXISTS oauth_session_request_idx",
		"session_instance_id_length_compatibility_check",
		"CHECK (length(instance_id) <= 128)",
		"session_profile_id_length_compatibility_check",
		"CHECK (length(profile_id) <= 128)",
		"session_client_id_length_compatibility_check",
		"CHECK (length(client_id) <= 255)",
		"CREATE TRIGGER principal_identity_immutable",
		"CREATE TRIGGER session_revocation_monotonic",
		"GRANT DELETE ON access.oauth_session, access.oauth_client_assertion TO leapview_control_runtime",
		"REVOKE SELECT ON access.session, access.local_credential",
		"REVOKE SELECT, INSERT, UPDATE ON access.credential, access.access_grant",
		"REVOKE SELECT ON access.credential, access.access_grant",
		"ALTER DEFAULT PRIVILEGES FOR ROLE leapview_control_owner IN SCHEMA access",
		"REVOKE DELETE ON TABLES FROM leapview_control_runtime",
		"REVOKE SELECT ON TABLES FROM leapview_control_readonly",
	} {
		if !strings.Contains(sql, marker) {
			t.Errorf("access compatibility migration missing %q", marker)
		}
	}
	for _, forbidden := range []string{
		"internal/access/postgres/schema.sql",
		"FAI-648",
		"FAI-649",
		"FAI-632",
		"DROP TABLE access.",
		"DELETE FROM access.session",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("access compatibility migration contains forbidden operation/reference %q", forbidden)
		}
	}
}

func validRevisions() map[int64]recordingRow {
	return map[int64]recordingRow{
		BaselineRevision: {
			revision: BaselineRevision, migrationID: BaselineMigrationID, checksum: BaselineChecksum(),
		},
		IdentityLedgerRevision: {
			revision: IdentityLedgerRevision, migrationID: IdentityLedgerMigrationID, checksum: IdentityLedgerChecksum(),
		},
		ContractPublicationRevision: {
			revision: ContractPublicationRevision, migrationID: ContractPublicationMigrationID, checksum: ContractPublicationChecksum(),
		},
		ActivationTransitionJournalRevision: {
			revision: ActivationTransitionJournalRevision, migrationID: ActivationTransitionJournalMigrationID, checksum: ActivationTransitionJournalChecksum(),
		},
		ActivationTransitionReferencesRevision: {
			revision: ActivationTransitionReferencesRevision, migrationID: ActivationTransitionReferencesMigrationID, checksum: ActivationTransitionReferencesChecksum(),
		},
		IdentityRestoreTransitionRevision: {
			revision: IdentityRestoreTransitionRevision, migrationID: IdentityRestoreTransitionMigrationID, checksum: IdentityRestoreTransitionChecksum(),
		},
		AccessAuthorityCompatibilityRevision: {
			revision: AccessAuthorityCompatibilityRevision, migrationID: AccessAuthorityCompatibilityMigrationID, checksum: AccessAuthorityCompatibilityChecksum(),
		},
	}
}
