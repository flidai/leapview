package module

import (
	"context"
	"errors"
	nethttp "net/http"
	"strings"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
	refreshpresentation "github.com/flidai/leapview/internal/refresh/presentation"
	refresh "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workspace"
)

type RunReader interface {
	ListTargetRuns(context.Context, string, string, string, refresh.RunPage) ([]refresh.RunRecord, error)
	LatestSuccessfulTargetRun(context.Context, string, string, string, string) (refresh.RunRecord, bool, error)
}

type DataVersionReader interface {
	NextRun(context.Context, string, string, string) (time.Time, bool, error)
	DataVersion(context.Context, string, string, string) (refreshschedule.DataVersion, bool, error)
}

type WorkspaceRefreshPresentation interface {
	Sections() []string
	StreamID(workspaceID, assetID, section string) string
	Signals(workspace.WorkspaceView, workspace.AssetView, []workspace.AssetView, []workspace.AssetEdgeView, refreshpresentation.AssetRefreshState, string) pagestream.SignalPatch
}

type WorkspaceSupport struct {
	Runs           func() (RunReader, error)
	QueuePipeline  func(context.Context, refresh.QueuePipelineInput) (refresh.QueueAssetResult, error)
	RunCreated     func(context.Context, refresh.RunRecord) error
	Environment    func(*nethttp.Request) servingstate.Environment
	PrincipalID    func(*nethttp.Request) string
	DispatchQueued func()
	Broker         interface {
		Publish(string, pagestream.SignalPatch)
	}
	AssetCatalog         func(context.Context, string) ([]workspace.AssetView, []workspace.AssetEdgeView, bool)
	WorkspaceView        func(*nethttp.Request, string) workspace.WorkspaceView
	WorkspaceViewContext func(context.Context, string) workspace.WorkspaceView
	DataVersions         DataVersionReader
	Presentation         WorkspaceRefreshPresentation
}

func (s WorkspaceSupport) RefreshAsset(_ context.Context, r *nethttp.Request, workspaceID string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView) error {
	return s.queueAssetRefreshWithPatches(r, workspaceID, asset, assets, edges)
}

func (s WorkspaceSupport) AssetRefreshState(ctx context.Context, workspaceID, environment string, asset workspace.AssetView) (refreshpresentation.AssetRefreshState, error) {
	if !workspaceAssetRefreshable(asset) {
		return refreshpresentation.AssetRefreshState{}, nil
	}
	repo, err := s.runRepository()
	if err != nil {
		return refreshpresentation.AssetRefreshState{}, err
	}
	targetType := refresh.TargetRefreshPipeline
	targetID := assetRefreshTargetID(asset)
	environment = string(servingstate.NormalizeEnvironment(servingstate.Environment(environment)))
	runs, err := repo.ListTargetRuns(ctx, workspaceID, targetType, targetID, refresh.RunPage{Limit: 50, Environment: environment})
	if err != nil {
		return refreshpresentation.AssetRefreshState{}, err
	}
	state := refreshpresentation.AssetRefreshState{
		RunCommand: refreshgen.GenUIActionCreateRefreshRun(),
		Runs:       uiRefreshRuns(runs),
	}
	pipelineID := strings.TrimPrefix(asset.Key, workspaceID+".")
	if s.DataVersions != nil {
		nextRun, ok, err := s.DataVersions.NextRun(ctx, workspaceID, environment, pipelineID)
		if err != nil {
			return refreshpresentation.AssetRefreshState{}, err
		}
		if ok {
			state.NextRun = nextRun
		}
	}
	if len(state.Runs) > 0 {
		state.Latest = state.Runs[0]
	}
	if latest, ok, err := repo.LatestSuccessfulTargetRun(ctx, workspaceID, environment, targetType, targetID); err != nil {
		return refreshpresentation.AssetRefreshState{}, err
	} else if ok {
		state.LatestSuccessful = uiRefreshRun(latest)
	}
	if s.DataVersions != nil {
		modelID := refreshPipelineModelID(asset)
		if version, ok, err := s.DataVersions.DataVersion(ctx, workspaceID, environment, modelID); err != nil {
			return refreshpresentation.AssetRefreshState{}, err
		} else if ok {
			state.DataVersion = refreshpresentation.AssetDataVersion{
				SnapshotID: version.SnapshotID, ServingStateID: version.ServingStateID,
				RefreshedAt: version.RefreshedAt, Source: version.Source,
			}
		}
	}
	return state, nil
}

