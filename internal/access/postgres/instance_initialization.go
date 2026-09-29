package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

var _ access.InstanceInitializer = (*Repository)(nil)
var _ access.ProjectClaimPublisherRepository = (*Repository)(nil)

// Initialized reports whether the access-owned one-shot initialization marker
// exists. The marker lives in access.platform_setting, not the platform
// bootstrap settings table, so native Admin replay and acknowledgement share
// the exact authority that performs initialization.
func (r *Repository) Initialized(ctx context.Context) (bool, error) {
	db, err := r.requireDB()
	if err != nil {
		return false, err
	}
	return accessdb.New(db).HasPlatformSetting(ctx, access.InstanceInitializedSetting)
}

// InitializeInstance performs the one-shot administrator bootstrap as one
// database transaction. The marker, principal, role, publisher credential,
// and audit event therefore cannot be observed independently.
func (r *Repository) InitializeInstance(
	ctx context.Context,
	input access.InstanceInitializationInput,
	prepare func(access.InitialInstanceCredentials) error,
) (access.InitialInstanceCredentials, error) {
	var result access.InitialInstanceCredentials
	if input.EvaluationDataIngest && input.Environment != "evaluation" {
		return result, fmt.Errorf("evaluation data ingest is restricted to the evaluation environment")
	}
	permissions, err := access.InitialProjectClaimPermissions(input.InstanceID)
	if err != nil {
		return result, fmt.Errorf("initial project-claim permission: %w", err)
	}
	err = r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		postgresRepo, ok := txRepo.(*Repository)
		if !ok {
			return nil, fmt.Errorf("initialize access transaction is unavailable")
		}
		db, err := postgresRepo.requireDB()
		if err != nil {
			return nil, err
		}
		initializedEpoch, err := accessdb.New(db).DatabaseNow(ctx)
		if err != nil {
			return nil, err
		}
		initializedAt := dbEpochMicros(initializedEpoch)
		inserted, err := postgresRepo.InsertPlatformSettingIfMissing(
			ctx,
			access.InstanceInitializedSetting,
			initializedAt.UTC().Format(time.RFC3339Nano),
		)
		if err != nil {
			return nil, err
		}
		if !inserted {
			return nil, access.ErrInstanceAlreadyInitialized
		}
		created, err := txRepo.CreateLocalUser(ctx, access.LocalUserInput{
			Email: input.Email, DisplayName: input.Email, MustChange: true,
		})
		if err != nil {
			return nil, err
		}
		principal, err := txRepo.SetPlatformRole(ctx, access.PlatformRoleInput{
			PrincipalID: created.Principal.ID,
			Email:       input.Email,
			DisplayName: input.Email,
			Role:        access.PlatformRoleAdmin,
		})
		if err != nil {
			return nil, err
		}

		// Expiration is derived from PostgreSQL's clock so the bootstrap
		// credential follows the same temporal authority as token validation.
		expiresEpoch, err := accessdb.New(db).DatabaseNowPlus24Hours(ctx)
		if err != nil {
			return nil, err
		}
		expires := dbEpochMicros(expiresEpoch)
		expires = expires.UTC().Truncate(time.Second)
		scoped, ok := txRepo.(access.ScopedAPITokenRepository)
		if !ok {
			return nil, fmt.Errorf("typed scoped API token issuance is unavailable")
		}
		token, metadata, err := scoped.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
			PrincipalID: principal.ID,
			Name:        access.APITokenNameInitialProjectClaim,
			Permissions: permissions,
			ExpiresAt:   expires,
		})
		if err != nil {
			return nil, err
		}
		claimID, err := pgUUID(metadata.ID)
		if err != nil {
			return nil, err
		}
		principalID, err := pgUUID(principal.ID)
		if err != nil {
			return nil, err
		}
		if err := accessdb.New(db).CreateInitialPasswordSetup(ctx, accessdb.CreateInitialPasswordSetupParams{ClaimCredentialID: claimID, PrincipalID: principalID, InstanceID: input.InstanceID}); err != nil {
			return nil, err
		}
		result = access.InitialInstanceCredentials{
			Email:                      input.Email,
			TemporaryPassword:          created.Password,
			ProjectClaimToken:          token,
			ProjectClaimTokenExpiresAt: expires,
		}
		if prepare != nil {
			if err := prepare(result); err != nil {
				return nil, err
			}
		}
		return []access.AuditEventInput{{
			PrincipalID:  principal.ID,
			Action:       "instance.initialized",
			ResourceKind: "instance",
			ResourceID:   input.Environment,
			Status:       "success",
		}}, nil
	})
	if err != nil {
		return access.InitialInstanceCredentials{}, err
	}
	return result, nil
}

