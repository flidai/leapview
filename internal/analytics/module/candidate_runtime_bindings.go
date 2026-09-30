package module

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/typednil"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type candidateRuntimeBindingKey struct {
	candidateID string
	projectID   projectgraph.ResourceID
}

type candidateRuntimeBindingEntry struct {
	token    uint64
	resolver analyticsruntime.ConnectionResolver
}

// CandidateLocalConnection binds one committed local credential pin to its
// exact synchronous runtime resolver. CredentialVersionID remains separate
// from provider validation evidence.
type CandidateLocalConnection struct {
	Evidence            ConnectionBindingEvidence
	CredentialVersionID string
	Resolver            analyticsruntime.ConnectionResolver
}

// CandidateRuntimeEvidence carries the local credential version separately
// from provider validation evidence. Binding remains the unchanged durable
// non-secret binding projection.
type CandidateRuntimeEvidence struct {
	Binding             ConnectionBindingEvidence
	CredentialVersionID string
}

type CandidateAuthoredConnection struct {
	ConnectionID  projectgraph.ResourceID
	ConnectorKind string
	Access        semanticmodel.ConnectionAccess
}

type candidateRuntimeBindingRegistry struct {
	mu      sync.RWMutex
	next    uint64
	current map[candidateRuntimeBindingKey]candidateRuntimeBindingEntry
}

// RuntimeBindingRegistration owns validated target pool leases for one
// candidate runtime. Closing it removes future discovery, waits for active
// callbacks and their cleanup, then releases the exact pool generations.
type RuntimeBindingRegistration struct {
	once          sync.Once
	mu            sync.RWMutex
	registry      *candidateRuntimeBindingRegistry
	key           candidateRuntimeBindingKey
	token         uint64
	leases        *connectionbinding.RuntimeBindingLeases
	lifetime      *candidateRuntimeResolverLifetime
	localEvidence []CandidateRuntimeEvidence
}

func (module *Module) BindCandidateRuntime(
	candidateID string,
	projectID projectgraph.ResourceID,
	leases *RuntimeBindingLeases,
	authoredConnections []CandidateAuthoredConnection,
	localConnections []CandidateLocalConnection,
) (*RuntimeBindingRegistration, error) {
	candidateID = strings.TrimSpace(candidateID)
	if module == nil || candidateID == "" || projectID.Validate() != nil || leases == nil {
		return nil, fmt.Errorf(
			"%w: candidate, project, and validated leases are required",
			connectionbinding.ErrInvalidBinding,
		)
	}
	key := candidateRuntimeBindingKey{
		candidateID: candidateID, projectID: projectID,
	}
	authored, authoredAccess, err := candidateAuthoredConnectionSet(authoredConnections)
	if err != nil {
		return nil, err
	}
	local, localEvidence, err := candidateLocalConnectionSet(module, projectID, leases, authored, localConnections)
	if err != nil {
		return nil, err
	}
	lifetime := newCandidateRuntimeResolverLifetime()
	resolver := runtimeBindingConnectionResolver{
		leases: leases, authored: authored, authoredAccess: authoredAccess,
		local: local, lifetime: lifetime,
	}
	token := module.candidateRuntimeBindings.register(key, resolver)
	return &RuntimeBindingRegistration{
		registry: &module.candidateRuntimeBindings,
		key:      key, token: token, leases: leases, lifetime: lifetime,
		localEvidence: localEvidence,
	}, nil
}

func (registration *RuntimeBindingRegistration) Evidence() []CandidateRuntimeEvidence {
	if registration == nil || registration.leases == nil {
		return nil
	}
	registration.mu.RLock()
	defer registration.mu.RUnlock()
	runtimeEvidence := registration.leases.Evidence()
	evidence := make([]CandidateRuntimeEvidence, 0, len(runtimeEvidence)+len(registration.localEvidence))
	for _, value := range runtimeEvidence {
		evidence = append(evidence, CandidateRuntimeEvidence{Binding: value.BindingEvidence})
	}
	evidence = append(evidence, registration.localEvidence...)
	sort.Slice(evidence, func(i, j int) bool {
		return evidence[i].Binding.ConnectionID.String() < evidence[j].Binding.ConnectionID.String()
	})
	return evidence
}

func (registration *RuntimeBindingRegistration) Close() error {
	if registration == nil {
		return nil
	}
	registration.once.Do(func() {
		registration.registry.remove(registration.key, registration.token)
		registration.lifetime.retireAndWait()
		registration.leases.Release()
		registration.mu.Lock()
		registration.localEvidence = nil
		registration.mu.Unlock()
	})
	return nil
}

func (registry *candidateRuntimeBindingRegistry) register(
	key candidateRuntimeBindingKey,
	resolver analyticsruntime.ConnectionResolver,
) uint64 {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if registry.current == nil {
		registry.current = make(map[candidateRuntimeBindingKey]candidateRuntimeBindingEntry)
	}
	registry.next++
	registry.current[key] = candidateRuntimeBindingEntry{
		token: registry.next, resolver: resolver,
	}
	return registry.next
}

