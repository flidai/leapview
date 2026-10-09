package app

import (
	"context"

	"github.com/flidai/leapview/internal/access"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/jackc/pgx/v5"
)

type firstSourceCredentialPreparationCommandAdapter struct {
	service *firstSourceCredentialPreparationService
}

func (a firstSourceCredentialPreparationCommandAdapter) Authorize(ctx context.Context, actor string, resource credentialmodule.ValidationResource) error {
	if a.service == nil {
		return credentialmodule.ErrValidationUnavailable
	}
	return a.service.authority.WithAuthorization(ctx, actor, resource, access.ActionConnectionManage, func(context.Context, pgx.Tx, credentialmodule.FirstSourceAdmission) error { return nil })
}

func firstSourceCredentialPreparationCommands(service *firstSourceCredentialPreparationService) credentialmodule.FirstSourcePreparationCommandService {
	if service == nil {
		return nil
	}
	return firstSourceCredentialPreparationCommandAdapter{service: service}
}

func (a firstSourceCredentialPreparationCommandAdapter) Prepare(ctx context.Context, actor string, resource credentialmodule.ValidationResource, request credentialmodule.FirstSourcePreparationCommandRequest) (credentialmodule.FirstSourcePreparationCommandResult, error) {
	if a.service == nil {
		return credentialmodule.FirstSourcePreparationCommandResult{}, credentialmodule.ErrValidationUnavailable
	}
	result, err := a.service.Prepare(ctx, actor, resource, firstSourceCredentialPreparationRequest{
		PreparationID: request.PreparationID, VersionID: request.VersionID, ReceiptID: request.ReceiptID,
		PublisherID: actor, SourceOwnerID: actor,
		SourceDigest: request.SourceDigest, SourceAttestationDigest: request.SourceAttestationDigest,
		PlanIdempotencyKey: request.PlanIdempotencyKey, ExpectedTargetRevision: request.ExpectedTargetRevision,
	})
	return firstSourceCredentialPreparationCommandMetadata(result), err
}

func (a firstSourceCredentialPreparationCommandAdapter) Renew(ctx context.Context, actor string, resource credentialmodule.ValidationResource, preparation, receipt string) (credentialmodule.FirstSourcePreparationCommandResult, error) {
	if a.service == nil {
		return credentialmodule.FirstSourcePreparationCommandResult{}, credentialmodule.ErrValidationUnavailable
	}
	result, err := a.service.Renew(ctx, actor, resource, preparation, receipt)
	return firstSourceCredentialPreparationCommandMetadata(result), err
}

func firstSourceCredentialPreparationCommandMetadata(result firstSourceCredentialPreparationResult) credentialmodule.FirstSourcePreparationCommandResult {
	return credentialmodule.FirstSourcePreparationCommandResult{PreparationID: result.PreparationID, IntentDigest: result.IntentDigest, PlanRequestDigest: result.PlanRequestDigest, CreatedAt: result.CreatedAt}
}