// ExchangeProjectClaimPublisher is a mutation primitive intended to run on
// the tx-scoped Repository supplied to an audited mutation callback. Replays
// revoke all earlier publishers for this exact claim before returning the new
// secret, so a lost response is recoverable by retrying the still-live claim.
func (r *Repository) ExchangeProjectClaimPublisher(
	ctx context.Context,
	input access.ProjectClaimPublisherExchangeInput,
) (access.ProjectClaimPublisherCredentials, error) {
	var result access.ProjectClaimPublisherCredentials
	projectID, err := validateProjectClaimPublisherExchange(input.InstanceID, input.ProjectID, input.PrincipalID, input.ClaimCredentialID, input.ClaimedProjectID, input.ClaimedBy)
	if err != nil {
		return result, err
	}
	if err := r.requireProjectClaimPublisherTransaction(); err != nil {
		return result, err
	}
	db, err := r.requireDB()
	if err != nil {
		return result, err
	}
	if err := lockProjectClaimPublisher(ctx, db, input.ClaimCredentialID); err != nil {
		return result, err
	}
	nowEpoch, err := accessdb.New(db).DatabaseNow(ctx)
	if err != nil {
		return result, err
	}
	now := dbEpochMicros(nowEpoch)
	claim, err := r.APITokenAuthorityEvidence(ctx, input.PrincipalID, input.ClaimCredentialID, now)
	if err != nil || !isInitialProjectClaimToken(claim, input.InstanceID) {
		return result, access.ErrForbidden
	}

	permissions, err := access.InitialProjectPublisherPermissions(projectID)
	if err != nil {
		return result, err
	}
	tokenName := access.InitialProjectClaimPublisherTokenName(input.ClaimCredentialID)
	revoked, err := revokeNamedProjectClaimPublishers(ctx, db, input.PrincipalID, tokenName)
	if err != nil {
		return result, err
	}
	expiresAt := now.Add(defaultAPITokenTTL).UTC().Truncate(time.Second)
	secret, metadata, err := r.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{
		PrincipalID: input.PrincipalID,
		Name:        tokenName,
		Permissions: permissions,
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		return result, err
	}
	publisherID, err := pgUUID(metadata.ID)
	if err != nil {
		return result, err
	}
	claimID, err := pgUUID(input.ClaimCredentialID)
	if err != nil {
		return result, err
	}
	principalID, err := pgUUID(input.PrincipalID)
	if err != nil {
		return result, err
	}
	// A legacy claim may still exchange normally, but cannot invent an
	// initial-password window. Closed records are linked without reopening.
	if err := accessdb.New(db).RecordInitialPublisherOrigin(ctx, accessdb.RecordInitialPublisherOriginParams{PublisherCredentialID: publisherID, ClaimCredentialID: claimID, PrincipalID: principalID, InstanceID: input.InstanceID, ProjectID: projectID.String()}); err != nil {
		return result, err
	}
	parsedExpiry, err := time.Parse(time.RFC3339Nano, metadata.ExpiresAt)
	if err != nil {
		return result, fmt.Errorf("parse initial publisher expiry: %w", err)
	}
	result = access.ProjectClaimPublisherCredentials{
		ClaimCredentialID:             input.ClaimCredentialID,
		PublisherCredentialID:         metadata.ID,
		PublisherToken:                secret,
		PublisherTokenExpiresAt:       parsedExpiry.UTC(),
		RevokedPublisherCredentialIDs: revoked,
	}
	return result, nil
}

