package module

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
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
// candidate runtime. Closing it removes future discovery and releases the
// exact pool generations only after Runtime Host drains the candidate runtime.
type RuntimeBindingRegistration struct {
	once     sync.Once
	registry *candidateRuntimeBindingRegistry
	key      candidateRuntimeBindingKey
	token    uint64
	leases   *connectionbinding.RuntimeBindingLeases
}

func (module *Module) BindCandidateRuntime(
	candidateID string,
	projectID projectgraph.ResourceID,
	leases *RuntimeBindingLeases,
	authoredConnections []CandidateAuthoredConnection,
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
	resolver := runtimeBindingConnectionResolver{leases: leases, authored: authored, authoredAccess: authoredAccess}
	token := module.candidateRuntimeBindings.register(key, resolver)
	return &RuntimeBindingRegistration{
		registry: &module.candidateRuntimeBindings,
		key:      key, token: token, leases: leases,
	}, nil
}

func (registration *RuntimeBindingRegistration) Evidence() []ConnectionBindingEvidence {
	if registration == nil || registration.leases == nil {
		return nil
	}
	runtimeEvidence := registration.leases.Evidence()
	evidence := make([]ConnectionBindingEvidence, 0, len(runtimeEvidence))
	for _, value := range runtimeEvidence {
		evidence = append(evidence, value.BindingEvidence)
	}
	return evidence
}

func (registration *RuntimeBindingRegistration) Close() error {
	if registration == nil {
		return nil
	}
	registration.once.Do(func() {
		registration.registry.remove(registration.key, registration.token)
		registration.leases.Release()
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
}

func (resolver runtimeBindingConnectionResolver) Resolve(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
) (resolved semanticmodel.Connection, resultErr error) {
	connectionID, err := connectionbinding.ParseConnectionID(strings.TrimSpace(name))
	if err != nil {
		return semanticmodel.Connection{}, connectionbinding.ErrBindingNotFound
	}
	logicalKind := strings.TrimSpace(logical.Kind)
	if authoredKind, ok := resolver.authored[connectionID.String()]; ok {
		if logicalKind != authoredKind {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		spec, exists := connectors.LookupConnection(logicalKind)
		if !exists || spec.ActivationMode != connectors.AuthoredActivation {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		if logical.Access != "" && logical.Access != semanticmodel.ConnectionAccessPublic {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		if logical.Access == semanticmodel.ConnectionAccessPublic && !spec.AllowPublicAccess {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		if resolver.authoredAccess[connectionID.String()] != logical.Access {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		return logical, nil
	}
	if spec, exists := connectors.LookupConnection(logicalKind); exists &&
		spec.ActivationMode == connectors.AuthoredActivation {
		return semanticmodel.Connection{}, connectionbinding.ErrBindingNotFound
	}
	if resolver.leases == nil {
		return semanticmodel.Connection{}, connectionbinding.ErrBindingNotFound
	}
	err = resolver.leases.UsePool(
		connectionID,
		func(pool connectionbinding.RuntimePool) error {
			target, ok := pool.(analyticsruntime.ConnectionResolver)
			if !ok {
				return connectionbinding.ErrProviderUnavailable
			}
			resolved, resultErr = target.Resolve(ctx, name, logical)
			return resultErr
		},
	)
	if err != nil {
		return semanticmodel.Connection{}, err
	}
	return resolved, resultErr
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
