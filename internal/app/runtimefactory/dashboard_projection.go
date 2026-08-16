package runtimefactory

import (
	"context"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workspace"
)

func (r dashboardRuntimeWithGraph) Verify(ctx context.Context) error {
	return r.Service.Verify(ctx)
}

type dashboardRuntimeWithGraph struct {
	*dashboardruntime.Service
	projectID       projectgraph.ResourceID
	servingStateID  string
	authorization   accesssnapshot.AuthorizationSnapshot
	authoredSources map[string]projectartifact.AuthoredDashboardSource
}

// AuthorizationSnapshot returns the immutable authorization policy compiled
// for this serving generation. Runtimehost exposes it on leases so canonical
// project-resource guards can authorize against the exact active generation.
func (r dashboardRuntimeWithGraph) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
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
	if r.projectID.String() != workspaceID || r.servingStateID != servingStateID {
		return nil, nil, false
	}
	// Project graphs no longer carry workspace-scoped asset payloads. The
	// legacy asset adapter therefore fails closed until its project catalog
	// consumer is composed.
	return nil, nil, false
}
