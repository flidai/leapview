package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/credential"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func retirementResource(stored credential.StoredVersion) credential.Resource {
	b := stored.Metadata.Binding
	return credential.Resource{ScopeKind: b.ScopeKind, TargetID: b.TargetID, ProjectID: b.ProjectID, Environment: b.Environment, ResourceID: b.ResourceID}
}
func noRetirementDependencies(context.Context, pgx.Tx, credential.Metadata) ([]credential.VersionDependency, error) {
	return nil, nil
}

func TestRetirementPreservesEnvelopeButRejectsResolveAndNewValidation(t *testing.T) {
	db, runtime, repository := credentialDB(t)
	stored, audit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, audit)
	b := stored.Metadata.Binding
	resource := retirementResource(stored)
	tx, err := runtime.Begin(t.Context())
	require.NoError(t, err)
	status, err := repository.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, resource, b.VersionID, noRetirementDependencies)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	require.Equal(t, "retired_local", status.State)
	require.False(t, status.RetiredAt.IsZero())
	_, err = repository.GetStoredDraft(t.Context(), b.DeploymentID, b.OwnerID, resource, b.VersionID)
	require.ErrorIs(t, err, credential.ErrNotFound)
	receipt, intent := postgresValidationReceipt(t, stored)
	require.Error(t, repository.SaveValidation(t.Context(), receipt, intent))
	var envelopes, audits int
	require.NoError(t, db.QueryRow(t.Context(), "SELECT count(*) FROM credential.envelope WHERE version_id=$1", b.VersionID).Scan(&envelopes))
	require.Equal(t, 1, envelopes)
	require.NoError(t, db.QueryRow(t.Context(), "SELECT count(*) FROM audit.audit_event WHERE action='credential.version.retired_local' AND metadata->>'version_id'=$1", b.VersionID).Scan(&audits))
	require.Equal(t, 1, audits)
	tx, err = runtime.Begin(t.Context())
	require.NoError(t, err)
	replay, err := repository.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, resource, b.VersionID, noRetirementDependencies)
	require.NoError(t, err)
	require.Equal(t, status, replay)
	require.NoError(t, tx.Commit(t.Context()))
	_, err = runtime.Exec(t.Context(), "DELETE FROM credential.version_retirement WHERE version_id=$1", b.VersionID)
	require.Error(t, err)
}

func TestRetirementRejectsDependenciesAndAuditFailureAtomically(t *testing.T) {
	db, runtime, repository := credentialDB(t)
	stored, audit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, audit)
	b := stored.Metadata.Binding
	resource := retirementResource(stored)
	tx, err := runtime.Begin(t.Context())
	require.NoError(t, err)
	status, err := repository.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, resource, b.VersionID, func(context.Context, pgx.Tx, credential.Metadata) ([]credential.VersionDependency, error) {
		return []credential.VersionDependency{{Kind: "agent_configuration", ID: "7"}}, nil
	})
	require.ErrorIs(t, err, credential.ErrConflict)
	require.Len(t, status.Dependencies, 1)
	require.NoError(t, tx.Commit(t.Context()))
	broken, err := New(runtime, credentialFailingAudit{err: errors.New("audit interrupted")})
	require.NoError(t, err)
	tx, err = runtime.Begin(t.Context())
	require.NoError(t, err)
	_, err = broken.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, resource, b.VersionID, noRetirementDependencies)
	require.Error(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	var count int
	require.NoError(t, db.QueryRow(t.Context(), "SELECT count(*) FROM credential.version_retirement").Scan(&count))
	require.Zero(t, count)
	_, err = repository.GetStoredDraft(t.Context(), b.DeploymentID, b.OwnerID, resource, b.VersionID)
	require.NoError(t, err)
}

func TestRetirementSerializesWithValidationWriter(t *testing.T) {
	_, runtime, repository := credentialDB(t)
	stored, audit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, audit)
	b := stored.Metadata.Binding
	resource := retirementResource(stored)
	tx, err := runtime.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	_, err = repository.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, resource, b.VersionID, noRetirementDependencies)
	require.NoError(t, err)
	receipt, intent := postgresValidationReceipt(t, stored)
	done := make(chan error, 1)
	go func() { done <- repository.SaveValidation(t.Context(), receipt, intent) }()
	select {
	case err := <-done:
		t.Fatalf("validation bypassed uncommitted version fence: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, tx.Commit(t.Context()))
	select {
	case err := <-done:
		require.Error(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("validation did not observe committed retirement")
	}
}

func TestRetirementReportsLiveReceiptWithoutConsumingIt(t *testing.T) {
	_, runtime, repository := credentialDB(t)
	stored, audit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, audit)
	receipt, intent := postgresValidationReceipt(t, stored)
	require.NoError(t, repository.SaveValidation(t.Context(), receipt, intent))
	b := stored.Metadata.Binding
	tx, err := runtime.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	status, err := repository.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, retirementResource(stored), b.VersionID, noRetirementDependencies)
	require.ErrorIs(t, err, credential.ErrConflict)
	require.Equal(t, []credential.VersionDependency{{Kind: "live_validation_receipt", ID: receipt.ReceiptID}}, status.Dependencies)
}

func TestAgentVersionRetirementUsesInstanceScopedAudit(t *testing.T) {
	db, runtime, repository := credentialDB(t)
	stored, audit := testStoredVersion(t)
	b := &stored.Metadata.Binding
	b.ScopeKind = "agent"
	b.ResourceID = b.DeploymentID
	b.TargetID = ""
	b.ProjectID = ""
	b.Environment = ""
	b.Purpose = "agent-provider"
	b.Provider = "openai"
	audit.ScopeID = b.DeploymentID
	audit.ResourceKind = "instance"
	audit.ResourceID = b.ResourceID
	metadata, _ := json.Marshal(map[string]string{"purpose": b.Purpose, "version_id": b.VersionID})
	audit.MetadataJSON = string(metadata)
	saveValidationDraft(t, repository, stored, audit)
	tx, err := runtime.Begin(t.Context())
	require.NoError(t, err)
	status, err := repository.RetireVersionTx(t.Context(), tx, b.DeploymentID, b.OwnerID, stored.Metadata.ActorID, retirementResource(stored), b.VersionID, noRetirementDependencies)
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	require.Equal(t, "retired_local", status.State)
	var count int
	require.NoError(t, db.QueryRow(t.Context(), `SELECT count(*) FROM audit.audit_event WHERE operation='retireAgentCredentialVersion' AND action='credential.agent_version.retired_local' AND scope_id=$1 AND resource_id=$1 AND resource_kind='instance'`, b.DeploymentID).Scan(&count))
	require.Equal(t, 1, count)
}

func TestRetirementMissingVersionReturnsNotFoundWithoutEnumeratingOtherOwner(t *testing.T) {
	_, runtime, repository := credentialDB(t)
	stored, audit := testStoredVersion(t)
	saveValidationDraft(t, repository, stored, audit)
	b := stored.Metadata.Binding
	tx, err := runtime.Begin(t.Context())
	require.NoError(t, err)
	defer tx.Rollback(context.Background())
	_, err = repository.InspectVersionTx(t.Context(), tx, b.DeploymentID, "other-owner", retirementResource(stored), b.VersionID, noRetirementDependencies)
	require.ErrorIs(t, err, credential.ErrNotFound)
}
