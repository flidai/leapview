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
	"github.com/jackc/pgx/v5/pgtype"
)

func (r *Repository) CreateGrantAdminEnvelope(ctx context.Context, in access.GrantAdminEnvelopeInput) (result access.GrantAdminEnvelope, err error) {
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
	fingerprint, err := access.GrantAdminEnvelopeFingerprint(in, issued, expires)
	if err != nil {
		return result, err
	}
	requestDigest := grantRequestDigest(in.RequestDigest, fingerprint)
	var inserted bool
	err = r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		txAuthority, ok := txRepo.(*Repository)
		if !ok {
			return nil, errors.New("grant administration envelope transaction authority is unavailable")
		}
		result, inserted, err = txAuthority.insertGrantAdminEnvelope(ctx, in, fingerprint, requestDigest)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return []access.AuditEventInput{grantAuditInput(result.Issuer.PrincipalID, "grant_admin_envelope.replayed", result.TargetProjectID.String(), result.TargetResourceID.String(), result.TargetResourceKind, result.Fingerprint, result.BoundPrincipalID)}, nil
		}
		return []access.AuditEventInput{grantAuditInput(result.Issuer.PrincipalID, "grant_admin_envelope.issued", result.TargetProjectID.String(), result.TargetResourceID.String(), result.TargetResourceKind, result.Fingerprint, result.BoundPrincipalID)}, nil
	})
	return result, err
}

func (r *Repository) insertGrantAdminEnvelope(ctx context.Context, in access.GrantAdminEnvelopeInput, fingerprint, requestDigest string) (access.GrantAdminEnvelope, bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.GrantAdminEnvelope{}, false, err
	}
	if err := r.checkGrantIssuance(ctx, db, in.Issuer, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: in.BoundPrincipalID}, in.IssuancePermissions, in.IssuancePolicy, in.TargetProjectID, ""); err != nil {
		return access.GrantAdminEnvelope{}, false, err
	}
	permissions, err := access.EncodePermissionPairs(in.Permissions)
	if err != nil {
		return access.GrantAdminEnvelope{}, false, err
	}
	issuerPrincipalID, err := pgUUID(in.Issuer.PrincipalID)
	if err != nil {
		return access.GrantAdminEnvelope{}, false, err
	}
	boundPrincipalID, err := pgUUID(in.BoundPrincipalID)
	if err != nil {
		return access.GrantAdminEnvelope{}, false, err
	}
	tag, err := accessdb.New(db).InsertGrantAdminEnvelope(ctx, accessdb.InsertGrantAdminEnvelopeParams{
		ID: in.ID, Profile: access.DurableGrantProfile, IssuerPrincipalID: issuerPrincipalID,
		IssuerCredentialClass: in.Issuer.Credential.Class, IssuerCredentialID: in.Issuer.Credential.ID,
		IssuerCredentialFingerprint: in.Issuer.Credential.Fingerprint, BoundPrincipalID: boundPrincipalID,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: permissions, TargetProjectID: in.TargetProjectID.String(),
		TargetResourceKind: nullableString(string(in.TargetResourceKind)), TargetResourceID: nullableString(in.TargetResourceID.String()),
		RecipientSelector: in.RecipientSelector, RoleVersion: in.RoleVersion, IssuedAt: pgTimestamp(in.IssuedAt),
		ExpiresAt: pgTimestamp(in.ExpiresAt), Fingerprint: fingerprint, IdempotencyKey: in.IdempotencyKey,
		RequestDigest: requestDigest, AllowOnwardDelegation: in.AllowOnwardDelegation,
	})
	if err != nil {
		return access.GrantAdminEnvelope{}, false, err
	}
	if tag.RowsAffected() == 0 {
		existing, getErr := r.grantAdminEnvelopeByIdempotency(ctx, db, in.Issuer.PrincipalID, in.Issuer.Credential.ID, in.IdempotencyKey)
		if getErr != nil {
			return access.GrantAdminEnvelope{}, false, getErr
		}
		if existing.RequestDigest != requestDigest {
			return access.GrantAdminEnvelope{}, false, access.ErrGrantIdempotencyConflict
		}
		return existing, false, nil
	}
	grant, err := r.grantAdminEnvelopeByID(ctx, db, in.ID)
	return grant, true, err
}

func (r *Repository) GrantAdminEnvelope(ctx context.Context, id string) (access.GrantAdminEnvelope, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.GrantAdminEnvelope{}, err
	}
	return r.grantAdminEnvelopeByID(ctx, db, id)
}

func (r *Repository) RevokeGrantAdminEnvelope(ctx context.Context, id, actorID, reason string) error {
	return r.revokeDurableGrant(ctx, "grant_admin_envelope", id, actorID, reason)
}

// CurrentGrantAdminEnvelopeForMutation takes a shared row lock so a
// concurrent revoke and the authorized mutation cannot both claim to precede
// one another. Callers use this through an already-open audited transaction.
func (r *Repository) CurrentGrantAdminEnvelopeForMutation(ctx context.Context, id, principalID string) (access.GrantAdminEnvelope, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.GrantAdminEnvelope{}, err
	}
	row, err := accessdb.New(db).GetGrantAdminEnvelopeForMutation(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			err = access.ErrGrantNotFound
		}
		return access.GrantAdminEnvelope{}, err
	}
	grant, err := grantAdminEnvelopeFromFields(
		row.ID, row.Profile, row.IssuerPrincipalID, row.IssuerCredentialClass,
		row.IssuerCredentialID, row.IssuerCredentialFingerprint, row.BoundPrincipalID,
		row.PermissionProfile, row.Permissions, row.TargetProjectID,
		row.TargetResourceKind, row.TargetResourceID, row.RecipientSelector,
		row.RoleVersion, row.IssuedAt, row.ExpiresAt, row.Fingerprint,
		row.IdempotencyKey, row.RequestDigest, row.AllowOnwardDelegation,
		row.RevokedAt, row.RevokedByPrincipalID, row.RevocationReason,
	)
	return r.validateCurrentGrantAdminEnvelope(ctx, grant, principalID, err)
}

