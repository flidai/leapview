package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func (r *Repository) CreateExecutionGrant(ctx context.Context, in access.ExecutionGrantInput) (result access.ExecutionGrant, err error) {
	if err = in.Validate(); err != nil {
		return result, err
	}
	issued, expires, err := grantTimes(in.IssuedAt, in.ExpiresAt, in.TTL, true)
	if err != nil {
		return result, err
	}
	in.IssuedAt, in.ExpiresAt = issued, expires
	if in.ID == "" {
		in.ID, err = newUUID()
		if err != nil {
			return result, err
		}
	}
	fingerprint, err := access.ExecutionGrantFingerprint(in, issued, expires)
	if err != nil {
		return result, err
	}
	requestDigest := grantRequestDigest(in.RequestDigest, fingerprint)
	var inserted bool
	err = r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		txAuthority, ok := txRepo.(*Repository)
		if !ok {
			return nil, errors.New("execution grant transaction authority is unavailable")
		}
		result, inserted, err = txAuthority.insertExecutionGrant(ctx, in, fingerprint, requestDigest)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return []access.AuditEventInput{grantAuditInput(result.Issuer.PrincipalID, "execution_grant.replayed", result.Target.ProjectID.String(), result.Target.ResourceID.String(), result.Target.ResourceKind, result.Fingerprint, result.ExecutionPrincipalID)}, nil
		}
		return []access.AuditEventInput{grantAuditInput(result.Issuer.PrincipalID, "execution_grant.issued", result.Target.ProjectID.String(), result.Target.ResourceID.String(), result.Target.ResourceKind, result.Fingerprint, result.ExecutionPrincipalID)}, nil
	})
	return result, err
}

func (r *Repository) insertExecutionGrant(ctx context.Context, in access.ExecutionGrantInput, fingerprint, requestDigest string) (access.ExecutionGrant, bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.ExecutionGrant{}, false, err
	}
	if err := r.checkGrantIssuance(ctx, db, in.Issuer, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: in.ExecutionPrincipalID}, in.IssuancePermissions, in.IssuancePolicy, in.Target.ProjectID, in.Target.InstanceID); err != nil {
		return access.ExecutionGrant{}, false, err
	}
	permissions, err := access.EncodePermissionPairs(in.Permissions)
	if err != nil {
		return access.ExecutionGrant{}, false, err
	}
	resourceUID, err := pgUUID(in.Target.ResourceUID)
	if err != nil {
		return access.ExecutionGrant{}, false, err
	}
	issuerPrincipalID, err := pgUUID(in.Issuer.PrincipalID)
	if err != nil {
		return access.ExecutionGrant{}, false, err
	}
	executionPrincipalID, err := pgUUID(in.ExecutionPrincipalID)
	if err != nil {
		return access.ExecutionGrant{}, false, err
	}
	tag, err := accessdb.New(db).InsertExecutionGrant(ctx, accessdb.InsertExecutionGrantParams{
		ID: in.ID, Profile: access.DurableGrantProfile, InstanceID: in.Target.InstanceID,
		ProjectID: in.Target.ProjectID.String(), ResourceUid: resourceUID, ResourceID: in.Target.ResourceID.String(),
		ResourceKind: string(in.Target.ResourceKind), IssuerPrincipalID: issuerPrincipalID,
		IssuerCredentialClass: in.Issuer.Credential.Class, IssuerCredentialID: in.Issuer.Credential.ID,
		IssuerCredentialFingerprint: in.Issuer.Credential.Fingerprint, ExecutionPrincipalID: executionPrincipalID,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: permissions, WorkflowID: in.WorkflowID,
		WorkflowRevision: in.WorkflowRevision, ClosureDigest: in.ClosureDigest, BindingDigest: in.BindingDigest,
		DestinationDigest: in.DestinationDigest, TriggerDigest: in.TriggerDigest, IssuedAt: pgTimestamp(in.IssuedAt),
		ExpiresAt: pgTimestamp(in.ExpiresAt), Fingerprint: fingerprint, IdempotencyKey: in.IdempotencyKey,
		RequestDigest: requestDigest,
	})
	if err != nil {
		return access.ExecutionGrant{}, false, durableResourceTargetError(err)
	}
	if tag.RowsAffected() == 0 {
		existing, getErr := r.executionGrantByIdempotency(ctx, db, in.Issuer.PrincipalID, in.Issuer.Credential.ID, in.IdempotencyKey)
		if getErr != nil {
			return access.ExecutionGrant{}, false, getErr
		}
		if existing.RequestDigest != requestDigest {
			return access.ExecutionGrant{}, false, access.ErrGrantIdempotencyConflict
		}
		return existing, false, nil
	}
	grant, err := r.executionGrantByID(ctx, db, in.ID)
	return grant, true, err
}

func (r *Repository) executionGrant(ctx context.Context, id string) (access.ExecutionGrant, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.ExecutionGrant{}, err
	}
	return r.executionGrantByID(ctx, db, id)
}

func (r *Repository) RevokeExecutionGrant(ctx context.Context, id, actorID, reason string) error {
	return r.revokeDurableGrant(ctx, "execution_grant", id, actorID, reason)
}