func (registry *candidateRuntimeBindingRegistry) lookup(
	key candidateRuntimeBindingKey,
) (analyticsruntime.ConnectionResolver, bool) {
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	entry, ok := registry.current[key]
	return entry.resolver, ok
}

func (registry *candidateRuntimeBindingRegistry) remove(
	key candidateRuntimeBindingKey,
	token uint64,
) {
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if current, ok := registry.current[key]; ok && current.token == token {
		delete(registry.current, key)
	}
}

func (module *Module) candidateRuntimeConnectionResolver(
	candidateID string,
	projectID projectgraph.ResourceID,
) (analyticsruntime.ConnectionResolver, bool) {
	if module == nil {
		return nil, false
	}
	return module.candidateRuntimeBindings.lookup(candidateRuntimeBindingKey{
		candidateID: strings.TrimSpace(candidateID),
		projectID:   projectID,
	})
}

type runtimeBindingConnectionResolver struct {
	leases         *connectionbinding.RuntimeBindingLeases
	authored       map[string]string
	authoredAccess map[string]semanticmodel.ConnectionAccess
	local          map[string]CandidateLocalConnection
	lifetime       *candidateRuntimeResolverLifetime
}

func (resolver runtimeBindingConnectionResolver) WithConnection(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
	consume func(semanticmodel.Connection) error,
) error {
	if resolver.lifetime != nil {
		release, admitted := resolver.lifetime.enter()
		if !admitted {
			return connectionbinding.ErrProviderUnavailable
		}
		defer release()
	}
	if consume == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	connectionID, err := connectionbinding.ParseConnectionID(strings.TrimSpace(name))
	if err != nil {
		return connectionbinding.ErrBindingNotFound
	}
	if local, ok := resolver.local[connectionID.String()]; ok {
		logicalKind := strings.TrimSpace(logical.Kind)
		if logicalKind != local.Evidence.ConnectorKind || logical.Access != local.Evidence.Access {
			return connectionbinding.ErrIncompatibleBinding
		}
		spec, exists := connectors.LookupConnection(logicalKind)
		if !exists || spec.ActivationMode != connectors.TargetBindingActivation {
			return connectionbinding.ErrIncompatibleBinding
		}
		return local.Resolver.WithConnection(ctx, connectionID.String(), logical, consume)
	}
	logicalKind := strings.TrimSpace(logical.Kind)
	if authoredKind, ok := resolver.authored[connectionID.String()]; ok {
		if logicalKind != authoredKind {
			return connectionbinding.ErrIncompatibleBinding
		}
		spec, exists := connectors.LookupConnection(logicalKind)
		if !exists || spec.ActivationMode != connectors.AuthoredActivation {
			return connectionbinding.ErrIncompatibleBinding
		}
		if logical.Access != "" && logical.Access != semanticmodel.ConnectionAccessPublic {
			return connectionbinding.ErrIncompatibleBinding
		}
		if logical.Access == semanticmodel.ConnectionAccessPublic && !spec.AllowPublicAccess {
			return connectionbinding.ErrIncompatibleBinding
		}
		if resolver.authoredAccess[connectionID.String()] != logical.Access {
			return connectionbinding.ErrIncompatibleBinding
		}
		logical.Auth = nil
		return consume(logical)
	}
	if spec, exists := connectors.LookupConnection(logicalKind); exists &&
		spec.ActivationMode == connectors.AuthoredActivation {
		return connectionbinding.ErrBindingNotFound
	}
	if resolver.leases == nil {
		return connectionbinding.ErrBindingNotFound
	}
	return resolver.leases.UsePool(
		connectionID,
		func(pool connectionbinding.RuntimePool) error {
			target, ok := pool.(analyticsruntime.ConnectionResolver)
			if !ok {
				return connectionbinding.ErrProviderUnavailable
			}
			return target.WithConnection(ctx, name, logical, consume)
		},
	)
}

type candidateRuntimeResolverLifetime struct {
	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
	active int
}

func newCandidateRuntimeResolverLifetime() *candidateRuntimeResolverLifetime {
	lifetime := &candidateRuntimeResolverLifetime{}
	lifetime.cond = sync.NewCond(&lifetime.mu)
	return lifetime
}

func (lifetime *candidateRuntimeResolverLifetime) enter() (func(), bool) {
	if lifetime == nil {
		return func() {}, true
	}
	lifetime.mu.Lock()
	if lifetime.closed {
		lifetime.mu.Unlock()
		return nil, false
	}
	lifetime.active++
	lifetime.mu.Unlock()
	return func() {
		lifetime.mu.Lock()
		lifetime.active--
		if lifetime.active == 0 {
			lifetime.cond.Broadcast()
		}
		lifetime.mu.Unlock()
	}, true
}

