package runtimefactory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/candidatecatalog"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboardauthoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	"github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

func TestCandidatePlanExecutionIdentityIncludesDataModeAndEffectiveBindings(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_1")
	if err != nil {
		t.Fatal(err)
	}
	base := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		Generation: release.CandidateGenerationArtifact{
			Identity: identity, DataRevision: "snapshot:7", DataMode: release.GenerationDataReuseSnapshot, Deterministic: true,
			Connections: []release.CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}},
		},
		Compiler: release.CandidateCompilerEvidence{Plan: projectcompiler.ProjectPlan{Project: "project_delivery"}},
	}
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: base.Artifact.SourceDigest,
		Candidate: deployment.Candidate{ID: "candidate_1", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}},
	}
	first, err := CandidatePlanRequest(input, base, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := deployment.NewDeliveryPlan(deployment.DeliveryPlan{
		ID: first.ID, ActorID: first.ActorID, TargetID: first.TargetID, ProjectID: projectID,
		Environment: first.Environment, Operation: first.Operation, SourceDigest: first.SourceDigest,
		Execution: first.Execution, Provenance: first.Provenance, Governance: first.Governance,
		Evidence: first.Evidence, CreatedAt: first.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}

	changedMode := base
	changedMode.Generation.DataMode = release.GenerationDataRefreshSources
	changedMode.Generation.DataRevision = "sources:revision-9"
	second, err := CandidatePlanRequest(input, changedMode, "runtime:v1", first.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if second.Execution.ConfigDigest == first.Execution.ConfigDigest {
		t.Fatal("data mode/revision change preserved config execution identity")
	}

	changedBinding := base
	changedBinding.Generation.Connections = []release.CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "quack"}}
	third, err := CandidatePlanRequest(input, changedBinding, "runtime:v1", first.CreatedAt)
	if err != nil {
		t.Fatal(err)
	}
	if third.Execution.BindingDigest == first.Execution.BindingDigest {
		t.Fatal("effective connector binding change preserved binding execution identity")
	}

	if firstPlan.ExecutionDigest == "" {
		t.Fatal("candidate plan did not compute execution identity")
	}
}

func TestCandidatePlanRejectsLegacySnapshotReuseWithControlledRebuildDiagnostic(t *testing.T) {
	_, err := CandidatePlanRequestWithPolicyAndReuse(
		deployment.DeliveryCandidateBuildInput{},
		release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseSnapshotLegacy}},
		"runtime:v1", CandidateDeliveryPolicy{}, time.Time{}, nil,
	)
	if !errors.Is(err, release.ErrLegacyReuseSnapshot) {
		t.Fatalf("legacy candidate plan error = %v, want controlled rebuild diagnostic", err)
	}
}

func TestQualificationRequestForCandidateCarriesReviewerPolicyDigest(t *testing.T) {
	authorizationFingerprint := deliveryPlanDigest('c')
	request := QualificationRequestForCandidate(release.CandidateArtifactSet{
		AuthorizationFingerprint: authorizationFingerprint,
		Generation: release.CandidateGenerationArtifact{
			Identity: projectgraph.ServingIdentity{GenerationID: "candidate_qualification"},
		},
	})
	if request.PolicyDigest != authorizationFingerprint {
		t.Fatalf("qualification policy digest = %q, want authorization fingerprint %q", request.PolicyDigest, authorizationFingerprint)
	}
	if request.ReviewerPolicyDigest != authorizationFingerprint {
		t.Fatalf("qualification reviewer policy digest = %q, want authorization fingerprint %q", request.ReviewerPolicyDigest, authorizationFingerprint)
	}
	if request.Policy == nil {
		t.Fatal("qualification request has no policy callback")
	}
}