func refreshPipelineModelID(asset workspace.AssetView) string {
	for _, key := range []string{"semanticModel", "SemanticModel"} {
		if value, ok := asset.Payload[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func (s WorkspaceSupport) queueAssetRefreshWithPatches(r *nethttp.Request, workspaceID string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView) error {
	operationID := refreshgen.GenCommandOperationCreateRefreshRun()
	if err := uicommand.VerifyClaim(uicommand.OperationClaims(r), operationID.APIGenOperationID()); err != nil {
		return err
	}
	requestID := strings.TrimSpace(r.Header.Get("X-Request-ID"))
	ctx, _, err := refreshgen.BeginGenCreateRefreshRunCommand(r.Context(), refreshgen.GenCreateRefreshRunCommandInvocation{
		Surface:        apigencommand.SurfaceUI,
		Workspace:      strings.TrimSpace(workspaceID),
		IdempotencyKey: "ui:" + operationID.APIGenOperationID() + ":" + requestID,
		RequestID:      requestID,
		CorrelationID:  strings.TrimSpace(r.Header.Get("X-Correlation-ID")),
	})
	if err != nil {
		return err
	}
	r = r.WithContext(ctx)
	if s.QueuePipeline == nil {
		return errors.New("workspace refresh service is required")
	}
	if s.RunCreated == nil {
		return errors.New("generated refresh lifecycle completion is required")
	}
	environment := servingstate.DefaultEnvironment
	if s.Environment != nil {
		environment = s.Environment(r)
	}
	pipelineID := strings.TrimPrefix(asset.Key, workspaceID+".")
	result, err := s.QueuePipeline(ctx, refresh.QueuePipelineInput{
		WorkspaceID: workspaceID,
		Environment: environment,
		PrincipalID: s.principalID(r),
		PipelineID:  pipelineID,
		TriggerType: refresh.TriggerManual,
	})
	if err != nil {
		return err
	}
	if err := s.RunCreated(ctx, result.Run); err != nil {
		return err
	}
	if s.DispatchQueued != nil {
		s.DispatchQueued()
	}
	return nil
}

func (s WorkspaceSupport) PublishWorkspaceAssetRefreshPatch(r *nethttp.Request, workspaceID string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView) {
	if s.Presentation == nil {
		return
	}
	for _, section := range s.Presentation.Sections() {
		s.publish(s.Presentation.StreamID(workspaceID, asset.ID, section), s.workspaceAssetRefreshPatch(r, workspaceID, asset, assets, edges, section))
	}
}

func (s WorkspaceSupport) PublishWorkspaceAssetRefreshPatchesForTarget(ctx context.Context, workspaceID, environment, targetType, targetID string) {
	if targetType != refresh.TargetRefreshPipeline {
		return
	}
	assets, edges, ok := s.workspaceAssetsAndEdges(ctx, workspaceID)
	if !ok {
		return
	}
	for _, asset := range assets {
		if assetRefreshTargetID(asset) != targetID {
			continue
		}
		if asset.Type != string(workspace.AssetTypeRefreshPipeline) {
			continue
		}
		s.publishAssetRefreshSignals(ctx, nil, workspaceID, environment, asset, assets, edges, "")
	}
}

func (s WorkspaceSupport) publishAssetRefreshSignals(ctx context.Context, r *nethttp.Request, workspaceID, environment string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView, onlySection string) {
	if !workspaceAssetRefreshable(asset) {
		return
	}
	refresh, err := s.AssetRefreshState(ctx, workspaceID, environment, asset)
	if err != nil {
		return
	}
	if s.Presentation == nil {
		return
	}
	sections := s.Presentation.Sections()
	if onlySection != "" {
		sections = []string{onlySection}
	}
	view := workspace.WorkspaceView{ID: workspaceID}
	if s.WorkspaceView != nil && r != nil {
		view = s.WorkspaceView(r, workspaceID)
	} else if s.WorkspaceViewContext != nil {
		view = s.WorkspaceViewContext(ctx, workspaceID)
	}
	for _, section := range sections {
		s.publish(s.Presentation.StreamID(workspaceID, asset.ID, section), s.Presentation.Signals(view, asset, assets, edges, refresh, section))
	}
}

func (s WorkspaceSupport) workspaceAssetRefreshPatch(r *nethttp.Request, workspaceID string, asset workspace.AssetView, assets []workspace.AssetView, edges []workspace.AssetEdgeView, section string) pagestream.SignalPatch {
	environment := string(servingstate.DefaultEnvironment)
	if s.Environment != nil {
		environment = string(s.Environment(r))
	}
	refresh, err := s.AssetRefreshState(r.Context(), workspaceID, environment, asset)
	if err != nil {
		refresh = refreshpresentation.AssetRefreshState{Latest: refreshpresentation.AssetRefreshRun{Status: "failed"}}
	}
	view := workspace.WorkspaceView{ID: workspaceID}
	if s.WorkspaceView != nil {
		view = s.WorkspaceView(r, workspaceID)
	}
	if s.Presentation == nil {
		return nil
	}
	return s.Presentation.Signals(view, asset, assets, edges, refresh, section)
}

func (s WorkspaceSupport) workspaceAssetsAndEdges(ctx context.Context, workspaceID string) ([]workspace.AssetView, []workspace.AssetEdgeView, bool) {
	if s.AssetCatalog == nil {
		return nil, nil, false
	}
	return s.AssetCatalog(ctx, workspaceID)
}

func (s WorkspaceSupport) runRepository() (RunReader, error) {
	if s.Runs == nil {
		return nil, errors.New("materialization run repository is required")
	}
	return s.Runs()
}

func (s WorkspaceSupport) principalID(r *nethttp.Request) string {
	if s.PrincipalID == nil {
		return ""
	}
	return s.PrincipalID(r)
}

func (s WorkspaceSupport) publish(streamID string, patch pagestream.SignalPatch) {
	if s.Broker == nil {
		return
	}
	s.Broker.Publish(streamID, patch)
}

func uiRefreshRuns(runs []refresh.RunRecord) []refreshpresentation.AssetRefreshRun {
	out := make([]refreshpresentation.AssetRefreshRun, 0, len(runs))
	for _, run := range runs {
		out = append(out, uiRefreshRun(run))
	}
	return out
}

func uiRefreshRun(run refresh.RunRecord) refreshpresentation.AssetRefreshRun {
	return refreshpresentation.AssetRefreshRun{
		ID:                   run.ID,
		PrincipalDisplayName: run.PrincipalDisplayName,
		TriggerType:          run.TriggerType,
		Status:               run.Status,
		StartedAt:            run.StartedAt,
		FinishedAt:           run.FinishedAt,
		Error:                run.Error,
	}
}

func assetRefreshTargetID(asset workspace.AssetView) string {
	return asset.Key
}

func workspaceAssetRefreshable(asset workspace.AssetView) bool {
	return asset.Type == string(workspace.AssetTypeRefreshPipeline)
}
