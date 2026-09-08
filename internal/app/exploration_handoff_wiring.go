package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/access"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmodule "github.com/flidai/leapview/internal/project/module"
)

// Resolve through the configured access module at request time. Capturing its
// method before persistence wiring finishes could retain an unavailable module.
func (r *capabilityRoutes) explorationAuthorizationSubjects(ctx context.Context, principalID string) ([]access.SubjectRef, error) {
	if r == nil || r.accessModule == nil {
		return nil, fmt.Errorf("exploration authorization is unavailable")
	}
	return r.accessModule.AuthorizationSubjects(ctx, principalID)
}

// explorationModelProvider binds query_visual to the selected model and its
// activation-owned compiled planner. The reader owns authorization and the
// active lease; subjects are resolved by the supplied callback for every
// request rather than captured during process composition.
func explorationModelProvider(provider projectmodule.RuntimeProvider, subjects projectmodule.AuthorizationSubjects) agentmodule.VisualExplorationModelFunc {
	reader, ok := projectmodule.NewAuthorizedProjectDefinitionReader(provider, subjects).(projectmodule.AuthorizedProjectModelReader)
	if !ok {
		return func(context.Context, string, string, string, string) (*analyticsmodule.SemanticModel, *analyticsmodule.CompiledSemanticModel, error) {
			return nil, nil, fmt.Errorf("authorized exploration model reader is unavailable")
		}
	}
	return func(ctx context.Context, projectID, principalID, modelID, servingSnapshot string) (*analyticsmodule.SemanticModel, *analyticsmodule.CompiledSemanticModel, error) {
		projectID = strings.TrimSpace(projectID)
		if projectID == "" {
			return nil, nil, fmt.Errorf("exploration project id is required")
		}
		project, err := projectgraph.NewResourceID(projectID)
		if err != nil || project.String() != projectID {
			return nil, nil, fmt.Errorf("exploration project id is invalid")
		}
		principalID = strings.TrimSpace(principalID)
		if principalID == "" {
			return nil, nil, fmt.Errorf("exploration principal id is required")
		}
		modelID = strings.TrimSpace(modelID)
		model, err := projectgraph.NewResourceID(modelID)
		if err != nil || model.String() != modelID {
			return nil, nil, fmt.Errorf("exploration semantic model id is invalid")
		}
		if strings.TrimSpace(servingSnapshot) == "" || strings.TrimSpace(servingSnapshot) != servingSnapshot {
			return nil, nil, fmt.Errorf("exploration serving snapshot is required")
		}

		definition, compiled, identity, err := reader.AuthorizedExploreModel(ctx, project, principalID, model.String())
		if err != nil {
			return nil, nil, err
		}
		if err := identity.Validate(); err != nil || identity.ProjectID != project || identity.GenerationID == "" || identity.GenerationID != servingSnapshot {
			return nil, nil, fmt.Errorf("exploration serving snapshot is stale")
		}
		if definition == nil || compiled == nil || !compiled.MatchesModel(definition) {
			return nil, nil, fmt.Errorf("exploration model and compiled planner are inconsistent")
		}
		return definition, compiled, nil
	}
}
