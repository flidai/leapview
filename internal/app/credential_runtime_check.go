package app

import (
	"context"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// The module boundary must return only fixed, redacted errors and context
// cancellation, never native-client or reader diagnostics.
type localRuntimeCredentialAnalytics interface {
	CheckLocalRuntimeCredential(context.Context, connectionbinding.TargetBinding, string, func(context.Context, func(map[string]string) error) error) (connectionbinding.CredentialIdentity, error)
}

var _ localRuntimeCredentialAnalytics = (*analyticsmodule.Module)(nil)

// localRuntimeCredentialCheck connects an injected authorized reader to a
// synchronous pool check. The foreground wrapper supplies its request-local
// authority and exact serving identity; a successful check does not install
// or activate a runtime.
type localRuntimeCredentialCheck struct {
	targetID    string
	environment string
	reader      credentialmodule.RuntimeCredentialReader
	owners      credentialmodule.CustomerOwnerReader
	bindings    credentialConnectionBindingLookup
	analytics   localRuntimeCredentialAnalytics
}

func (check localRuntimeCredentialCheck) check(
	ctx context.Context,
	identity projectgraph.ServingIdentity,
	binding connectionbinding.TargetBinding,
	versionID string,
) (connectionbinding.CredentialIdentity, error) {
	want := connectionbinding.CredentialIdentity{CredentialVersionID: versionID}
	if ctx == nil || identity.Validate() != nil || want.Validate() != nil ||
		binding.Validate() != nil || !binding.Enabled || binding.ConnectorKind != "postgres" ||
		binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle ||
		binding.TargetID.String() != check.targetID || binding.Scope.Environment != check.environment ||
		binding.Scope.ProjectID != identity.ProjectID || binding.Scope.Environment != identity.Environment ||
		typednil.IsNil(check.reader) || typednil.IsNil(check.owners) || typednil.IsNil(check.bindings) || typednil.IsNil(check.analytics) {
		return connectionbinding.CredentialIdentity{}, credentialmodule.ErrRuntimeInvalid
	}
	resource := credentialmodule.RuntimeResource{
		ScopeKind: "connection", TargetID: check.targetID, ProjectID: identity.ProjectID.String(),
		Environment: identity.Environment, ResourceID: binding.ConnectionID.String(),
	}
	// The module invokes read only after acquiring source-work admission, and
	// closes its local pool before the consumer returns. No client escapes.
	read := func(admitted context.Context, consume func(map[string]string) error) error {
		if admitted == nil || consume == nil {
			return credentialmodule.ErrRuntimeUnavailable
		}
		expected, err := check.scope(admitted, binding, resource)
		if err != nil {
			return err
		}
		err = check.reader.WithCredential(admitted, identity, resource, func(reference credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
			current, err := check.scope(admitted, binding, resource)
			if err != nil {
				return err
			}
			if current != expected || reference.Scope != expected || reference.VersionID != versionID {
				return credentialmodule.ErrRuntimeConflict
			}
			if err := consume(fields); err != nil {
				return err
			}
			current, err = check.scope(admitted, binding, resource)
			if err != nil {
				return err
			}
			if current != expected {
				return credentialmodule.ErrRuntimeConflict
			}
			return nil
		})
		if err != nil {
			return safeRuntimeCredentialReadError(admitted, err)
		}
		return admitted.Err()
	}
	got, err := check.analytics.CheckLocalRuntimeCredential(ctx, binding, versionID, read)
	if err != nil {
		return connectionbinding.CredentialIdentity{}, err
	}
	if got != want {
		return connectionbinding.CredentialIdentity{}, credentialmodule.ErrRuntimeUnavailable
	}
	return got, nil
}

func (check localRuntimeCredentialCheck) scope(ctx context.Context, expected connectionbinding.TargetBinding, resource credentialmodule.RuntimeResource) (credentialmodule.RuntimeScope, error) {
	if err := ctx.Err(); err != nil {
		return credentialmodule.RuntimeScope{}, err
	}
	current, err := check.bindings.Binding(ctx, expected.Scope, expected.TargetID, expected.ConnectionID)
	if err != nil {
		return credentialmodule.RuntimeScope{}, safeRuntimeCredentialReadError(ctx, err)
	}
	if current.Validate() != nil || current.Evidence() != expected.Evidence() || current.Enabled != expected.Enabled ||
		current.AuthenticationMode != expected.AuthenticationMode || current.CredentialReference != expected.CredentialReference {
		return credentialmodule.RuntimeScope{}, credentialmodule.ErrRuntimeConflict
	}
	owner, err := check.owners.CustomerOwner(ctx)
	if err != nil {
		return credentialmodule.RuntimeScope{}, safeRuntimeCredentialReadError(ctx, err)
	}
	if owner == "" || len(owner) > 255 || !utf8.ValidString(owner) ||
		strings.IndexFunc(owner, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) >= 0 {
		return credentialmodule.RuntimeScope{}, credentialmodule.ErrRuntimeUnavailable
	}
	return credentialmodule.RuntimeScope{
		Resource: resource, OwnerID: owner, Purpose: "connection-authentication", Provider: "postgres",
		Destination: expected.Evidence().EndpointConfigHash,
	}, nil
}

func safeRuntimeCredentialReadError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	for _, safe := range []error{context.Canceled, context.DeadlineExceeded, credentialmodule.ErrRuntimeForbidden, credentialmodule.ErrRuntimeNotFound, credentialmodule.ErrRuntimeConflict, credentialmodule.ErrRuntimeInvalid} {
		if errors.Is(err, safe) {
			return safe
		}
	}
	return credentialmodule.ErrRuntimeUnavailable
}
