package runtimefactory

import (
	"context"

	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	"github.com/flidai/leapview/internal/workspace"
)

func (r dashboardRuntimeWithGraph) Verify(ctx context.Context) error {
	return r.Service.Verify(ctx)
}

type dashboardRuntimeWithGraph struct {
	*dashboardruntime.Service
	workspaceID     string
	servingStateID  string
	graph           workspace.AssetGraph
	authoredSources map[string]projectartifact.AuthoredDashboardSource
}

// AuthoredDashboardSource returns a fresh deep copy of retained authored
// dashboard source and metadata. A missing source is explicit via false.
func (r dashboardRuntimeWithGraph) AuthoredDashboardSource(dashboardID string) (projectartifact.AuthoredDashboardSource, bool) {
	source, ok := r.authoredSources[dashboardID]
	if !ok {
		return projectartifact.AuthoredDashboardSource{}, false
	}
	return projectartifact.CloneAuthoredDashboardSource(source)
}

func (r dashboardRuntimeWithGraph) WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	if r.workspaceID != workspaceID || r.servingStateID != servingStateID {
		return nil, nil, false
	}
	return append([]workspace.Asset(nil), r.graph.Assets...), append([]workspace.AssetEdge(nil), r.graph.Edges...), true
}