func TestCandidatePlanPreservesSemanticRelationshipPathsAndQualificationScope(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_relationships")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		Generation:               release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Deterministic: true},
		Compiler: release.CandidateCompilerEvidence{Plan: projectcompiler.ProjectPlan{
			Project: "project_delivery", DependencyChanges: []projectcompiler.ProjectPlanDependencyChange{{From: "orders", To: "customers", Type: "model", Action: "change"}, {From: "customers", To: "regions", Type: "model", Action: "change"}},
		}},
	}
	input := deployment.DeliveryCandidateBuildInput{ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: artifacts.Artifact.SourceDigest, Candidate: deployment.Candidate{ID: "candidate_relationships", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod"}}}
	request, err := CandidatePlanRequest(input, artifacts, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Evidence.GraphImpact.RelationshipPaths) != 2 || request.Evidence.GraphImpact.RelationshipPaths[0] != "orders -> customers" || request.Evidence.GraphImpact.RelationshipPaths[1] != "customers -> regions" {
		t.Fatalf("relationship paths = %#v", request.Evidence.GraphImpact.RelationshipPaths)
	}
	if len(request.Evidence.GraphImpact.IndirectlyAffected) != 2 || len(request.Evidence.Qualification.Steps) < 2 {
		t.Fatalf("impact/qualification scope = %#v / %#v", request.Evidence.GraphImpact.IndirectlyAffected, request.Evidence.Qualification.Steps)
	}
}

func TestCandidatePlanReuseDecisionUsesExactActiveIdentity(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_1")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		Generation: release.CandidateGenerationArtifact{
			Identity: identity, DataRevision: "snapshot:7", DataMode: release.GenerationDataReuseSnapshot, Deterministic: true,
			Connections: []release.CandidateConnectionRequirement{{ConnectionID: "warehouse", ConnectorKind: "postgres"}},
		},
		Compiler: release.CandidateCompilerEvidence{Plan: projectcompiler.ProjectPlan{Project: "project_delivery"}},
	}
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: artifacts.Artifact.SourceDigest,
		Candidate: deployment.Candidate{ID: "candidate_1", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}},
	}
	baseRequest, err := CandidatePlanRequest(input, artifacts, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	baseExecution, err := baseRequest.Execution.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	reuse := &deployment.DeliveryReuseInput{
		BaseExecutionDigest: baseExecution, CatalogDigest: deliveryPlanDigest('d'), BaseCatalogDigest: deliveryPlanDigest('d'),
		PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('e'), BaseCompatibilityDigest: deliveryPlanDigest('e'), Deterministic: true,
	}
	exact, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{}, baseRequest.CreatedAt, reuse)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Evidence.Reuse) != 1 || !exact.Evidence.Reuse[0].Reusable || exact.Evidence.Reuse[0].ReuseKeyDigest == "" {
		t.Fatalf("exact active identity reuse = %#v", exact.Evidence.Reuse)
	}
	changed := *reuse
	changed.BaseCatalogDigest = deliveryPlanDigest('f')
	mismatch, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{}, baseRequest.CreatedAt, &changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatch.Evidence.Reuse) != 1 || mismatch.Evidence.Reuse[0].Reusable {
		t.Fatalf("mismatching active identity unexpectedly reused: %#v", mismatch.Evidence.Reuse)
	}
	undeclared := artifacts
	undeclared.Generation.Deterministic = false
	nondeterministic, err := CandidatePlanRequestWithPolicyAndReuse(input, undeclared, "runtime:v1", CandidateDeliveryPolicy{}, baseRequest.CreatedAt, reuse)
	if err != nil {
		t.Fatal(err)
	}
	if len(nondeterministic.Evidence.Reuse) != 1 || nondeterministic.Evidence.Reuse[0].Reusable || !strings.Contains(nondeterministic.Evidence.Reuse[0].Reason, "nondeterminism") {
		t.Fatalf("undeclared nondeterminism planner decision = %#v", nondeterministic.Evidence.Reuse)
	}
}

