package module

import (
	"context"

	"github.com/flidai/leapview/internal/workspace"
	workspacehttp "github.com/flidai/leapview/internal/workspace/http"
	"github.com/flidai/leapview/internal/workspace/ui"
)

func (m *Module) AssetCatalogReader() (workspacehttp.AssetCatalogReader, error) {
	if m == nil || m.assetCatalog == nil {
		return nil, nil
	}
	return m.assetCatalog, nil
}

func (m *Module) AssetVersionsState(ctx context.Context, workspaceID, environment string, asset workspace.AssetView, section string) (ui.AssetVersionsState, error) {
	state := ui.AssetVersionsState{CurrentContentHash: asset.ContentHash}
	if section != "versions" || m == nil || m.readModel == nil {
		return state, nil
	}
	versions, err := m.readModel.AssetVersions(ctx, workspace.WorkspaceID(workspaceID), environment, workspace.AssetID(asset.ID))
	if err != nil {
		return state, err
	}
	state.Versions = make([]ui.AssetVersionState, 0, len(versions))
	for _, version := range versions {
		state.Versions = append(state.Versions, ui.AssetVersionState{
			ServingStateID: string(version.ServingStateID),
			Status:         version.Status, Digest: version.Digest, CreatedBy: version.CreatedBy,
			CreatedAt: version.CreatedAt, ActivatedAt: version.ActivatedAt,
			SourceFile: version.SourceFile, ContentHash: version.ContentHash,
		})
	}
	return state, nil
}

type moduleRefreshState struct {
	module   *Module
	upstream RefreshStateProvider
}

func (s moduleRefreshState) AssetRefreshState(ctx context.Context, workspaceID, environment string, asset workspace.AssetView) (ui.AssetRefreshState, error) {
	if s.upstream == nil {
		return ui.AssetRefreshState{}, nil
	}
	return s.upstream.AssetRefreshState(ctx, workspaceID, environment, asset)
}

func (s moduleRefreshState) AssetVersionsState(ctx context.Context, workspaceID, environment string, asset workspace.AssetView, section string) (ui.AssetVersionsState, error) {
	return s.module.AssetVersionsState(ctx, workspaceID, environment, asset, section)
}

type moduleRefreshRunner struct {
	upstream AssetRefreshRunner
}

func (r moduleRefreshRunner) RefreshAsset(ctx context.Context, input workspacehttp.AssetRefreshInput) error {
	if r.upstream == nil {
		return nil
	}
	return r.upstream.RefreshAsset(ctx, AssetRefreshInput{
		Request: input.Request, WorkspaceID: input.WorkspaceID,
		Asset: input.Asset, Assets: input.Assets, Edges: input.Edges,
	})
}

func (r moduleRefreshRunner) RetryAsset(ctx context.Context, input workspacehttp.AssetRefreshInput, retryOf string) error {
	if r.upstream == nil {
		return nil
	}
	return r.upstream.RetryAsset(ctx, AssetRefreshInput{
		Request: input.Request, WorkspaceID: input.WorkspaceID,
		Asset: input.Asset, Assets: input.Assets, Edges: input.Edges,
	}, retryOf)
}

func (r moduleRefreshRunner) CancelRefreshRun(ctx context.Context, input workspacehttp.PipelineRunCancelInput) error {
	if r.upstream == nil {
		return nil
	}
	return r.upstream.CancelRefreshRun(ctx, PipelineRunCancelInput{
		Request: input.Request, WorkspaceID: input.WorkspaceID, PipelineID: input.PipelineID, RunID: input.RunID,
	})
}
