package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type authorizationLease interface {
	AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot
}

func activatedRevalidationGeneration(
	ctx context.Context,
	states interface {
		ByID(context.Context, servingstate.ID) (servingstate.State, error)
		ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	},
	runtimeHost *runtimehostmodule.Module,
	deploymentIdentity projectgraph.ServingIdentity,
	priorGenerationID string,
) (authoring.RevalidationGeneration, error) {
	if runtimeHost == nil || states == nil {
		return authoring.RevalidationGeneration{}, errors.New("runtime host is required for dashboard revalidation")
	}
	lease, err := runtimeHost.Acquire(ctx)
	if err != nil {
		return authoring.RevalidationGeneration{}, err
	}
	defer lease.Release()
	if lease.Identity() != deploymentIdentity {
		return authoring.RevalidationGeneration{}, fmt.Errorf("active runtime identity %#v does not match activated deployment %#v", lease.Identity(), deploymentIdentity)
	}
	bound, ok := lease.(authorizationLease)
	if !ok {
		return authoring.RevalidationGeneration{}, errors.New("active runtime lease does not expose authorization snapshot")
	}
	authorization := bound.AuthorizationSnapshot()
	if authorization.Identity() != deploymentIdentity {
		return authoring.RevalidationGeneration{}, fmt.Errorf("authorization snapshot identity %#v does not match activated deployment %#v", authorization.Identity(), deploymentIdentity)
	}
	graph := authorization.Project()
	if err := graph.Validate(); err != nil {
		return authoring.RevalidationGeneration{}, fmt.Errorf("active authorization graph: %w", err)
	}
	currentArtifactRef, err := states.ArtifactByServingState(ctx, servingstate.ID(deploymentIdentity.GenerationID))
	if err != nil {
		return authoring.RevalidationGeneration{}, fmt.Errorf("load activated serving artifact %q: %w", deploymentIdentity.GenerationID, err)
	}
	current, err := loadCompiledArtifact(currentArtifactRef.Path)
	if err != nil {
		return authoring.RevalidationGeneration{}, fmt.Errorf("load activated project artifact %q: %w", deploymentIdentity.GenerationID, err)
	}
	if current.ProjectID != deploymentIdentity.ProjectID || current.Graph.ProjectID() != deploymentIdentity.ProjectID {
		return authoring.RevalidationGeneration{}, fmt.Errorf("activated project artifact identity does not match deployment identity")
	}
	changed, err := changedGraphResources(ctx, states, deploymentIdentity, priorGenerationID, current)
	if err != nil {
		return authoring.RevalidationGeneration{}, err
	}
	return authoring.RevalidationGeneration{Identity: deploymentIdentity, Graph: graph, Authorization: authorization, ChangedIDs: changed}, nil
}

func changedGraphResources(
	ctx context.Context,
	states interface {
		ByID(context.Context, servingstate.ID) (servingstate.State, error)
		ArtifactByServingState(context.Context, servingstate.ID) (servingstate.Artifact, error)
	},
	identity projectgraph.ServingIdentity,
	priorGenerationID string,
	current projectbundle.CompiledProjectArtifact,
) ([]projectgraph.ResourceID, error) {
	if priorGenerationID == "" {
		ids := make([]projectgraph.ResourceID, 0, len(current.Graph.Resources()))
		for _, resource := range current.Graph.Resources() {
			ids = append(ids, resource.ID)
		}
		return sortedResourceIDs(ids), nil
	}
	priorState, err := states.ByID(ctx, servingstate.ID(priorGenerationID))
	if err != nil {
		return nil, fmt.Errorf("load prior serving generation %q: %w", priorGenerationID, err)
	}
	if priorState.ProjectID != identity.ProjectID || priorState.Environment != servingstate.Environment(identity.Environment) {
		return nil, fmt.Errorf("prior serving generation %q is outside activated project scope", priorGenerationID)
	}
	priorArtifact, err := states.ArtifactByServingState(ctx, servingstate.ID(priorGenerationID))
	if err != nil {
		return nil, fmt.Errorf("load prior serving artifact %q: %w", priorGenerationID, err)
	}
	prior, err := loadCompiledArtifact(priorArtifact.Path)
	if err != nil {
		return nil, fmt.Errorf("load prior project artifact %q: %w", priorGenerationID, err)
	}
	return diffCompiledArtifacts(prior, current), nil
}

