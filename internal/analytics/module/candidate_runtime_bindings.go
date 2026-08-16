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
)

type candidateRuntimeBindingKey struct {
	candidateID string
	projectID   string
}

type candidateRuntimeBindingEntry struct {
	token    uint64
	resolver analyticsruntime.ConnectionResolver
}

type CandidateAuthoredConnection struct {
	LogicalConnectionID string
	ConnectorKind       string
}

type candidateRuntimeBindingRegistry struct {
	mu      sync.RWMutex
	next    uint64
	current map[candidateRuntimeBindingKey]candidateRuntimeBindingEntry
}

// RuntimeBindingRegistration owns validated target pool leases for one
// candidate project. Closing it removes future discovery and releases the
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
	projectID string,
	leases *RuntimeBindingLeases,
	authoredConnections []CandidateAuthoredConnection,
) (*RuntimeBindingRegistration, error) {
	candidateID = strings.TrimSpace(candidateID)
	projectID = strings.TrimSpace(projectID)
	if module == nil || candidateID == "" || projectID == "" || leases == nil {
		return nil, fmt.Errorf(
			"%w: candidate, project, and validated leases are required",
			connectionbinding.ErrInvalidBinding,
		)
	}
	key := candidateRuntimeBindingKey{
		candidateID: candidateID, projectID: projectID,
	}
	authored, err := candidateAuthoredConnectionSet(authoredConnections)
	if err != nil {
		return nil, err
	}
	resolver := runtimeBindingConnectionResolver{leases: leases, authored: authored}
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
	return registration.leases.Evidence()
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
	projectID string,
) (analyticsruntime.ConnectionResolver, bool) {
	if module == nil {
		return nil, false
	}
	return module.candidateRuntimeBindings.lookup(candidateRuntimeBindingKey{
		candidateID: strings.TrimSpace(candidateID),
		projectID:   strings.TrimSpace(projectID),
	})
}

type runtimeBindingConnectionResolver struct {
	leases   *connectionbinding.RuntimeBindingLeases
	authored map[string]string
}

func (resolver runtimeBindingConnectionResolver) Resolve(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
) (resolved semanticmodel.Connection, resultErr error) {
	logicalID, err := connectionbinding.ParseLogicalConnectionID(strings.TrimSpace(name))
	if err != nil {
		return semanticmodel.Connection{}, connectionbinding.ErrBindingNotFound
	}
	logicalKind := strings.TrimSpace(logical.Kind)
	if authoredKind, ok := resolver.authored[logicalID.String()]; ok {
		if logicalKind != authoredKind {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		spec, exists := connectors.LookupConnection(logicalKind)
		if !exists || spec.ActivationMode != connectors.AuthoredActivation {
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
		logicalID,
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
) (map[string]string, error) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		logicalID, err := connectionbinding.ParseLogicalConnectionID(
			strings.TrimSpace(value.LogicalConnectionID),
		)
		kind := strings.TrimSpace(value.ConnectorKind)
		spec, exists := connectors.LookupConnection(kind)
		if err != nil || kind == "" || !exists ||
			spec.ActivationMode != connectors.AuthoredActivation {
			return nil, connectionbinding.ErrIncompatibleBinding
		}
		if _, duplicate := result[logicalID.String()]; duplicate {
			return nil, connectionbinding.ErrIncompatibleBinding
		}
		result[logicalID.String()] = kind
	}
	return result, nil
}