func TestCandidatePlanEmitsRelationScopedReuseDecisions(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_relations")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		// Materialization-impacting code changes refresh affected relations
		// while still retaining unchanged base references.
		Generation: release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:revision", DataMode: release.GenerationDataRefreshSources, Deterministic: true},
		Compiler: release.CandidateCompilerEvidence{
			Plan:                  projectcompiler.ProjectPlan{Project: "project_delivery"},
			RelationExecution:     map[string]string{"model_orders": deliveryPlanDigest('1'), "model_customers": deliveryPlanDigest('2')},
			BaseRelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1'), "model_customers": deliveryPlanDigest('9')},
		},
	}
	input := deployment.DeliveryCandidateBuildInput{ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: artifacts.Artifact.SourceDigest, Candidate: deployment.Candidate{ID: "candidate_relations", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}}}
	base, err := CandidatePlanRequest(input, artifacts, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	baseExecution, err := base.Execution.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	baseContext, err := base.Execution.ContextDigest()
	if err != nil {
		t.Fatal(err)
	}
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{}, base.CreatedAt, &deployment.DeliveryReuseInput{BaseExecutionDigest: baseExecution, BaseContextDigest: baseContext, CatalogDigest: deliveryPlanDigest('d'), BaseCatalogDigest: deliveryPlanDigest('d'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('e'), BaseCompatibilityDigest: deliveryPlanDigest('e'), Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Evidence.Reuse) != 2 || request.Evidence.Reuse[0].ResourceID != "model_customers" || request.Evidence.Reuse[0].Reusable || !request.Evidence.Reuse[0].RetainBase || request.Evidence.Reuse[1].ResourceID != "model_orders" || !request.Evidence.Reuse[1].Reusable {
		t.Fatalf("relation reuse evidence = %#v", request.Evidence.Reuse)
	}
}

func TestRestatementPlanUsesExplicitCandidateLevelFullRefresh(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_restatement")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		Generation:               release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:revision", DataMode: release.GenerationDataRefreshSources, Deterministic: true},
		Compiler:                 release.CandidateCompilerEvidence{Plan: projectcompiler.ProjectPlan{Project: "project_delivery"}, RelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1')}, BaseRelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1')}},
	}
	input := deployment.DeliveryCandidateBuildInput{ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: artifacts.Artifact.SourceDigest, Operation: deployment.DeliveryOperationRestatement, Candidate: deployment.Candidate{ID: "candidate_restatement", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}}}
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{}, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), &deployment.DeliveryReuseInput{
		BaseExecutionDigest: deliveryPlanDigest('1'), CatalogDigest: deliveryPlanDigest('2'), BaseCatalogDigest: deliveryPlanDigest('2'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('3'), BaseCompatibilityDigest: deliveryPlanDigest('3'), Deterministic: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Evidence.Reuse) != 1 || request.Evidence.Reuse[0].ResourceID != input.Candidate.ID || request.Evidence.Reuse[0].Reusable || request.Evidence.Reuse[0].RetainBase {
		t.Fatalf("restatement reuse evidence = %#v, want explicit candidate-level full refresh", request.Evidence.Reuse)
	}
	plan := &deployment.DeliveryPlan{Operation: deployment.DeliveryOperationRestatement, Evidence: request.Evidence}
	if err := validateReuseEvidenceCoverage(plan, artifacts, input.Candidate.ID); err != nil {
		t.Fatalf("restatement candidate-level evidence rejected: %v", err)
	}
}

func TestCandidateRunnerForcesRestatementRefreshFromReuseBase(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
			baseCalled = true
			return nil, errors.New("restatement base resolver must not run")
		}},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate-restatement"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseBase}},
	}
	plan := deployment.DeliveryPlan{Operation: deployment.DeliveryOperationRestatement, BaseGenerationID: "generation-1", Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate-restatement", Reason: "operation requires explicit full materialization"}}}}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: plan})
	if err == nil || baseCalled {
		t.Fatalf("restatement reused base: err=%v baseCalled=%v", err, baseCalled)
	}
	if runner.artifacts.Generation.DataMode != release.GenerationDataRefreshSources {
		t.Fatalf("restatement data mode = %q, want %q", runner.artifacts.Generation.DataMode, release.GenerationDataRefreshSources)
	}
}

