package deploymentpostgres

import (
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	refreshplan "github.com/flidai/leapview/internal/refresh/plan"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestValidateNativeRefreshScopeAllowsOnlyCapturedCompiledWork(t *testing.T) {
	artifacts, plan := unitNativeRefreshScopeFixture(t, true)
	require.NoError(t, validateNativeRefreshScope(artifacts, &plan))

	// Nil means an ordinary delivery operation, which does not have a refresh
	// scope to compare against the complete candidate artifact.
	require.NoError(t, validateNativeRefreshScope(release.CandidateArtifactSet{}, nil))
}

func TestValidateNativeRefreshScopeRejectsUncapturedCandidateWork(t *testing.T) {
	t.Run("extra table in prepared project", func(t *testing.T) {
		artifacts, _ := unitNativeRefreshScopeFixture(t, true)
		artifacts = unitNativeRefreshScopeCandidateWithPartialDataset(t, artifacts)
		plan := unitNativeRefreshScopePipelinePlan(t, artifacts)
		require.Len(t, plan.MaterializationScope, 1)
		require.Len(t, artifacts.Compiler.Artifact.RefreshDefinition().ModelTables, 2)
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("extra source on same connection", func(t *testing.T) {
		artifacts, captured := unitNativeRefreshScopeFixture(t, true)
		artifacts = unitNativeRefreshScopeCandidateWithUnusedSource(t, artifacts)
		// The candidate-derived pipeline plan remains valid and captures the
		// same source inputs, while the semantic model has an additional source
		// on the already captured connection.
		plan := unitNativeRefreshScopePipelinePlan(t, artifacts)
		require.Equal(t, captured.MaterializationScope, plan.MaterializationScope)
		require.Equal(t, captured.SourceInputs, plan.SourceInputs)
		require.Equal(t, captured.BindingDigest, plan.BindingDigest)
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("extra external connection", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		artifacts, _ = unitNativeRefreshScopeFixtureWithExtraConnection(t, "unused_external", "postgres", true)
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("extra authored connection", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		artifacts, _ = unitNativeRefreshScopeFixtureWithExtraConnection(t, "unused_authored", "http", true)
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("extra managed connection", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		artifacts, _ = unitNativeRefreshScopeFixtureWithExtraConnection(t, "unused_managed", "managed", true)
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("candidate binding kind differs from compiled connection", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		artifacts.Generation.Connections[0].ConnectorKind = "s3"
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("candidate binding access differs from compiled connection", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		artifacts.Generation.Connections[0].Access = semanticmodel.ConnectionAccessPublic
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("missing source connection ID mapping", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, false)
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("project identity mismatch", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		plan = rewriteNativeRefreshPlan(t, plan, func(value *projectpipelineplan.Plan) { value.ProjectID = "project:other" })
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("environment identity mismatch", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		plan = rewriteNativeRefreshPlan(t, plan, func(value *projectpipelineplan.Plan) { value.Environment = "staging" })
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("captured source does not exist", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		plan = rewriteNativeRefreshPlan(t, plan, func(value *projectpipelineplan.Plan) {
			value.SourceInputs = append(value.SourceInputs, "source:missing")
		})
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("invalid plan digest", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		plan.Digest = unitNativeRefreshScopeDigest('e')
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("captured binding digest changed", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		plan = rewriteNativeRefreshPlan(t, plan, func(value *projectpipelineplan.Plan) {
			value.BindingDigest = unitNativeRefreshScopeDigest('c')
		})
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("captured selection digest changed", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		plan = rewriteNativeRefreshPlan(t, plan, func(value *projectpipelineplan.Plan) {
			value.SelectionDigest = unitNativeRefreshScopeDigest('d')
		})
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})

	t.Run("same connection name rebound to a different canonical ID", func(t *testing.T) {
		artifacts, plan := unitNativeRefreshScopeFixture(t, true)
		artifacts = unitNativeRefreshScopeCandidateWithReboundConnection(t, artifacts, "connection:warehouse-v2")
		requireNativeRefreshScopeConflict(t, validateNativeRefreshScope(artifacts, &plan))
	})
}

func requireNativeRefreshScopeConflict(t *testing.T, err error) {
	t.Helper()
	require.ErrorIs(t, err, deploymentdomain.ErrDeliveryConflict)
	require.NotContains(t, err.Error(), "warehouse")
	require.NotContains(t, err.Error(), "unused")
}

func unitNativeRefreshScopeFixture(t *testing.T, includeConnectionIndex bool) (release.CandidateArtifactSet, projectpipelineplan.Plan) {
	t.Helper()
	artifacts, err := unitNativeRefreshScopeArtifacts(t, includeConnectionIndex, "")
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project:refresh-scope", "prod", "generation:base")
	require.NoError(t, err)
	artifacts.Generation.Identity = identity
	artifacts.Generation.Connections = []release.CandidateConnectionRequirement{{ConnectionID: "connection:warehouse", ConnectorKind: "postgres"}}
	return artifacts, unitNativeRefreshScopePipelinePlan(t, artifacts)
}

func unitNativeRefreshScopeFixtureWithExtraConnection(t *testing.T, id, kind string, includeConnectionIndex bool) (release.CandidateArtifactSet, projectpipelineplan.Plan) {
	t.Helper()
	artifacts, err := unitNativeRefreshScopeArtifacts(t, includeConnectionIndex, id+":"+kind)
	require.NoError(t, err)
	identity, err := projectgraph.NewServingIdentity("project:refresh-scope", "prod", "generation:base")
	require.NoError(t, err)
	artifacts.Generation.Identity = identity
	// Derive the whole candidate activation set from the compiled bundle just
	// as candidate preparation does, including the extra activation outside
	// the plan's source closure.
	activations, err := artifacts.Compiler.Artifact.ConnectionActivations()
	require.NoError(t, err)
	for _, activation := range activations {
		connectionID := projectgraph.ResourceID(activation.LogicalConnectionID)
		switch activation.Mode {
		case projectartifact.TargetBindingActivation:
			artifacts.Generation.Connections = append(artifacts.Generation.Connections, release.CandidateConnectionRequirement{ConnectionID: connectionID, ConnectorKind: activation.ConnectorKind, Access: activation.Access})
		case projectartifact.AuthoredActivation:
			artifacts.Generation.AuthoredConnections = append(artifacts.Generation.AuthoredConnections, release.CandidateAuthoredConnection{ConnectionID: connectionID, ConnectorKind: activation.ConnectorKind, Access: activation.Access})
		case projectartifact.ManagedActivation:
			artifacts.Generation.ManagedDataPins = append(artifacts.Generation.ManagedDataPins, release.ManagedDataPin{ConnectionID: connectionID.String(), RevisionID: "sha256:revision"})
		}
	}
	return artifacts, unitNativeRefreshScopePipelinePlan(t, artifacts)
}

func unitNativeRefreshScopePipelinePlan(t *testing.T, artifacts release.CandidateArtifactSet) projectpipelineplan.Plan {
	t.Helper()
	identity := artifacts.Generation.Identity
	selection, err := refreshplan.ForPipeline(artifacts.Compiler.Artifact.RefreshDefinition(), identity.ProjectID, "pipeline:sales")
	require.NoError(t, err)
	bound, err := selection.BindGeneration(identity, artifacts.Artifact.SourceDigest)
	require.NoError(t, err)
	pipeline, err := bound.DeliveryPipelinePlan()
	require.NoError(t, err)
	return pipeline
}

func unitNativeRefreshScopeCandidateWithPartialDataset(t *testing.T, artifacts release.CandidateArtifactSet) release.CandidateArtifactSet {
	t.Helper()
	manifest := artifacts.Compiler.Artifact.Manifest()
	model := manifest.SemanticModels["semantic:sales"]
	delete(model.Datasets, "customers")
	delete(model.Tables, "customers")
	delete(model.Sources, "customers")
	return unitNativeRefreshScopeCandidateWithManifest(t, artifacts, artifacts.Compiler.Graph, manifest)
}

func unitNativeRefreshScopeCandidateWithUnusedSource(t *testing.T, artifacts release.CandidateArtifactSet) release.CandidateArtifactSet {
	t.Helper()
	manifest := artifacts.Compiler.Artifact.Manifest()
	model := manifest.SemanticModels["semantic:sales"]
	model.Sources["unused"] = semanticmodel.Source{Connection: "warehouse"}
	resources := artifacts.Compiler.Graph.Resources()
	resources = append(resources, projectgraph.Resource{ID: "source:unused", Kind: projectgraph.KindSource, Name: "unused"})
	edges := artifacts.Compiler.Graph.Edges()
	edges = append(edges, projectgraph.Edge{From: "source:unused", To: "connection:warehouse"})
	manifest.Sources["source:unused"] = semanticmodel.Source{Connection: "connection:warehouse"}
	manifest.NameIndex.Sources["unused"] = "source:unused"
	graph, err := projectgraph.NewProjectGraph(resources, edges)
	require.NoError(t, err)
	return unitNativeRefreshScopeCandidateWithManifest(t, artifacts, graph, manifest)
}

func unitNativeRefreshScopeCandidateWithManifest(t *testing.T, artifacts release.CandidateArtifactSet, graph projectgraph.ProjectGraph, manifest projectmanifest.ResourceManifest) release.CandidateArtifactSet {
	t.Helper()
	bundle, err := projectartifact.NewSourceBundle(graph, manifest)
	require.NoError(t, err)
	artifacts.Compiler.Graph = bundle.Graph()
	artifacts.Compiler.Manifest = bundle.Manifest()
	artifacts.Compiler.Artifact = bundle
	artifacts.Artifact.SourceDigest = bundle.Digest()
	artifacts.Artifact.ProjectDigest = bundle.Digest()
	artifacts.Artifact.ContentDigest = bundle.Digest()
	return artifacts
}

func unitNativeRefreshScopeCandidateWithReboundConnection(t *testing.T, artifacts release.CandidateArtifactSet, replacement projectgraph.ResourceID) release.CandidateArtifactSet {
	t.Helper()
	old := projectgraph.ResourceID("connection:warehouse")
	resources := artifacts.Compiler.Graph.Resources()
	for index := range resources {
		if resources[index].ID == old {
			resources[index].ID = replacement
		}
	}
	edges := artifacts.Compiler.Graph.Edges()
	for index := range edges {
		if edges[index].To == old {
			edges[index].To = replacement
		}
	}
	graph, err := projectgraph.NewProjectGraph(resources, edges)
	require.NoError(t, err)

	manifest := artifacts.Compiler.Manifest
	connection := manifest.Connections[old.String()]
	delete(manifest.Connections, old.String())
	manifest.Connections[replacement.String()] = connection
	for sourceID, source := range manifest.Sources {
		if source.Connection == old.String() {
			source.Connection = replacement.String()
			manifest.Sources[sourceID] = source
		}
	}
	manifest.NameIndex.Connections["warehouse"] = replacement.String()
	bundle, err := projectartifact.NewSourceBundle(graph, manifest)
	require.NoError(t, err)
	artifacts.Compiler.Graph = bundle.Graph()
	artifacts.Compiler.Manifest = bundle.Manifest()
	artifacts.Compiler.Artifact = bundle
	artifacts.Artifact.SourceDigest = bundle.Digest()
	artifacts.Artifact.ProjectDigest = bundle.Digest()
	artifacts.Artifact.ContentDigest = bundle.Digest()
	artifacts.Generation.Connections = []release.CandidateConnectionRequirement{{ConnectionID: replacement, ConnectorKind: "postgres"}}
	return artifacts
}

func unitNativeRefreshScopeArtifacts(t *testing.T, includeConnectionIndex bool, extraConnection string) (release.CandidateArtifactSet, error) {
	t.Helper()
	resources := []projectgraph.Resource{
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "source:customers", Kind: projectgraph.KindSource, Name: "customers"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "model:customers", Kind: projectgraph.KindModel, Name: "customers_model"},
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "pipeline:sales", Kind: projectgraph.KindPipeline, Name: "sales_refresh"},
	}
	edges := []projectgraph.Edge{
		{From: "source:orders", To: "connection:warehouse"},
		{From: "source:customers", To: "connection:warehouse"},
		{From: "model:orders", To: "source:orders"},
		{From: "model:customers", To: "source:customers"},
		{From: "semantic:sales", To: "model:orders"},
		{From: "semantic:sales", To: "model:customers"},
		{From: "pipeline:sales", To: "semantic:sales"},
	}
	connections := map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "postgres"}}
	connectionNames := map[string]string{"warehouse": "connection:warehouse"}
	if extraConnection != "" {
		separator := strings.LastIndexByte(extraConnection, ':')
		require.Greater(t, separator, 0)
		name, kind := extraConnection[:separator], extraConnection[separator+1:]
		id := projectgraph.ResourceID("connection:" + name)
		resources = append(resources, projectgraph.Resource{ID: id, Kind: projectgraph.KindConnection, Name: name})
		connections[id.String()] = semanticmodel.Connection{Kind: kind}
		connectionNames[name] = id.String()
	}
	graph, err := projectgraph.NewProjectGraph(resources, edges)
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	nameIndex := projectmanifest.NameIndex{
		Sources:        map[string]string{"orders": "source:orders", "customers": "source:customers"},
		Models:         map[string]string{"orders_model": "model:orders", "customers_model": "model:customers"},
		SemanticModels: map[string]string{"sales": "semantic:sales"},
		Pipelines:      map[string]string{"sales_refresh": "pipeline:sales"},
	}
	if includeConnectionIndex {
		nameIndex.Connections = connectionNames
	}
	bundle, err := projectartifact.NewSourceBundle(graph, projectmanifest.ResourceManifest{
		Connections: connections,
		Sources: map[string]semanticmodel.Source{
			"source:orders":    {Connection: "connection:warehouse"},
			"source:customers": {Connection: "connection:warehouse"},
		},
		Models: map[string]semanticmodel.Table{
			"model:orders":    {ModelName: "orders_model", Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}},
			"model:customers": {ModelName: "customers_model", Execution: semanticmodel.ExecutionDefinition{Source: "source:customers"}, SourceDependencies: []string{"source:customers"}},
		},
		SemanticModels: map[string]*semanticmodel.Model{
			"semantic:sales": {
				Name: "sales",
				Sources: map[string]semanticmodel.Source{
					"orders":    {Connection: "warehouse"},
					"customers": {Connection: "warehouse"},
				},
				Datasets: map[string]semanticmodel.SemanticDatasetSpec{
					"orders":    {Model: "orders_model"},
					"customers": {Model: "customers_model"},
				},
				Tables: map[string]semanticmodel.Table{
					"orders":    {ModelName: "orders_model", Execution: semanticmodel.ExecutionDefinition{Source: "orders"}},
					"customers": {ModelName: "customers_model", Execution: semanticmodel.ExecutionDefinition{Source: "customers"}},
				},
			},
		},
		RefreshPipelines: map[string]refreshschedule.Definition{
			"pipeline:sales": {ID: "pipeline:sales", Name: "sales_refresh", SemanticModelID: "semantic:sales", SelectionDigest: unitNativeRefreshScopeDigest('b')},
		},
		NameIndex: nameIndex,
	})
	if err != nil {
		return release.CandidateArtifactSet{}, err
	}
	return release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{SourceDigest: unitNativeRefreshScopeDigest('a'), ProjectDigest: bundle.Digest(), ContentDigest: bundle.Digest()},
		Compiler: release.CandidateCompilerEvidence{Graph: bundle.Graph(), Manifest: bundle.Manifest(), Artifact: bundle},
	}, nil
}

func rewriteNativeRefreshPlan(t *testing.T, original projectpipelineplan.Plan, mutate func(*projectpipelineplan.Plan)) projectpipelineplan.Plan {
	t.Helper()
	value := original
	value.MaterializationScope = append([]string(nil), original.MaterializationScope...)
	value.ModelExecutionOrder = append([]string(nil), original.ModelExecutionOrder...)
	value.SourceInputs = append([]string(nil), original.SourceInputs...)
	mutate(&value)
	value.ExecutionDigest, value.ProvenanceDigest, value.GovernanceDigest, value.EvidenceDigest, value.Digest = "", "", "", "", ""
	canonical, err := projectpipelineplan.New(value)
	require.NoError(t, err)
	return canonical
}

func unitNativeRefreshScopeDigest(char byte) string {
	return "sha256:" + strings.Repeat(string(char), 64)
}
