package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
)

var _ access.InstanceInitializer = (*Repository)(nil)

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
	err := r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
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
		capabilities := access.InitialPublisherCapabilities()
		if input.EvaluationDataIngest {
			capabilities = access.LocalEvaluationPublisherCapabilities()
		}
		token, _, err := txRepo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
			PrincipalID:  principal.ID,
			Name:         access.APITokenNameInitialPublisher,
			Capabilities: capabilities,
			ExpiresAt:    expires,
		})
		if err != nil {
			return nil, err
		}
		result = access.InitialInstanceCredentials{
			Email:                   input.Email,
			TemporaryPassword:       created.Password,
			PublisherToken:          token,
			PublisherTokenExpiresAt: expires,
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
	return result, err
}
