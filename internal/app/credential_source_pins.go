package app

import (
	"context"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/jackc/pgx/v5"
)

// LocalCredentialPin never accepts a version from request context. The private
// context carries only the already-reserved operation; its durable receipt is
// re-read and authorized before candidate work. All other pins come from an
// independently verified committed generation.
func (a *sourceCredentialActivation) LocalCredentialPin(ctx context.Context, request connectionbinding.RuntimeBindingRequest, binding connectionbinding.TargetBinding) (string, error) {
	if request.TargetID.String() != a.config.TargetID || request.Identity.Environment != a.config.Environment || binding.TargetID != request.TargetID || binding.Scope.ProjectID != request.Identity.ProjectID || binding.Scope.Environment != request.Identity.Environment {
		return "", credentialmodule.ErrValidationConflict
	}
	if operation, ok := ctx.Value(sourceCredentialOperationKey{}).(string); ok {
		row, err := a.config.Credentials.GetActivationRequest(ctx, a.config.TargetID, operation)
		if err != nil {
			return "", err
		}
		if row.State != "preparing" || row.Receipt.ActorID != request.Actor || row.Resource().ProjectID != request.Identity.ProjectID.String() || row.Resource().Environment != request.Identity.Environment {
			return "", credentialmodule.ErrValidationConflict
		}
		if row.Resource().ResourceID == binding.ConnectionID.String() {
			if err = a.transaction(ctx, func(tx pgx.Tx) error {
				if err := a.auth(row, request.Actor)(ctx, tx); err != nil {
					return err
				}
				return a.config.Credentials.CheckActivationReceiptFreshTx(ctx, tx, a.config.TargetID, operation)
			}); err != nil {
				return "", err
			}
			return sourceReceiptPin(row, request, binding)
		}
	}
	target, err := a.config.Delivery.Target(ctx, a.config.TargetID)
	if err != nil {
		return "", err
	}
	if target.ProjectID != request.Identity.ProjectID.String() || target.Environment != request.Identity.Environment {
		return "", credentialmodule.ErrValidationConflict
	}
	if target.ActiveGenerationID == "" {
		return "", nil
	}
	evidence, err := a.config.Evidence.BindingEvidence(ctx, target.ActiveGenerationID, target.ProjectID)
	if err != nil {
		return "", err
	}
	return sourceCommittedPin(evidence, binding)
}
func sourceReceiptPin(row credentialmodule.ActivationRequestRecord, request connectionbinding.RuntimeBindingRequest, binding connectionbinding.TargetBinding) (string, error) {
	receipt := row.Receipt
	b := receipt.Binding
	if row.State != "preparing" || row.Request.VersionID != b.VersionID || row.Request.ExpectedBindingRevision != receipt.BindingRevision || receipt.ActorID != request.Actor || b.ScopeKind != "connection" || b.DeploymentID != request.TargetID.String() || b.TargetID != request.TargetID.String() || b.ProjectID != request.Identity.ProjectID.String() || b.Environment != request.Identity.Environment || b.ResourceID != binding.ConnectionID.String() || b.Provider != binding.ConnectorKind || receipt.BindingID != binding.ID.String() || receipt.BindingRevision != binding.Revision || b.Destination != binding.Evidence().EndpointConfigHash || !binding.Enabled || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle || (connectionbinding.CredentialIdentity{CredentialVersionID: b.VersionID}).Validate() != nil {
		return "", credentialmodule.ErrValidationConflict
	}
	return b.VersionID, nil
}
func sourceCommittedPin(evidence []analyticsmodule.ActiveRuntimeBindingEvidence, binding connectionbinding.TargetBinding) (string, error) {
	var found *analyticsmodule.ActiveRuntimeBindingEvidence
	for _, pin := range evidence {
		if pin.ConnectionID == binding.ConnectionID {
			if found != nil {
				return "", credentialmodule.ErrValidationConflict
			}
			copy := pin
			found = &copy
		}
	}
	if found == nil || found.CredentialVersionID == "" {
		return "", nil
	}
	if found.BindingID != binding.ID || found.ConnectorKind != binding.ConnectorKind || found.Revision != binding.Revision || found.EndpointConfigHash != binding.Evidence().EndpointConfigHash || found.ValidatedVersion != "" || found.Access != "" || !binding.Enabled || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle || (connectionbinding.CredentialIdentity{CredentialVersionID: found.CredentialVersionID}).Validate() != nil {
		return "", credentialmodule.ErrValidationConflict
	}
	return found.CredentialVersionID, nil
}
