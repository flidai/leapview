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

func (r *Repository) CreateResourceShareGrant(ctx context.Context, in access.ResourceShareGrantInput) (result access.ResourceShareGrant, err error) {
	if err = in.Validate(); err != nil {
		return result, err
	}
	issued, expires, err := grantTimes(in.IssuedAt, in.ExpiresAt, in.TTL, false)
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
	fingerprint, err := access.ResourceShareGrantFingerprint(in, issued, expires)
	if err != nil {
		return result, err
	}
	requestDigest := grantRequestDigest(in.RequestDigest, fingerprint)
	var inserted bool
	err = r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		txAuthority, ok := txRepo.(*Repository)
		if !ok {
			return nil, errors.New("durable share grant transaction authority is unavailable")
		}
		result, inserted, err = txAuthority.insertResourceShareGrant(ctx, in, fingerprint, requestDigest)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return []access.AuditEventInput{grantAuditInput(result.Issuer.PrincipalID, "resource_share_grant.replayed", result.Target.ProjectID.String(), result.Target.ResourceID.String(), result.Target.ResourceKind, result.Fingerprint, result.Recipient.ID)}, nil
		}
		return []access.AuditEventInput{grantAuditInput(result.Issuer.PrincipalID, "resource_share_grant.issued", result.Target.ProjectID.String(), result.Target.ResourceID.String(), result.Target.ResourceKind, result.Fingerprint, result.Recipient.ID)}, nil
	})
	return result, err
}

func (r *Repository) insertResourceShareGrant(ctx context.Context, in access.ResourceShareGrantInput, fingerprint, requestDigest string) (access.ResourceShareGrant, bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	recipient, err := access.ShareRecipient(in.Recipient, in.RecipientPrincipalID)
	if err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	if err := r.checkGrantIssuance(ctx, db, in.Issuer, recipient, in.IssuancePermissions, in.IssuancePolicy, in.Target.ProjectID, in.Target.InstanceID); err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	permissions, err := access.EncodePermissionPairs(in.Permissions)
	if err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	resourceUID, err := pgUUID(in.Target.ResourceUID)
	if err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	issuerPrincipalID, err := pgUUID(in.Issuer.PrincipalID)
	if err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	recipientID, err := pgUUID(recipient.ID)
	if err != nil {
		return access.ResourceShareGrant{}, false, err
	}
	tag, err := accessdb.New(db).InsertResourceShareGrant(ctx, accessdb.InsertResourceShareGrantParams{
		ID: in.ID, Profile: access.DurableGrantProfile, InstanceID: in.Target.InstanceID,
		ProjectID: in.Target.ProjectID.String(), ResourceUid: resourceUID, ResourceID: in.Target.ResourceID.String(),
		ResourceKind: string(in.Target.ResourceKind), IssuerPrincipalID: issuerPrincipalID,
		IssuerCredentialClass: in.Issuer.Credential.Class, IssuerCredentialID: in.Issuer.Credential.ID,
		IssuerCredentialFingerprint: in.Issuer.Credential.Fingerprint, RecipientKind: string(recipient.Kind),
		RecipientID: recipientID, PermissionProfile: access.PermissionCatalogProfile, Permissions: permissions,
		IssuedAt: pgTimestamp(in.IssuedAt), ExpiresAt: nullableTimestamp(in.ExpiresAt), Fingerprint: fingerprint,
		IdempotencyKey: in.IdempotencyKey, RequestDigest: requestDigest, AllowOnwardDelegation: in.AllowOnwardDelegation,
	})
	if err != nil {
		return access.ResourceShareGrant{}, false, durableResourceTargetError(err)
	}
	if tag.RowsAffected() == 0 {
		var existing access.ResourceShareGrant
		existing, err = r.resourceShareGrantByIdempotency(ctx, db, in.Issuer.PrincipalID, in.Issuer.Credential.ID, in.IdempotencyKey)
		if err != nil {
			return access.ResourceShareGrant{}, false, err
		}
		if existing.RequestDigest != requestDigest {
			return access.ResourceShareGrant{}, false, access.ErrGrantIdempotencyConflict
		}
		return existing, false, nil
	}
	grant, err := r.resourceShareGrantByID(ctx, db, in.ID)
	return grant, true, err
}

func (r *Repository) ResourceShareGrant(ctx context.Context, id string) (access.ResourceShareGrant, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.ResourceShareGrant{}, err
	}
	return r.resourceShareGrantByID(ctx, db, id)
}

func (r *Repository) RevokeResourceShareGrant(ctx context.Context, id, actorID, reason string) error {
	return r.revokeDurableGrant(ctx, "resource_share_grant", id, actorID, reason)
}

