package runtimefactory

import (
	"context"
	"encoding/json"
	"fmt"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
)

func (r dashboardRuntimeWithGraph) Verify(ctx context.Context) error {
	return r.Service.Verify(ctx)
}

// CompiledSemanticModel exposes the activation-owned compiled semantic model
// through the project runtime boundary. The dashboard service owns the
// consumer planner port, so this composition adapter performs the concrete
// planner assertion once and fails closed for an unavailable or unexpected
// planner implementation. Project readers can then consume only the
// analytics-query contract without depending on dashboard internals.
func (r dashboardRuntimeWithGraph) CompiledSemanticModel(modelID string) (*semanticquery.CompiledModel, bool) {
	if r.Service == nil {
		return nil, false
	}
	planner, ok := r.Service.Planner(modelID)
	if !ok || planner == nil || !planner.IsCompiled() {
		return nil, false
	}
	concrete, ok := planner.(*semanticquery.Planner)
	if !ok || concrete == nil || concrete.CompiledModel() == nil {
		return nil, false
	}
	return concrete.CompiledModel(), true
}

type dashboardRuntimeWithGraph struct {
	*dashboardruntime.Service
	projectID       projectgraph.ResourceID
	servingStateID  string
	authorization   accesssnapshot.AuthorizationSnapshot
	authoredSources map[string]dashboardauthoring.AuthoredDashboardSource
	projectManifest projectmanifest.Project
}

// AuthorizationSnapshot returns the immutable authorization policy compiled
// for this serving generation. Runtimehost exposes it on leases so canonical
// project-resource guards can authorize against the exact active generation.
func (r dashboardRuntimeWithGraph) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}

// ProjectManifest returns a detached copy of the complete compiled project
// definition for this serving generation. The portable graph deliberately
// contains only identity, metadata, and topology; browser detail projections
// must read their typed definitions from the exact active generation instead
// of interpreting graph metadata as a resource document.
func (r dashboardRuntimeWithGraph) ProjectManifest() projectmanifest.Project {
	encoded, err := json.Marshal(r.projectManifest)
	if err != nil {
		return projectmanifest.Project{}
	}
	var cloned projectmanifest.Project
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return projectmanifest.Project{}
	}
	return cloned
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
				Domain: source.Metadata.Domain,
				Tags:   append([]string(nil), source.Metadata.Tags...),
			},
			Path: source.Path,
		}
		if document.ID != dashboardID {
			return nil, fmt.Errorf("dashboard source %q has mismatched document id %q", id, document.ID)
		}
	}
	return sources, nil
}
