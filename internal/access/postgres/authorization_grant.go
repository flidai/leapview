package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func authorizationGrantCommandDigest(input access.AuthorizationGrantInput) (string, error) {
	encoded, err := json.Marshal(struct {
		Operation string
		Input     access.AuthorizationGrantInput
	}{"upsert-grant", input})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func readAuthorizationPolicyGrants(ctx context.Context, db DBTX, scope access.AuthorizationPolicyScope, revision int64) ([]access.AuthorizationGrant, error) {
	rows, err := accessdb.New(db).ListAuthorizationPolicyGrants(ctx, accessdb.ListAuthorizationPolicyGrantsParams{TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: revision})
	if err != nil {
		return nil, err
	}
	var grants []access.AuthorizationGrant
	for _, row := range rows {
		resource, err := access.NewResourceRef(graph.ResourceID(row.ResourceID), graph.Kind(row.ResourceKind))
		if err != nil {
			return nil, err
		}
		grant := access.AuthorizationGrant{ID: row.ID, Name: row.Name, Subject: access.SubjectRef{Kind: access.SubjectKind(row.SubjectKind), ID: row.SubjectID}, Resource: resource}
		if row.PermissionProfile != nil || row.Permissions != nil {
			if row.PermissionProfile == nil || row.Capability != nil {
				return nil, fmt.Errorf("decode authorization policy grant %q: mixed or incomplete typed representation", row.ID)
			}
			pairs, decodeErr := access.DecodePermissionPairs(row.Permissions)
			if decodeErr != nil {
				return nil, fmt.Errorf("decode authorization policy grant %q permissions: %w", row.ID, decodeErr)
			}
			grant.PermissionProfile, grant.Permissions = *row.PermissionProfile, pairs
		} else {
			if row.Capability == nil {
				return nil, fmt.Errorf("decode authorization policy grant %q: legacy capability is missing", row.ID)
			}
			grant.Capability = access.Capability(*row.Capability)
		}
		if err := access.ValidateAuthorizationGrantForScope(grant, scope); err != nil {
			return nil, err
		}
		grants = append(grants, grant)
	}
	return grants, nil
}
func insertAuthorizationPolicyGrants(ctx context.Context, db DBTX, scope access.AuthorizationPolicyScope, revision int64, grants []access.AuthorizationGrant) error {
	for _, g := range grants {
		params := accessdb.InsertAuthorizationPolicyGrantParams{
			TargetID: scope.TargetID, ProjectID: scope.ProjectID, Environment: scope.Environment, Revision: revision,
			ID: g.ID, Name: g.Name, SubjectKind: string(g.Subject.Kind), SubjectID: g.Subject.ID,
			ResourceKind: string(g.Resource.Kind()), ResourceID: string(g.Resource.ID()),
		}
		if g.PermissionProfile != "" || g.Permissions != nil {
			encoded, err := access.EncodePermissionPairs(g.Permissions)
			if err != nil {
				return fmt.Errorf("encode authorization policy grant %q permissions: %w", g.ID, err)
			}
			params.PermissionProfile = nullableString(g.PermissionProfile)
			params.Permissions = encoded
		} else {
			params.Capability = nullableString(string(g.Capability))
		}
		if err := accessdb.New(db).InsertAuthorizationPolicyGrant(ctx, params); err != nil {
			return err
		}
	}
	return nil
}
func (r *Repository) upsertAuthorizationGrantCore(ctx context.Context, db DBTX, grantInput access.AuthorizationGrantInput) (access.AuthorizationPolicy, error) {
	input := access.AuthorizationRoleBindingInput{Scope: grantInput.Scope, ExpectedRevision: grantInput.ExpectedRevision, IdempotencyKey: grantInput.IdempotencyKey, Binding: access.RoleBinding{ID: grantInput.Grant.ID}}
	if ctx == nil {
		return access.AuthorizationPolicy{}, errors.New("authorization policy context is nil")
	}
	if err := r.validateAuthorizationPolicyScope(input.Scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if input.ExpectedRevision < 0 {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: expected revision cannot be negative", access.ErrAuthorizationPolicyConflict)
	}
	key, err := bounded(input.IdempotencyKey, "authorization policy idempotency key", maxAuthorizationPolicyIdempotencyKeyBytes)
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("%w: %v", access.ErrAuthorizationPolicyIdempotency, err)
	}
	input.IdempotencyKey = key
	requestDigest, err := authorizationGrantCommandDigest(grantInput)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if replay, ok, replayErr := r.checkAuthorizationPolicyOperation(ctx, db, input.Scope, input.IdempotencyKey, requestDigest); ok || replayErr != nil {
		return replay, replayErr
	}
	if err := access.ValidateAuthorizationGrantForScope(grantInput.Grant, grantInput.Scope); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if err := validateAuthorizationPolicySubject(ctx, db, grantInput.Grant.Subject); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	queries := accessdb.New(db)
	headRow, err := queries.LockAuthorizationPolicyHead(ctx, policyScopeLockParams(input.Scope))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.AuthorizationPolicy{}, fmt.Errorf("%w: policy head disappeared", access.ErrAuthorizationPolicyNotFound)
		}
		return access.AuthorizationPolicy{}, fmt.Errorf("lock authorization policy head: %w", err)
	}
	head := policyHeadFromLockRow(headRow)
	// A second caller with the same key can have waited on the head lock after
	// the first caller committed. Re-check idempotency after locking before CAS.
	if replay, ok, replayErr := r.checkAuthorizationPolicyOperation(ctx, db, input.Scope, input.IdempotencyKey, requestDigest); ok || replayErr != nil {
		return replay, replayErr
	}
	if input.ExpectedRevision != head.Revision {
		return access.AuthorizationPolicy{}, staleAuthorizationPolicyRevisionError(input.ExpectedRevision, head.Revision)
	}
	current, err := r.authorizationPolicyAtRevision(ctx, db, input.Scope, head.Revision, head.Digest)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	grants := append([]access.AuthorizationGrant(nil), current.Grants...)
	replaced := false
	for i := range grants {
		if grants[i].ID == grantInput.Grant.ID {
			grants[i] = grantInput.Grant
			replaced = true
		}
	}
	if !replaced {
		grants = append(grants, grantInput.Grant)
	}
	digest, err := access.AuthorizationPolicyDigest(input.Scope, current.RoleBindings, grants...)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	bindings := current.RoleBindings
	nextRevision := head.Revision + 1
	if err := queries.InsertAuthorizationPolicyRevision(ctx, accessdb.InsertAuthorizationPolicyRevisionParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: nextRevision, Digest: digest}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("insert authorization policy revision: %w", err)
	}
	for _, binding := range bindings {
		caps, permissions, profile, permissionRole, marshalErr := authorizationRoleBindingEncoding(binding)
		if marshalErr != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("encode authorization policy role binding %q: %w", binding.ID, marshalErr)
		}
		if err := queries.InsertAuthorizationPolicyRoleBinding(ctx, accessdb.InsertAuthorizationPolicyRoleBindingParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, Revision: nextRevision, ID: binding.ID, SubjectKind: string(binding.Subject.Kind), SubjectID: binding.Subject.ID, Role: nullableString(string(binding.Role)), Capabilities: caps, PermissionProfile: profile, Permissions: permissions, PermissionRole: permissionRole, Name: binding.Name}); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("insert authorization policy role binding %q: %w", binding.ID, err)
		}
	}
	if err := insertAuthorizationPolicyGrants(ctx, db, input.Scope, nextRevision, grants); err != nil {
		return access.AuthorizationPolicy{}, err
	}
	updateTag, err := queries.UpdateAuthorizationPolicyHead(ctx, accessdb.UpdateAuthorizationPolicyHeadParams{Revision: nextRevision, Digest: digest, ExpectedRevision: head.Revision, TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment})
	if err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("advance authorization policy revision: %w", err)
	}
	if updateTag.RowsAffected() != 1 {
		return access.AuthorizationPolicy{}, staleAuthorizationPolicyRevisionError(input.ExpectedRevision, head.Revision)
	}
	if err := queries.InsertAuthorizationPolicyOperation(ctx, accessdb.InsertAuthorizationPolicyOperationParams{TargetID: input.Scope.TargetID, ProjectID: input.Scope.ProjectID, Environment: input.Scope.Environment, IdempotencyKey: input.IdempotencyKey, RequestDigest: requestDigest, Revision: nextRevision, PolicyDigest: digest, BindingID: input.Binding.ID}); err != nil {
		return access.AuthorizationPolicy{}, fmt.Errorf("record authorization policy idempotency: %w", err)
	}
	return (&Repository{db: db}).authorizationPolicyAtRevision(ctx, db, input.Scope, nextRevision, digest)
}

func (r *Repository) UpsertAuthorizationGrant(ctx context.Context, input access.AuthorizationGrantInput) (result access.AuthorizationPolicy, err error) {
	tx, owned, err := r.txOrBegin(ctx)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if owned {
		defer func() { _ = tx.Rollback(ctx) }()
	}
	result, err = r.upsertAuthorizationGrantCore(ctx, tx, input)
	if err != nil {
		return access.AuthorizationPolicy{}, err
	}
	if owned {
		if err := tx.Commit(ctx); err != nil {
			return access.AuthorizationPolicy{}, fmt.Errorf("commit authorization policy upsert: %w", err)
		}
	}
	return result, nil
}
