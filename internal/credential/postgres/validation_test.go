package postgres

import (
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/google/uuid"
)

func postgresValidationReceipt(t *testing.T, stored credential.StoredVersion) (credential.ValidationReceipt, access.AuditIntent) {
	t.Helper()
	validatedAt := time.Now().UTC().Truncate(time.Microsecond)
	receipt := credential.ValidationReceipt{
		ReceiptID: uuid.NewString(), Binding: stored.Metadata.Binding, ActorID: uuid.NewString(),
		BindingID: "binding_prod_warehouse", BindingRevision: 7,
		ConfigurationDigest: "sha256:" + strings.Repeat("b", 64),
		ValidatedAt:         validatedAt, ExpiresAt: validatedAt.Add(5 * time.Minute),
	}
	intent, err := receipt.AuditIntent()
	if err != nil {
		t.Fatalf("build validation audit intent: %v", err)
	}
	return receipt, intent
}

func saveValidationDraft(t *testing.T, repository *Repository, stored credential.StoredVersion, intent access.AuditIntent) {
	t.Helper()
	if err := repository.ReserveEncryption(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID, testKeyCommitment(stored.Envelope.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveDraft(t.Context(), stored, intent); err != nil {
		t.Fatal(err)
	}
}

func TestSaveValidationCommitsImmutableReceiptAndRedactedAuditTogether(t *testing.T) {
	db, _, repository := credentialDB(t)
	stored, draftAudit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, draftAudit)
	receipt, audit := postgresValidationReceipt(t, stored)

	if err := repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}
	var deploymentID, versionID, ownerID, scopeKind, targetID, projectID, environment string
	var resourceID, purpose, provider, destination, actorID, bindingID, configDigest string
	var bindingRevision int64
	var validatedAt, expiresAt time.Time
	if err := db.QueryRow(t.Context(), `
		SELECT deployment_id, version_id, owner_id, scope_kind, target_id,
		       project_id, environment, resource_id, purpose, provider,
		       destination, actor_id, binding_id, binding_revision,
		       configuration_digest, validated_at, expires_at
		FROM credential.validation_receipt WHERE receipt_id = $1`, receipt.ReceiptID).Scan(
		&deploymentID, &versionID, &ownerID, &scopeKind, &targetID, &projectID,
		&environment, &resourceID, &purpose, &provider, &destination, &actorID,
		&bindingID, &bindingRevision, &configDigest, &validatedAt, &expiresAt,
	); err != nil {
		t.Fatal(err)
	}
	binding := receipt.Binding
	if deploymentID != binding.DeploymentID || versionID != binding.VersionID || ownerID != binding.OwnerID ||
		scopeKind != binding.ScopeKind || targetID != binding.TargetID || projectID != binding.ProjectID ||
		environment != binding.Environment || resourceID != binding.ResourceID || purpose != binding.Purpose ||
		provider != binding.Provider || destination != binding.Destination || actorID != receipt.ActorID ||
		bindingID != receipt.BindingID || bindingRevision != receipt.BindingRevision || configDigest != receipt.ConfigurationDigest ||
		!validatedAt.Equal(receipt.ValidatedAt) || !expiresAt.Equal(validatedAt.Add(5*time.Minute)) {
		t.Fatalf("persisted receipt fields did not match the exact draft and observation: binding=%#v receipt=%#v", binding, receipt)
	}

	var auditCount int
	var auditMetadata []byte
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key = $1`, audit.AggregateKey).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT metadata FROM audit.audit_event WHERE aggregate_key = $1`, audit.AggregateKey).Scan(&auditMetadata); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || !bytes.Contains(auditMetadata, []byte(receipt.ReceiptID)) ||
		!bytes.Contains(auditMetadata, []byte(receipt.ConfigurationDigest)) ||
		bytes.Contains(auditMetadata, stored.Envelope.Ciphertext) || bytes.Contains(auditMetadata, []byte("password")) ||
		bytes.Contains(auditMetadata, []byte("ciphertext")) {
		t.Fatalf("validation audit count=%d metadata=%q is missing receipt identity or contains credential material", auditCount, auditMetadata)
	}
}

