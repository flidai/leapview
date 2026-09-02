package module

import (
	"errors"
	"strings"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
	"github.com/stretchr/testify/require"
)

func TestCandidateConnectionRequirementsAreProjectScoped(t *testing.T) {
	values, managed, authored, err := candidateConnectionRequirements([]projectartifact.ConnectionActivation{
		{LogicalConnectionID: "warehouse", ConnectorKind: "postgres", Mode: projectartifact.TargetBindingActivation},
		{LogicalConnectionID: "olist", ConnectorKind: "quack", Mode: projectartifact.ManagedActivation},
		{LogicalConnectionID: "public_http", ConnectorKind: "http", Mode: projectartifact.AuthoredActivation},
	})
	require.NoError(t, err)
	warehouse, _ := projectgraph.NewResourceID("warehouse")
	publicHTTP, _ := projectgraph.NewResourceID("public_http")
	require.Equal(t, []release.CandidateConnectionRequirement{{ConnectionID: warehouse, ConnectorKind: "postgres"}}, values)
	require.Equal(t, []string{"olist"}, managed)
	require.Equal(t, []release.CandidateAuthoredConnection{{ConnectionID: publicHTTP, ConnectorKind: "http"}}, authored)
}

func TestCandidateConnectionRequirementsRejectInvalidConnectionIdentity(t *testing.T) {
	_, _, _, err := candidateConnectionRequirements([]projectartifact.ConnectionActivation{{LogicalConnectionID: "warehouse with space", ConnectorKind: "postgres", Mode: projectartifact.TargetBindingActivation}})
	require.Error(t, err)
}

func TestCandidateManagedPinsSortAndFindMissingConnections(t *testing.T) {
	pins := map[string]string{"z_connection": "revision_z", "a_connection": "revision_a"}
	require.Equal(t, []release.ManagedDataPin{{ConnectionID: "a_connection", RevisionID: "revision_a"}, {ConnectionID: "z_connection", RevisionID: "revision_z"}}, candidateManagedDataPins(pins))
	require.Equal(t, []string{"missing"}, missingCandidateManagedConnections([]string{"a_connection", "missing"}, pins))
}

func TestCandidateManagedDataPinMapDropsRemovedBaseConnections(t *testing.T) {
	base := map[string]string{"orders": "revision_orders", "legacy": "revision_legacy"}

	pins := candidateManagedDataPinMap([]string{"orders"}, base)

	require.Equal(t, map[string]string{"orders": "revision_orders"}, pins)
}

func TestCandidateManagedDataPinMapDoesNotRetainSwitchedManagedConnection(t *testing.T) {
	base := map[string]string{"legacy": "revision_legacy"}

	pins := candidateManagedDataPinMap([]string{"orders"}, base)

	require.Empty(t, pins)
	require.Equal(t, []string{"orders"}, missingCandidateManagedConnections([]string{"orders"}, pins))
}

func TestCandidateSourcesDataRevisionIsPinOrderIndependent(t *testing.T) {
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	first, err := release.CandidateSourcesDataRevision(artifactDigest, []release.ManagedDataPin{
		{ConnectionID: "z_connection", RevisionID: "revision_z"},
		{ConnectionID: "a_connection", RevisionID: "revision_a"},
	})
	require.NoError(t, err)
	second, err := release.CandidateSourcesDataRevision(artifactDigest, []release.ManagedDataPin{
		{ConnectionID: "a_connection", RevisionID: "revision_a"},
		{ConnectionID: "z_connection", RevisionID: "revision_z"},
	})
	require.NoError(t, err)
	if first != second {
		t.Fatalf("source data revision changed with pin order: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, "sources:") {
		t.Fatalf("source data revision = %q, want sources: prefix", first)
	}
}

func TestCandidateSourcesDataRevisionChangesWhenManagedDataPinChanges(t *testing.T) {
	artifactDigest := "sha256:" + strings.Repeat("a", 64)
	base := map[string]string{"orders": "revision_a"}
	changed := map[string]string{"orders": "revision_b"}
	baseRevision, err := release.CandidateSourcesDataRevision(artifactDigest, []release.ManagedDataPin{{ConnectionID: "orders", RevisionID: base["orders"]}})
	require.NoError(t, err)
	changedRevision, err := release.CandidateSourcesDataRevision(artifactDigest, []release.ManagedDataPin{{ConnectionID: "orders", RevisionID: changed["orders"]}})
	require.NoError(t, err)
	if baseRevision == changedRevision {
		t.Fatal("source data revision did not change when managed-data pin changed")
	}
}

func TestCandidateArtifactSetCarriesOneGenerationIdentity(t *testing.T) {
	identity, err := projectgraph.NewServingIdentity("project_1", "prod", "generation_1")
	require.NoError(t, err)
	set := release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{Identity: identity, ArtifactDigest: "sha256:artifact", DataRevision: "snapshot:1", DataMode: release.GenerationDataReuseBase}}
	require.Equal(t, identity, set.Generation.Identity)
	require.Empty(t, set.Generation.AuthoredConnections)
}

func TestCandidateServingStateIDHonorsCallerOwnedGeneration(t *testing.T) {
	const generationID = "018f0e4e-6f2a-7abc-8def-0123456789ab"
	request := release.CandidateArtifactRequest{CandidateID: "candidate-1", GenerationID: generationID}

	require.Equal(t, generationID, string(candidateServingStateID(request)))

	request.GenerationID = ""
	require.Equal(t, "state-"+shortCandidateDigest(request.CandidateID), string(candidateServingStateID(request)))
}

