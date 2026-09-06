package module

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/access"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectcatalog "github.com/flidai/leapview/internal/project/catalog"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type semanticCatalogRuntime interface {
	ProjectManifest() projectmanifest.ResourceManifest
	CompiledSemanticModel(string) (*semanticquery.CompiledModel, bool)
}

// SemanticCatalogVisibility uses the exact catalog lease, not a second active
// generation lookup. Resource RBAC is still required by catalog.Service.
func SemanticCatalogVisibility(instanceID string, resolve func(context.Context) (access.SemanticAttributeResolution, error)) projectcatalog.SemanticModelVisibility {
	return func(ctx context.Context, lease projectcatalog.Lease, principalID string, modelID projectgraph.ResourceID) (bool, error) {
		port, ok := lease.(interface{ Runtime() projectruntime.Runtime })
		if !ok || port.Runtime() == nil {
			return false, projectcatalog.ErrUnavailable
		}
		runtime, ok := port.Runtime().(semanticCatalogRuntime)
		if !ok {
			return false, projectcatalog.ErrUnavailable
		}
		identity := lease.Identity()
		if err := identity.Validate(); err != nil {
			return false, err
		}
		definition := runtime.ProjectManifest()
		if lease.AuthorizationSnapshot().Identity() != identity || lease.AuthorizationSnapshot().ValidateBound() != nil {
			return false, projectcatalog.ErrSnapshotChanged
		}
		model := definition.SemanticModels[modelID.String()]
		compiled, available := runtime.CompiledSemanticModel(modelID.String())
		if model == nil || !available || compiled == nil || !compiled.MatchesModel(model) {
			return false, projectcatalog.ErrUnavailable
		}
		if model.AccessPolicy.Empty() {
			return true, nil
		}
		if resolve == nil {
			return false, projectcatalog.ErrUnavailable
		}
		initial, err := resolve(ctx)
		if err != nil {
			return false, err
		}
		if initial.Subject.Kind != access.SubjectKindPrincipal || initial.Subject.ID != principalID {
			return false, projectcatalog.ErrUnavailable
		}
		provider := func() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
			if lease.Identity() != identity {
				return semanticquery.SemanticAccessAttributeSnapshot{}, semanticquery.SemanticAccessAuthority{}, projectcatalog.ErrSnapshotChanged
			}
			resolved, err := resolve(ctx)
			if err != nil {
				return semanticquery.SemanticAccessAttributeSnapshot{}, semanticquery.SemanticAccessAuthority{}, err
			}
			return semanticquery.SemanticAccessResolutionSnapshot(instanceID, initial.Subject.ID, resolved)
		}
		consumer, err := semanticquery.NewSemanticAccessDiscovery(compiled, semanticquery.SemanticAccessConsumerConfig{
			ProjectID: identity.ProjectID.String(), Environment: identity.Environment,
			InstanceID: instanceID, ModelID: modelID.String(), Generation: identity.GenerationID,
			PrincipalID: initial.Subject.ID, Authority: provider,
		})
		if err != nil {
			return false, fmt.Errorf("semantic catalog authority: %w", err)
		}
		for _, dataset := range compiled.DatasetNames() {
			if consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: dataset}) == nil {
				return true, nil
			}
		}
		return false, nil
	}
}
