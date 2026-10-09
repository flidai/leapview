package postgres

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func rotationMaintenancePool(t *testing.T, db, runtime *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	_, err := db.Exec(t.Context(), `DO $$ BEGIN
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='leapview_control_owner') THEN CREATE ROLE leapview_control_owner; END IF;
 IF NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='leapview_control_maintenance') THEN CREATE ROLE leapview_control_maintenance LOGIN PASSWORD 'credential-maintenance'; END IF;
 END $$;
 GRANT USAGE,CREATE ON SCHEMA credential TO leapview_control_owner;
 GRANT USAGE ON SCHEMA audit TO leapview_control_owner;
 GRANT ALL ON ALL TABLES IN SCHEMA credential,audit TO leapview_control_owner;`)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := schemaFS.ReadFile("rotation_schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(t.Context(), string(schema)); err != nil {
		t.Fatal(err)
	}
	configuration := runtime.Config().Copy()
	configuration.ConnConfig.User = "leapview_control_maintenance"
	configuration.ConnConfig.Password = "credential-maintenance"
	configuration.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(t.Context(), configuration)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return pool
}
func rotationReplacementKeys(t *testing.T, includeOld bool) *encryption.Keyring {
	t.Helper()
	entries := []map[string]string{{"key_id": "new-key", "key_base64": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x42}, 32)), "state": "active_write"}}
	if includeOld {
		entries = append(entries, map[string]string{"key_id": "old-key", "key_base64": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x41}, 32)), "state": "decrypt_only"})
	}
	body, _ := json.Marshal(map[string]any{"format": "credential-keyring-v1", "deployment_id": "deployment-prod", "active_write_key_id": "new-key", "keys": entries})
	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "keys.json")
	if err = os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	keys, err := encryption.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

type interruptedRotation struct {
	*RotationRepository
	failAt, calls int
	collision     string
}