func TestCandidatePlanPolicyOnlyChangeRetainsPhysicalRelations(t *testing.T) {
	projectID := projectgraph.ResourceID("project_delivery")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_policy_only")
	if err != nil {
		t.Fatal(err)
	}
	base := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: deliveryPlanDigest('b'), CompilerVersion: "compiler:v1", SchemaVersion: 1},
		AuthorizationFingerprint: deliveryPlanDigest('c'),
		Generation:               release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataReuseBase, Deterministic: true},
		Compiler: release.CandidateCompilerEvidence{
			Plan:                  projectcompiler.ProjectPlan{Project: "project_delivery"},
			RelationExecution:     map[string]string{"model_orders": deliveryPlanDigest('1')},
			BaseRelationExecution: map[string]string{"model_orders": deliveryPlanDigest('1')},
		},
	}
	input := deployment.DeliveryCandidateBuildInput{ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: base.Artifact.SourceDigest, Candidate: deployment.Candidate{ID: "candidate_policy_only", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}}}
	baseRequest, err := CandidatePlanRequest(input, base, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	baseExecution, err := baseRequest.Execution.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	baseContext, err := baseRequest.Execution.ContextDigest()
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.AuthorizationFingerprint = deliveryPlanDigest('d')
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, changed, "runtime:v1", CandidateDeliveryPolicy{}, baseRequest.CreatedAt, &deployment.DeliveryReuseInput{
		BaseExecutionDigest: baseExecution, BaseContextDigest: baseContext,
		CatalogDigest: deliveryPlanDigest('e'), BaseCatalogDigest: deliveryPlanDigest('e'),
		PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('f'), BaseCompatibilityDigest: deliveryPlanDigest('f'), Deterministic: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestExecution, err := request.Execution.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if requestExecution != baseExecution {
		t.Fatalf("policy-only change altered physical execution identity: %s != %s", requestExecution, baseExecution)
	}
	if request.Governance.PolicyDigest == baseRequest.Governance.PolicyDigest || request.Governance.AuthorizationDigest == baseRequest.Governance.AuthorizationDigest {
		t.Fatal("policy-only change did not alter governance evidence")
	}
	if len(request.Evidence.Reuse) != 1 || !request.Evidence.Reuse[0].Reusable {
		t.Fatalf("policy-only relation was not reusable: %#v", request.Evidence.Reuse)
	}
}

func TestCandidatePlanDashboardOnlyChangeRetainsPhysicalRelations(t *testing.T) {
	baseArtifact := dashboardPhysicalArtifact(t, "Dashboard v1", false)
	changedArtifact := dashboardPhysicalArtifact(t, "Dashboard v2", true)
	relationContext := "sha256:" + strings.Repeat("a", 64)
	baseRelations, err := baseArtifact.RelationExecutionDigests(relationContext)
	if err != nil {
		t.Fatal(err)
	}
	changedRelations, err := changedArtifact.RelationExecutionDigests(relationContext)
	if err != nil {
		t.Fatal(err)
	}
	if baseArtifact.Graph().Digest() == changedArtifact.Graph().Digest() || baseArtifact.Digest() == changedArtifact.Digest() {
		t.Fatal("dashboard-only fixture did not change full graph/artifact identity")
	}
	if len(baseRelations) == 0 || !strings.EqualFold(baseRelations["model:orders"], changedRelations["model:orders"]) {
		t.Fatalf("dashboard-only change altered relation execution identities: base=%#v changed=%#v", baseRelations, changedRelations)
	}
	projectID := projectgraph.ResourceID("project:dashboard")
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_dashboard_only")
	if err != nil {
		t.Fatal(err)
	}
	base := release.CandidateArtifactSet{
		Artifact:                 release.ProjectArtifactProvenance{SourceDigest: deliveryPlanDigest('a'), ProjectDigest: baseArtifact.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		AuthorizationFingerprint: deliveryPlanDigest('b'),
		Generation:               release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataReuseBase, Deterministic: true},
		Compiler:                 release.CandidateCompilerEvidence{Graph: baseArtifact.Graph(), Plan: projectcompiler.ProjectPlan{Project: "project:dashboard"}, Artifact: baseArtifact, RelationExecution: baseRelations, BaseRelationExecution: baseRelations},
	}
	input := deployment.DeliveryCandidateBuildInput{ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: base.Artifact.SourceDigest, Candidate: deployment.Candidate{ID: "candidate_dashboard_only", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}}}
	baseRequest, err := CandidatePlanRequest(input, base, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	baseExecution, err := baseRequest.Execution.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	baseContext, err := baseRequest.Execution.ContextDigest()
	if err != nil {
		t.Fatal(err)
	}
	changed := base
	changed.Artifact.ProjectDigest = changedArtifact.Digest()
	changed.Compiler.Graph = changedArtifact.Graph()
	changed.Compiler.Artifact = changedArtifact
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, changed, "runtime:v1", CandidateDeliveryPolicy{}, baseRequest.CreatedAt, &deployment.DeliveryReuseInput{
		BaseExecutionDigest: baseExecution, BaseContextDigest: baseContext,
		CatalogDigest: deliveryPlanDigest('c'), BaseCatalogDigest: deliveryPlanDigest('c'),
		PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('d'), BaseCompatibilityDigest: deliveryPlanDigest('d'), Deterministic: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	requestExecution, err := request.Execution.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	if requestExecution != baseExecution {
		t.Fatalf("dashboard-only change altered execution identity: %s != %s", requestExecution, baseExecution)
	}
	if len(request.Evidence.Reuse) != 1 || !request.Evidence.Reuse[0].Reusable {
		t.Fatalf("dashboard-only relation was not reusable: %#v", request.Evidence.Reuse)
	}
	if request.Evidence.Qualification.Steps == nil || request.Provenance.BuildDefinition == "" {
		t.Fatal("dashboard-only plan lost qualification/provenance evidence")
	}
}

func dashboardPhysicalArtifact(t *testing.T, dashboardTitle string, accessVariant bool) projectartifact.Project {
	t.Helper()
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:dashboard", Kind: projectgraph.KindProject, Name: "dashboard"},
		{ID: "connection:warehouse", Kind: projectgraph.KindConnection, Name: "warehouse"},
		{ID: "source:orders", Kind: projectgraph.KindSource, Name: "orders"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "semantic:sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "dashboard:sales", Kind: projectgraph.KindDashboard, Name: "sales_dashboard", Metadata: projectgraph.Metadata{Description: dashboardTitle}},
	}, []projectgraph.Edge{
		{From: "source:orders", To: "connection:warehouse", Relation: "uses_connection"},
		{From: "model:orders", To: "source:orders", Relation: "reads_source"},
		{From: "semantic:sales", To: "model:orders", Relation: "uses_model"},
		{From: "dashboard:sales", To: "semantic:sales", Relation: "renders"},
	})
	if err != nil {
		t.Fatal(err)
	}
	access := projectmanifest.AccessPolicy{}
	if accessVariant {
		access = projectmanifest.AccessPolicy{Groups: map[string]projectmanifest.Group{"analysts": {ID: "analysts", Name: "Analysts"}}}
	}
	artifact, err := projectartifact.NewProject(graphValue, projectmanifest.Project{
		ID: "project:dashboard", Name: "dashboard",
		Connections:          map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed", Scope: "warehouse"}},
		Sources:              map[string]semanticmodel.Source{"source:orders": {Connection: "connection:warehouse", Format: "csv", Path: "orders.csv"}},
		Models:               map[string]semanticmodel.Table{"model:orders": {Source: "source:orders"}},
		SemanticModels:       map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {Source: "orders"}}}},
		DashboardDefinitions: map[string]dashboarddefinition.Definition{"dashboard:sales": {ID: "dashboard:sales", Title: dashboardTitle, SemanticModel: "semantic:sales"}},
		DashboardSources:     map[string]projectmanifest.DashboardSource{"dashboard:sales": {Document: dashboardauthoring.Dashboard{ID: "dashboard:sales", SemanticModel: "semantic:sales"}}},
		Access:               access,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func TestReuseEvidenceCoverageRequiresExactCurrentRelations(t *testing.T) {
	artifacts := release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{RelationExecution: map[string]string{
		"model_orders": deliveryPlanDigest('1'), "model_customers": deliveryPlanDigest('2'),
	}}}
	valid := func(ids ...string) *deployment.DeliveryPlan {
		decisions := make([]deployment.DeliveryReuseDecision, len(ids))
		for i, id := range ids {
			decisions[i] = deployment.DeliveryReuseDecision{ResourceID: id, Reusable: true}
		}
		return &deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: decisions}}
	}
	for name, plan := range map[string]*deployment.DeliveryPlan{
		"missing":   valid("model_orders"),
		"unknown":   valid("model_orders", "model_regions"),
		"duplicate": valid("model_orders", "model_orders"),
	} {
		if err := validateReuseEvidenceCoverage(plan, artifacts, "candidate-1"); err == nil {
			t.Errorf("%s relation evidence unexpectedly accepted", name)
		}
	}
	if err := validateReuseEvidenceCoverage(valid("model_orders", "model_customers"), artifacts, "candidate-1"); err != nil {
		t.Fatalf("exact relation evidence rejected: %v", err)
	}
	if err := validateReuseEvidenceCoverage(&deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "other", Reusable: true}}}}, release.CandidateArtifactSet{}, "candidate-1"); err == nil {
		t.Fatal("candidate-level evidence accepted wrong resource ID")
	}
}