func (r *Repository) CurrentResourceShareGrant(ctx context.Context, id, recipientID string) (access.ResourceShareGrant, error) {
	grant, err := r.ResourceShareGrant(ctx, id)
	if err != nil {
		return grant, err
	}
	if recipientID != "" && grant.Recipient.ID != recipientID {
		return access.ResourceShareGrant{}, access.ErrGrantPrincipalInactive
	}
	if err := r.checkCurrentGrant(ctx, grant.Recipient, grant.Target, grant.RevokedAt, grant.ExpiresAt); err != nil {
		return access.ResourceShareGrant{}, err
	}
	return grant, nil
}

func (r *Repository) resourceShareGrantByID(ctx context.Context, db DBTX, id string) (access.ResourceShareGrant, error) {
	row, err := accessdb.New(db).GetResourceShareGrant(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.ResourceShareGrant{}, access.ErrGrantNotFound
		}
		return access.ResourceShareGrant{}, err
	}
	var grant access.ResourceShareGrant
	var uid, kind, issuerPrincipal, recipientKind, recipient, profile string
	var permissionsJSON []byte
	var issuerClass, issuerID, issuerFP, fingerprint, idem, requestDigest, reason, revokedBy string
	var issued, expires, revoked time.Time
	var onward bool
	grant.ID, grant.Profile, grant.Target.InstanceID, grant.Target.ProjectID = row.ID, row.Profile, row.InstanceID, projectgraph.ResourceID(row.ProjectID)
	uid, grant.Target.ResourceID, kind, issuerPrincipal = row.ResourceUid, projectgraph.ResourceID(row.ResourceID), row.ResourceKind, row.IssuerPrincipalID
	issuerClass, issuerID, issuerFP, recipientKind, recipient = row.IssuerCredentialClass, row.IssuerCredentialID, row.IssuerCredentialFingerprint, row.RecipientKind, row.RecipientID
	profile, permissionsJSON, issued, expires, fingerprint, idem, requestDigest, onward = row.PermissionProfile, row.Permissions, durableGrantTime(row.IssuedAt), durableGrantTime(row.ExpiresAt), row.Fingerprint, row.IdempotencyKey, row.RequestDigest, row.AllowOnwardDelegation
	revoked, revokedBy, reason = durableGrantTime(row.RevokedAt), row.RevokedByPrincipalID, row.RevocationReason
	grant.Target.ResourceUID = uid
	grant.Target.ResourceKind = projectgraph.Kind(kind)
	grant.Issuer = access.GrantIssuerEvidence{PrincipalID: issuerPrincipal, Credential: access.GrantCredentialEvidence{Class: issuerClass, ID: issuerID, Fingerprint: issuerFP}}
	recipientRef, err := access.NewSubjectRef(access.SubjectKind(recipientKind), recipient)
	if err != nil {
		return access.ResourceShareGrant{}, fmt.Errorf("%w: persisted recipient: %v", access.ErrInvalidDurableGrant, err)
	}
	grant.Recipient = recipientRef
	if grant.Recipient.Kind == access.SubjectKindPrincipal {
		grant.RecipientPrincipalID = recipient
	}
	grant.Permissions, grant.IssuedAt, grant.Fingerprint, grant.IdempotencyKey, grant.RequestDigest, grant.AllowOnwardDelegation = nil, issued, fingerprint, idem, requestDigest, onward
	grant.ExpiresAt, grant.RevokedAt = expires, revoked
	if expires.Equal(time.Unix(0, 0).UTC()) {
		grant.ExpiresAt = time.Time{}
	}
	if revoked.Equal(time.Unix(0, 0).UTC()) {
		grant.RevokedAt = time.Time{}
	}
	grant.RevokedByPrincipalID, grant.RevocationReason = revokedBy, reason
	decoded, err := access.DecodePermissionPairs(permissionsJSON)
	if err != nil || grant.Profile != access.DurableGrantProfile || profile != access.PermissionCatalogProfile {
		return access.ResourceShareGrant{}, fmt.Errorf("%w: persisted share permissions: %v", access.ErrInvalidDurableGrant, err)
	}
	if err := grant.Target.Validate(); err != nil {
		return access.ResourceShareGrant{}, fmt.Errorf("%w: persisted share target: %v", access.ErrInvalidDurableGrant, err)
	}
	if err := grant.Issuer.Validate(); err != nil {
		return access.ResourceShareGrant{}, fmt.Errorf("%w: persisted share issuer: %v", access.ErrInvalidDurableGrant, err)
	}
	grant.Permissions = decoded
	return grant, nil
}

func (r *Repository) resourceShareGrantByIdempotency(ctx context.Context, db DBTX, issuer, credential, key string) (access.ResourceShareGrant, error) {
	issuerID, err := pgUUID(issuer)
	if err != nil {
		return access.ResourceShareGrant{}, err
	}
	id, err := accessdb.New(db).GetResourceShareGrantByIdempotency(ctx, accessdb.GetResourceShareGrantByIdempotencyParams{IssuerPrincipalID: issuerID, IssuerCredentialID: credential, IdempotencyKey: key})
	if err != nil {
		return access.ResourceShareGrant{}, err
	}
	return r.resourceShareGrantByID(ctx, db, id)
}
