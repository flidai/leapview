package app

import (
	"context"
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	"github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	"github.com/flidai/leapview/internal/project/graph"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

type canonicalPublishReaderFake struct {
	candidate deployment.DeliveryCandidate
	seal      deployment.CatalogSeal
	plan      deployment.DeliveryPlan
}

func TestLocalEvaluationRuntimeIsLimitedToDevelopmentAndDisposableEvaluation(t *testing.T) {
	for _, test := range []struct {
		name       string
		production bool
		evaluation bool
		want       bool
	}{
		{name: "development", want: true},
		{name: "disposable evaluation", production: true, evaluation: true, want: true},
		{name: "production", production: true, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := allowsLocalEvaluationRuntime(test.production, test.evaluation); got != test.want {
				t.Fatalf("allowsLocalEvaluationRuntime(%v, %v) = %v, want %v", test.production, test.evaluation, got, test.want)
			}
		})
	}
}

func (f canonicalPublishReaderFake) DeliveryCandidateByID(context.Context, string) (deployment.DeliveryCandidate, error) {
	return f.candidate, nil
}
func (f canonicalPublishReaderFake) DeliveryCatalogSealByID(context.Context, string) (deployment.CatalogSeal, error) {
	return f.seal, nil
}
func (f canonicalPublishReaderFake) PlanByID(context.Context, string) (deployment.DeliveryPlan, error) {
	return f.plan, nil
}

func TestBuildCanonicalPublishRequestUsesReadyCandidateWithoutGenerationRow(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	digest := "sha256:" + strings.Repeat("a", 64)
	candidate := deployment.DeliveryCandidate{
		ID: "candidate-1", PlanID: "plan-1", PlanDigest: digest, TargetID: "target-1", ProjectID: graph.ResourceID("project-1"), Environment: "dev",
		CatalogDigest: digest, CatalogObjectKey: "catalog/catalog-1.duckdb", PhysicalPoolID: "pool-1", CompatibilityDigest: digest,
		SealID: "seal-1", ServingArtifactID: "artifact-1", ServingArtifactDigest: digest, ServingStateID: "state-1", QualificationDigest: digest,
		Status: deployment.DeliveryCandidateReady, CreatedAt: now,
	}
	seal := deployment.CatalogSeal{
		ID: "seal-1", AttemptID: "attempt-1", PlanID: "plan-1", PlanDigest: digest, ExecutionDigest: digest,
		PhysicalPoolID: "pool-1", CatalogDigest: digest, CompatibilityDigest: digest, ServingArtifactID: "artifact-1", ServingArtifactDigest: digest, ServingStateID: "state-1",
		ObjectKey: "catalog/catalog-1.duckdb", ObjectSize: 1, QualificationDigest: digest, Status: deployment.CatalogSealVerified, CreatedAt: now, VerifiedAt: now,
	}
	plan := deployment.DeliveryPlan{ID: "plan-1", Digest: digest, TargetID: "target-1", ProjectID: graph.ResourceID("project-1"), Environment: "dev", Evidence: deployment.DeliveryPlanEvidence{Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}}, CreatedAt: now}
	request, err := buildCanonicalPublishRequest(t.Context(), canonicalPublishReaderFake{candidate: candidate, seal: seal, plan: plan}, "candidate-1", "target-1")
	if err != nil {
		t.Fatalf("buildCanonicalPublishRequest() error = %v", err)
	}
	if request.Generation.ID != candidate.ServingStateID || request.Generation.ServingStateID != candidate.ServingStateID || request.Publication.GenerationID != candidate.ServingStateID {
		t.Fatalf("publication generation identity = %#v / %#v, want %q", request.Generation, request.Publication, candidate.ServingStateID)
	}
	expectedPublicationID := "publication-" + strings.TrimPrefix(
		deployment.CanonicalDeliveryDigest([]byte("candidate-publication:"+candidate.ID)),
		"sha256:",
	)
	if request.Publication.ID != expectedPublicationID {
		t.Fatalf("publication identity = %q, want %q", request.Publication.ID, expectedPublicationID)
	}
}

func TestBindCandidateManagedDataRootsUsesCanonicalNameIndexOnDetachedModels(t *testing.T) {
	models := map[string]*semanticmodel.Model{
		"semantic:sales": {Connections: map[string]semanticmodel.Connection{
			"olist":     {Kind: "managed", Scope: "authored-scope"},
			"warehouse": {Kind: "s3", Scope: "s3://warehouse/"},
		}},
	}
	if err := analyticsmodule.BindCandidateManagedDataRoots(models, map[string]string{"olist": "connection:olist", "warehouse": "connection:warehouse"}, map[string]string{"connection:olist": "/managed/olist/revision"}); err != nil {
		t.Fatal(err)
	}
	managed := models["semantic:sales"].Connections["olist"]
	if managed.Root != "/managed/olist/revision" || managed.Scope != "" {
		t.Fatalf("managed candidate binding = %#v, want root with empty scope", managed)
	}
	if got := models["semantic:sales"].Connections["warehouse"].Scope; got != "s3://warehouse/" {
		t.Fatalf("authored connection scope = %q, want unchanged", got)
	}
}