func TestSaveValidationAuditFailureRollsBackReceipt(t *testing.T) {
	db, runtimeDB, repository := credentialDB(t)
	stored, draftAudit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, draftAudit)
	broken, err := New(runtimeDB, credentialFailingAudit{err: errors.New("audit unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	receipt, audit := postgresValidationReceipt(t, stored)
	if err := broken.SaveValidation(t.Context(), receipt, audit); err == nil {
		t.Fatal("SaveValidation succeeded despite audit failure")
	}
	var receipts, auditEvents int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.validation_receipt WHERE receipt_id = $1`, receipt.ReceiptID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key = $1`, audit.AggregateKey).Scan(&auditEvents); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 || auditEvents != 0 {
		t.Fatalf("after audit failure receipts=%d audit events=%d; want 0 and 0", receipts, auditEvents)
	}
}

func TestSaveValidationRejectsBindingVersionAndAuditTampering(t *testing.T) {
	db, _, repository := credentialDB(t)
	stored, draftAudit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, draftAudit)

	tests := []struct {
		name   string
		change func(*credential.ValidationReceipt, *access.AuditIntent)
		want   error
	}{
		{name: "owner mismatch", change: func(receipt *credential.ValidationReceipt, _ *access.AuditIntent) {
			receipt.Binding.OwnerID = "different-owner"
		}, want: credential.ErrConflict},
		{name: "provider mismatch", change: func(receipt *credential.ValidationReceipt, _ *access.AuditIntent) { receipt.Binding.Provider = "mysql" }, want: credential.ErrConflict},
		{name: "destination mismatch", change: func(receipt *credential.ValidationReceipt, _ *access.AuditIntent) {
			receipt.Binding.Destination = "sha256:" + strings.Repeat("c", 64)
		}, want: credential.ErrConflict},
		{name: "version mismatch", change: func(receipt *credential.ValidationReceipt, _ *access.AuditIntent) {
			receipt.Binding.VersionID = uuid.NewString()
		}, want: credential.ErrConflict},
		{name: "audit action mismatch", change: func(_ *credential.ValidationReceipt, intent *access.AuditIntent) {
			intent.Action = "credential.draft.saved"
		}, want: credential.ErrInvalid},
		{name: "audit metadata addition", change: func(_ *credential.ValidationReceipt, intent *access.AuditIntent) {
			intent.MetadataJSON = `{"secret":"sentinel"}`
		}, want: credential.ErrInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt, intent := postgresValidationReceipt(t, stored)
			test.change(&receipt, &intent)
			// For scope tampering, make a matching canonical audit intent so the
			// insert-select itself must reject the receipt against stored metadata.
			if test.want == credential.ErrConflict {
				var err error
				intent, err = receipt.AuditIntent()
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := repository.SaveValidation(t.Context(), receipt, intent); !errors.Is(err, test.want) {
				t.Fatalf("SaveValidation error = %v, want %v", err, test.want)
			}
			var count int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.validation_receipt WHERE receipt_id = $1`, receipt.ReceiptID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rejected validation saved %d receipt rows", count)
			}
		})
	}
}

func TestSaveValidationUsesDatabaseTransactionClockForExpiry(t *testing.T) {
	db, _, repository := credentialDB(t)
	stored, draftAudit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, draftAudit)
	now := time.Now().UTC().Truncate(time.Microsecond)

	tests := []struct {
		name string
		at   time.Time
	}{
		{name: "expired", at: now.Add(-6 * time.Minute)},
		{name: "future dated", at: now.Add(time.Minute)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receipt, _ := postgresValidationReceipt(t, stored)
			receipt.ValidatedAt = test.at
			receipt.ExpiresAt = test.at.Add(5 * time.Minute)
			intent, err := receipt.AuditIntent()
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.SaveValidation(t.Context(), receipt, intent); !errors.Is(err, credential.ErrConflict) {
				t.Fatalf("SaveValidation error = %v, want conflict from database-clock admission", err)
			}
			var count int
			if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.validation_receipt WHERE receipt_id = $1`, receipt.ReceiptID).Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("database-clock rejection saved %d receipt rows", count)
			}
		})
	}

	malformed, _ := postgresValidationReceipt(t, stored)
	malformed.ExpiresAt = malformed.ExpiresAt.Add(time.Second)
	intent, err := malformed.AuditIntent()
	if err == nil {
		t.Fatal("receipt with a non-five-minute expiry produced an audit intent")
	}
	if err := repository.SaveValidation(t.Context(), malformed, intent); !errors.Is(err, credential.ErrInvalid) {
		t.Fatalf("SaveValidation with malformed expiry = %v, want invalid", err)
	}
}

func TestValidationReceiptIsImmutableAndHasLeastPrivilegeGrants(t *testing.T) {
	db, runtimeDB, repository := credentialDB(t)
	stored, draftAudit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, draftAudit)
	receipt, audit := postgresValidationReceipt(t, stored)
	if err := repository.SaveValidation(t.Context(), receipt, audit); err != nil {
		t.Fatal(err)
	}

	var runtimeInsert, runtimeSelect, runtimeUpdate, runtimeDelete, backupSelect, backupInsert bool
	if err := db.QueryRow(t.Context(), `SELECT
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'INSERT'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'SELECT'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'UPDATE'),
		has_table_privilege('leapview_control_runtime', 'credential.validation_receipt', 'DELETE'),
		has_table_privilege('leapview_control_backup', 'credential.validation_receipt', 'SELECT'),
		has_table_privilege('leapview_control_backup', 'credential.validation_receipt', 'INSERT')`).Scan(
		&runtimeInsert, &runtimeSelect, &runtimeUpdate, &runtimeDelete, &backupSelect, &backupInsert,
	); err != nil {
		t.Fatal(err)
	}
	if !runtimeInsert || !runtimeSelect || runtimeUpdate || runtimeDelete || !backupSelect || backupInsert {
		t.Fatalf("validation receipt grants runtime insert/select/update/delete=%t/%t/%t/%t backup select/insert=%t/%t", runtimeInsert, runtimeSelect, runtimeUpdate, runtimeDelete, backupSelect, backupInsert)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE credential.validation_receipt SET actor_id = 'changed' WHERE receipt_id = $1`, receipt.ReceiptID); err == nil {
		t.Fatal("runtime updated an immutable validation receipt")
	}
	if _, err := db.Exec(t.Context(), `DELETE FROM credential.validation_receipt WHERE receipt_id = $1`, receipt.ReceiptID); err == nil {
		t.Fatal("validation receipt delete bypassed immutability trigger")
	}
}
