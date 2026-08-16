package runtimefactory

import (
	"context"
	"fmt"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func (r dashboardRuntimeWithGraph) Verify(ctx context.Context) error {
	return r.Service.Verify(ctx)
}

type dashboardRuntimeWithGraph struct {
	*dashboardruntime.Service
	projectID       projectgraph.ResourceID
	servingStateID  string
	authorization   accesssnapshot.AuthorizationSnapshot
	authoredSources map[string]dashboardauthoring.AuthoredDashboardSource
}

// AuthorizationSnapshot returns the immutable authorization policy compiled
// for this serving generation. Runtimehost exposes it on leases so canonical
// project-resource guards can authorize against the exact active generation.
func (r dashboardRuntimeWithGraph) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}

// AuthoredDashboardSource returns a fresh deep copy of retained authored
// dashboard source and metadata. A missing source is explicit via false.
func (r dashboardRuntimeWithGraph) AuthoredDashboardSource(dashboardID string) (dashboardauthoring.AuthoredDashboardSource, bool) {
	source, ok := r.authoredSources[dashboardID]
	if !ok {
		return dashboardauthoring.AuthoredDashboardSource{}, false
	}
	document, err := source.Document.Clone()
	if err != nil {
		return dashboardauthoring.AuthoredDashboardSource{}, false
	}
	source.Document = document
	source.Metadata.Tags = append([]string(nil), source.Metadata.Tags...)
	return source, true
}

func authoredDashboardSources(manifest projectmanifest.Project, projectID projectgraph.ResourceID) (map[string]dashboardauthoring.AuthoredDashboardSource, error) {
	sources := make(map[string]dashboardauthoring.AuthoredDashboardSource, len(manifest.DashboardSources))
	for id, source := range manifest.DashboardSources {
		dashboardID, err := projectgraph.NewResourceID(id)
		if err != nil {
			return nil, err
		}
		document, err := source.Document.Clone()
		if err != nil {
			return nil, err
		}
		sources[id] = dashboardauthoring.AuthoredDashboardSource{
			Document: document,
			Metadata: dashboardauthoring.AuthoredDashboardMetadata{
				Project: projectID, Name: source.Metadata.Name, Title: source.Metadata.Title,
				Description: source.Metadata.Description, Owner: source.Metadata.Owner,
				Tags: append([]string(nil), source.Metadata.Tags...),
			},
			Path: source.Path,
		}
		if document.ID != dashboardID {
			return nil, fmt.Errorf("dashboard source %q has mismatched document id %q", id, document.ID)
		}
	}
	return sources, nil
}
