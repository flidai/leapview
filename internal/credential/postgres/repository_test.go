package postgres

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type credentialAuditAdapter struct {
	repository *accesspostgres.AuditRepository
}

func (a credentialAuditAdapter) RecordAuditEvent(ctx context.Context, tx Tx, intent access.AuditIntent) error {
	_, err := a.repository.RecordAuditEvent(ctx, tx, intent)
	return err
}

type credentialFailingAudit struct{ err error }

func (a credentialFailingAudit) RecordAuditEvent(context.Context, Tx, access.AuditIntent) error {
	return a.err
}

type testCredentialScopeResolver struct{ scope credential.Scope }

func (r testCredentialScopeResolver) ResolveCredentialScope(_ context.Context, _ credential.Resource) (credential.Scope, error) {
	return r.scope, nil
}

type allowCredentialAuthorizer struct{}

func (allowCredentialAuthorizer) RequirePermission(context.Context, string, access.PermissionPair) error {
	return nil
}

func credentialDB(t *testing.T) (*pgxpool.Pool, *pgxpool.Pool, *Repository) {
	t.Helper()
	h := postgrestest.Start(t)
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "credential-runtime", Login: true})
	backupRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_backup"})
	database := h.NewDatabase(t, "credential_test")
	h.GrantDatabase(t, database.Name, runtimeRole, "CONNECT")
	h.GrantDatabase(t, database.Name, backupRole, "CONNECT")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	tx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := ApplySchema(t.Context(), tx); err != nil {
		_ = tx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	runtimeDB, err := pgxpool.New(t.Context(), database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtimeDB.Close)
	if err := runtimeDB.Ping(t.Context()); err != nil {
		t.Fatal(err)
	}
	repository, err := New(runtimeDB, credentialAuditAdapter{repository: accesspostgres.New()})
	if err != nil {
		t.Fatal(err)
	}
	return db, runtimeDB, repository
}

func testKeyCommitment(keyID string) encryption.KeyCommitment {
	return encryption.KeyCommitment(sha256.Sum256([]byte("test-key-material:" + keyID)))
}

func loadTestCredentialKeyring(t *testing.T, deploymentID, keyID string, material []byte) *encryption.Keyring {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "credential-keyring.json")
	document := fmt.Sprintf(`{"format":"credential-keyring-v1","deployment_id":%q,"active_write_key_id":%q,"keys":[{"key_id":%q,"key_base64":%q,"state":"active_write"}]}`, deploymentID, keyID, keyID, base64.StdEncoding.EncodeToString(material))
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	keyring, err := encryption.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	return keyring
}

func testStoredVersion(t *testing.T) (credential.StoredVersion, access.AuditIntent) {
	t.Helper()
	versionID := uuid.NewString()
	actorID := uuid.NewString()
	binding := encryption.Binding{
		DeploymentID: "deployment-prod", OwnerID: "customer-42", ScopeKind: "connection",
		TargetID: "lvinst-prod", ProjectID: "sales", Environment: "prod", ResourceID: "warehouse",
		Purpose: "connection-auth", Provider: "postgres", Destination: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64)), VersionID: versionID,
	}
	metadata := credential.Metadata{Binding: binding, ActorID: actorID, CreatedAt: time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)}
	stored := credential.StoredVersion{Metadata: metadata, Envelope: encryption.Envelope{
		Format: "aes-256-gcm-random-nonce-v1", KeyID: "key-prod-1", Ciphertext: bytes.Repeat([]byte{0xa5}, 44),
	}}
	intent := access.AuditIntent{
		EventID: uuid.NewString(), ScopeID: binding.ProjectID, ActorID: actorID, PrincipalID: actorID,
		Source: "credential", Operation: "saveCredentialDraft", Action: "credential.draft.saved",
		ResourceKind: "connection", ResourceID: binding.ResourceID, Outcome: "success",
		AggregateKey: "credential:" + versionID, AggregateSequence: 1,
		MetadataJSON: fmt.Sprintf(`{"purpose":%q,"version_id":%q}`, binding.Purpose, versionID),
	}
	return stored, intent
}