func (lifetime *candidateRuntimeResolverLifetime) retireAndWait() {
	if lifetime == nil {
		return
	}
	lifetime.mu.Lock()
	lifetime.closed = true
	for lifetime.active > 0 {
		lifetime.cond.Wait()
	}
	lifetime.mu.Unlock()
}

func candidateLocalConnectionSet(
	module *Module,
	projectID projectgraph.ResourceID,
	leases *connectionbinding.RuntimeBindingLeases,
	authored map[string]string,
	values []CandidateLocalConnection,
) (map[string]CandidateLocalConnection, []CandidateRuntimeEvidence, error) {
	local := make(map[string]CandidateLocalConnection, len(values))
	evidence := make([]CandidateRuntimeEvidence, 0, len(values))
	if len(values) == 0 {
		return local, evidence, nil
	}
	if module == nil || projectID.Validate() != nil || module.targetID == "" || module.targetEnvironment == "" {
		return nil, nil, connectionbinding.ErrIncompatibleBinding
	}
	targetID, err := connectionbinding.ParseTargetID(module.targetID)
	if err != nil {
		return nil, nil, connectionbinding.ErrIncompatibleBinding
	}
	providerIDs := make(map[string]struct{})
	providerBindingIDs := make(map[string]struct{})
	for _, value := range leases.Evidence() {
		providerIDs[value.ConnectionID.String()] = struct{}{}
		providerBindingIDs[value.BindingID.String()] = struct{}{}
	}
	localBindingIDs := make(map[string]struct{}, len(values))
	for _, value := range values {
		item := value.Evidence
		bindingID, bindingErr := connectionbinding.ParseBindingID(item.BindingID.String())
		itemTargetID, targetErr := connectionbinding.ParseTargetID(item.TargetID.String())
		connectionID, connectionErr := connectionbinding.ParseConnectionID(item.ConnectionID.String())
		identity := connectionbinding.CredentialIdentity{CredentialVersionID: value.CredentialVersionID}
		spec, knownConnector := connectors.LookupConnection(item.ConnectorKind)
		validHealth := item.Health == connectionbinding.HealthPending || item.Health == connectionbinding.HealthHealthy || item.Health == connectionbinding.HealthDegraded
		if bindingErr != nil || targetErr != nil || connectionErr != nil ||
			item.BindingID.String() != bindingID.String() || item.TargetID != targetID || itemTargetID != targetID ||
			item.ConnectionID != connectionID || item.Scope.ProjectID != projectID ||
			item.Scope.ProjectID.Validate() != nil || item.Scope.Environment != strings.TrimSpace(item.Scope.Environment) ||
			item.Scope.Environment == "" || item.Scope.Environment != module.targetEnvironment ||
			item.ConnectorKind != "postgres" || !knownConnector || spec.ActivationMode != connectors.TargetBindingActivation ||
			item.BindingRevision < 1 || item.ValidatedVersion != "" || item.Access != "" || !validHealth ||
			platformdigest.ValidateSHA256Identity(item.EndpointConfigHash) != nil ||
			identity.Validate() != nil || typednil.IsNil(value.Resolver) {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		id := item.ConnectionID.String()
		if _, exists := local[id]; exists {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		if _, exists := providerIDs[id]; exists {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		if _, exists := authored[id]; exists {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		bindingKey := item.BindingID.String()
		if _, exists := providerBindingIDs[bindingKey]; exists {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		if _, exists := localBindingIDs[bindingKey]; exists {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		localBindingIDs[bindingKey] = struct{}{}
		local[id] = value
		evidence = append(evidence, CandidateRuntimeEvidence{
			Binding: item, CredentialVersionID: value.CredentialVersionID,
		})
	}
	return local, evidence, nil
}

func candidateAuthoredConnectionSet(
	values []CandidateAuthoredConnection,
) (map[string]string, map[string]semanticmodel.ConnectionAccess, error) {
	result := make(map[string]string, len(values))
	access := make(map[string]semanticmodel.ConnectionAccess, len(values))
	for _, value := range values {
		connectionID, err := connectionbinding.ParseConnectionID(
			strings.TrimSpace(value.ConnectionID.String()),
		)
		kind := strings.TrimSpace(value.ConnectorKind)
		spec, exists := connectors.LookupConnection(kind)
		if err != nil || kind == "" || !exists ||
			spec.ActivationMode != connectors.AuthoredActivation {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		if value.Access != "" && value.Access != semanticmodel.ConnectionAccessPublic {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		if value.Access == semanticmodel.ConnectionAccessPublic && !spec.AllowPublicAccess {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		if _, duplicate := result[connectionID.String()]; duplicate {
			return nil, nil, connectionbinding.ErrIncompatibleBinding
		}
		result[connectionID.String()] = kind
		access[connectionID.String()] = value.Access
	}
	return result, access, nil
}