func TestCandidateArtifactErrorClassificationIsStable(t *testing.T) {
	invalid := candidateArtifactInvalid(errors.New("invalid graph"))
	require.ErrorIs(t, invalid, release.ErrCandidateArtifactInvalid)
	unavailable := candidateArtifactUnavailable(errors.New("source unavailable"))
	require.ErrorIs(t, unavailable, release.ErrCandidateArtifactUnavailable)
}

func TestCandidateRelationContextScopesPinChangesToDependentRelation(t *testing.T) {
	artifact := relationContextFixture(t)
	first, err := candidateRelationContexts(map[string]string{"connection:orders": "revision-a", "connection:customers": "revision-a"}, artifact)
	require.NoError(t, err)
	second, err := candidateRelationContexts(map[string]string{"connection:orders": "revision-b", "connection:customers": "revision-a"}, artifact)
	require.NoError(t, err)
	firstDigests, err := artifact.RelationExecutionDigestsByContext(first)
	require.NoError(t, err)
	secondDigests, err := artifact.RelationExecutionDigestsByContext(second)
	require.NoError(t, err)
	if firstDigests["model:orders"] == secondDigests["model:orders"] {
		t.Fatal("dependent orders relation ignored its changed source pin")
	}
	if firstDigests["model:customers"] != secondDigests["model:customers"] {
		t.Fatal("unrelated customers relation changed when orders pin changed")
	}
	changedManifest := artifact.Manifest()
	changedManifest.Connections["connection:orders"] = semanticmodel.Connection{Kind: "managed", Description: "eu-west-1"}
	changedArtifact, err := projectartifact.NewProject(artifact.Graph(), changedManifest)
	require.NoError(t, err)
	changedContexts, err := candidateRelationContexts(map[string]string{"connection:orders": "revision-a", "connection:customers": "revision-a"}, changedArtifact)
	require.NoError(t, err)
	changedConnectionDigests, err := changedArtifact.RelationExecutionDigestsByContext(changedContexts)
	require.NoError(t, err)
	if firstDigests["model:orders"] == changedConnectionDigests["model:orders"] {
		t.Fatal("dependent orders relation ignored its changed connection descriptor")
	}
	if firstDigests["model:customers"] != changedConnectionDigests["model:customers"] {
		t.Fatal("unrelated customers relation changed when orders connection changed")
	}
	if firstDigests["model:summary"] == "" || firstDigests["model:summary"] == secondDigests["model:summary"] {
		t.Fatal("transitive summary relation did not include changed orders context")
	}
}

func TestRelationExecutionDigestsResolveCanonicalIDTransitiveModelDependencies(t *testing.T) {
	artifact := relationContextFixture(t)
	baseContext, err := candidateRelationContexts(map[string]string{"connection:orders": "revision-a", "connection:customers": "revision-a"}, artifact)
	require.NoError(t, err)
	changedContext, err := candidateRelationContexts(map[string]string{"connection:orders": "revision-b", "connection:customers": "revision-a"}, artifact)
	require.NoError(t, err)
	base, err := artifact.RelationExecutionDigestsByContext(baseContext)
	require.NoError(t, err)
	changed, err := artifact.RelationExecutionDigestsByContext(changedContext)
	require.NoError(t, err)
	// summary's dependency is written as canonical model:orders, while the
	// digest walker indexes graph names. It must resolve the ID and carry the
	// changed orders context transitively.
	require.NotEqual(t, base["model:summary"], changed["model:summary"])
}

func relationContextFixture(t *testing.T) projectartifact.Project {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:context", Kind: projectgraph.KindProject, Name: "context"},
		{ID: "connection:orders", Kind: projectgraph.KindConnection, Name: "orders_connection"},
		{ID: "connection:customers", Kind: projectgraph.KindConnection, Name: "customers_connection"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders_source"},
		{ID: "source:customers", Kind: projectgraph.KindSource, Name: "customers_source"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "model:customers", Kind: projectgraph.KindModel, Name: "customers_model"},
		{ID: "model:summary", Kind: projectgraph.KindModel, Name: "summary_model"},
	}, []projectgraph.Edge{{From: "source:orders", To: "connection:orders"}, {From: "source:customers", To: "connection:customers"}, {From: "model:orders", To: "source:orders"}, {From: "model:customers", To: "source:customers"}, {From: "model:summary", To: "model:orders"}})
	require.NoError(t, err)
	artifact, err := projectartifact.NewProject(graphValue, projectmanifest.Project{
		ID:          "project:context",
		Connections: map[string]semanticmodel.Connection{"connection:orders": {Kind: "managed"}, "connection:customers": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"source:orders": {Connection: "connection:orders", Format: "csv"}, "source:customers": {Connection: "connection:customers", Format: "csv"}},
		Models:      map[string]semanticmodel.Table{"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}, SourceDependencies: []string{"source:orders"}}, "model:customers": {Execution: semanticmodel.ExecutionDefinition{Source: "source:customers"}, SourceDependencies: []string{"source:customers"}}, "model:summary": {Execution: semanticmodel.ExecutionDefinition{SQL: "select * from model.orders"}, ModelDependencies: []string{"model:orders"}}},
	})
	require.NoError(t, err)
	return artifact
}
