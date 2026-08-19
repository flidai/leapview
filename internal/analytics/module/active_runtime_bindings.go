package module

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/analytics/connectors"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsruntime "github.com/flidai/leapview/internal/analytics/runtime"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ActiveRuntimeBindingEvidence is the non-secret, immutable connection proof
// retained with a ready release.
type ActiveRuntimeBindingEvidence struct {
	BindingID          connectionbinding.BindingID
	ConnectionID       projectgraph.ResourceID
	ConnectorKind      string
	Revision           int64
	ValidatedVersion   string
	EndpointConfigHash string
	Access             semanticmodel.ConnectionAccess
}

type ActiveRuntimeBindingEvidenceSource interface {
	BindingEvidence(context.Context, string, string) ([]ActiveRuntimeBindingEvidence, error)
}

func (m *Module) ConfigureActiveRuntimeBindings(source ActiveRuntimeBindingEvidenceSource) error {
	if m == nil || source == nil || m.connectionBindings == nil || m.connectionFactory == nil {
		return connectionbinding.ErrProviderUnavailable
	}
	m.activeRuntimeBindingEvidence = source
	return nil
}

type activeRuntimeConnectionResolver struct {
	module         *Module
	servingStateID string
	projectID      projectgraph.ResourceID
	environment    string

	mu       sync.Mutex
	evidence map[string]ActiveRuntimeBindingEvidence
}

func (r *activeRuntimeConnectionResolver) Resolve(
	ctx context.Context,
	name string,
	logical semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	if r == nil || r.module == nil {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	spec, ok := connectors.LookupConnection(strings.TrimSpace(logical.Kind))
	if !ok {
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	if logical.Access != "" && logical.Access != semanticmodel.ConnectionAccessPublic {
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	if logical.Access == semanticmodel.ConnectionAccessPublic && !spec.AllowPublicAccess {
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	switch spec.ActivationMode {
	case connectors.AuthoredActivation:
		return logical, nil
	case connectors.TargetBindingActivation:
	case connectors.ManagedActivation:
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	default:
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	evidence, err := r.evidenceFor(ctx, name)
	if err != nil {
		return semanticmodel.Connection{}, err
	}
	connectionID, err := connectionbinding.ParseConnectionID(strings.TrimSpace(name))
	if err != nil {
		return semanticmodel.Connection{}, connectionbinding.ErrBindingNotFound
	}
	binding, err := r.module.connectionBindings.Binding(ctx, connectionbinding.BindingScope{
		ProjectID: r.projectID, Environment: r.environment,
	}, connectionbinding.TargetID(r.module.targetID), connectionID)
	if err != nil {
		return semanticmodel.Connection{}, err
	}
	actual := binding.Evidence()
	if !binding.Enabled || binding.ID != evidence.BindingID ||
		binding.ConnectionID != evidence.ConnectionID ||
		binding.ConnectorKind != evidence.ConnectorKind || binding.Revision < evidence.Revision ||
		actual.EndpointConfigHash != evidence.EndpointConfigHash || evidence.Access != logical.Access ||
		strings.TrimSpace(evidence.ValidatedVersion) == "" {
		return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
	}
	var snapshot connectionbinding.CredentialSnapshot
	if logical.Access == semanticmodel.ConnectionAccessPublic {
		if binding.AuthenticationMode != connectionbinding.AuthenticationNone {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		snapshot = connectionbinding.NewNoAuthCredentialSnapshot(time.Now())
	} else {
		if binding.AuthenticationMode == connectionbinding.AuthenticationNone {
			return semanticmodel.Connection{}, connectionbinding.ErrIncompatibleBinding
		}
		resolver, resolverErr := connectionbinding.SelectResolver(connectionbinding.ResolverSelection{
			TargetID: binding.TargetID, ProjectID: binding.Scope.ProjectID, Environment: binding.Scope.Environment,
			TargetClass: r.module.targetClass, Kind: r.module.connectionResolverKind(),
		}, r.module.targetResolvers)
		if resolverErr != nil {
			return semanticmodel.Connection{}, resolverErr
		}
		versioned, ok := resolver.(connectionbinding.VersionedCredentialResolver)
		if !ok {
			return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
		}
		snapshot, err = versioned.ResolveVersion(ctx, binding.CredentialReference, evidence.ValidatedVersion)
		if err != nil {
			return semanticmodel.Connection{}, err
		}
	}
	defer snapshot.Destroy()
	pool, err := r.module.connectionFactory.Prepare(ctx, binding, snapshot)
	if err != nil || pool == nil {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	// The prepared pool is an activation probe, not shared mutable binding state.
	// Resolve returns an isolated connection copy before the probe is destroyed.
	defer pool.Close()
	if err := pool.HealthCheck(ctx); err != nil {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	target, ok := pool.(analyticsruntime.ConnectionResolver)
	if !ok {
		return semanticmodel.Connection{}, connectionbinding.ErrProviderUnavailable
	}
	return target.Resolve(ctx, name, logical)
}

func (r *activeRuntimeConnectionResolver) evidenceFor(
	ctx context.Context,
	name string,
) (ActiveRuntimeBindingEvidence, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.evidence == nil {
		values, err := r.module.activeRuntimeBindingEvidence.BindingEvidence(
			ctx, r.servingStateID, r.projectID.String(),
		)
		if err != nil {
			return ActiveRuntimeBindingEvidence{}, err
		}
		evidence := make(map[string]ActiveRuntimeBindingEvidence, len(values))
		for _, value := range values {
			if value.BindingID.String() != strings.TrimSpace(value.BindingID.String()) ||
				value.ConnectionID.String() != strings.TrimSpace(value.ConnectionID.String()) ||
				value.ConnectorKind != strings.TrimSpace(value.ConnectorKind) ||
				value.ValidatedVersion != strings.TrimSpace(value.ValidatedVersion) ||
				value.EndpointConfigHash != strings.TrimSpace(value.EndpointConfigHash) {
				return ActiveRuntimeBindingEvidence{}, fmt.Errorf(
					"%w: active binding evidence is not canonical",
					connectionbinding.ErrIncompatibleBinding,
				)
			}
			if value.ConnectionID == "" || value.BindingID == "" || value.ConnectorKind == "" ||
				value.Revision < 1 || value.ValidatedVersion == "" ||
				platformdigest.ValidateSHA256Identity(value.EndpointConfigHash) != nil {
				return ActiveRuntimeBindingEvidence{}, fmt.Errorf(
					"%w: active binding evidence is invalid",
					connectionbinding.ErrIncompatibleBinding,
				)
			}
			if value.Access != "" && value.Access != semanticmodel.ConnectionAccessPublic {
				return ActiveRuntimeBindingEvidence{}, fmt.Errorf(
					"%w: active binding evidence has unsupported access policy",
					connectionbinding.ErrIncompatibleBinding,
				)
			}
			if _, exists := evidence[value.ConnectionID.String()]; exists {
				return ActiveRuntimeBindingEvidence{}, fmt.Errorf(
					"%w: duplicate active binding evidence",
					connectionbinding.ErrIncompatibleBinding,
				)
			}
			evidence[value.ConnectionID.String()] = value
		}
		r.evidence = evidence
	}
	evidence, ok := r.evidence[strings.TrimSpace(name)]
	if !ok {
		return ActiveRuntimeBindingEvidence{}, connectionbinding.ErrBindingNotFound
	}
	return evidence, nil
}
