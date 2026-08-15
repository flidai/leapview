package app

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Yacobolo/toolbelt/pagestream"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	refreshmodule "github.com/flidai/leapview/internal/refresh/module"
	servingstatemodule "github.com/flidai/leapview/internal/servingstate/module"
	workspacemodule "github.com/flidai/leapview/internal/workspace/module"
)

type workspaceRefreshPresentationBridge struct{}

func (workspaceRefreshPresentationBridge) Sections() []string {
	return (workspacemodule.RefreshPresentation{}).Sections()
}

func (workspaceRefreshPresentationBridge) StreamID(workspaceID, assetID, section string) string {
	return (workspacemodule.RefreshPresentation{}).StreamID(workspaceID, assetID, section)
}

func (workspaceRefreshPresentationBridge) Signals(
	view workspacemodule.WorkspaceView,
	asset workspacemodule.AssetView,
	assets []workspacemodule.AssetView,
	edges []workspacemodule.AssetEdgeView,
	refresh refreshmodule.AssetRefreshState,
	section string,
) pagestream.SignalPatch {
	return (workspacemodule.RefreshPresentation{}).Signals(
		view,
		asset,
		assets,
		edges,
		workspaceAssetRefreshState(refresh),
		section,
	)
}

func workspaceAssetRefreshState(state refreshmodule.AssetRefreshState) workspacemodule.AssetRefreshState {
	return workspacemodule.AssetRefreshState{
		CSRFToken:        state.CSRFToken,
		RunCommand:       state.RunCommand,
		CancelCommand:    state.CancelCommand,
		Runs:             workspaceAssetRefreshRuns(state.Runs),
		Latest:           workspaceAssetRefreshRun(state.Latest),
		LatestSuccessful: workspaceAssetRefreshRun(state.LatestSuccessful),
		DataVersion: workspacemodule.AssetDataVersion{
			SnapshotID: state.DataVersion.SnapshotID, ServingStateID: state.DataVersion.ServingStateID,
			RefreshedAt: state.DataVersion.RefreshedAt, Source: state.DataVersion.Source,
		},
		NextRun: state.NextRun,
	}
}

func workspaceAssetRefreshRuns(runs []refreshmodule.AssetRefreshRun) []workspacemodule.AssetRefreshRun {
	out := make([]workspacemodule.AssetRefreshRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, workspaceAssetRefreshRun(run))
	}
	return out
}

func workspaceAssetRefreshRun(run refreshmodule.AssetRefreshRun) workspacemodule.AssetRefreshRun {
	return workspacemodule.AssetRefreshRun{
		ID: run.ID, Environment: run.Environment, ModelID: run.ModelID, ServingStateID: run.ServingStateID,
		PrincipalID: run.PrincipalID, PrincipalDisplayName: run.PrincipalDisplayName, TriggerType: run.TriggerType,
		ParentRunID: run.ParentRunID, RetryOf: run.RetryOf, TargetGeneration: run.TargetGeneration,
		Status: run.Status, CreatedAt: run.CreatedAt, UpdatedAt: run.UpdatedAt,
		StartedAt: run.StartedAt, FinishedAt: run.FinishedAt, Error: run.Error,
	}
}

type workspaceRefreshStateBridge struct {
	support refreshmodule.WorkspaceSupport
}

func (b workspaceRefreshStateBridge) AssetRefreshState(
	ctx context.Context,
	workspaceID string,
	environment string,
	asset workspacemodule.AssetView,
) (workspacemodule.AssetRefreshState, error) {
	state, err := b.support.AssetRefreshState(ctx, workspaceID, environment, asset)
	if err != nil {
		return workspacemodule.AssetRefreshState{}, err
	}
	return workspaceAssetRefreshState(state), nil
}

type workspaceRefreshDependencies struct {
	access                *accessmodule.Module
	dashboards            func() *dashboardmodule.Module
	refresh               func() *refreshmodule.Module
	workspaces            func() *workspacemodule.Module
	broker                *pagestream.Broker
	persistenceConfigured bool
	defaultEnvironment    string
}