func (r *interruptedRotation) RewrapEnvelope(ctx context.Context, previous credential.EnvelopeRewrap, next encryption.Envelope, intent access.AuditIntent) error {
	r.calls++
	if r.calls == r.failAt {
		return credential.ErrUnavailable
	}
	if r.collision != "" {
		intent.EventID = r.collision
	}
	return r.RotationRepository.RewrapEnvelope(ctx, previous, next, intent)
}
func TestEnvelopeRotationMaintenanceCASAuditBudgetAndResume(t *testing.T) {
	db, runtime, repository := credentialDB(t)
	maintenance := rotationMaintenancePool(t, db, runtime)
	operator, err := NewRotationRepository(t.Context(), maintenance)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = NewRotationRepository(t.Context(), runtime); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("runtime gained rotation capability: %v", err)
	}
	old := loadTestCredentialKeyring(t, "deployment-prod", "old-key", bytes.Repeat([]byte{0x41}, 32))
	keys := rotationReplacementKeys(t, true)
	missing := rotationReplacementKeys(t, false)
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}
	scope := credential.Scope{Resource: resource, OwnerID: "customer", Purpose: "connection", Provider: "postgres", Destination: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))}
	service, err := credential.NewService(repository, old, testCredentialScopeResolver{scope: scope}, allowCredentialAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err = service.SaveDraft(t.Context(), uuid.NewString(), resource, map[string]string{"password": "rotation-secret"}); err != nil {
			t.Fatal(err)
		}
	}
	original, err := operator.ListEnvelopeRewraps(t.Context(), old.DeploymentID(), keys.ActiveWriteKeyID(), 100)
	if err != nil || len(original) != 3 {
		t.Fatalf("original envelopes: %d %v", len(original), err)
	}
	if _, err = credential.RotateEnvelopes(t.Context(), operator, missing, 100); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("missing old key accepted: %v", err)
	}
	var budgetCount int
	if err = db.QueryRow(t.Context(), `SELECT count(*) FROM credential.encryption_budget WHERE key_id='new-key'`).Scan(&budgetCount); err != nil || budgetCount != 0 {
		t.Fatal("missing-key preflight consumed encryption budget")
	}
	var collision string
	if err = db.QueryRow(t.Context(), `SELECT audit_id::text FROM audit.audit_event LIMIT 1`).Scan(&collision); err != nil {
		t.Fatal(err)
	}
	if progress, e := credential.RotateEnvelopes(t.Context(), &interruptedRotation{RotationRepository: operator, collision: collision}, keys, 1); e == nil || progress.Rewrapped != 0 {
		t.Fatalf("audit failure did not roll back: %+v %v", progress, e)
	}
	unchanged, err := operator.ListEnvelopeRewraps(t.Context(), old.DeploymentID(), keys.ActiveWriteKeyID(), 100)
	if err != nil || len(unchanged) != 3 || unchanged[0].Revision != 1 {
		t.Fatal("audit rollback changed envelope")
	}
	var uses int
	if err = db.QueryRow(t.Context(), `SELECT uses FROM credential.encryption_budget WHERE key_id='new-key'`).Scan(&uses); err != nil || uses != 1 {
		t.Fatalf("audit rollback reclaimed encryption budget: %d %v", uses, err)
	}
	if progress, e := credential.RotateEnvelopes(t.Context(), operator, keys, 1); e != nil || progress.Rewrapped != 1 || progress.Complete {
		t.Fatalf("first bounded batch: %+v %v", progress, e)
	}
	current, err := repository.GetStoredDraft(t.Context(), old.DeploymentID(), scope.OwnerID, resource, original[0].Stored.Metadata.Binding.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	audit, err := credential.EnvelopeRewrapAudit(original[0], current.Envelope, uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if err = operator.RewrapEnvelope(t.Context(), original[0], current.Envelope, audit); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("stale envelope CAS accepted: %v", err)
	}
	if progress, e := credential.RotateEnvelopes(t.Context(), &interruptedRotation{RotationRepository: operator, failAt: 2}, keys, 100); !errors.Is(e, credential.ErrUnavailable) || progress.Rewrapped != 1 {
		t.Fatalf("interruption: %+v %v", progress, e)
	}
	if progress, e := credential.RotateEnvelopes(t.Context(), operator, keys, 100); e != nil || progress.Rewrapped != 1 || !progress.Complete {
		t.Fatalf("resume: %+v %v", progress, e)
	}
	if progress, e := credential.RotateEnvelopes(t.Context(), operator, keys, 100); e != nil || progress.Rewrapped != 0 || !progress.Complete {
		t.Fatalf("completed replay: %+v %v", progress, e)
	}
	if err = db.QueryRow(t.Context(), `SELECT uses FROM credential.encryption_budget WHERE key_id='new-key'`).Scan(&uses); err != nil || uses != 5 {
		t.Fatalf("failed and successful encryptions not independently counted: %d %v", uses, err)
	}
	for _, before := range original {
		after, e := repository.GetStoredDraft(t.Context(), old.DeploymentID(), scope.OwnerID, resource, before.Stored.Metadata.Binding.VersionID)
		if e != nil || !reflect.DeepEqual(after.Metadata, before.Stored.Metadata) {
			t.Fatal("rewrap changed logical version or AAD metadata")
		}
		plain, e := keys.Decrypt(after.Metadata.Binding, after.Envelope)
		if e != nil || !bytes.Contains(plain, []byte("rotation-secret")) {
			t.Fatal("rewrap changed secret or lost AAD")
		}
		clear(plain)
	}
	var audits int
	var metadata string
	if err = db.QueryRow(t.Context(), `SELECT count(*), string_agg(metadata::text,' ') FROM audit.audit_event WHERE action='credential.envelope.rewrapped'`).Scan(&audits, &metadata); err != nil || audits != 3 || bytes.Contains([]byte(metadata), []byte("rotation-secret")) {
		t.Fatalf("atomic redacted audits: count=%d err=%v", audits, err)
	}
	if _, err = runtime.Exec(t.Context(), `SELECT credential.rewrap_envelope('deployment-prod','version',1,'old-key','new-key','aes-256-gcm-random-nonce-v1',$1,$2::uuid,$3)`, bytes.Repeat([]byte{1}, 28), uuid.NewString(), "sha256:"+string(bytes.Repeat([]byte{'a'}, 64))); err == nil {
		t.Fatal("runtime invoked maintenance-only SQL capability")
	}
	if _, err = db.Exec(t.Context(), `UPDATE credential.envelope SET envelope_revision=envelope_revision+1,key_id='old-key',ciphertext=$1 WHERE version_id=$2`, bytes.Repeat([]byte{1}, 28), original[0].Stored.Metadata.Binding.VersionID); err == nil {
		t.Fatal("direct envelope rewrite bypassed guarded capability")
	}
	// An older backup retains the original envelope even after every live row is rewrapped.
	if _, err = missing.Decrypt(original[0].Stored.Metadata.Binding, original[0].Stored.Envelope); err == nil {
		t.Fatal("new-only keyring unexpectedly recovered an older backup envelope")
	}
	recovered, err := keys.Decrypt(original[0].Stored.Metadata.Binding, original[0].Stored.Envelope)
	if err != nil || !bytes.Contains(recovered, []byte("rotation-secret")) {
		t.Fatal("retained decrypt-only key could not recover older envelope")
	}
	clear(recovered)
}
