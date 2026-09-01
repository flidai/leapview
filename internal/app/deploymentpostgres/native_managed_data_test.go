package deploymentpostgres

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	manageddataresolver "github.com/flidai/leapview/internal/manageddata/resolver"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

func TestPrepareNativeMaterializationRequestBindsExactManagedRevisionOnDetachedModels(t *testing.T) {
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:managed", Kind: projectgraph.KindProject, Name: "managed"},
		{ID: "connection:sample", Kind: projectgraph.KindConnection, Name: "sample"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "semantic-model:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	manifest := projectmanifest.Project{
		ID: "project:managed", Name: "managed",
		Connections: map[string]semanticmodel.Connection{"connection:sample": {Kind: "managed"}},
		SemanticModels: map[string]*semanticmodel.Model{
			"semantic-model:sales": {Name: "sales", Connections: map[string]semanticmodel.Connection{"sample": {Kind: "managed"}}},
		},
		Models: map[string]semanticmodel.Table{"model:orders": {
			ModelName: "orders",
			Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT 1"},
		}},
		NameIndex: projectmanifest.NameIndex{Connections: map[string]string{"sample": "connection:sample"}, Models: map[string]string{"orders": "model:orders"}},
	}
	artifact, err := projectartifact.NewProject(graph, manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Generation: release.CandidateGenerationArtifact{
			ManagedDataPins: []release.ManagedDataPin{{ConnectionID: "connection:sample", RevisionID: "sha256:revision"}},
		},
		Compiler: release.CandidateCompilerEvidence{Artifact: artifact, Manifest: artifact.Manifest()},
	}
	lifetime := &recordingManagedDataLifetime{}
	resolver := &recordingNativeManagedDataResolver{resolution: manageddataresolver.Resolution{
		Roots:     map[projectgraph.ResourceID]string{"connection:sample": "/managed/sample/revision"},
		Revisions: map[projectgraph.ResourceID]string{"connection:sample": "sha256:revision"},
		Lifetime:  lifetime,
	}}
	request, gotLifetime, err := prepareNativeMaterializationRequest(t.Context(), resolver, artifacts, deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: "project:managed", Environment: "prod", TargetID: "target:prod",
	}, "generation:1", "candidate:1", "candidate_namespace", deploymentdomain.DeliveryPlan{})
	if err != nil {
		t.Fatal(err)
	}
	if resolver.projectID != "project:managed" || resolver.pins["connection:sample"] != "sha256:revision" {
		t.Fatalf("resolved project/pins = %q, %#v", resolver.projectID, resolver.pins)
	}
	if gotLifetime != lifetime {
		t.Fatal("managed-data lifetime was not propagated")
	}
	if got := request.Models["semantic-model:sales"].Connections["sample"].Root; got != "/managed/sample/revision" {
		t.Fatalf("bound managed root = %q", got)
	}
	if _, ok := request.ModelTables["orders"]; !ok || len(request.ModelTables) != 1 {
		t.Fatalf("physical model tables = %#v, want authored name orders", request.ModelTables)
	}
	if len(request.Tables) != 1 || request.Tables[0] != "orders" {
		t.Fatalf("physical materialization tables = %#v, want [orders]", request.Tables)
	}
	if got := artifact.Models()["semantic-model:sales"].Connections["sample"].Root; got != "" {
		t.Fatalf("portable artifact managed root mutated to %q", got)
	}
}

func TestPrepareNativeMaterializationRequestRejectsRevisionDriftAndReleasesLease(t *testing.T) {
	lifetime := &recordingManagedDataLifetime{}
	resolver := &recordingNativeManagedDataResolver{resolution: manageddataresolver.Resolution{
		Roots:     map[projectgraph.ResourceID]string{"connection:sample": "/managed/sample/other"},
		Revisions: map[projectgraph.ResourceID]string{"connection:sample": "sha256:other"},
		Lifetime:  lifetime,
	}}
	_, _, err := prepareNativeMaterializationRequest(t.Context(), resolver, release.CandidateArtifactSet{
		Generation: release.CandidateGenerationArtifact{ManagedDataPins: []release.ManagedDataPin{{ConnectionID: "connection:sample", RevisionID: "sha256:expected"}}},
	}, deploymentmodule.NativeDeliveryBuildRequest{ProjectID: "project:managed", Environment: "prod"}, "generation:1", "candidate:1", "", deploymentdomain.DeliveryPlan{})
	if !errors.Is(err, deploymentdomain.ErrDeliveryConflict) {
		t.Fatalf("revision drift error = %v", err)
	}
	if !lifetime.released {
		t.Fatal("managed-data lease was not released after validation failure")
	}
}

func TestPrepareNativeMaterializationRequestClassifiesInvalidBindingAsDeterministicPreflight(t *testing.T) {
	resolver := &recordingNativeManagedDataResolver{err: manageddataresolver.ErrInvalidMetadata}
	_, _, err := prepareNativeMaterializationRequest(t.Context(), resolver, release.CandidateArtifactSet{}, deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: "project:managed", Environment: "prod",
	}, "generation:1", "candidate:1", "", deploymentdomain.DeliveryPlan{})
	if !nativeBuildPreflightFailureIsDeterministic(err) || !errors.Is(err, manageddataresolver.ErrInvalidMetadata) {
		t.Fatalf("invalid binding classification = %v", err)
	}
}

func TestPrepareNativeMaterializationRequestLeavesResolverOutageRetryable(t *testing.T) {
	want := errors.New("managed-data store unavailable")
	resolver := &recordingNativeManagedDataResolver{err: want}
	_, _, err := prepareNativeMaterializationRequest(t.Context(), resolver, release.CandidateArtifactSet{}, deploymentmodule.NativeDeliveryBuildRequest{
		ProjectID: "project:managed", Environment: "prod",
	}, "generation:1", "candidate:1", "", deploymentdomain.DeliveryPlan{})
	if nativeBuildPreflightFailureIsDeterministic(err) || !errors.Is(err, want) {
		t.Fatalf("resolver outage classification = %v", err)
	}
}

type recordingNativeManagedDataResolver struct {
	resolution manageddataresolver.Resolution
	projectID  projectgraph.ResourceID
	pins       map[projectgraph.ResourceID]string
	err        error
}

func (r *recordingNativeManagedDataResolver) ResolveCandidateManagedData(_ context.Context, projectID projectgraph.ResourceID, pins map[projectgraph.ResourceID]string) (manageddataresolver.Resolution, error) {
	r.projectID = projectID
	r.pins = pins
	return r.resolution, r.err
}

type recordingManagedDataLifetime struct{ released bool }

func (l *recordingManagedDataLifetime) Release() error {
	l.released = true
	return nil
}