func TestDeliveryMaterializationDeltaSelectsOnlyImpactedTables(t *testing.T) {
	artifact := materializationDeltaFixture(t)
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Artifact: artifact, Graph: artifact.Graph(), Plan: projectcompiler.ProjectPlan{Changes: []projectcompiler.ProjectPlanChange{
		{Action: "change", ID: "model:orders", Type: string(projectgraph.KindModel), Key: "orders", MaterializationImpact: true},
		{Action: "remove", ID: "model:legacy", Type: string(projectgraph.KindModel), Key: "legacy", MaterializationImpact: true},
	}}}}
	plan := deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "model:customers", Reusable: false, Reason: "execution changed"}}}}
	changed, removed, refreshAll := deliveryMaterializationDelta(artifacts, plan)
	if refreshAll {
		t.Fatal("known model/table changes widened to full refresh")
	}
	if got, want := changed["semantic:sales"], []string{"customers", "orders"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("changed Models = %#v, want %#v", got, want)
	}
	if len(removed) != 1 || removed[0] != "legacy" {
		t.Fatalf("removed tables = %#v, want [legacy]", removed)
	}
	unknown := plan
	unknown.Evidence.Reuse = append(unknown.Evidence.Reuse, deployment.DeliveryReuseDecision{ResourceID: "unknown-relation", Reusable: false})
	if _, _, refreshAll = deliveryMaterializationDelta(artifacts, unknown); !refreshAll {
		t.Fatal("unknown relation evidence did not fail closed to full refresh")
	}
}

func TestDeliveryMaterializationDeltaAddsPipelineScopeWithoutGraphImpact(t *testing.T) {
	artifact := materializationDeltaFixture(t)
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Artifact: artifact, Graph: artifact.Graph(), Plan: projectcompiler.ProjectPlan{Project: "delta"}}}
	plan := deployment.DeliveryPlan{PipelinePlan: &deployment.PipelinePlan{MaterializationScope: []string{"customers"}}}
	changed, removed, refreshAll := deliveryMaterializationDelta(artifacts, plan)
	if refreshAll || len(removed) != 0 {
		t.Fatalf("pipeline scope widened unexpectedly: changed=%#v removed=%#v refreshAll=%v", changed, removed, refreshAll)
	}
	if got := changed["semantic:sales"]; len(got) != 1 || got[0] != "customers" {
		t.Fatalf("pipeline scope = %#v, want semantic:sales/customers", changed)
	}
}

func TestDeliveryMaterializationDeltaUsesSemanticDatasetAlias(t *testing.T) {
	base := materializationDeltaFixture(t)
	manifest := base.Manifest()
	model := manifest.SemanticModels["semantic:sales"]
	delete(model.Tables, "customers")
	model.Tables["customer_accounts"] = semanticmodel.Table{
		ModelName: "customers",
		Execution: semanticmodel.ExecutionDefinition{Source: "customers"},
	}
	artifact, err := projectartifact.NewProject(base.Graph(), manifest)
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{Artifact: artifact, Graph: artifact.Graph(), Plan: projectcompiler.ProjectPlan{Project: "delta"}}}
	plan := deployment.DeliveryPlan{PipelinePlan: &deployment.PipelinePlan{MaterializationScope: []string{"customers"}}}
	changed, removed, refreshAll := deliveryMaterializationDelta(artifacts, plan)
	if refreshAll || len(removed) != 0 {
		t.Fatalf("pipeline scope widened unexpectedly: changed=%#v removed=%#v refreshAll=%v", changed, removed, refreshAll)
	}
	if got := changed["semantic:sales"]; len(got) != 1 || got[0] != "customer_accounts" {
		t.Fatalf("pipeline scope = %#v, want semantic:sales/customer_accounts", changed)
	}
}

func materializationDeltaFixture(t *testing.T) projectartifact.Project {
	t.Helper()
	pathLocation := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "orders.csv", Format: "csv"},
		Format:                 "csv",
		Options:                projectcontracts.DefaultCSVReaderOptions(),
	}}
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:delta", Kind: projectgraph.KindProject, Name: "delta"},
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders_source"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders"},
		{ID: "model:customers", Kind: projectgraph.KindModel, Name: "customers"},
		{ID: "model:legacy", Kind: projectgraph.KindModel, Name: "legacy"},
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
	}, []projectgraph.Edge{{From: "source:orders", To: "connection:warehouse"}, {From: "model:orders", To: "source:orders"}, {From: "model:customers", To: "source:orders"}, {From: "model:legacy", To: "source:orders"}, {From: "semantic:sales", To: "model:orders"}, {From: "semantic:sales", To: "model:customers"}})
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := projectartifact.NewProject(graphValue, projectmanifest.Project{
		ID:          "project:delta",
		Connections: map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"source:orders": {Connection: "connection:warehouse", Format: "csv", Path: "orders.csv", PathLocation: pathLocation, EffectivePathLocation: pathLocation}},
		Models: map[string]semanticmodel.Table{
			"model:orders":    {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}},
			"model:customers": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}},
			"model:legacy":    {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}},
		},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}}, "customers": {Execution: semanticmodel.ExecutionDefinition{Source: "customers"}}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
