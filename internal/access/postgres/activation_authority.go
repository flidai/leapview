package postgres

import (
	"context"
	"encoding/hex"
	"errors"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

// AuthorizeCredentialActivationTx checks both exact connection.manage and
// connection.use permissions against retained and current access authority.
// The caller owns a READ COMMITTED transaction and must first fence the exact
// target/predecessor; this method does not establish receipt, owner, binding,
// publication approval or runtime readiness. No provider work may run inside tx.
// Successful checks retain revocation locks until the caller ends tx. Credential
// expiry is checked at the decision, not guaranteed for an arbitrary later commit.
func (r *Repository) AuthorizeCredentialActivationTx(ctx context.Context, tx Tx, scope access.AuthorizationPolicyScope, captured accesssnapshot.AuthorizationSnapshot, issuer access.GrantIssuerEvidence, requested []access.PermissionPair) error {
	if r == nil || !r.Configured() || typednil.IsNil(r.db) || ctx == nil || typednil.IsNil(tx) {
		return access.ErrGrantAuthorityUnavailable
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	identity := captured.Identity()
	if r.validateAuthorizationPolicyScope(scope) != nil || captured.ValidateBound() != nil ||
		captured.PermissionProfile() != access.PermissionCatalogProfile ||
		identity.ProjectID.String() != scope.ProjectID || identity.Environment != scope.Environment ||
		!activationConnectionPermissions(identity.ProjectID, requested) {
		return access.ErrGrantAuthorityInvalid
	}
	queries := accessdb.New(tx)
	isolation, err := queries.GetTransactionIsolation(ctx)
	if err != nil {
		return err
	}
	if isolation != "read committed" {
		return access.ErrGrantAuthorityInvalid
	}
	if err := verifyActivationSnapshot(ctx, queries, captured); err != nil {
		return err
	}
	head, err := queries.LockAuthorizationPolicyHeadForShare(ctx, policyScopeParamsForShare(scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrGrantAuthorityUnavailable
	}
	if err != nil {
		return err
	}
	current, err := r.authorizationPolicyAtRevision(ctx, tx, scope, head.Revision, head.Digest)
	if err != nil {
		return err
	}
	restricted, err := captured.RestrictToCurrentRoleBindings(current.RoleBindings)
	if err != nil {
		return err
	}
	restricted, err = restricted.RestrictToCurrentAuthorizationGrants(current.Grants)
	if err != nil {
		return err
	}
	subjects, err := activationSubjects(ctx, queries, issuer.PrincipalID)
	if err != nil {
		return err
	}
	permissions, err := restricted.EffectiveTypedPermissions(subjects)
	if err != nil {
		return err
	}
	for _, pair := range requested {
		if !access.PermissionSetAllows(permissions, pair) {
			return access.ErrForbidden
		}
	}
	// Check the credential last: preceding policy and subject locks may wait.
	return r.activationCredential(ctx, tx, issuer, requested)
}

func activationConnectionPermissions(projectID graph.ResourceID, requested []access.PermissionPair) bool {
	if len(requested) != 2 || requested[0].Target != requested[1].Target || requested[0].Action == requested[1].Action {
		return false
	}
	for _, pair := range requested {
		if pair.Validate() != nil || pair.Target.Scope != access.PermissionScopeResource ||
			pair.Target.ProjectID != projectID || pair.Target.ResourceKind != graph.KindConnection ||
			pair.Target.ResourceID == "" || pair.Target.IncludeFuture ||
			(pair.Action != access.ActionConnectionManage && pair.Action != access.ActionConnectionUse) {
			return false
		}
	}
	return true
}

func verifyActivationSnapshot(ctx context.Context, queries *accessdb.Queries, captured accesssnapshot.AuthorizationSnapshot) error {
	identity := captured.Identity()
	digest, err := captured.Digest()
	if err != nil {
		return access.ErrGrantAuthorityInvalid
	}
	stored, err := queries.GetAuthorizationSnapshotAuthority(ctx, accessdb.GetAuthorizationSnapshotAuthorityParams{
		ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrGrantAuthorityUnavailable
	}
	if err != nil {
		return err
	}
	if stored.Digest != digest || stored.PermissionProfile == nil || *stored.PermissionProfile != captured.PermissionProfile() {
		return access.ErrGrantAuthorityUnavailable
	}
	return nil
}

func activationSubjects(ctx context.Context, queries *accessdb.Queries, principalID string) ([]access.SubjectRef, error) {
	id, err := pgUUID(principalID)
	if err != nil || !id.Valid {
		return nil, access.ErrGrantPrincipalInactive
	}
	if _, err := queries.LockActivePrincipalForShare(ctx, id); errors.Is(err, pgx.ErrNoRows) {
		return nil, access.ErrGrantPrincipalInactive
	} else if err != nil {
		return nil, err
	}
	subjects := []access.SubjectRef{{Kind: access.SubjectKindPrincipal, ID: principalUUID(id)}}
	// Lock groups before memberships to match SCIM replacement. SHARE allows
	// foreign-key checks for concurrent additions, which we may safely omit.
	groups, err := queries.ListActivePrincipalGroupIDsForShare(ctx, id)
	if err != nil {
		return nil, err
	}
	for _, groupID := range groups {
		group, err := pgUUID(groupID)
		if err != nil {
			return nil, err
		}
		_, err = queries.LockActivePrincipalGroupMembershipForShare(ctx, accessdb.LockActivePrincipalGroupMembershipForShareParams{
			GroupID: group, PrincipalID: id,
		})
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, err
		}
		subjects = append(subjects, access.SubjectRef{Kind: access.SubjectKindGroup, ID: groupID})
	}
	return subjects, nil
}

func (r *Repository) activationCredential(ctx context.Context, tx Tx, issuer access.GrantIssuerEvidence, requested []access.PermissionPair) error {
	if issuer.Credential.Validate() != nil {
		return access.ErrGrantCredentialInvalid
	}
	id, err := pgUUID(issuer.Credential.ID)
	if err != nil || !id.Valid {
		return access.ErrGrantCredentialInvalid
	}
	principal, err := pgUUID(issuer.PrincipalID)
	if err != nil || !principal.Valid {
		return access.ErrGrantCredentialInvalid
	}
	fingerprint, err := hex.DecodeString(issuer.Credential.Fingerprint)
	if err != nil || len(fingerprint) != 32 {
		return access.ErrGrantCredentialInvalid
	}
	queries := accessdb.New(tx)
	if issuer.Credential.Class == access.GrantCredentialClassSession {
		_, err = queries.LockActiveBrowserSessionCredential(ctx, accessdb.LockActiveBrowserSessionCredentialParams{
			ID: id, PrincipalID: principal, Fingerprint: fingerprint,
		})
	} else {
		err = r.checkCredentialEvidence(ctx, tx, issuer)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return access.ErrGrantCredentialInvalid
	}
	if err != nil {
		return err
	}
	if err := r.checkCredentialPermissionCeiling(ctx, tx, issuer, requested); err != nil {
		return err
	}
	// A lock wait without a row update can outlive the earlier WHERE expiry
	// predicate. Recheck with the database clock after all locks are acquired.
	var active bool
	if issuer.Credential.Class == access.GrantCredentialClassSession {
		active, err = queries.IsActiveBrowserSessionCredential(ctx, accessdb.IsActiveBrowserSessionCredentialParams{
			ID: id, PrincipalID: principal, Fingerprint: fingerprint,
		})
	} else {
		active, err = queries.IsActiveAPITokenCredential(ctx, accessdb.IsActiveAPITokenCredentialParams{
			ID: id, PrincipalID: principal, Fingerprint: fingerprint,
		})
	}
	if err != nil {
		return err
	}
	if !active {
		return access.ErrGrantCredentialInvalid
	}
	return ctx.Err()
}