func TestCandidateRunnerRejectsPartialRelationEvidenceBeforeBase(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
			baseCalled = true
			return nil, errors.New("base resolver must not run")
		}},
		input: deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate-1"}},
		artifacts: release.CandidateArtifactSet{Compiler: release.CandidateCompilerEvidence{RelationExecution: map[string]string{
			"model_orders": deliveryPlanDigest('1'), "model_customers": deliveryPlanDigest('2'),
		}}},
	}
	plan := deployment.DeliveryPlan{BaseGenerationID: "generation-1", Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "model_orders", Reusable: true}}}}
	if _, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: plan}); err == nil || baseCalled {
		t.Fatalf("partial relation evidence err=%v baseCalled=%v", err, baseCalled)
	}
}

func TestCandidateRunnerRebuildsWhenReuseDecisionMismatches(t *testing.T) {
	basePlan := deployment.DeliveryPlan{Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate_1", Reusable: false, Reason: "catalog compatibility identity changed"}}}}
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{
			Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				baseCalled = true
				return nil, nil
			},
		},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate_1"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseSnapshot}},
	}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: basePlan})
	if err == nil || baseCalled {
		t.Fatalf("mismatching reuse decision err=%v baseCalled=%v", err, baseCalled)
	}
	if runner.artifacts.Generation.DataMode != release.GenerationDataRefreshSources {
		t.Fatalf("mismatching reuse decision left data mode %q", runner.artifacts.Generation.DataMode)
	}
}