func TestSaveDraftCommitsVersionEnvelopeAndRedactedAuditTogether(t *testing.T) {
	db, _, repository := credentialDB(t)
	stored, intent := testStoredVersion(t)
	if err := repository.ReserveEncryption(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID, testKeyCommitment(stored.Envelope.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveDraft(t.Context(), stored, intent); err != nil {
		t.Fatal(err)
	}
	resource := credential.Resource{ScopeKind: stored.Metadata.Binding.ScopeKind, TargetID: stored.Metadata.Binding.TargetID, ProjectID: stored.Metadata.Binding.ProjectID, Environment: stored.Metadata.Binding.Environment, ResourceID: stored.Metadata.Binding.ResourceID}
	loaded, err := repository.GetStoredDraft(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Metadata.Binding.OwnerID, resource, stored.Metadata.Binding.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Metadata.Binding != stored.Metadata.Binding || loaded.Metadata.ActorID != stored.Metadata.ActorID || !loaded.Metadata.CreatedAt.Equal(stored.Metadata.CreatedAt) {
		t.Fatalf("loaded metadata = %#v, want %#v", loaded.Metadata, stored.Metadata)
	}
	if loaded.Envelope.Format != stored.Envelope.Format || loaded.Envelope.KeyID != stored.Envelope.KeyID || !bytes.Equal(loaded.Envelope.Ciphertext, stored.Envelope.Ciphertext) {
		t.Fatal("stored encrypted envelope changed during round trip")
	}
	var auditCount int
	var auditMetadata []byte
	if err := db.QueryRow(t.Context(), `SELECT metadata FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&auditMetadata); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE audit_id = $1::uuid`, intent.EventID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 || !bytes.Contains(auditMetadata, []byte(stored.Metadata.Binding.VersionID)) || bytes.Contains(auditMetadata, stored.Envelope.Ciphertext) {
		t.Fatalf("audit count=%d metadata=%q; expected version identity only", auditCount, auditMetadata)
	}
	if bytes.Contains(auditMetadata, []byte("ciphertext")) || bytes.Contains(auditMetadata, []byte("secret")) {
		t.Fatalf("audit metadata contains secret-shaped content: %q", auditMetadata)
	}
}

func TestGetDraftRequiresExactDeploymentAndResourceScope(t *testing.T) {
	_, _, repository := credentialDB(t)
	stored, intent := testStoredVersion(t)
	if err := repository.ReserveEncryption(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID, testKeyCommitment(stored.Envelope.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveDraft(t.Context(), stored, intent); err != nil {
		t.Fatal(err)
	}
	resource := credential.Resource{ScopeKind: "connection", TargetID: "lvinst-prod", ProjectID: "sales", Environment: "prod", ResourceID: "warehouse"}
	wrongScopes := []struct {
		deployment string
		resource   credential.Resource
	}{
		{"deployment-other", resource},
		{stored.Metadata.Binding.DeploymentID, credential.Resource{ScopeKind: "connection", TargetID: "lvinst-other", ProjectID: "sales", Environment: "prod", ResourceID: "warehouse"}},
		{stored.Metadata.Binding.DeploymentID, credential.Resource{ScopeKind: "connection", TargetID: "lvinst-prod", ProjectID: "finance", Environment: "prod", ResourceID: "warehouse"}},
		{stored.Metadata.Binding.DeploymentID, credential.Resource{ScopeKind: "connection", TargetID: "lvinst-prod", ProjectID: "sales", Environment: "dev", ResourceID: "warehouse"}},
		{stored.Metadata.Binding.DeploymentID, credential.Resource{ScopeKind: "connection", TargetID: "lvinst-prod", ProjectID: "sales", Environment: "prod", ResourceID: "another"}},
	}
	for _, test := range wrongScopes {
		if _, err := repository.GetDraftMetadata(t.Context(), test.deployment, stored.Metadata.Binding.OwnerID, test.resource, stored.Metadata.Binding.VersionID); !errors.Is(err, credential.ErrNotFound) {
			t.Errorf("GetDraft(%+v, deployment=%q) = %v, want not found", test.resource, test.deployment, err)
		}
	}
	if _, err := repository.GetDraftMetadata(t.Context(), stored.Metadata.Binding.DeploymentID, "different-owner", resource, stored.Metadata.Binding.VersionID); !errors.Is(err, credential.ErrNotFound) {
		t.Fatalf("cross-owner metadata read = %v, want not found", err)
	}
}

func TestListDraftsUsesExactScopeMetadataOnlyAndStableKeyset(t *testing.T) {
	_, _, repository := credentialDB(t)
	baseTime := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	versions := make([]credential.StoredVersion, 3)
	for index := range versions {
		stored, intent := testStoredVersion(t)
		stored.Metadata.CreatedAt = baseTime.Add(time.Duration(index) * time.Minute)
		stored.Metadata.Binding.Provider = []string{"postgres", "mysql", "snowflake"}[index]
		stored.Metadata.Binding.Destination = "sha256:" + strings.Repeat(string(rune('a'+index)), 64)
		versions[index] = stored
		if err := repository.ReserveEncryption(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID, testKeyCommitment(stored.Envelope.KeyID)); err != nil {
			t.Fatal(err)
		}
		if err := repository.SaveDraft(t.Context(), stored, intent); err != nil {
			t.Fatal(err)
		}
	}
	newest, oldest := versions[2].Metadata, versions[0].Metadata
	resource := credential.Resource{ScopeKind: newest.Binding.ScopeKind, TargetID: newest.Binding.TargetID, ProjectID: newest.Binding.ProjectID, Environment: newest.Binding.Environment, ResourceID: newest.Binding.ResourceID}
	page, err := repository.ListDrafts(t.Context(), newest.Binding.DeploymentID, newest.Binding.OwnerID, resource, 2, "")
	if err != nil || len(page.Items) != 2 || page.Items[0].Binding.VersionID != newest.Binding.VersionID || page.NextBeforeVersionID != page.Items[1].Binding.VersionID {
		t.Fatalf("first list page = %#v, %v", page, err)
	}
	if page.Items[0].Binding.Provider != "snowflake" || page.Items[0].Binding.Destination != newest.Binding.Destination {
		t.Fatal("list page did not preserve historical provider/destination metadata")
	}
	serialized, err := json.Marshal(page)
	if err != nil || bytes.Contains(serialized, versions[2].Envelope.Ciphertext) || bytes.Contains(serialized, []byte("ciphertext")) {
		t.Fatal("metadata-only list exposed encrypted envelope data")
	}
	page, err = repository.ListDrafts(t.Context(), newest.Binding.DeploymentID, newest.Binding.OwnerID, resource, 2, page.NextBeforeVersionID)
	if err != nil || len(page.Items) != 1 || page.Items[0].Binding.VersionID != oldest.Binding.VersionID || page.NextBeforeVersionID != "" {
		t.Fatalf("continuation page = %#v, %v", page, err)
	}
	if _, err := repository.ListDrafts(t.Context(), newest.Binding.DeploymentID, "other-owner", resource, 2, ""); err != nil {
		t.Fatal(err)
	} else if page, _ := repository.ListDrafts(t.Context(), newest.Binding.DeploymentID, "other-owner", resource, 2, ""); len(page.Items) != 0 {
		t.Fatalf("cross-owner list returned %d drafts", len(page.Items))
	}
	otherResource := resource
	otherResource.ProjectID = "another-project"
	if page, err := repository.ListDrafts(t.Context(), newest.Binding.DeploymentID, newest.Binding.OwnerID, otherResource, 2, ""); err != nil || len(page.Items) != 0 {
		t.Fatalf("cross-resource list = %#v, %v; want empty", page, err)
	}
	if _, err := repository.ListDrafts(t.Context(), newest.Binding.DeploymentID, newest.Binding.OwnerID, resource, 2, uuid.NewString()); !errors.Is(err, credential.ErrInvalidCursor) {
		t.Fatalf("out-of-scope cursor error = %v, want invalid cursor", err)
	}
}

func TestSaveDraftAuditFailureRollsBackVersionAndEnvelopeButKeepsBudget(t *testing.T) {
	db, runtimeDB, _ := credentialDB(t)
	stored, intent := testStoredVersion(t)
	broken, err := New(runtimeDB, credentialFailingAudit{err: errors.New("audit unavailable")})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.ReserveEncryption(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID, testKeyCommitment(stored.Envelope.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := broken.SaveDraft(t.Context(), stored, intent); err == nil {
		t.Fatal("SaveDraft succeeded despite audit failure")
	}
	var versions, envelopes int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.draft_version WHERE version_id=$1`, stored.Metadata.Binding.VersionID).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM credential.envelope WHERE version_id=$1`, stored.Metadata.Binding.VersionID).Scan(&envelopes); err != nil {
		t.Fatal(err)
	}
	var uses int64
	if err := db.QueryRow(t.Context(), `SELECT uses FROM credential.encryption_budget WHERE deployment_id=$1 AND key_id=$2`, stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if versions != 0 || envelopes != 0 || uses != 1 {
		t.Fatalf("after audit failure versions=%d envelopes=%d budget=%d; want 0, 0, 1", versions, envelopes, uses)
	}
}

func TestReserveEncryptionBudgetIsAtomicAtLimit(t *testing.T) {
	db, _, repository := credentialDB(t)
	const deploymentID, keyID = "deployment-prod", "key-prod-1"
	if err := repository.ReserveEncryption(t.Context(), deploymentID, keyID, testKeyCommitment(keyID)); err != nil {
		t.Fatal(err)
	}
	nearLimit := encryption.MaxEncryptions - 3
	setupTx, err := db.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(t.Context(), `ALTER TABLE credential.encryption_budget DISABLE TRIGGER encryption_budget_reservation_guard`); err != nil {
		_ = setupTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(t.Context(), `UPDATE credential.encryption_budget SET uses=$3 WHERE deployment_id=$1 AND key_id=$2`, deploymentID, keyID, nearLimit); err != nil {
		_ = setupTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if _, err := setupTx.Exec(t.Context(), `ALTER TABLE credential.encryption_budget ENABLE TRIGGER encryption_budget_reservation_guard`); err != nil {
		_ = setupTx.Rollback(t.Context())
		t.Fatal(err)
	}
	if err := setupTx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	const workers = 12
	var wg sync.WaitGroup
	results := make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- repository.ReserveEncryption(t.Context(), deploymentID, keyID, testKeyCommitment(keyID))
		}()
	}
	wg.Wait()
	close(results)
	var succeeded, exhausted int
	for err := range results {
		switch {
		case err == nil:
			succeeded++
		case errors.Is(err, encryption.ErrBudgetExhausted):
			exhausted++
		default:
			t.Errorf("ReserveEncryption error = %v", err)
		}
	}
	if succeeded != 3 || exhausted != workers-3 {
		t.Fatalf("reservations succeeded=%d exhausted=%d; want 3 and %d", succeeded, exhausted, workers-3)
	}
	var uses int64
	if err := db.QueryRow(t.Context(), `SELECT uses FROM credential.encryption_budget WHERE deployment_id=$1 AND key_id=$2`, deploymentID, keyID).Scan(&uses); err != nil {
		t.Fatal(err)
	}
	if uses != encryption.MaxEncryptions {
		t.Fatalf("budget uses=%d, want ceiling %d", uses, encryption.MaxEncryptions)
	}
}

func TestEncryptionKeyCommitmentsPreventRebindAndMaterialRelabel(t *testing.T) {
	_, _, repository := credentialDB(t)
	const deploymentID = "deployment-prod"
	oldRing := loadTestCredentialKeyring(t, deploymentID, "key-old", bytes.Repeat([]byte{0x31}, 32))
	oldCommitment, ok := oldRing.KeyCommitment("key-old")
	if !ok {
		t.Fatal("loaded keyring omitted its active key commitment")
	}
	if err := repository.ReserveEncryption(t.Context(), deploymentID, "key-old", oldCommitment); err != nil {
		t.Fatal(err)
	}

	reboundRing := loadTestCredentialKeyring(t, deploymentID, "key-old", bytes.Repeat([]byte{0x32}, 32))
	if err := repository.CheckKeyring(t.Context(), reboundRing); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("same-ID key material change = %v, want conflict", err)
	}
	if err := repository.ReserveEncryption(t.Context(), deploymentID, "key-old", encryption.KeyCommitment(sha256.Sum256(bytes.Repeat([]byte{0x32}, 32)))); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("reserve after same-ID rebind = %v, want conflict", err)
	}

	relabelled := loadTestCredentialKeyring(t, deploymentID, "key-new", bytes.Repeat([]byte{0x31}, 32))
	if err := repository.CheckKeyring(t.Context(), relabelled); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("same material under a new key ID = %v, want conflict", err)
	}
	if err := repository.ReserveEncryption(t.Context(), deploymentID, "key-new", oldCommitment); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("reserve same material under a new key ID = %v, want conflict", err)
	}

	unusedReplacement := loadTestCredentialKeyring(t, deploymentID, "key-new", bytes.Repeat([]byte{0x33}, 32))
	if err := repository.CheckKeyring(t.Context(), unusedReplacement); err != nil {
		t.Fatalf("omitting an unreferenced old key should not require retaining it: %v", err)
	}
}

func TestCredentialServiceEncryptsAndReadsThroughRuntimePostgreSQLRepository(t *testing.T) {
	db, runtimeDB, repository := credentialDB(t)
	keyBytes := bytes.Repeat([]byte{0x39}, 32)
	keyring := loadTestCredentialKeyring(t, "deployment-prod", "key-prod-1", keyBytes)
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}
	destinationHash := sha256.Sum256([]byte("warehouse.internal:5432/analytics"))
	scope := credential.Scope{
		Resource: resource, OwnerID: "customer-a", Purpose: "connection", Provider: "postgres",
		Destination: "sha256:" + fmt.Sprintf("%x", destinationHash),
	}
	service, err := credential.NewService(repository, keyring, testCredentialScopeResolver{scope: scope}, allowCredentialAuthorizer{})
	if err != nil {
		t.Fatal(err)
	}
	const secret = "postgres-password-sentinel"
	actorID := uuid.NewString()
	metadata, err := service.SaveDraft(t.Context(), actorID, resource, map[string]string{"password": secret})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CheckKeyring(t.Context(), keyring); err != nil {
		t.Fatalf("valid keyring preflight: %v", err)
	}
	rebound := loadTestCredentialKeyring(t, "deployment-prod", "key-prod-1", bytes.Repeat([]byte{0x38}, 32))
	if err := repository.CheckKeyring(t.Context(), rebound); !errors.Is(err, credential.ErrConflict) {
		t.Fatalf("rebound keyring preflight = %v, want conflict", err)
	}
	missing := loadTestCredentialKeyring(t, "deployment-prod", "key-prod-2", bytes.Repeat([]byte{0x40}, 32))
	if err := repository.CheckKeyring(t.Context(), missing); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("missing retained key preflight = %v, want unavailable", err)
	}
	stored, err := repository.GetStoredDraft(t.Context(), keyring.DeploymentID(), metadata.Binding.OwnerID, resource, metadata.Binding.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	plaintext, err := keyring.Decrypt(stored.Metadata.Binding, stored.Envelope)
	if err != nil {
		t.Fatalf("decrypt repository envelope with its stored binding: %v", err)
	}
	var fields map[string]string
	if err := json.Unmarshal(plaintext, &fields); err != nil || fields["password"] != secret {
		t.Fatal("repository envelope did not preserve the submitted secret bytes")
	}
	readMetadata, err := service.GetDraft(t.Context(), actorID, resource, metadata.Binding.VersionID)
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(readMetadata)
	if err != nil {
		t.Fatal(err)
	}
	secretDigest := sha256.Sum256(plaintext)
	if bytes.Contains(serialized, []byte(secret)) || bytes.Contains(serialized, []byte(fmt.Sprintf("%x", secretDigest))) || bytes.Contains(serialized, stored.Envelope.Ciphertext) {
		t.Fatal("metadata response exposed secret, ciphertext, or a raw secret digest")
	}
	var auditCount int
	if err := db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE aggregate_key=$1`, "credential:"+metadata.Binding.VersionID).Scan(&auditCount); err != nil {
		t.Fatal(err)
	}
	if auditCount != 1 {
		t.Fatalf("durable audit count = %d, want 1", auditCount)
	}
	var backupSelect, backupInsert bool
	if err := db.QueryRow(t.Context(), `SELECT has_table_privilege('leapview_control_backup', 'credential.envelope', 'SELECT'), has_table_privilege('leapview_control_backup', 'credential.envelope', 'INSERT')`).Scan(&backupSelect, &backupInsert); err != nil {
		t.Fatal(err)
	}
	if !backupSelect || backupInsert {
		t.Fatalf("backup envelope privileges: select=%t insert=%t; want read-only", backupSelect, backupInsert)
	}
	if _, err := runtimeDB.Exec(t.Context(), `UPDATE credential.envelope SET ciphertext=$2 WHERE version_id=$1`, metadata.Binding.VersionID, []byte(strings.Repeat("x", 44))); err == nil {
		t.Fatal("runtime role updated an immutable envelope")
	}
}

func TestCredentialDraftAndEnvelopeRowsAreImmutable(t *testing.T) {
	db, _, repository := credentialDB(t)
	stored, intent := testStoredVersion(t)
	if err := repository.ReserveEncryption(t.Context(), stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID, testKeyCommitment(stored.Envelope.KeyID)); err != nil {
		t.Fatal(err)
	}
	if err := repository.SaveDraft(t.Context(), stored, intent); err != nil {
		t.Fatal(err)
	}
	statements := []struct{ name, sql string }{
		{"version update", `UPDATE credential.draft_version SET owner_id='different' WHERE version_id=$1`},
		{"version delete", `DELETE FROM credential.draft_version WHERE version_id=$1`},
		{"envelope update", `UPDATE credential.envelope SET ciphertext=$2 WHERE version_id=$1`},
		{"envelope delete", `DELETE FROM credential.envelope WHERE version_id=$1`},
		{"budget decrement", `UPDATE credential.encryption_budget SET uses=uses-1 WHERE deployment_id=$1 AND key_id=$2`},
		{"budget delete", `DELETE FROM credential.encryption_budget WHERE deployment_id=$1 AND key_id=$2`},
	}
	for _, test := range statements {
		t.Run(test.name, func(t *testing.T) {
			args := []any{stored.Metadata.Binding.VersionID}
			if test.name == "envelope update" {
				args = append(args, []byte("changed"))
			} else if strings.HasPrefix(test.name, "budget ") {
				args = []any{stored.Metadata.Binding.DeploymentID, stored.Envelope.KeyID}
			}
			if _, err := db.Exec(t.Context(), test.sql, args...); err == nil {
				t.Fatal("immutable row mutation unexpectedly succeeded")
			}
		})
	}
}
