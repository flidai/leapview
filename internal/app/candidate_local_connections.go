package app

import (
	"context"
	"errors"
	"maps"
	"sort"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	"github.com/flidai/leapview/internal/analytics/sourcework"
	appdeploymentpostgres "github.com/flidai/leapview/internal/app/deploymentpostgres"
	apprefreshpostgres "github.com/flidai/leapview/internal/app/refreshpostgres"
	credentialmodule "github.com/flidai/leapview/internal/credential/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/platform/typednil"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

// Every refresh gets its own connection authority. Ordinary delivery retains
// its shared provider-only adapter; no job capability is installed on it.
type refreshCandidateMutationFactory struct {
	delivery    *appdeploymentpostgres.NativeDeliveryCoordinator
	base        candidateConnectionLeaser
	credentials refreshRuntimeCredentialReaderFactory
}

func (factory refreshCandidateMutationFactory) ForRefresh(ctx context.Context, job refreshrun.JobRecord) (apprefreshpostgres.NativeRefreshDeliveryMutations, error) {
	connections, err := newRefreshCandidateConnections(ctx, factory.base, factory.credentials, job)
	if err != nil {
		return nil, err
	}
	return factory.delivery.WithCandidateConnectionAuthorities(connections, connections)
}

type candidateLocalPin struct {
	binding   connectionbinding.TargetBinding
	reference credentialmodule.RuntimeCredentialReference
}

type refreshCandidateConnections struct {
	base      candidateConnectionLeaser
	authority refreshRuntimeCredentialAuthority
	actor     string
	pins      map[string]candidateLocalPin
	reader    credentialmodule.RuntimeCredentialReader
	useLocal  func(context.Context, connectionbinding.TargetBinding, connectionbinding.CredentialSnapshot, semanticmodel.Connection, func(semanticmodel.Connection) error) error
}

// Construction reads only committed metadata. The credential reader is bound
// here, but it cannot decrypt until SourceRuntime invokes a connection callback.
func newRefreshCandidateConnections(ctx context.Context, base candidateConnectionLeaser, factory refreshRuntimeCredentialReaderFactory, job refreshrun.JobRecord) (*refreshCandidateConnections, error) {
	if ctx == nil {
		return nil, credentialmodule.ErrRuntimeInvalid
	}
	authority, err := factory.bind(job)
	if err != nil {
		return nil, err
	}
	values, err := authority.evidence.BindingEvidence(ctx, job.Identity.GenerationID, job.Identity.ProjectID.String())
	if err != nil {
		return nil, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	result := &refreshCandidateConnections{base: base, authority: authority, actor: job.PrincipalID, pins: make(map[string]candidateLocalPin)}
	if base.module != nil {
		result.useLocal = base.module.WithLocalRuntimeConnection
	}
	for _, value := range values {
		if value.CredentialVersionID == "" {
			continue
		}
		resource := credentialmodule.RuntimeResource{ScopeKind: "connection", TargetID: authority.instanceID, ProjectID: job.Identity.ProjectID.String(), Environment: job.Identity.Environment, ResourceID: value.ConnectionID.String()}
		if !authority.capturedConnection(resource) {
			return nil, credentialmodule.ErrRuntimeForbidden
		}
		reference, err := authority.ResolveRuntimeCredential(ctx, job.Identity, resource)
		if err != nil {
			return nil, err
		}
		if reference.VersionID != value.CredentialVersionID {
			return nil, credentialmodule.ErrRuntimeConflict
		}
		binding, err := authority.bindings.Binding(ctx, connectionbinding.BindingScope{ProjectID: job.Identity.ProjectID, Environment: job.Identity.Environment}, connectionbinding.TargetID(authority.instanceID), value.ConnectionID)
		if err != nil {
			return nil, safeRuntimeCredentialAuthorityError(ctx, err)
		}
		if binding.Validate() != nil || !binding.Enabled || binding.TargetID.String() != resource.TargetID || binding.Scope.ProjectID.String() != resource.ProjectID || binding.Scope.Environment != resource.Environment || binding.ConnectionID.String() != resource.ResourceID ||
			binding.ID != value.BindingID || binding.Revision != value.Revision || binding.Evidence().EndpointConfigHash != value.EndpointConfigHash || binding.ConnectorKind != "postgres" || binding.AuthenticationMode != connectionbinding.AuthenticationExternalBundle {
			return nil, credentialmodule.ErrRuntimeConflict
		}
		if _, exists := result.pins[resource.ResourceID]; exists {
			return nil, credentialmodule.ErrRuntimeConflict
		}
		binding.Endpoint.Options = maps.Clone(binding.Endpoint.Options)
		result.pins[resource.ResourceID] = candidateLocalPin{binding: binding, reference: reference}
	}
	if len(result.pins) > 0 {
		result.reader, err = factory.reader(job)
		if err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

// split never permits a local pin to fall back to the ordinary provider route.
// All candidate connections must be inside the immutable queued permission set.
func (connections *refreshCandidateConnections) split(ctx context.Context, request deploymentmodule.CandidateConnectionRequest) (deploymentmodule.CandidateConnectionRequest, error) {
	if ctx == nil || connections == nil || request.Identity.Validate() != nil || request.Actor != connections.actor || request.TargetID != connections.authority.instanceID || request.Identity.ProjectID != connections.authority.identity.ProjectID || request.Identity.Environment != connections.authority.identity.Environment {
		return deploymentmodule.CandidateConnectionRequest{}, credentialmodule.ErrRuntimeInvalid
	}
	if err := sourcework.Revalidate(ctx); err != nil {
		return deploymentmodule.CandidateConnectionRequest{}, err
	}
	if err := connections.authority.revalidator.Revalidate(ctx, cloneRefreshCredentialAuthority(connections.authority.queued)); err != nil {
		return deploymentmodule.CandidateConnectionRequest{}, safeRuntimeCredentialAuthorityError(ctx, err)
	}
	provider := request
	provider.Requirements = nil
	seen := make(map[string]bool)
	check := func(id string) error {
		resource := credentialmodule.RuntimeResource{ScopeKind: "connection", TargetID: request.TargetID, ProjectID: request.Identity.ProjectID.String(), Environment: request.Identity.Environment, ResourceID: id}
		if seen[id] || !connections.authority.capturedConnection(resource) {
			return credentialmodule.ErrRuntimeForbidden
		}
		seen[id] = true
		return nil
	}
	for _, requirement := range request.Requirements {
		id := requirement.ConnectionID.String()
		if err := check(id); err != nil {
			return deploymentmodule.CandidateConnectionRequest{}, err
		}
		pin, local := connections.pins[id]
		if !local {
			provider.Requirements = append(provider.Requirements, requirement)
			continue
		}
		if requirement.ConnectorKind != "postgres" || requirement.Access != "" {
			return deploymentmodule.CandidateConnectionRequest{}, credentialmodule.ErrRuntimeConflict
		}
		if err := connections.checkPin(ctx, pin); err != nil {
			return deploymentmodule.CandidateConnectionRequest{}, err
		}
	}
	for _, authored := range request.AuthoredConnections {
		id := authored.ConnectionID.String()
		if err := check(id); err != nil {
			return deploymentmodule.CandidateConnectionRequest{}, err
		}
		if _, local := connections.pins[id]; local {
			return deploymentmodule.CandidateConnectionRequest{}, credentialmodule.ErrRuntimeConflict
		}
	}
	for id := range connections.pins {
		if !seen[id] {
			return deploymentmodule.CandidateConnectionRequest{}, credentialmodule.ErrRuntimeConflict
		}
	}
	return provider, ctx.Err()
}

func (connections *refreshCandidateConnections) checkPin(ctx context.Context, pin candidateLocalPin) error {
	resource := pin.reference.Scope.Resource
	if pin.binding.TargetID.String() != resource.TargetID || pin.binding.Scope.ProjectID.String() != resource.ProjectID || pin.binding.Scope.Environment != resource.Environment || pin.binding.ConnectionID.String() != resource.ResourceID {
		return credentialmodule.ErrRuntimeConflict
	}
	reference, err := connections.authority.ResolveRuntimeCredential(ctx, connections.authority.identity, pin.reference.Scope.Resource)
	if err != nil {
		return err
	}
	if reference != pin.reference {
		return credentialmodule.ErrRuntimeConflict
	}
	check := localRuntimeCredentialCheck{owners: connections.authority.owners, bindings: connections.authority.bindings}
	scope, err := check.scope(ctx, pin.binding, pin.reference.Scope.Resource)
	if err != nil {
		return err
	}
	if scope != pin.reference.Scope {
		return credentialmodule.ErrRuntimeConflict
	}
	return nil
}

func (connections *refreshCandidateConnections) Resolve(ctx context.Context, request deploymentmodule.CandidateConnectionRequest) ([]deploymentmodule.CandidateConnectionEvidence, error) {
	provider, err := connections.split(ctx, request)
	if err != nil {
		return nil, err
	}
	var evidence []deploymentmodule.CandidateConnectionEvidence
	if len(provider.Requirements) > 0 {
		evidence, err = connections.base.Resolve(ctx, provider)
		if err != nil {
			return nil, err
		}
	}
	for _, pin := range connections.pins {
		value := pin.binding.Evidence()
		evidence = append(evidence, deploymentmodule.CandidateConnectionEvidence{BindingID: value.BindingID.String(), ConnectionID: value.ConnectionID, ConnectorKind: value.ConnectorKind, Revision: value.BindingRevision, CredentialVersionID: pin.reference.VersionID, EndpointConfigHash: value.EndpointConfigHash})
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].ConnectionID.String() < evidence[j].ConnectionID.String() })
	return evidence, ctx.Err()
}

func (connections *refreshCandidateConnections) Acquire(ctx context.Context, request deploymentmodule.CandidateConnectionRequest) (deploymentmodule.CandidateConnectionLeases, error) {
	provider, err := connections.split(ctx, request)
	if err != nil {
		return nil, err
	}
	locals := make([]analyticsmodule.CandidateLocalConnection, 0, len(connections.pins))
	for _, pin := range connections.pins {
		evidence := pin.binding.Evidence()
		evidence.ValidatedVersion = ""
		locals = append(locals, analyticsmodule.CandidateLocalConnection{Evidence: evidence, CredentialVersionID: pin.reference.VersionID, Resolver: candidateLocalConnectionResolver{connections: connections, pin: pin}})
	}
	return connections.base.acquire(ctx, provider, locals)
}

type candidateLocalConnectionResolver struct {
	connections *refreshCandidateConnections
	pin         candidateLocalPin
}

func (resolver candidateLocalConnectionResolver) WithConnection(ctx context.Context, name string, logical semanticmodel.Connection, consume func(semanticmodel.Connection) error) (resultErr error) {
	connections, pin := resolver.connections, resolver.pin
	if ctx == nil || connections == nil || consume == nil || name != pin.binding.ConnectionID.String() || logical.Kind != "postgres" || logical.Access != "" || typednil.IsNil(connections.reader) || connections.useLocal == nil {
		return credentialmodule.ErrRuntimeInvalid
	}
	defer func() {
		if recover() != nil {
			resultErr = errors.Join(analyticsruntime.ErrConnectionCleanupFailed, ctx.Err())
		}
	}()
	if err := sourcework.Revalidate(ctx); err != nil {
		return err
	}
	if err := connections.checkPin(ctx, pin); err != nil {
		return err
	}
	var callbackErr error
	calls := 0
	err := connections.reader.WithCredential(ctx, connections.authority.identity, pin.reference.Scope.Resource, func(reference credentialmodule.RuntimeCredentialReference, fields map[string]string) error {
		calls++
		if calls != 1 || reference != pin.reference {
			return credentialmodule.ErrRuntimeConflict
		}
		if err := connections.checkPin(ctx, pin); err != nil {
			return err
		}
		snapshot, err := connectionbinding.NewLocalCredentialSnapshot(fields, reference.VersionID, time.Now().UTC(), time.Time{})
		if err != nil {
			return credentialmodule.ErrRuntimeInvalid
		}
		defer snapshot.Destroy()
		callbackErr = connections.useLocal(ctx, pin.binding, snapshot, logical, consume)
		return callbackErr
	})
	// The reader intentionally redacts consumer errors. Preserve the fixed
	// cleanup marker for SourceRuntime, which must retain its admission lease.
	if errors.Is(callbackErr, analyticsruntime.ErrConnectionCleanupFailed) || errors.Is(callbackErr, analyticsmodule.ErrLocalRuntimeCredentialCleanupFailed) {
		return errors.Join(analyticsruntime.ErrConnectionCleanupFailed, ctx.Err())
	}
	if err != nil {
		return safeRuntimeCredentialReadError(ctx, err)
	}
	if callbackErr != nil {
		return safeRuntimeCredentialReadError(ctx, callbackErr)
	}
	if calls != 1 {
		return credentialmodule.ErrRuntimeUnavailable
	}
	if err := sourcework.Revalidate(ctx); err != nil {
		return err
	}
	return connections.checkPin(ctx, pin)
}
