package app

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	releasemodule "github.com/flidai/leapview/internal/release/module"
	"github.com/google/uuid"
)

// resolveCommittedRuntimeCredential is the shared non-secret projection used
// after request- or job-specific runtime authority has been checked. It never
// chooses a latest version or treats candidate provenance as a committed pin.
func resolveCommittedRuntimeCredential(ctx context.Context, identity projectgraph.ServingIdentity, resource credentialmodule.RuntimeResource, evidenceSource activeConnectionEvidenceSource, owners credentialmodule.CustomerOwnerReader, bindings credentialConnectionBindingLookup) (credentialmodule.RuntimeCredentialReference, error) {
	connectionID, err := projectgraph.NewResourceID(resource.ResourceID)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeInvalid
	}
	evidence, err := evidenceSource.BindingEvidence(ctx, identity.GenerationID, identity.ProjectID.String())
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	var pin *analyticsmodule.ActiveRuntimeBindingEvidence
	for index := range evidence {
		item := evidence[index]
		if item.ConnectionID != connectionID {
			continue
		}
		if pin != nil {
			return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeConflict
		}
		pin = &item
	}
	if pin == nil || pin.CredentialVersionID == "" {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeNotFound
	}
	versionID, err := uuid.Parse(pin.CredentialVersionID)
	if err != nil || versionID == uuid.Nil || versionID.String() != pin.CredentialVersionID {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeConflict
	}

	binding, err := bindings.Binding(ctx, connectionbinding.BindingScope{
		ProjectID: identity.ProjectID, Environment: identity.Environment,
	}, connectionbinding.TargetID(resource.TargetID), connectionID)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if binding.Validate() != nil || !binding.Enabled || binding.ConnectorKind != "postgres" ||
		binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle ||
		binding.TargetID.String() != resource.TargetID || binding.Scope.ProjectID != identity.ProjectID ||
		binding.Scope.Environment != identity.Environment || binding.ConnectionID != connectionID ||
		binding.ID != pin.BindingID || binding.Revision != pin.Revision ||
		binding.ConnectorKind != pin.ConnectorKind || binding.Evidence().EndpointConfigHash != pin.EndpointConfigHash {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeConflict
	}
	ownerID, err := owners.CustomerOwner(ctx)
	if err != nil {
		return credentialmodule.RuntimeCredentialReference{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	if !canonicalRuntimeCredentialAuthorityValue(ownerID) {
		return credentialmodule.RuntimeCredentialReference{}, credentialmodule.ErrRuntimeUnavailable
	}
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeCredentialReference{}, err
	}
	return credentialmodule.RuntimeCredentialReference{
		VersionID: pin.CredentialVersionID,
		Scope: credentialmodule.RuntimeScope{
			Resource: resource, OwnerID: ownerID, Purpose: "connection-authentication",
			Provider: "postgres", Destination: pin.EndpointConfigHash,
		},
	}, nil
}

func canonicalRuntimeCredentialAuthorityValue(value string) bool {
	return value != "" && len(value) <= 255 && utf8.ValidString(value) && strings.TrimSpace(value) == value &&
		strings.IndexFunc(value, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) < 0
}

func safeRuntimeCredentialAuthorityError(ctx context.Context, err error) error {
	if ctx != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, credentialmodule.ErrRuntimeForbidden) || errors.Is(err, access.ErrForbidden) {
		return credentialmodule.ErrRuntimeForbidden
	}
	if errors.Is(err, credentialmodule.ErrRuntimeNotFound) {
		return credentialmodule.ErrRuntimeNotFound
	}
	if errors.Is(err, releasemodule.ErrNotFound) {
		return credentialmodule.ErrRuntimeNotFound
	}
	return credentialmodule.ErrRuntimeUnavailable
}
