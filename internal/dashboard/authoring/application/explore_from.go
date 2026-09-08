package application

import (
	"context"
	"fmt"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	"github.com/flidai/leapview/internal/dashboard/authoring/sourceadapter"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ResolveDashboardSourceKind asks the authorized catalog which published
// source owns this dashboard. URL possession never selects an instance or
// project source, and drafts are not candidates for this operation.
func (a *Application) ResolveDashboardSourceKind(ctx context.Context, requestedProject projectgraph.ResourceID, dashboardID authoring.DashboardID, actorID string) (sourceadapter.SourceKind, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	project, err := projectID(requestedProject)
	if err != nil {
		return "", err
	}
	dashboard, err := a.Get(ctx, catalog.GetRequest{ProjectID: project, DashboardID: dashboardID, ActorID: actorID})
	if err != nil {
		return "", err
	}
	switch dashboard.Source {
	case catalog.SourceProject:
		return sourceadapter.SourceProject, nil
	case catalog.SourceInstance:
		return sourceadapter.SourceInstance, nil
	default:
		return "", fmt.Errorf("unsupported dashboard source kind %q", dashboard.Source)
	}
}

// LoadDashboardSource resolves the published dashboard source through the
// existing source adapter. It deliberately has no draft variant: exploration
// handoffs may only expose an authored source which the caller is authorized
// to view, never another principal's unpublished working copy.
func (a *Application) LoadDashboardSource(ctx context.Context, requestedProject projectgraph.ResourceID, dashboardID authoring.DashboardID, actorID string) (sourceadapter.Source, error) {
	if err := a.validate(); err != nil {
		return sourceadapter.Source{}, err
	}
	project, err := projectID(requestedProject)
	if err != nil {
		return sourceadapter.Source{}, err
	}
	ref := sourceadapter.SourceRef{
		Kind:        sourceadapter.SourceProject,
		ProjectID:   project,
		DashboardID: dashboardID,
	}
	return a.sources.Load(ctx, ref, actorID)
}

// LoadPublishedDashboardSource resolves the exact published authored
// revision for an instance dashboard. It deliberately has no draft variant;
// a handoff may never expose another principal's unpublished working copy.
func (a *Application) LoadPublishedDashboardSource(ctx context.Context, requestedProject projectgraph.ResourceID, dashboardID authoring.DashboardID, actorID string) (sourceadapter.Source, error) {
	if err := a.validate(); err != nil {
		return sourceadapter.Source{}, err
	}
	project, err := projectID(requestedProject)
	if err != nil {
		return sourceadapter.Source{}, err
	}
	ref := sourceadapter.SourceRef{Kind: sourceadapter.SourceInstance, ProjectID: project, DashboardID: dashboardID}
	return a.sources.Load(ctx, ref, actorID)
}