func workspaceRefreshSupport(deps *workspaceRefreshDependencies) refreshmodule.WorkspaceSupport {
	support := refreshmodule.WorkspaceSupport{
		Runs: func() (refreshmodule.RunReader, error) {
			refresh := deps.refresh()
			if refresh == nil {
				return nil, fmt.Errorf("refresh module is required")
			}
			return refresh, nil
		},
		QueuePipeline: func(ctx context.Context, input refreshmodule.QueuePipelineInput) (refreshmodule.QueueAssetResult, error) {
			refresh := deps.refresh()
			if refresh == nil {
				return refreshmodule.QueueAssetResult{}, fmt.Errorf("refresh module is required")
			}
			return refresh.QueuePipelineRefresh(ctx, input)
		},
		RunCreated: func(ctx context.Context, run refreshmodule.RunRecord) error {
			refresh := deps.refresh()
			if refresh == nil {
				return fmt.Errorf("refresh module is required")
			}
			return refresh.VerifyRunCreated(ctx, run)
		},
		CancelRun: func(ctx context.Context, workspaceID, runID string) (refreshmodule.RunRecord, error) {
			refresh := deps.refresh()
			if refresh == nil {
				return refreshmodule.RunRecord{}, fmt.Errorf("refresh module is required")
			}
			return refresh.CancelRun(ctx, workspaceID, runID)
		},
		RunCancelled: func(ctx context.Context, run refreshmodule.RunRecord) error {
			refresh := deps.refresh()
			if refresh == nil {
				return fmt.Errorf("refresh module is required")
			}
			return refresh.VerifyRunCancelled(ctx, run)
		},
		Environment: func(r *http.Request) servingstatemodule.Environment {
			return requestServingEnvironment(deps.defaultEnvironment, r)
		},
		PrincipalID: func(r *http.Request) string {
			principal, _ := deps.access.CurrentPrincipal(r)
			return principal.ID
		},
		DispatchQueued: func() {
			if refresh := deps.refresh(); refresh != nil {
				refresh.Dispatch(context.Background())
			}
		},
		Broker: deps.broker,
		AssetCatalog: func(ctx context.Context, workspaceID string) ([]workspacemodule.AssetView, []workspacemodule.AssetEdgeView, bool) {
			assets, edges, err := deps.workspaces().WorkspaceAssetsAndEdgesForData(ctx, workspaceID, string(defaultServingEnvironment(deps.defaultEnvironment)))
			if err != nil || (len(assets) == 0 && len(edges) == 0) {
				return nil, nil, false
			}
			return assets, edges, true
		},
		WorkspaceView: func(r *http.Request, workspaceID string) workspacemodule.WorkspaceView {
			return deps.workspaces().WorkspaceResponse(r, workspaceID)
		},
		WorkspaceViewContext: func(ctx context.Context, workspaceID string) workspacemodule.WorkspaceView {
			return deps.workspaces().WorkspaceViewContext(ctx, workspaceID)
		},
		Presentation: workspaceRefreshPresentationBridge{},
	}
	if deps.persistenceConfigured {
		support.DataVersions = deps.refresh()
	}
	return support
}

func workspaceRefreshService(deps *workspaceRefreshDependencies, persistence persistenceInputs, workflow workflowInputs) (refreshmodule.Service, error) {
	repo, err := resolveServingStateRepository(persistence)
	if err != nil {
		return refreshmodule.Service{}, err
	}
	if repo == nil {
		return refreshmodule.Service{}, fmt.Errorf("serving state repository is required")
	}
	hooks := []refreshmodule.CandidateValidationHook{}
	if workflow.managedDataValidation != nil {
		hooks = append(hooks, workflow.managedDataValidation)
	}
	return refreshmodule.Service{
		ServingStates: repo,
		Runtime:       workflow.reloader,
		Publisher: refreshmodule.Publisher{
			Workspace: func() refreshmodule.WorkspaceSupport {
				return workspaceRefreshSupport(deps)
			},
			SemanticModelVersion: func(ctx context.Context, workspaceID, environment, modelID string) {
				refreshedAt := ""
				if refresh := deps.refresh(); refresh != nil {
					if version, ok, err := refresh.DataVersion(ctx, workspaceID, environment, modelID); err == nil && ok {
						refreshedAt = version.RefreshedAt.Format(time.RFC3339)
					}
				}
				if dashboards := deps.dashboards(); dashboards != nil {
					dashboards.PublishSemanticModelRefresh(workspaceID, environment, modelID, refreshedAt)
				}
			},
		},
		CandidateValidationHooks: hooks,
	}, nil
}
