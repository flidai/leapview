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

func (r *Repository) checkGrantIssuance(ctx context.Context, db DBTX, issuer access.GrantIssuerEvidence, recipient access.SubjectRef, issuancePermissions []access.PermissionPair, policy access.GrantIssuancePolicy, projectID projectgraph.ResourceID, instanceID string) error {
	if err := policy.Validate(); err != nil {
		return fmt.Errorf("%w: %v", access.ErrGrantAuthorityUnavailable, err)
	}
	if policy.Scope.ProjectID != projectID.String() || (instanceID != "" && policy.Scope.TargetID != instanceID) {
		return access.ErrGrantAuthorityInvalid
	}
	if err := r.validateAuthorizationPolicyScope(policy.Scope); err != nil {
		return err
	}
	head, err := accessdb.New(db).LockAuthorizationPolicyHeadForShare(ctx, policyScopeParamsForShare(policy.Scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrGrantAuthorityUnavailable
	}
	if err != nil {
		return fmt.Errorf("lock grant issuance policy: %w", err)
	}
	if head.Revision != policy.Revision || head.Digest != policy.Digest {
		return fmt.Errorf("%w: target policy changed before grant commit", access.ErrGrantAuthorityUnavailable)
	}
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
	queries := accessdb.New(db)
	switch issuer.Credential.Class {
	case access.GrantCredentialClassSession:
		_, err = queries.LockActiveSessionCredential(ctx, accessdb.LockActiveSessionCredentialParams{ID: parsed, PrincipalID: issuerPrincipal, Fingerprint: fingerprint})
	case access.GrantCredentialClassAPIToken:
		_, err = queries.LockActiveAPITokenCredential(ctx, accessdb.LockActiveAPITokenCredentialParams{ID: parsed, PrincipalID: issuerPrincipal, Fingerprint: fingerprint})
	default:
		return access.ErrGrantCredentialInvalid
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrGrantCredentialInvalid
	}
	if err != nil {
		return err
	}
	return nil
}

// checkCredentialPermissionCeiling resolves the typed ceiling attached to an
// API token. Sessions have no independent pair ceiling: their principal/group
// authority is resolved by DurableGrantService, while the session row is
// locked and checked for current validity by checkCredentialEvidence.
func (r *Repository) checkCredentialPermissionCeiling(ctx context.Context, db DBTX, issuer access.GrantIssuerEvidence, requested []access.PermissionPair) error {
	if requested == nil {
		return access.ErrGrantPermissionCeiling
	}
	if issuer.Credential.Class == access.GrantCredentialClassSession {
		return nil
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