func (r *Repository) CurrentExecutionGrant(ctx context.Context, id, executionPrincipalID string) (access.ExecutionGrant, error) {
	grant, err := r.executionGrant(ctx, id)
	if err != nil {
		return grant, err
	}
	if executionPrincipalID != "" && grant.ExecutionPrincipalID != executionPrincipalID {
		return access.ExecutionGrant{}, access.ErrGrantPrincipalInactive
	}
	if err := r.checkCurrentGrant(ctx, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: grant.ExecutionPrincipalID}, grant.Target, grant.RevokedAt, grant.ExpiresAt); err != nil {
		return access.ExecutionGrant{}, err
	}
	return grant, nil
}

func (r *Repository) executionGrantByID(ctx context.Context, db DBTX, id string) (access.ExecutionGrant, error) {
	row, err := accessdb.New(db).GetExecutionGrant(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ExecutionGrant{}, access.ErrGrantNotFound
		}
		return access.ExecutionGrant{}, err
	}
	var grant access.ExecutionGrant
	var uid, kind, issuerPrincipal, executionPrincipal, profile, revokedBy string
	var permissionsJSON []byte
	var issuerClass, issuerID, issuerFP, workflow, workflowRevision, closure, binding, destination, trigger, fingerprint, idem, requestDigest, reason string
	var issued, expires, revoked time.Time
	grant.ID, grant.Profile, grant.Target.InstanceID, grant.Target.ProjectID = row.ID, row.Profile, row.InstanceID, projectgraph.ResourceID(row.ProjectID)
	uid, grant.Target.ResourceID, kind, issuerPrincipal = row.ResourceUid, projectgraph.ResourceID(row.ResourceID), row.ResourceKind, row.IssuerPrincipalID
	issuerClass, issuerID, issuerFP, executionPrincipal, profile, permissionsJSON = row.IssuerCredentialClass, row.IssuerCredentialID, row.IssuerCredentialFingerprint, row.ExecutionPrincipalID, row.PermissionProfile, row.Permissions
	workflow, workflowRevision, closure, binding, destination, trigger = row.WorkflowID, row.WorkflowRevision, row.ClosureDigest, row.BindingDigest, row.DestinationDigest, row.TriggerDigest
	issued, expires, fingerprint, idem, requestDigest = durableGrantTime(row.IssuedAt), durableGrantTime(row.ExpiresAt), row.Fingerprint, row.IdempotencyKey, row.RequestDigest
	revoked, revokedBy, reason = durableGrantTime(row.RevokedAt), row.RevokedByPrincipalID, row.RevocationReason
	grant.Target.ResourceUID, grant.Target.ResourceKind = uid, projectgraph.Kind(kind)
	grant.Issuer = access.GrantIssuerEvidence{PrincipalID: issuerPrincipal, Credential: access.GrantCredentialEvidence{Class: issuerClass, ID: issuerID, Fingerprint: issuerFP}}
	grant.ExecutionPrincipalID, grant.Permissions, grant.IssuedAt, grant.ExpiresAt = executionPrincipal, nil, issued, expires
	grant.WorkflowID, grant.WorkflowRevision, grant.ClosureDigest, grant.BindingDigest, grant.DestinationDigest, grant.TriggerDigest = workflow, workflowRevision, closure, binding, destination, trigger
	grant.Fingerprint, grant.IdempotencyKey, grant.RequestDigest, grant.RevokedAt, grant.RevokedByPrincipalID, grant.RevocationReason = fingerprint, idem, requestDigest, revoked, revokedBy, reason
	if revoked.Equal(time.Unix(0, 0).UTC()) {
		grant.RevokedAt = time.Time{}
	}
	decoded, err := access.DecodePermissionPairs(permissionsJSON)
	if err != nil || grant.Profile != access.DurableGrantProfile || profile != access.PermissionCatalogProfile {
		return access.ExecutionGrant{}, fmt.Errorf("%w: persisted execution permissions: %v", access.ErrInvalidDurableGrant, err)
	}
	if err := grant.Target.Validate(); err != nil {
		return access.ExecutionGrant{}, fmt.Errorf("%w: persisted execution target: %v", access.ErrInvalidDurableGrant, err)
	}
	if err := grant.Issuer.Validate(); err != nil {
		return access.ExecutionGrant{}, fmt.Errorf("%w: persisted execution issuer: %v", access.ErrInvalidDurableGrant, err)
	}
	grant.Permissions = decoded
	return grant, nil
}

func (r *Repository) executionGrantByIdempotency(ctx context.Context, db DBTX, issuer, credential, key string) (access.ExecutionGrant, error) {
	issuerID, err := pgUUID(issuer)
	if err != nil {
		return access.ExecutionGrant{}, err
	}
	id, err := accessdb.New(db).GetExecutionGrantByIdempotency(ctx, accessdb.GetExecutionGrantByIdempotencyParams{IssuerPrincipalID: issuerID, IssuerCredentialID: credential, IdempotencyKey: key})
	if err != nil {
		return access.ExecutionGrant{}, err
	}
	return r.executionGrantByID(ctx, db, id)
}
