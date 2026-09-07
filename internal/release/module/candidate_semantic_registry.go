package module

import (
	"context"
	"fmt"
	"sort"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (service *candidateArtifactService) compileCandidateSemanticAccess(ctx context.Context, projectID projectgraph.ResourceID, models map[string]*semanticmodel.Model) (*access.SemanticRegistryContext, error) {
	names := make([]string, 0)
	for name, model := range models {
		if semanticquery.ModelRequiresSemanticAccess(model) {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, nil
	}
	if service.instanceID == "" || service.semanticRegistry == nil {
		return nil, fmt.Errorf("protected candidate semantic registry is unavailable")
	}
	value, err := service.semanticRegistry.ReadSemanticRegistry(ctx, service.instanceID)
	if err != nil {
		return nil, err
	}
	if err := value.Validate(service.instanceID, projectID); err != nil {
		return nil, err
	}
	sort.Strings(names)
	for _, name := range names {
		if _, err := semanticquery.CompileModelWithSemanticAccess(models[name], semanticquery.SemanticAccessCompileContext{Registry: value.Registry}); err != nil {
			return nil, fmt.Errorf("candidate semantic model %q: %w", name, err)
		}
	}
	if err := service.validateCandidateSemanticRegistry(ctx, projectID, &value); err != nil {
		return nil, err
	}
	return &value, nil
}

func (service *candidateArtifactService) validateCandidateSemanticRegistry(ctx context.Context, projectID projectgraph.ResourceID, expected *access.SemanticRegistryContext) error {
	if expected == nil {
		return nil
	}
	if service.semanticRegistry == nil {
		return fmt.Errorf("protected candidate semantic registry is unavailable")
	}
	current, err := service.semanticRegistry.ReadSemanticRegistry(ctx, service.instanceID)
	if err != nil {
		return err
	}
	if err := current.Validate(service.instanceID, projectID); err != nil {
		return err
	}
	if !expected.Equal(current) {
		return fmt.Errorf("candidate semantic registry or control revision is stale")
	}
	return nil
}

func sameCandidateSemanticRegistry(left, right *access.SemanticRegistryContext) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return left.Equal(*right)
}
