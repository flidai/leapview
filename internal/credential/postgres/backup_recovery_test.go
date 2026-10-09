package postgres

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func runCredentialBackupTool(t *testing.T, name string, pool *pgxpool.Pool, args ...string) {
	t.Helper()
	cfg := pool.Config().ConnConfig
	require.Nil(t, cfg.TLSConfig, "this disposable loopback fixture must not downgrade a TLS configuration")
	command := exec.CommandContext(t.Context(), name, args...)
	command.Env = append(os.Environ(), "PGHOST="+cfg.Host, "PGPORT="+strconv.Itoa(int(cfg.Port)), "PGUSER="+cfg.User, "PGPASSWORD="+cfg.Password, "PGDATABASE="+cfg.Database, "PGSSLMODE=disable")
	// Never surface database credentials or private dump contents in test logs.
	require.NoError(t, command.Run(), "private PostgreSQL backup command failed")
}

func TestRetainedDatabaseBackupRecoversOnlyWithIndependentHistoricalKey(t *testing.T) {
	db, runtime, repository := credentialDB(t)
	maintenance := rotationMaintenancePool(t, db, runtime)
	operator, err := NewRotationRepository(t.Context(), maintenance)
	require.NoError(t, err)
	old := loadTestCredentialKeyring(t, "deployment-prod", "old-key", bytes.Repeat([]byte{0x41}, 32))
	resource := credential.Resource{ScopeKind: "connection", TargetID: "target", ProjectID: "project", Environment: "production", ResourceID: "connection"}
	scope := credential.Scope{Resource: resource, OwnerID: "customer", Purpose: "connection", Provider: "postgres", Destination: "sha256:" + string(bytes.Repeat([]byte{'a'}, 64))}
	service, err := credential.NewService(repository, old, testCredentialScopeResolver{scope: scope}, allowCredentialAuthorizer{})
	require.NoError(t, err)
	metadata, err := service.SaveDraft(t.Context(), uuid.NewString(), resource, map[string]string{"password": "independent-backup-only-secret"})
	require.NoError(t, err)
	// Copy to an independent private key file before the database backup. The
	// restored reader must reopen this file; no live keyring is passed to it.
	keyRoot, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	keyCopy := filepath.Join(keyRoot, "historical-keyring.json")
	require.NoError(t, os.WriteFile(keyCopy, []byte(`{"format":"credential-keyring-v1","deployment_id":"deployment-prod","active_write_key_id":"old-key","keys":[{"key_id":"old-key","key_base64":"QUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUFBQUE=","state":"active_write"}]}`), 0600))
	dumpRoot := t.TempDir()
	archive := filepath.Join(dumpRoot, "control.dump")
	runCredentialBackupTool(t, "pg_dump", db, "--format=custom", "--no-owner", "--no-acl", "--file="+archive)
	require.NoError(t, os.Chmod(archive, 0600))
	rotated := rotationReplacementKeys(t, true)
	progress, err := credential.RotateEnvelopes(t.Context(), operator, rotated, 100)
	require.NoError(t, err)
	require.True(t, progress.Complete)
	live, err := repository.GetStoredDraft(t.Context(), old.DeploymentID(), scope.OwnerID, resource, metadata.Binding.VersionID)
	require.NoError(t, err)
	require.Equal(t, "new-key", live.Envelope.KeyID)
	restoredName := "credential_restore_" + uuid.NewString()[:8]
	_, err = db.Exec(t.Context(), "CREATE DATABASE "+pgx.Identifier{restoredName}.Sanitize())
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = db.Exec(context.Background(), "DROP DATABASE "+pgx.Identifier{restoredName}.Sanitize()+" WITH (FORCE)")
	})
	cfg := db.Config().Copy()
	cfg.ConnConfig.Database = restoredName
	restored, err := pgxpool.NewWithConfig(t.Context(), cfg)
	require.NoError(t, err)
	t.Cleanup(restored.Close)
	runCredentialBackupTool(t, "pg_restore", restored, "--exit-on-error", "--no-owner", "--no-acl", "--dbname="+restoredName, archive)
	recoveredRepository, err := New(restored, credentialAuditAdapter{repository: accesspostgres.New()})
	require.NoError(t, err)
	stored, err := recoveredRepository.GetStoredDraft(t.Context(), old.DeploymentID(), scope.OwnerID, resource, metadata.Binding.VersionID)
	require.NoError(t, err)
	require.Equal(t, "old-key", stored.Envelope.KeyID)
	newOnly := rotationReplacementKeys(t, false)
	_, err = newOnly.Decrypt(stored.Metadata.Binding, stored.Envelope)
	require.Error(t, err, "active replacement key must not decrypt retained backup")
	recoveredKeys, err := encryption.Load(keyCopy)
	require.NoError(t, err)
	plaintext, err := recoveredKeys.Decrypt(stored.Metadata.Binding, stored.Envelope)
	require.NoError(t, err)
	require.True(t, bytes.Contains(plaintext, []byte("independent-backup-only-secret")))
	clear(plaintext)
	var auditSecret bool
	require.NoError(t, restored.QueryRow(t.Context(), `SELECT EXISTS(SELECT 1 FROM audit.audit_event WHERE metadata::text LIKE '%independent-backup-only-secret%')`).Scan(&auditSecret))
	require.False(t, auditSecret)
}