func (r *Repository) validateCurrentGrantAdminEnvelope(ctx context.Context, grant access.GrantAdminEnvelope, principalID string, err error) (access.GrantAdminEnvelope, error) {
	if err != nil {
		return grant, err
	}
	if principalID != "" && grant.BoundPrincipalID != principalID {
		return access.GrantAdminEnvelope{}, access.ErrGrantPrincipalInactive
	}
	if err := r.checkCurrentPrincipal(ctx, principalIDOr(grant.BoundPrincipalID, principalID)); err != nil {
		return access.GrantAdminEnvelope{}, err
	}
	if !grant.RevokedAt.IsZero() {
		return access.GrantAdminEnvelope{}, access.ErrGrantRevoked
	}
	if !grant.ExpiresAt.IsZero() && !time.Now().UTC().Before(grant.ExpiresAt) {
		return access.GrantAdminEnvelope{}, access.ErrGrantExpired
	}
	return grant, nil
}

func (r *Repository) grantAdminEnvelopeByID(ctx context.Context, db DBTX, id string) (access.GrantAdminEnvelope, error) {
	row, err := accessdb.New(db).GetGrantAdminEnvelope(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.GrantAdminEnvelope{}, access.ErrGrantNotFound
		}
		return access.GrantAdminEnvelope{}, err
	}
	return grantAdminEnvelopeFromFields(
		row.ID, row.Profile, row.IssuerPrincipalID, row.IssuerCredentialClass,
		row.IssuerCredentialID, row.IssuerCredentialFingerprint, row.BoundPrincipalID,
		row.PermissionProfile, row.Permissions, row.TargetProjectID,
		row.TargetResourceKind, row.TargetResourceID, row.RecipientSelector,
		row.RoleVersion, row.IssuedAt, row.ExpiresAt, row.Fingerprint,
		row.IdempotencyKey, row.RequestDigest, row.AllowOnwardDelegation,
		row.RevokedAt, row.RevokedByPrincipalID, row.RevocationReason,
	)
}

func grantAdminEnvelopeFromFields(id, grantProfile, issuerPrincipal, issuerClass, issuerID, issuerFP, boundPrincipal, permissionProfile string, permissionsJSON []byte, projectID, kind, resourceID, selector, role string, issuedValue, expiresValue pgtype.Timestamptz, fingerprint, idem, requestDigest string, onward bool, revokedValue pgtype.Timestamptz, revokedBy, reason string) (access.GrantAdminEnvelope, error) {
	issued, expires, revoked := durableGrantTime(issuedValue), durableGrantTime(expiresValue), durableGrantTime(revokedValue)
	grant := access.GrantAdminEnvelope{ID: id, Profile: grantProfile}
	grant.Issuer = access.GrantIssuerEvidence{PrincipalID: issuerPrincipal, Credential: access.GrantCredentialEvidence{Class: issuerClass, ID: issuerID, Fingerprint: issuerFP}}
	grant.BoundPrincipalID, grant.TargetProjectID = boundPrincipal, projectgraph.ResourceID(projectID)
	grant.TargetResourceKind, grant.TargetResourceID = projectgraph.Kind(kind), projectgraph.ResourceID(resourceID)
	grant.RecipientSelector, grant.RoleVersion, grant.IssuedAt, grant.ExpiresAt = selector, role, issued, expires
	grant.Fingerprint, grant.IdempotencyKey, grant.RequestDigest, grant.AllowOnwardDelegation, grant.RevokedAt, grant.RevokedByPrincipalID, grant.RevocationReason = fingerprint, idem, requestDigest, onward, revoked, revokedBy, reason
	if revoked.Equal(time.Unix(0, 0).UTC()) {
		grant.RevokedAt = time.Time{}
	}
	decoded, err := access.DecodePermissionPairs(permissionsJSON)
	if err != nil || grant.Profile != access.DurableGrantProfile || permissionProfile != access.PermissionCatalogProfile {
		return access.GrantAdminEnvelope{}, fmt.Errorf("%w: persisted envelope permissions: %v", access.ErrInvalidDurableGrant, err)
	}
	if err := grant.Issuer.Validate(); err != nil {
		return access.GrantAdminEnvelope{}, fmt.Errorf("%w: persisted envelope issuer: %v", access.ErrInvalidDurableGrant, err)
	}
	grant.Permissions = decoded
	return grant, nil
}

func (r *Repository) grantAdminEnvelopeByIdempotency(ctx context.Context, db DBTX, issuer, credential, key string) (access.GrantAdminEnvelope, error) {
	issuerID, err := pgUUID(issuer)
	if err != nil {
		return access.GrantAdminEnvelope{}, err
	}
	id, err := accessdb.New(db).GetGrantAdminEnvelopeByIdempotency(ctx, accessdb.GetGrantAdminEnvelopeByIdempotencyParams{IssuerPrincipalID: issuerID, IssuerCredentialID: credential, IdempotencyKey: key})
	if err != nil {
		return access.GrantAdminEnvelope{}, err
	}
	return r.grantAdminEnvelopeByID(ctx, db, id)
}
