package postgres

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// DurableGrantAuthority is the narrow access port used by delegated
// execution and sharing callers. It intentionally does not expose the
// generation-bound authorization_grant methods.
type DurableGrantAuthority interface {
	CreateResourceShareGrant(context.Context, access.ResourceShareGrantInput) (access.ResourceShareGrant, error)
	ResourceShareGrant(context.Context, string) (access.ResourceShareGrant, error)
	RevokeResourceShareGrant(context.Context, string, string, string) error
	CurrentResourceShareGrant(context.Context, string, string) (access.ResourceShareGrant, error)
	CurrentShareGrant(context.Context, string, string) (access.ShareGrant, error)
	CreateExecutionGrant(context.Context, access.ExecutionGrantInput) (access.ExecutionGrant, error)
	ExecutionGrant(context.Context, string) (access.ExecutionGrant, error)
	RevokeExecutionGrant(context.Context, string, string, string) error
	CurrentExecutionGrant(context.Context, string, string) (access.ExecutionGrant, error)
	CreateGrantAdminEnvelope(context.Context, access.GrantAdminEnvelopeInput) (access.GrantAdminEnvelope, error)
	GrantAdminEnvelope(context.Context, string) (access.GrantAdminEnvelope, error)
	RevokeGrantAdminEnvelope(context.Context, string, string, string) error
	CurrentGrantAdminEnvelope(context.Context, string, string) (access.GrantAdminEnvelope, error)
}

