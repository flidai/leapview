package module

import (
	"context"
	"encoding/json"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/typednil"
	"github.com/jackc/pgx/v5"
)

// CreatePersonalTokenWithAuthority keeps token creation, caller-owned authority
// locks and audit in one native transaction. The caller retains its target fence
// and must establish the complete live permission ceiling in authorize using tx.
// Persistence adapters stay behind the Access module surface.
func CreatePersonalTokenWithAuthority(ctx context.Context, repository access.AuditedMutationRepository, input access.ScopedAPITokenInput, authorize func(context.Context, pgx.Tx) error) (string, error) {
	if ctx == nil || typednil.IsNil(repository) || authorize == nil {
		return "", access.ErrForbidden
	}
	var secret string
	err := repository.RunAuditedMutation(ctx, func(repository access.Repository) (access.AuditEventInput, error) {
		native, ok := repository.(*accesspostgres.Repository)
		if !ok || native == nil {
			return access.AuditEventInput{}, access.ErrForbidden
		}
		tx, ok := native.DB().(pgx.Tx)
		if !ok || typednil.IsNil(tx) {
			return access.AuditEventInput{}, access.ErrForbidden
		}
		if err := authorize(ctx, tx); err != nil {
			return access.AuditEventInput{}, err
		}
		var token access.APIToken
		var err error
		secret, token, err = native.CreateScopedAPITokenWithMetadata(ctx, input)
		metadata, _ := json.Marshal(map[string]string{"name": input.Name})
		return access.AuditEventInput{PrincipalID: input.PrincipalID, Action: "api_token.created", ResourceKind: "api_token", ResourceID: token.ID, Status: "success", MetadataJSON: string(metadata)}, err
	})
	if err != nil {
		return "", err
	}
	return secret, nil
}
