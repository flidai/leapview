package app

import (
	"context"
	"fmt"
	"time"

	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
)

// projectRefreshService binds refresh execution to the active project
// serving-state repository. It deliberately has no container read-model
// adapter: refresh/run owns project graph identity and generation validation.
func projectRefreshService(persistence persistenceInputs, workflow workflowInputs, dashboards func() *dashboardmodule.Module) (refreshrun.Service, error) {
	repo, err := resolveServingStateRepository(persistence)
	if err != nil {
		return refreshrun.Service{}, err
	}
	if repo == nil {
		return refreshrun.Service{}, fmt.Errorf("serving state repository is required")
	}
	hooks := []refreshrun.CandidateValidationHook{}
	if workflow.managedDataValidation != nil {
		hooks = append(hooks, workflow.managedDataValidation)
	}
	return refreshrun.Service{
		ServingStates: repo,
		Runtime:       workflow.reloader,
		Publisher: refreshmodule.Publisher{
			SemanticModelVersion: func(ctx context.Context, identity projectgraph.ServingIdentity, modelID projectgraph.ResourceID) {
				if module := dashboards(); module != nil {
					module.PublishSemanticModelRefresh(identity.ProjectID.String(), identity.Environment, modelID.String(), time.Now().UTC().Format(time.RFC3339))
				}
			},
		},
		CandidateValidationHooks: hooks,
	}, nil
}
