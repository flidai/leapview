package runtimefactory

import (
	"context"
	"encoding/json"
	"fmt"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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

// SemanticModel exposes the activation-owned model snapshot used by the
// planner. The dashboard service retains its authored projection for runtime
// construction, but schema discovery is an activation fact; consumers must
// observe the planner's detached source snapshot so metadata prechecks and
// execution bind to one generation.
func (r dashboardRuntimeWithGraph) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	compiled, ok := r.CompiledSemanticModel(modelID)
	if !ok || compiled == nil {
		return nil, false
	}
	source := compiled.SourceModel()
	if source == nil || !compiled.MatchesModel(source) {
		return nil, false
	}
	// The planner is selected by the canonical model resource ID, but retain a
	// cheap identity check at this boundary so a malformed runtime cannot expose
	// an unrelated activation snapshot as the requested model.
	authored, exists := r.projectManifest.SemanticModels[modelID]
	if !exists || authored == nil || authored.Name != source.Name || !activationModelsCompatible(compiled, authored, source) {
		return nil, false
	}
	runtimeSafe, err := authored.RuntimeSnapshot()
	if err != nil || runtimeSafe == nil {
		return nil, false
	}
	// Source/connection metadata is descriptive runtime state rather than
	// planner authority. Preserve it through the same redacted detached
	// projection used by ProjectManifest while retaining the activation-owned
	// executable graph and discovered schema above.
	source.Connections = runtimeSafe.Connections
	source.Sources = runtimeSafe.Sources
	source.DefaultConnection = runtimeSafe.DefaultConnection
	return source, true
}

// activationModelsCompatible compares the authored execution contract with
// the activated source snapshot through the model's existing discovery
// derivation. Physical schemas are activation-owned inputs; resolving them on a
// detached authored snapshot reconstructs derived dimensions/columns without
// weakening the full compiled-model fingerprint check.
func activationModelsCompatible(compiled *semanticquery.CompiledModel, authored, activated *semanticmodel.Model) bool {
	authoredSnapshot := authored.ExecutionSnapshot()
	activatedSnapshot := activated.ExecutionSnapshot()
	if authoredSnapshot == nil || activatedSnapshot == nil {
		return false
	}
	if compiled.MatchesModel(authoredSnapshot) {
		return true
	}
	for name, activatedTable := range activatedSnapshot.Tables {
		authoredTable, ok := authoredSnapshot.Tables[name]
		if !ok {
			return false
		}
		authoredTable.Schema.Columns = append([]semanticmodel.ColumnSchema(nil), activatedTable.Schema.Columns...)
		authoredSnapshot.Tables[name] = authoredTable
	}
	if err := authoredSnapshot.ResolveDiscoveredModelFields(); err != nil {
		return false
	}
	return compiled.MatchesModel(authoredSnapshot)
}

// SemanticModelByID is the resource-ID form of SemanticModel used by runtime
// resolver consumers. It deliberately shares the same activation projection
// and fail-closed behavior.
func (r dashboardRuntimeWithGraph) SemanticModelByID(modelID projectgraph.ResourceID) (*semanticmodel.Model, bool) {
	return r.SemanticModel(modelID.String())
}

type dashboardRuntimeWithGraph struct {
	*dashboardruntime.Service
	projectID       projectgraph.ResourceID
	servingStateID  string
	authorization   accesssnapshot.AuthorizationSnapshot
	authoredSources map[string]dashboardauthoring.AuthoredDashboardSource
	projectManifest projectmanifest.ResourceManifest
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
func (r dashboardRuntimeWithGraph) ProjectManifest() projectmanifest.ResourceManifest {
	encoded, err := json.Marshal(r.projectManifest)
	if err != nil {
		return projectmanifest.ResourceManifest{}
	}
	var cloned projectmanifest.ResourceManifest
	if err := json.Unmarshal(encoded, &cloned); err != nil {
		return projectmanifest.ResourceManifest{}
	}
	for modelID, model := range cloned.SemanticModels {
		compiled, ok := r.CompiledSemanticModel(modelID)
		if !ok || compiled == nil {
			continue
		}
		source := compiled.SourceModel()
		runtimeSafe, err := model.RuntimeSnapshot()
		if source == nil || err != nil || runtimeSafe == nil {
			return projectmanifest.ResourceManifest{}
		}
		source.Connections = runtimeSafe.Connections
		source.Sources = runtimeSafe.Sources
		source.DefaultConnection = runtimeSafe.DefaultConnection
		cloned.SemanticModels[modelID] = source
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

func authoredDashboardSources(manifest projectmanifest.ResourceManifest, projectID projectgraph.ResourceID) (map[string]dashboardauthoring.AuthoredDashboardSource, error) {
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
		if document.Metadata.ID != dashboardID.String() {
			return nil, fmt.Errorf("dashboard source %q has mismatched document id %q", id, document.Metadata.ID)
		}
	}
	return sources, nil
}
