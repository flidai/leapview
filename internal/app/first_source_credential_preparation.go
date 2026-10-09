package app

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/access"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type firstSourceCredentialPreparationRequest struct {
	PreparationID, VersionID, ReceiptID string
	// Ordinary HTTP planning sets source owner to publisher. Explicit owner
	// intent here also preserves normal internal author/reviewer workflows;
	// neither identity is inferred from the credential operator's session.
	PublisherID, SourceOwnerID            string
	SourceDigest, SourceAttestationDigest string
	PlanIdempotencyKey                    string
	ExpectedTargetRevision                int64
}

// Returned metadata identifies the immutable preparation without revealing a
// credential, binding configuration, receipt proof or publication authority.
type firstSourceCredentialPreparationResult struct {
	PreparationID, IntentDigest, PlanRequestDigest string
	CreatedAt                                      time.Time
}

type firstSourceRetainedSource struct {
	ProjectID, SourceDigest, SourceAttestationDigest string
}

type firstSourceCredentialReceiptReader interface {
	ReadValidationReceipt(context.Context, string, string) (credentialmodule.ValidationReceipt, error)
}

type firstSourcePreparationTargetReader interface {
	TargetTx(context.Context, pgx.Tx, string) (deploymentpostgres.DeliveryTarget, error)
}

type firstSourceCredentialPreparationService struct {
	authority    firstSourceCredentialAuthority
	receipts     firstSourceCredentialReceiptReader
	preparations credentialmodule.FirstSourcePreparationMutations
	targets      firstSourcePreparationTargetReader
	// Composition uses the normal exact retained attestation reader with the
	// explicit owner intent. Normal native planning retains source ownership
	// authorization; this read cannot confer it or select an ambient owner.
	retainedSource func(context.Context, string, string, string, string) (firstSourceRetainedSource, error)
	probePolicy    func() string
}

func (s firstSourceCredentialPreparationService) Prepare(ctx context.Context, actor string, resource credentialmodule.ValidationResource, request firstSourceCredentialPreparationRequest) (firstSourceCredentialPreparationResult, error) {
	if err := s.ready(ctx, resource); err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	if !firstSourcePreparationUUID(request.PreparationID) || !firstSourcePreparationUUID(request.VersionID) || !firstSourcePreparationUUID(request.ReceiptID) ||
		!firstSourcePreparationText(request.PublisherID) || !firstSourcePreparationText(request.SourceOwnerID) || !firstSourcePreparationText(request.PlanIdempotencyKey) || request.ExpectedTargetRevision < 1 {
		return firstSourceCredentialPreparationResult{}, credentialmodule.ErrInvalidValidation
	}
	receipt, err := s.receipts.ReadValidationReceipt(ctx, s.authority.targetID, request.ReceiptID)
	if err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	if !firstSourcePreparationReceiptScope(receipt, actor, resource, request.VersionID) {
		return firstSourceCredentialPreparationResult{}, credentialmodule.ErrValidationConflict
	}
	if err := s.checkRetainedSource(ctx, resource.ProjectID, request.SourceOwnerID, request.SourceDigest, request.SourceAttestationDigest); err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	native := deploymentmodule.NativeDeliveryPlanRequest{ProjectID: projectgraph.ResourceID(resource.ProjectID), TargetID: resource.TargetID, Environment: resource.Environment,
		PrincipalID: request.PublisherID, SourceOwnerID: request.SourceOwnerID, Operation: "code_change", SourceDigest: request.SourceDigest, SourceAttestationDigest: request.SourceAttestationDigest,
		IdempotencyKey: request.PlanIdempotencyKey, FirstSourcePreparationID: request.PreparationID}
	requestDigest, err := deploymentmodule.NativeDeliveryPlanRequestDigest(native)
	if err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	intent := credentialmodule.FirstSourcePreparationIntent{PreparationID: request.PreparationID, Receipt: receipt, PublisherID: request.PublisherID, SourceOwnerID: request.SourceOwnerID,
		SourceDigest: request.SourceDigest, SourceAttestationDigest: request.SourceAttestationDigest, PlanOperation: "code_change", PlanIdempotencyKey: request.PlanIdempotencyKey,
		PlanRequestDigest: requestDigest, ExpectedTargetRevision: request.ExpectedTargetRevision}
	var result credentialmodule.FirstSourcePreparation
	err = s.authority.WithAuthorization(ctx, actor, resource, access.ActionConnectionManage, func(ctx context.Context, tx pgx.Tx, admission credentialmodule.FirstSourceAdmission) error {
		intent.AdmissionOperationID, intent.AdmissionDigest = admission.Intent.OperationID, admission.IntentDigest
		if err := s.checkIntentTx(ctx, tx, admission, intent); err != nil {
			return err
		}
		var err error
		result, err = s.preparations.PrepareTx(ctx, tx, intent, func(ctx context.Context, tx pgx.Tx, current credentialmodule.FirstSourcePreparationIntent) error {
			return s.checkIntentTx(ctx, tx, admission, current)
		})
		return err
	})
	if err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	return firstSourcePreparationMetadata(result), nil
}