// AcknowledgeProjectClaimPublisher revokes the claim credential only after a
// caller presents the currently active publisher with the exact initial pair
// set. The operation is replay-safe: an already-revoked claim is accepted only
// when the active current publisher still proves this exact handoff.
func (r *Repository) AcknowledgeProjectClaimPublisher(
	ctx context.Context,
	input access.ProjectClaimPublisherAcknowledgeInput,
) error {
	projectID, err := validateProjectClaimPublisherExchange(input.InstanceID, input.ProjectID, input.PrincipalID, input.ClaimCredentialID, input.ClaimedProjectID, input.ClaimedBy)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.PublisherCredentialID) == "" || strings.TrimSpace(input.PublisherCredentialID) != input.PublisherCredentialID {
		return access.ErrForbidden
	}
	if err := r.requireProjectClaimPublisherTransaction(); err != nil {
		return err
	}
	db, err := r.requireDB()
	if err != nil {
		return err
	}
	if err := lockProjectClaimPublisher(ctx, db, input.ClaimCredentialID); err != nil {
		return err
	}
	nowEpoch, err := accessdb.New(db).DatabaseNow(ctx)
	if err != nil {
		return err
	}
	now := dbEpochMicros(nowEpoch)
	claim, err := r.apiToken(ctx, input.ClaimCredentialID)
	if err != nil || !isInitialProjectClaimToken(claim, input.InstanceID) || claim.PrincipalID != input.PrincipalID {
		return access.ErrForbidden
	}
	publisher, err := r.APITokenAuthorityEvidence(ctx, input.PrincipalID, input.PublisherCredentialID, now)
	if err != nil || publisher.Name != access.InitialProjectClaimPublisherTokenName(input.ClaimCredentialID) ||
		!isExactInitialProjectPublisher(publisher, projectID) {
		return access.ErrForbidden
	}
	if claim.RevokedAt != "" {
		return nil
	}
	if err := r.RevokeAPITokenForPrincipal(ctx, input.PrincipalID, input.ClaimCredentialID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			// A concurrent ACK is serialized by the advisory lock. Treat a
			// disappeared token as forbidden rather than weakening evidence.
			return access.ErrForbidden
		}
		return err
	}
	return nil
}

func validateProjectClaimPublisherExchange(instanceID, projectID, principalID, claimCredentialID, claimedProjectID, claimedBy string) (projectgraph.ResourceID, error) {
	for _, value := range map[string]string{
		"instance": instanceID, "project": projectID, "principal": principalID,
		"claim credential": claimCredentialID, "claimed project": claimedProjectID, "claim principal": claimedBy,
	} {
		if strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value {
			return "", access.ErrForbidden
		}
	}
	if projectID != claimedProjectID || principalID != claimedBy {
		return "", access.ErrForbidden
	}
	parsed, err := projectgraph.NewResourceID(projectID)
	if err != nil {
		return "", access.ErrForbidden
	}
	return parsed, nil
}

func (r *Repository) requireProjectClaimPublisherTransaction() error {
	if r == nil || r.db == nil {
		return errors.New("project-claim publisher transaction is required")
	}
	if _, ok := r.db.(pgx.Tx); !ok {
		return errors.New("project-claim publisher mutation must run in an audited transaction")
	}
	return nil
}

func lockProjectClaimPublisher(ctx context.Context, db DBTX, claimCredentialID string) error {
	return accessdb.New(db).LockProjectClaimPublisher(ctx, claimCredentialID)
}

func revokeNamedProjectClaimPublishers(ctx context.Context, db DBTX, principalID, tokenName string) ([]string, error) {
	principalID, err := uuidID("principal id", principalID)
	if err != nil {
		return nil, err
	}
	parsedPrincipalID, err := pgUUID(principalID)
	if err != nil {
		return nil, err
	}
	ids, err := accessdb.New(db).RevokeNamedProjectClaimPublishers(ctx, accessdb.RevokeNamedProjectClaimPublishersParams{
		PrincipalID: parsedPrincipalID,
		TokenName:   tokenName,
	})
	if err != nil {
		return nil, err
	}
	revoked := make([]string, 0, len(ids))
	for _, id := range ids {
		revoked = append(revoked, principalUUID(id))
	}
	return revoked, nil
}

func isInitialProjectClaimToken(token access.APIToken, instanceID string) bool {
	want, err := access.InitialProjectClaimPermissions(instanceID)
	return err == nil && token.Name == access.APITokenNameInitialProjectClaim &&
		token.PermissionProfile == access.PermissionCatalogProfile && len(token.Capabilities) == 0 &&
		exactPermissionSet(token.Permissions, want)
}

func isExactInitialProjectPublisher(token access.APIToken, projectID projectgraph.ResourceID) bool {
	want, err := access.InitialProjectPublisherPermissions(projectID)
	return err == nil && token.PermissionProfile == access.PermissionCatalogProfile && len(token.Capabilities) == 0 &&
		exactPermissionSet(token.Permissions, want)
}

func exactPermissionSet(actual, expected []access.PermissionPair) bool {
	if len(actual) != len(expected) {
		return false
	}
	wanted := make(map[string]struct{}, len(expected))
	for _, pair := range expected {
		wanted[pair.Key()] = struct{}{}
	}
	for _, pair := range actual {
		if _, ok := wanted[pair.Key()]; !ok {
			return false
		}
		delete(wanted, pair.Key())
	}
	return len(wanted) == 0
}
