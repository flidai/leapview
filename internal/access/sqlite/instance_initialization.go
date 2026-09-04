package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/access"
)

// Initialized reports whether the access-owned one-shot initialization marker
// exists. A missing marker is the normal pre-bootstrap state.
func (r *Repository) Initialized(ctx context.Context) (bool, error) {
	if r == nil || r.db == nil {
		return false, fmt.Errorf("access repository database is required")
	}
	var marker string
	err := r.db.QueryRowContext(ctx, `SELECT value FROM platform_settings WHERE key = ?`, access.InstanceInitializedSetting).Scan(&marker)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

func (r *Repository) InitializeInstance(
	ctx context.Context,
	input access.InstanceInitializationInput,
	prepare func(access.InitialInstanceCredentials) error,
) (access.InitialInstanceCredentials, error) {
	var result access.InitialInstanceCredentials
	if input.EvaluationDataIngest &&
		input.Environment != "evaluation" {
		return result, fmt.Errorf(
			"evaluation data ingest is restricted to the evaluation environment",
		)
	}
	err := r.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		sqliteRepo, ok := txRepo.(*Repository)
		if !ok {
			return nil, fmt.Errorf("initialize access transaction is unavailable")
		}
		inserted, err := sqliteRepo.InsertPlatformSettingIfMissing(
			ctx,
			access.InstanceInitializedSetting,
			input.Now.UTC().Format(time.RFC3339),
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
		expires := input.Now.UTC().Add(24 * time.Hour).Truncate(time.Second)
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