func (s firstSourceCredentialPreparationService) Renew(ctx context.Context, actor string, resource credentialmodule.ValidationResource, preparationID, receiptID string) (firstSourceCredentialPreparationResult, error) {
	if err := s.ready(ctx, resource); err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	if !firstSourcePreparationUUID(preparationID) || !firstSourcePreparationUUID(receiptID) {
		return firstSourceCredentialPreparationResult{}, credentialmodule.ErrInvalidValidation
	}
	// All pool-owning receipt/source reads finish before acquiring authority.
	prepared, err := s.preparations.Preparation(ctx, s.authority.targetID, preparationID)
	if err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	i := prepared.Intent
	receipt, err := s.receipts.ReadValidationReceipt(ctx, s.authority.targetID, receiptID)
	if err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	if !firstSourcePreparationReceiptScope(receipt, actor, resource, i.Receipt.Binding.VersionID) || receipt.Binding != i.Receipt.Binding || receipt.BindingID != i.Receipt.BindingID || receipt.BindingRevision != i.Receipt.BindingRevision || receipt.ConfigurationDigest != i.Receipt.ConfigurationDigest {
		return firstSourceCredentialPreparationResult{}, credentialmodule.ErrValidationConflict
	}
	if err := s.checkRetainedSource(ctx, resource.ProjectID, i.SourceOwnerID, i.SourceDigest, i.SourceAttestationDigest); err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	err = s.authority.WithAuthorization(ctx, actor, resource, access.ActionConnectionManage, func(ctx context.Context, tx pgx.Tx, admission credentialmodule.FirstSourceAdmission) error {
		if err := s.checkIntentTx(ctx, tx, admission, i); err != nil {
			return err
		}
		_, err := s.preparations.RenewReceiptTx(ctx, tx, resource.TargetID, preparationID, receiptID, func(ctx context.Context, tx pgx.Tx, current credentialmodule.FirstSourcePreparationIntent) error {
			digest, err := current.Digest()
			if err != nil || digest != prepared.IntentDigest {
				return credentialmodule.ErrValidationConflict
			}
			return s.checkIntentTx(ctx, tx, admission, current)
		})
		return err
	})
	if err != nil {
		return firstSourceCredentialPreparationResult{}, err
	}
	return firstSourcePreparationMetadata(prepared), nil
}

func (s firstSourceCredentialPreparationService) ready(ctx context.Context, resource credentialmodule.ValidationResource) error {
	if ctx == nil || !s.authority.production || resource.Validate() != nil || resource.ScopeKind != "connection" || resource.TargetID != s.authority.targetID || resource.Environment != s.authority.environment {
		return credentialmodule.ErrValidationForbidden
	}
	if typednil.IsNil(s.receipts) || typednil.IsNil(s.preparations) || typednil.IsNil(s.targets) || s.retainedSource == nil || s.probePolicy == nil {
		return credentialmodule.ErrValidationUnavailable
	}
	return nil
}

func (s firstSourceCredentialPreparationService) checkRetainedSource(ctx context.Context, project, owner, digest, attestation string) error {
	if platformdigest.ValidateSHA256Identity(digest) != nil || platformdigest.ValidateSHA256Identity(attestation) != nil {
		return credentialmodule.ErrInvalidValidation
	}
	retained, err := s.retainedSource(ctx, project, owner, digest, attestation)
	if err != nil {
		return err
	}
	if retained.ProjectID != project || retained.SourceDigest != digest || retained.SourceAttestationDigest != attestation {
		return credentialmodule.ErrValidationConflict
	}
	return nil
}

// This callback performs only bounded reads using the supplied transaction.
// Publication remains a separately authorized normal native plan operation.
func (s firstSourceCredentialPreparationService) checkIntentTx(ctx context.Context, tx pgx.Tx, admission credentialmodule.FirstSourceAdmission, i credentialmodule.FirstSourcePreparationIntent) error {
	if i.Validate() != nil || !i.MatchesAdmission(admission) || i.Receipt.Binding.Purpose != "connection-authentication" {
		return credentialmodule.ErrValidationConflict
	}
	binding, err := s.authority.lockAdmittedStateTx(ctx, tx, admission)
	if err != nil {
		return err
	}
	digest, err := credentialValidationConfigurationDigest(binding, s.probePolicy())
	if err != nil || digest != i.Receipt.ConfigurationDigest || binding.Evidence().EndpointConfigHash != i.Receipt.Binding.Destination {
		return credentialmodule.ErrValidationConflict
	}
	target, err := s.targets.TargetTx(ctx, tx, s.authority.targetID)
	if errors.Is(err, deploymentpostgres.ErrNotFound) {
		if i.ExpectedTargetRevision != 1 {
			return credentialmodule.ErrValidationConflict
		}
		return nil // The outer fence alone may own the temporary initial row.
	}
	if err != nil {
		return err
	}
	if target.TargetID != admission.Intent.TargetID || target.ProjectID != admission.Intent.ProjectID || target.Environment != admission.Intent.Environment || target.ActiveGenerationID != "" || target.ActivePublicationID != "" || target.TargetRevision != i.ExpectedTargetRevision {
		return credentialmodule.ErrValidationConflict
	}
	return nil
}

func firstSourcePreparationReceiptScope(receipt credentialmodule.ValidationReceipt, actor string, resource credentialmodule.ValidationResource, version string) bool {
	b := receipt.Binding
	return receipt.Validate() == nil && receipt.ActorID == actor && b.VersionID == version && b.DeploymentID == resource.TargetID &&
		b.ScopeKind == resource.ScopeKind && b.TargetID == resource.TargetID && b.ProjectID == resource.ProjectID && b.Environment == resource.Environment && b.ResourceID == resource.ResourceID
}

func firstSourcePreparationUUID(id string) bool {
	parsed, err := uuid.Parse(id)
	return err == nil && parsed != uuid.Nil && parsed.String() == id
}

func firstSourcePreparationText(value string) bool {
	return value != "" && len(value) <= 255 && value == strings.TrimSpace(value)
}

func firstSourcePreparationMetadata(p credentialmodule.FirstSourcePreparation) firstSourceCredentialPreparationResult {
	return firstSourceCredentialPreparationResult{PreparationID: p.Intent.PreparationID, IntentDigest: p.IntentDigest, PlanRequestDigest: p.Intent.PlanRequestDigest, CreatedAt: p.CreatedAt}
}
