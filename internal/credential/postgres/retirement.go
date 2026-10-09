package postgres

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	"github.com/flidai/leapview/internal/credential/encryption"
	credentialdb "github.com/flidai/leapview/internal/credential/postgres/internal/db"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// RetirementAuthorizer acquires the live target/identity fences and returns
// references owned by other capabilities. It must include retained history,
// not just the currently selected serving generation or configuration.
type RetirementAuthorizer func(context.Context, pgx.Tx, credential.Metadata) ([]credential.VersionDependency, error)

func (r *Repository) versionStatusTx(ctx context.Context, tx pgx.Tx, deployment, owner string, resource credential.Resource, version string, authorize RetirementAuthorizer) (credential.Metadata, credential.VersionStatus, error) {
	var metadata credential.Metadata
	status := credential.VersionStatus{VersionID: version, State: "available", Dependencies: []credential.VersionDependency{}}
	if r == nil || ctx == nil || typednil.IsNil(tx) || authorize == nil || !canonical(deployment, 255) || !canonical(owner, 255) || resource.Validate() != nil || !canonicalPreparationUUID(version) {
		return metadata, status, credential.ErrInvalid
	}
	queries := credentialdb.New(tx)
	row, err := queries.GetDraftMetadata(ctx, credentialdb.GetDraftMetadataParams{DeploymentID: deployment, OwnerID: owner, ScopeKind: resource.ScopeKind, TargetID: resource.TargetID, ProjectID: resource.ProjectID, Environment: resource.Environment, ResourceID: resource.ResourceID, VersionID: version})
	if errors.Is(err, pgx.ErrNoRows) {
		return metadata, status, credential.ErrNotFound
	}
	if err != nil {
		return metadata, status, normalizeDatabaseError(err)
	}
	metadata = credential.Metadata{Binding: encryption.Binding{DeploymentID: row.DeploymentID, OwnerID: row.OwnerID, ScopeKind: row.ScopeKind, TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment, ResourceID: row.ResourceID, Purpose: row.Purpose, Provider: row.Provider, Destination: row.Destination, VersionID: row.VersionID}, ActorID: row.ActorID, CreatedAt: row.CreatedAt}
	// The authorizer locks the publication fence before the version lock.
	// Query references again after the version lock so a just-committed writer
	// is visible in this required READ COMMITTED transaction.
	if _, err = authorize(ctx, tx, metadata); err != nil {
		return metadata, status, err
	}
	if err = queries.LockCredentialVersion(ctx, credentialdb.LockCredentialVersionParams{DeploymentID: deployment, VersionID: version}); err != nil {
		return metadata, status, normalizeDatabaseError(err)
	}
	dependencies, err := authorize(ctx, tx, metadata)
	if err != nil {
		return metadata, status, err
	}
	retired, err := queries.GetCredentialRetirement(ctx, credentialdb.GetCredentialRetirementParams{DeploymentID: deployment, VersionID: version})
	if err == nil {
		status.State = "retired_local"
		status.RetiredAt = retired.RetiredAt
		return metadata, status, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return metadata, status, normalizeDatabaseError(err)
	}
	rows, err := queries.CredentialVersionDependencies(ctx, credentialdb.CredentialVersionDependenciesParams{DeploymentID: deployment, VersionID: version})
	if err != nil {
		return metadata, status, normalizeDatabaseError(err)
	}
	for _, row := range rows {
		dependencies = append(dependencies, credential.VersionDependency{Kind: row.DependencyKind, ID: row.DependencyID})
	}
	status.MoreDependencies = len(dependencies) > 100
	status.Dependencies = append(status.Dependencies, dependencies[:min(100, len(dependencies))]...)
	return metadata, status, nil
}

func (r *Repository) InspectVersionTx(ctx context.Context, tx pgx.Tx, deployment, owner string, resource credential.Resource, version string, authorize RetirementAuthorizer) (credential.VersionStatus, error) {
	_, status, err := r.versionStatusTx(ctx, tx, deployment, owner, resource, version, authorize)
	return status, err
}

func (r *Repository) RetireVersionTx(ctx context.Context, outer pgx.Tx, deployment, owner, actor string, resource credential.Resource, version string, authorize RetirementAuthorizer) (credential.VersionStatus, error) {
	var status credential.VersionStatus
	if r == nil || typednil.IsNil(r.audit) || typednil.IsNil(outer) || ctx == nil || !canonical(actor, 255) {
		return status, credential.ErrInvalid
	}
	tx, err := outer.Begin(ctx)
	if err != nil {
		return status, err
	}
	defer tx.Rollback(context.Background())
	metadata, status, err := r.versionStatusTx(ctx, tx, deployment, owner, resource, version, authorize)
	if err != nil {
		return status, err
	}
	if status.State == "retired_local" {
		return status, tx.Commit(ctx)
	}
	if len(status.Dependencies) > 0 || status.MoreDependencies {
		return status, credential.ErrConflict
	}
	payload, err := json.Marshal(struct {
		Version string `json:"version_id"`
		State   string `json:"state"`
	}{version, "retired_local"})
	if err != nil {
		return status, err
	}
	scope, kind := metadata.Binding.ProjectID, "connection"
	operation, action := "retireCredentialVersion", "credential.version.retired_local"
	if resource.ScopeKind == "agent" {
		scope, kind = deployment, "instance"
		operation, action = "retireAgentCredentialVersion", "credential.agent_version.retired_local"
	}
	principal := ""
	if id, e := uuid.Parse(actor); e == nil && id.String() == actor {
		principal = actor
	}
	event := uuid.New()
	intent, err := (access.AuditIntent{EventID: event.String(), ScopeID: scope, ActorID: actor, PrincipalID: principal, Source: "credential", Operation: operation, Action: action, ResourceKind: kind, ResourceID: resource.ResourceID, Outcome: "success", AggregateKey: "credential-retirement:" + version, AggregateSequence: 1, MetadataJSON: string(payload)}).Canonicalize()
	if err != nil {
		return status, err
	}
	if err = r.audit.RecordAuditEvent(ctx, tx, intent); err != nil {
		return status, err
	}
	row, err := credentialdb.New(tx).InsertCredentialRetirement(ctx, credentialdb.InsertCredentialRetirementParams{DeploymentID: deployment, VersionID: version, RetiredBy: actor, AuditID: pgtype.UUID{Bytes: event, Valid: true}})
	if err != nil {
		return status, normalizeDatabaseError(err)
	}
	status.State, status.RetiredAt = "retired_local", row.RetiredAt
	return status, tx.Commit(ctx)
}