var _ DurableGrantAuthority = (*Repository)(nil)

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
	if err := r.checkGrantIssuance(ctx, db, in.Issuer, recipient, in.IssuancePermissions); err != nil {
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

func (r *Repository) GetResourceShareGrant(ctx context.Context, id string) (access.ResourceShareGrant, error) {
	return r.ResourceShareGrant(ctx, id)
}

func (r *Repository) ShareGrant(ctx context.Context, id string) (access.ShareGrant, error) {
	return r.ResourceShareGrant(ctx, id)
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

func (r *Repository) CurrentShareGrant(ctx context.Context, id, recipientID string) (access.ShareGrant, error) {
	return r.CurrentResourceShareGrant(ctx, id, recipientID)
}

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
	if err := r.checkGrantIssuance(ctx, db, in.Issuer, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: in.ExecutionPrincipalID}, in.IssuancePermissions); err != nil {
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

func (r *Repository) GetExecutionGrant(ctx context.Context, id string) (access.ExecutionGrant, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.ExecutionGrant{}, err
	}
	return r.executionGrantByID(ctx, db, id)
}

func (r *Repository) ExecutionGrant(ctx context.Context, id string) (access.ExecutionGrant, error) {
	return r.GetExecutionGrant(ctx, id)
}

func (r *Repository) RevokeExecutionGrant(ctx context.Context, id, actorID, reason string) error {
	return r.revokeDurableGrant(ctx, "execution_grant", id, actorID, reason)
}

func (r *Repository) CurrentExecutionGrant(ctx context.Context, id, executionPrincipalID string) (access.ExecutionGrant, error) {
	grant, err := r.GetExecutionGrant(ctx, id)
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
	if err := r.checkGrantIssuance(ctx, db, in.Issuer, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: in.BoundPrincipalID}, in.IssuancePermissions); err != nil {
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

func (r *Repository) GetGrantAdminEnvelope(ctx context.Context, id string) (access.GrantAdminEnvelope, error) {
	db, err := r.requireDB()
	if err != nil {
		return access.GrantAdminEnvelope{}, err
	}
	return r.grantAdminEnvelopeByID(ctx, db, id)
}

func (r *Repository) GrantAdminEnvelope(ctx context.Context, id string) (access.GrantAdminEnvelope, error) {
	return r.GetGrantAdminEnvelope(ctx, id)
}

func (r *Repository) RevokeGrantAdminEnvelope(ctx context.Context, id, actorID, reason string) error {
	return r.revokeDurableGrant(ctx, "grant_admin_envelope", id, actorID, reason)
}

func (r *Repository) CurrentGrantAdminEnvelope(ctx context.Context, id, principalID string) (access.GrantAdminEnvelope, error) {
	grant, err := r.GetGrantAdminEnvelope(ctx, id)
	if err != nil {
		return grant, err
	}
	if principalID != "" && grant.BoundPrincipalID != principalID {
		return access.GrantAdminEnvelope{}, access.ErrGrantPrincipalInactive
	}
	if grant.AllowOnwardDelegation == false {
		// False is valid for direct administration. It only rejects an attempt
		// to use this envelope as a parent, via AllowsOnwardDelegation below.
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

func principalIDOr(bound, requested string) string {
	if requested != "" {
		return requested
	}
	return bound
}

func grantTimes(issuedAt, expiresAt time.Time, ttl time.Duration, required bool) (time.Time, time.Time, error) {
	issued := issuedAt.UTC()
	if issued.IsZero() {
		issued = time.Now().UTC()
	}
	if ttl < 0 || ttl > 365*24*time.Hour {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: grant TTL is outside the one-year bound", access.ErrInvalidDurableGrant)
	}
	expires := expiresAt.UTC()
	if expires.IsZero() && ttl > 0 {
		expires = issued.Add(ttl)
	}
	if required && expires.IsZero() {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: grant expiry is required", access.ErrInvalidDurableGrant)
	}
	if !expires.IsZero() && (!expires.After(issued) || expires.After(issued.Add(365*24*time.Hour))) {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: grant expiry is outside the one-year bound", access.ErrInvalidDurableGrant)
	}
	return issued, expires, nil
}

func nullableTimestamp(value time.Time) pgtype.Timestamptz {
	if value.IsZero() {
		return pgtype.Timestamptz{}
	}
	return pgTimestamp(value)
}

func durableGrantTime(value pgtype.Timestamptz) time.Time {
	if !value.Valid {
		return time.Time{}
	}
	return value.Time.UTC()
}

func grantRequestDigest(request, fingerprint string) string {
	if request != "" {
		return request
	}
	return fingerprint
}

func grantAuditInput(principalID, action, projectID, resourceID string, kind projectgraph.Kind, fingerprint, recipient string) access.AuditEventInput {
	metadata, _ := json.Marshal(map[string]string{"grantFingerprint": fingerprint, "recipientPrincipalId": recipient})
	return access.AuditEventInput{PrincipalID: principalID, Action: action, ProjectID: projectID, ResourceKind: string(kind), ResourceID: resourceID, Status: "success", MetadataJSON: string(metadata)}
}

func (r *Repository) checkGrantIssuance(ctx context.Context, db DBTX, issuer access.GrantIssuerEvidence, recipient access.SubjectRef, issuancePermissions []access.PermissionPair) error {
	if err := r.checkCurrentPrincipalOn(ctx, db, issuer.PrincipalID); err != nil {
		return err
	}
	if err := r.checkCurrentSubjectOn(ctx, db, recipient); err != nil {
		return err
	}
	if err := r.checkCredentialEvidence(ctx, db, issuer); err != nil {
		return err
	}
	return r.checkCredentialPermissionCeiling(ctx, db, issuer, issuancePermissions)
}

func (r *Repository) checkCurrentPrincipal(ctx context.Context, id string) error {
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	return r.checkCurrentPrincipalOn(ctx, db, id)
}

func (r *Repository) checkCurrentPrincipalOn(ctx context.Context, db DBTX, id string) error {
	parsed, err := pgUUID(id)
	if err != nil || !parsed.Valid {
		return access.ErrGrantPrincipalInactive
	}
	active, err := accessdb.New(db).IsCurrentPrincipal(ctx, parsed)
	if err != nil {
		return err
	}
	if !active {
		return access.ErrGrantPrincipalInactive
	}
	return nil
}

func (r *Repository) checkCurrentSubjectOn(ctx context.Context, db DBTX, subject access.SubjectRef) error {
	parsed, err := pgUUID(subject.ID)
	if err != nil || !parsed.Valid {
		return access.ErrGrantPrincipalInactive
	}
	var active bool
	switch subject.Kind {
	case access.SubjectKindPrincipal:
		active, err = accessdb.New(db).IsCurrentPrincipal(ctx, parsed)
	case access.SubjectKindGroup:
		active, err = accessdb.New(db).IsCurrentGroup(ctx, parsed)
	default:
		return access.ErrGrantPrincipalInactive
	}
	if err != nil {
		return err
	}
	if !active {
		return access.ErrGrantPrincipalInactive
	}
	return nil
}

func (r *Repository) checkCredentialEvidence(ctx context.Context, db DBTX, issuer access.GrantIssuerEvidence) error {
	if err := issuer.Credential.Validate(); err != nil {
		return err
	}
	parsed, err := pgUUID(issuer.Credential.ID)
	if err != nil || !parsed.Valid {
		return access.ErrGrantCredentialInvalid
	}
	fingerprint, err := hex.DecodeString(issuer.Credential.Fingerprint)
	if err != nil || len(fingerprint) != 32 {
		return access.ErrGrantCredentialInvalid
	}
	issuerPrincipal, err := pgUUID(issuer.PrincipalID)
	if err != nil || !issuerPrincipal.Valid {
		return access.ErrGrantCredentialInvalid
	}
	var active bool
	switch issuer.Credential.Class {
	case access.GrantCredentialClassSession:
		active, err = accessdb.New(db).IsActiveSessionCredential(ctx, accessdb.IsActiveSessionCredentialParams{ID: parsed, PrincipalID: issuerPrincipal, Fingerprint: fingerprint})
	case access.GrantCredentialClassAPIToken:
		active, err = accessdb.New(db).IsActiveAPITokenCredential(ctx, accessdb.IsActiveAPITokenCredentialParams{ID: parsed, PrincipalID: issuerPrincipal, Fingerprint: fingerprint})
	default:
		return access.ErrGrantCredentialInvalid
	}
	if err != nil {
		return err
	}
	if !active {
		return access.ErrGrantCredentialInvalid
	}
	return nil
}

// checkCredentialPermissionCeiling resolves the typed ceiling attached to an
// API token. Browser sessions do not yet have a durable typed permission
// projection, so accepting caller-supplied issuance permissions for them
// would turn evidence into authority; fail closed until that resolver exists.
func (r *Repository) checkCredentialPermissionCeiling(ctx context.Context, db DBTX, issuer access.GrantIssuerEvidence, requested []access.PermissionPair) error {
	if requested == nil {
		return access.ErrGrantPermissionCeiling
	}
	if issuer.Credential.Class == access.GrantCredentialClassSession {
		return fmt.Errorf("%w: typed session authority resolver is unavailable", access.ErrGrantPermissionCeiling)
	}
	credentialID, err := pgUUID(issuer.Credential.ID)
	if err != nil {
		return access.ErrGrantPermissionCeiling
	}
	principalID, err := pgUUID(issuer.PrincipalID)
	if err != nil {
		return access.ErrGrantPermissionCeiling
	}
	row, err := accessdb.New(db).GetAPITokenPermissionCeiling(ctx, accessdb.GetAPITokenPermissionCeilingParams{ID: credentialID, PrincipalID: principalID})
	if err != nil {
		return access.ErrGrantPermissionCeiling
	}
	if row.PermissionProfile != access.PermissionCatalogProfile {
		return access.ErrGrantPermissionCeiling
	}
	ceiling, err := access.DecodePermissionPairs(row.Permissions)
	if err != nil || len(ceiling) == 0 {
		return access.ErrGrantPermissionCeiling
	}
	for _, pair := range requested {
		if !access.PermissionSetAllows(ceiling, pair) {
			return fmt.Errorf("%w: credential ceiling excludes %q", access.ErrGrantPermissionCeiling, pair.Action)
		}
	}
	return nil
}

func (r *Repository) checkCurrentGrant(ctx context.Context, recipient access.SubjectRef, target access.DurableGrantTarget, revokedAt, expiresAt time.Time) error {
	if !revokedAt.IsZero() {
		return access.ErrGrantRevoked
	}
	if !expiresAt.IsZero() && !time.Now().UTC().Before(expiresAt) {
		return access.ErrGrantExpired
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	if err := r.checkCurrentSubjectOn(ctx, db, recipient); err != nil {
		return err
	}
	resourceUID, err := pgUUID(target.ResourceUID)
	if err != nil {
		return access.ErrGrantResourceInactive
	}
	active, err := accessdb.New(db).IsActiveResourceTarget(ctx, accessdb.IsActiveResourceTargetParams{
		InstanceID: target.InstanceID, ProjectID: target.ProjectID.String(), ResourceUid: resourceUID,
		ResourceID: target.ResourceID.String(), ResourceKind: string(target.ResourceKind),
	})
	if err != nil {
		return err
	}
	if !active {
		return access.ErrGrantResourceInactive
	}
	return nil
}

func (r *Repository) revokeDurableGrant(ctx context.Context, table, id, actorID, reason string) error {
	if table != "resource_share_grant" && table != "execution_grant" && table != "grant_admin_envelope" {
		return access.ErrInvalidDurableGrant
	}
	parsedActor, parseErr := pgUUID(actorID)
	if strings.TrimSpace(id) == "" || parseErr != nil || !parsedActor.Valid {
		return access.ErrInvalidDurableGrant
	}
	if len(reason) > 1024 || strings.ContainsAny(reason, "\x00\r\n") {
		return access.ErrInvalidDurableGrant
	}
	return r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		txAuthority := txRepo.(*Repository)
		db, err := txAuthority.requireDB()
		if err != nil {
			return nil, err
		}
		var projectID, resourceID, resourceKind, recipient, issuer string
		switch table {
		case "resource_share_grant":
			var row accessdb.RevokeResourceShareGrantRow
			row, err = accessdb.New(db).RevokeResourceShareGrant(ctx, accessdb.RevokeResourceShareGrantParams{ActorID: parsedActor, Reason: reason, ID: id})
			issuer, projectID, resourceID, resourceKind, recipient = row.IssuerPrincipalID, row.ProjectID, row.ResourceID, row.ResourceKind, row.RecipientID
		case "execution_grant":
			var row accessdb.RevokeExecutionGrantRow
			row, err = accessdb.New(db).RevokeExecutionGrant(ctx, accessdb.RevokeExecutionGrantParams{ActorID: parsedActor, Reason: reason, ID: id})
			issuer, projectID, resourceID, resourceKind, recipient = row.IssuerPrincipalID, row.ProjectID, row.ResourceID, row.ResourceKind, row.RecipientID
		case "grant_admin_envelope":
			var row accessdb.RevokeGrantAdminEnvelopeRow
			row, err = accessdb.New(db).RevokeGrantAdminEnvelope(ctx, accessdb.RevokeGrantAdminEnvelopeParams{ActorID: parsedActor, Reason: reason, ID: id})
			issuer, projectID, resourceID, resourceKind, recipient = row.IssuerPrincipalID, row.ProjectID, row.ResourceID, row.ResourceKind, row.RecipientID
		}
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, access.ErrGrantNotFound
			}
			return nil, err
		}
		metadata, _ := json.Marshal(map[string]string{"recipientPrincipalId": recipient, "reason": reason})
		return []access.AuditEventInput{{PrincipalID: actorID, Action: table + ".revoked", ProjectID: projectID, ResourceKind: resourceKind, ResourceID: resourceID, Status: "success", MetadataJSON: string(metadata), RequestID: issuer}}, nil
	})
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

func (r *Repository) grantAdminEnvelopeByID(ctx context.Context, db DBTX, id string) (access.GrantAdminEnvelope, error) {
	row, err := accessdb.New(db).GetGrantAdminEnvelope(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return access.GrantAdminEnvelope{}, access.ErrGrantNotFound
		}
		return access.GrantAdminEnvelope{}, err
	}
	var grant access.GrantAdminEnvelope
	var issuerPrincipal, boundPrincipal, profile, kind, resourceID, revokedBy string
	var permissionsJSON []byte
	var issuerClass, issuerID, issuerFP, projectID, selector, role, fingerprint, idem, requestDigest, reason string
	var issued, expires, revoked time.Time
	var onward bool
	grant.ID, grant.Profile = row.ID, row.Profile
	issuerPrincipal, issuerClass, issuerID, issuerFP, boundPrincipal = row.IssuerPrincipalID, row.IssuerCredentialClass, row.IssuerCredentialID, row.IssuerCredentialFingerprint, row.BoundPrincipalID
	profile, permissionsJSON, projectID, kind, resourceID = row.PermissionProfile, row.Permissions, row.TargetProjectID, row.TargetResourceKind, row.TargetResourceID
	selector, role, issued, expires = row.RecipientSelector, row.RoleVersion, durableGrantTime(row.IssuedAt), durableGrantTime(row.ExpiresAt)
	fingerprint, idem, requestDigest, onward = row.Fingerprint, row.IdempotencyKey, row.RequestDigest, row.AllowOnwardDelegation
	revoked, revokedBy, reason = durableGrantTime(row.RevokedAt), row.RevokedByPrincipalID, row.RevocationReason
	grant.Issuer = access.GrantIssuerEvidence{PrincipalID: issuerPrincipal, Credential: access.GrantCredentialEvidence{Class: issuerClass, ID: issuerID, Fingerprint: issuerFP}}
	grant.BoundPrincipalID, grant.Permissions, grant.TargetProjectID = boundPrincipal, nil, projectgraph.ResourceID(projectID)
	grant.TargetResourceKind, grant.TargetResourceID = projectgraph.Kind(kind), projectgraph.ResourceID(resourceID)
	grant.RecipientSelector, grant.RoleVersion, grant.IssuedAt, grant.ExpiresAt = selector, role, issued, expires
	grant.Fingerprint, grant.IdempotencyKey, grant.RequestDigest, grant.AllowOnwardDelegation, grant.RevokedAt, grant.RevokedByPrincipalID, grant.RevocationReason = fingerprint, idem, requestDigest, onward, revoked, revokedBy, reason
	if revoked.Equal(time.Unix(0, 0).UTC()) {
		grant.RevokedAt = time.Time{}
	}
	decoded, err := access.DecodePermissionPairs(permissionsJSON)
	if err != nil || grant.Profile != access.DurableGrantProfile || profile != access.PermissionCatalogProfile {
		return access.GrantAdminEnvelope{}, fmt.Errorf("%w: persisted envelope permissions: %v", access.ErrInvalidDurableGrant, err)
	}
	if err := grant.Issuer.Validate(); err != nil {
		return access.GrantAdminEnvelope{}, fmt.Errorf("%w: persisted envelope issuer: %v", access.ErrInvalidDurableGrant, err)
	}
	grant.Permissions = decoded
	return grant, nil
}

// The target registry is the only external identity that can make a durable
// resource grant usable. Normalize both the registry FK failure and the
// exact-target trigger failure to one fail-closed domain error; issuer and
// recipient existence are checked before the insert and are not conflated.
func durableResourceTargetError(err error) error {
	if err == nil {
		return nil
	}
	var databaseError *pgconn.PgError
	if errors.As(err, &databaseError) && databaseError.Code == "23503" {
		return fmt.Errorf("%w: %v", access.ErrGrantResourceUIDMismatch, err)
	}
	if strings.Contains(err.Error(), "durable grant exact resource target") {
		return fmt.Errorf("%w: %v", access.ErrGrantResourceUIDMismatch, err)
	}
	return err
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