func TestCandidateRunnerUsesBaseForExactReuseDecision(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{
			Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				baseCalled = true
				return nil, errors.New("base resolver reached")
			},
		},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate_1"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseSnapshot}},
	}
	plan := deployment.DeliveryPlan{BaseGenerationID: "generation_1", Evidence: deployment.DeliveryPlanEvidence{Reuse: []deployment.DeliveryReuseDecision{{ResourceID: "candidate_1", Reusable: true, Reason: "exact identity"}}}}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: plan})
	if err == nil || !strings.Contains(err.Error(), "base resolver reached") || !baseCalled {
		t.Fatalf("exact reuse err=%v baseCalled=%v", err, baseCalled)
	}
}

func TestCandidateRunnerMissingReuseDecisionRebuilds(t *testing.T) {
	baseCalled := false
	runner := &candidateCatalogRunner{
		config: CandidateCatalogRunnerConfig{
			Base: func(context.Context, deployment.DeliveryBuildInput) (*candidatecatalog.SealedArtifact, error) {
				baseCalled = true
				return nil, errors.New("base resolver must not run")
			},
		},
		input:     deployment.DeliveryCandidateBuildInput{Candidate: deployment.Candidate{ID: "candidate_1"}},
		artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseSnapshot}},
	}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{Plan: deployment.DeliveryPlan{BaseGenerationID: "generation_1"}})
	if err == nil || baseCalled {
		t.Fatalf("missing reuse decision err=%v baseCalled=%v", err, baseCalled)
	}
	if runner.artifacts.Generation.DataMode != release.GenerationDataRefreshSources {
		t.Fatalf("missing reuse decision left data mode %q", runner.artifacts.Generation.DataMode)
	}
}

func TestCandidateRunnerRejectsLegacySnapshotReuseBeforePhysicalWork(t *testing.T) {
	runner := &candidateCatalogRunner{artifacts: release.CandidateArtifactSet{Generation: release.CandidateGenerationArtifact{DataMode: release.GenerationDataReuseSnapshotLegacy}}}
	_, err := runner.Construct(context.Background(), deployment.DeliveryBuildInput{})
	if !errors.Is(err, release.ErrLegacyReuseSnapshot) {
		t.Fatalf("legacy candidate build error = %v, want controlled rebuild diagnostic", err)
	}
}

func deliveryPlanDigest(char byte) string { return "sha256:" + strings.Repeat(string(char), 64) }
