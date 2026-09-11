package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

func validateServingPolicyInitialization(scope access.AuthorizationPolicyScope, generationID string, bindings []access.RoleBinding) (string, error) {
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return "", err
	}
	if generationID == "" || generationID != strings.TrimSpace(generationID) || len(generationID) > 255 || strings.ContainsAny(generationID, "\x00\r\n\t") {
		return "", fmt.Errorf("%w: active generation id is invalid", access.ErrAuthorizationPolicyInvalidScope)
	}
	digest, err := access.AuthorizationPolicyDigest(scope, bindings)
	if err != nil {
		return "", fmt.Errorf("digest active serving authorization policy: %w", err)
	}
	return digest, nil
}

func initializeAuthorizationPolicyFromServingPolicyDB(
	ctx context.Context,
	db DBTX,
	scope access.AuthorizationPolicyScope,
	generationID, digest string,
	bindings []access.RoleBinding,
) (access.AuthorizationPolicy, error) {
	queries := accessdb.New(db)
	head, err := queries.GetAuthorizationPolicyHead(ctx, policyScopeParams(scope))
	if err == nil {
		return (&Repository{db: db}).authorizationPolicyAtRevision(ctx, db, scope, head.Revision, head.Digest)
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, fmt.Errorf("read authorization policy upgrade head: %w", err)
	}
	insertTag, err := queries.InsertAuthorizationPolicyHead(ctx, accessdb.InsertAuthorizationPolicyHeadParams{
		TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: 1, Digest: digest,
	})
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("create upgraded authorization policy head: %w", err)
	}
	if insertTag.RowsAffected() == 0 {
		lockedHead, lockErr := queries.LockAuthorizationPolicyHead(ctx, policyScopeLockParams(scope))
		if lockErr != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("lock concurrently initialized authorization policy: %w", lockErr)
		}
		return (&Repository{db: db}).authorizationPolicyAtRevision(ctx, db, scope, lockedHead.Revision, lockedHead.Digest)
	}
	if err := queries.InsertAuthorizationPolicyRevisionFromGeneration(ctx, accessdb.InsertAuthorizationPolicyRevisionFromGenerationParams{
		TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: 1, Digest: digest, SourceGenerationID: &generationID,
	}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("insert upgraded authorization policy revision: %w", err)
	}
	for _, binding := range bindings {
		capabilities, marshalErr := json.Marshal(binding.Capabilities)
		if marshalErr != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("encode upgraded role binding %q: %w", binding.ID, marshalErr)
		}
		if err := queries.InsertAuthorizationPolicyRoleBinding(ctx, accessdb.InsertAuthorizationPolicyRoleBindingParams{
			TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: 1,
			ID: binding.ID, Name: binding.Name, SubjectKind: string(binding.Subject.Kind), SubjectID: binding.Subject.ID,
			Role: string(binding.Role), Capabilities: capabilities,
		}); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("insert upgraded role binding %q: %w", binding.ID, err)
		}
	}
	return access.AuthorizationPolicy{Scope: scope, Revision: 1, Digest: digest, RoleBindings: cloneAuthorizationRoleBindings(bindings)}, nil
}

// InitializeAuthorizationPolicyFromServingPolicy establishes revision one
// from an already-active generation's immutable serving-policy document. The
// caller must decode and validate that authoritative document; this repository
// persists exactly the supplied role bindings and records their source
// generation. It never re-resolves subjects or invents bindings.
func (r *Repository) InitializeAuthorizationPolicyFromServingPolicy(
	ctx context.Context,
	scope access.AuthorizationPolicyScope,
	generationID string,
	bindings []access.RoleBinding,
) (result access.AuthorizationPolicy, err error) {
	if err := r.validateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	digest, err := validateServingPolicyInitialization(scope, generationID, bindings)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	result, err = initializeAuthorizationPolicyFromServingPolicyDB(ctx, tx, scope, generationID, digest, bindings)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("commit authorization policy upgrade: %w", err)
		}
	}
	return result, nil
}

// AuthorizationPolicyTx reads the current policy through a caller-owned
// transaction so an application coordinator can bind it to another authority
// lock without opening a sibling transaction.
func AuthorizationPolicyTx(ctx context.Context, tx Tx, scope access.AuthorizationPolicyScope) (access.AuthorizationPolicy, error) {
	if tx == nil {
		return access.AuthorizationPolicy{}, errors.New("authorization policy PostgreSQL transaction is required")
	}
	if err := access.ValidateAuthorizationPolicyScope(scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	row, err := accessdb.New(tx).GetAuthorizationPolicyHead(ctx, policyScopeParams(scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: target=%s project=%s environment=%s", access.ErrAuthorizationPolicyNotFound, scope.TargetID, scope.ProjectID, scope.Environment)
	}
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("read authorization policy head: %w", err)
	}
	return (&Repository{db: tx}).authorizationPolicyAtRevision(ctx, tx, scope, row.Revision, row.Digest)
}

// InitializeAuthorizationPolicyFromServingPolicyTx performs the upgrade write
// inside the caller's transaction. The caller is responsible for holding the
// matching delivery-target share lock through commit.
func InitializeAuthorizationPolicyFromServingPolicyTx(
	ctx context.Context,
	tx Tx,
	scope access.AuthorizationPolicyScope,
	generationID string,
	bindings []access.RoleBinding,
) (access.AuthorizationPolicy, error) {
	if tx == nil {
		return access.AuthorizationPolicy{}, errors.New("authorization policy PostgreSQL transaction is required")
	}
	digest, err := validateServingPolicyInitialization(scope, generationID, bindings)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	return initializeAuthorizationPolicyFromServingPolicyDB(ctx, tx, scope, generationID, digest, bindings)
}