func loadCompiledArtifact(path string) (projectbundle.CompiledProjectArtifact, error) {
	if path == "" {
		return projectbundle.CompiledProjectArtifact{}, errors.New("serving artifact path is empty")
	}
	root, err := os.MkdirTemp("", "leapview-revalidation-")
	if err != nil {
		return projectbundle.CompiledProjectArtifact{}, err
	}
	defer os.RemoveAll(root)
	if err := projectbundle.ExtractArtifact(path, root); err != nil {
		return projectbundle.CompiledProjectArtifact{}, err
	}
	compiled, _, err := projectbundle.LoadCompiledProjectArtifact(root)
	if err != nil {
		return projectbundle.CompiledProjectArtifact{}, err
	}
	return compiled, nil
}

// diffCompiledArtifacts compares deployable definitions by stable resource ID
// and graph topology. Graph metadata/provenance (domain, path, title, etc.) is
// intentionally excluded: it does not change query execution or authorization
// dependencies and therefore must not invalidate authored dashboards.
func diffCompiledArtifacts(prior, current projectbundle.CompiledProjectArtifact) []projectgraph.ResourceID {
	changed := map[projectgraph.ResourceID]struct{}{}
	compareDefinitions := func(priorDefs, currentDefs any) {
		priorValue, currentValue := reflect.ValueOf(priorDefs), reflect.ValueOf(currentDefs)
		if priorValue.Kind() != reflect.Map || currentValue.Kind() != reflect.Map {
			return
		}
		for _, key := range append(priorValue.MapKeys(), currentValue.MapKeys()...) {
			id := projectgraph.ResourceID(fmt.Sprint(key.Interface()))
			priorEntry, currentEntry := priorValue.MapIndex(key), currentValue.MapIndex(key)
			if !priorEntry.IsValid() || !currentEntry.IsValid() || !reflect.DeepEqual(priorEntry.Interface(), currentEntry.Interface()) {
				changed[id] = struct{}{}
			}
		}
	}
	compareDefinitions(prior.Manifest.Connections, current.Manifest.Connections)
	compareDefinitions(prior.Manifest.Sources, current.Manifest.Sources)
	compareDefinitions(prior.Manifest.Models, current.Manifest.Models)
	compareDefinitions(prior.Manifest.SemanticModels, current.Manifest.SemanticModels)
	compareDefinitions(prior.Manifest.DashboardDefinitions, current.Manifest.DashboardDefinitions)
	compareDefinitions(prior.Manifest.RefreshPipelines, current.Manifest.RefreshPipelines)
	type edgeKey struct {
		from, to projectgraph.ResourceID
		relation string
	}
	edges := func(graph projectgraph.ProjectGraph) map[edgeKey]struct{} {
		out := make(map[edgeKey]struct{}, len(graph.Edges()))
		for _, edge := range graph.Edges() {
			out[edgeKey{from: edge.From, to: edge.To, relation: edge.Relation}] = struct{}{}
		}
		return out
	}
	priorEdges, currentEdges := edges(prior.Graph), edges(current.Graph)
	for edge := range priorEdges {
		if _, ok := currentEdges[edge]; !ok {
			changed[edge.from] = struct{}{}
			changed[edge.to] = struct{}{}
		}
	}
	for edge := range currentEdges {
		if _, ok := priorEdges[edge]; !ok {
			changed[edge.from] = struct{}{}
			changed[edge.to] = struct{}{}
		}
	}
	ids := make([]projectgraph.ResourceID, 0, len(changed))
	for id := range changed {
		ids = append(ids, id)
	}
	return sortedResourceIDs(ids)
}

func sortedResourceIDs(ids []projectgraph.ResourceID) []projectgraph.ResourceID {
	seen := make(map[projectgraph.ResourceID]struct{}, len(ids))
	out := make([]projectgraph.ResourceID, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
