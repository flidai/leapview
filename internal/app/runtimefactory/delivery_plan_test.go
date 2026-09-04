package runtimefactory

import (
	"strings"
	"testing"
	"time"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	dashboarddocument "github.com/flidai/leapview/internal/dashboard/document"
	"github.com/flidai/leapview/internal/deployment"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcompiler "github.com/flidai/leapview/internal/project/compiler"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/release"
)

func TestPipelineScopeRelationIDsUsesOpaqueGraphIdentity(t *testing.T) {
	graphValue, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: "project:delivery", Kind: projectgraph.KindProject, Name: "delivery"},
		{ID: "model:orders", Kind: projectgraph.KindModel, Name: "orders_model"},
		{ID: "model:customer_orders", Kind: projectgraph.KindModel, Name: "customer_orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := pipelineScopeRelationIDs(
		[]string{"orders_model"},
		graphValue,
		map[string]string{"model:orders": deliveryPlanDigest('1'), "model:customer_orders": deliveryPlanDigest('2')},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := resolved["model:orders"]; got != "orders_model" || len(resolved) != 1 {
		t.Fatalf("resolved scope = %#v, want exact model:orders identity", resolved)
	}
	if _, err := pipelineScopeRelationIDs([]string{"orders"}, graphValue, map[string]string{"model:customer_orders": deliveryPlanDigest('2')}); err == nil {
		t.Fatal("suffix-only model name unexpectedly resolved")
	}
}

func TestPipelinePlanRejectsRefreshOfRelationOutsideExactScope(t *testing.T) {
	artifact := dashboardPhysicalArtifact(t, "Sales", false)
	projectID := projectgraph.ResourceID("project:dashboard")
	sourceDigest := deliveryPlanDigest('a')
	pipelinePlan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-sales", PipelineID: "pipeline:sales", ProjectID: projectID.String(), Environment: "prod",
		SemanticModelID: "semantic:sales", ServingGenerationID: "generation_0", ArtifactDigest: sourceDigest,
		SelectionDigest: deliveryPlanDigest('b'), MaterializationScope: []string{"orders_model"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_pipeline")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact:   release.ProjectArtifactProvenance{SourceDigest: sourceDigest, ProjectDigest: artifact.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		Generation: release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Deterministic: true},
		Compiler: release.CandidateCompilerEvidence{
			Graph: artifact.Graph(), Artifact: artifact, Plan: projectcompiler.ProjectPlan{Project: projectID.String()},
			RelationExecution:     map[string]string{"model:orders": deliveryPlanDigest('1'), "model:customers": deliveryPlanDigest('2')},
			BaseRelationExecution: map[string]string{"model:orders": deliveryPlanDigest('1')},
		},
	}
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: sourceDigest, Operation: deployment.DeliveryOperationRestatement, PipelinePlan: &pipelinePlan,
		Candidate: deployment.Candidate{ID: "candidate_pipeline", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}},
	}
	_, err = CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC), &deployment.DeliveryReuseInput{
		CatalogDigest: deliveryPlanDigest('c'), BaseCatalogDigest: deliveryPlanDigest('c'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('d'), BaseCompatibilityDigest: deliveryPlanDigest('d'), Deterministic: true,
	})
	if err == nil || !strings.Contains(err.Error(), `relation "model:customers" is outside materialization scope`) {
		t.Fatalf("pipeline plan error = %v, want exact-scope rejection", err)
	}
}

func TestPipelinePlanRetainsExactUnselectedRelationWithoutReexecutingIt(t *testing.T) {
	artifact := dashboardPhysicalArtifact(t, "Sales", false)
	projectID := projectgraph.ResourceID("project:dashboard")
	sourceDigest := deliveryPlanDigest('a')
	pipelinePlan, err := deployment.NewPipelinePlan(deployment.PipelinePlan{
		ID: "pipeline-plan-sales", PipelineID: "pipeline:sales", ProjectID: projectID.String(), Environment: "prod",
		SemanticModelID: "semantic:sales", ServingGenerationID: "generation_0", ArtifactDigest: sourceDigest,
		SelectionDigest: deliveryPlanDigest('b'), MaterializationScope: []string{"orders_model"}, InvocationSource: "manual",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "prod", "candidate_pipeline")
	if err != nil {
		t.Fatal(err)
	}
	artifacts := release.CandidateArtifactSet{
		Artifact: release.ProjectArtifactProvenance{SourceDigest: sourceDigest, ProjectDigest: artifact.Digest(), CompilerVersion: projectartifact.CompilerVersion, SchemaVersion: projectartifact.Version},
		// The project contains undeclared nondeterminism, but the unselected
		// relation is retained from the sealed base and is never re-executed.
		Generation: release.CandidateGenerationArtifact{Identity: identity, DataRevision: "sources:1", DataMode: release.GenerationDataRefreshSources, Deterministic: false},
		Compiler: release.CandidateCompilerEvidence{
			Graph: artifact.Graph(), Artifact: artifact, Plan: projectcompiler.ProjectPlan{Project: projectID.String()},
			RelationExecution:     map[string]string{"model:orders": deliveryPlanDigest('1'), "model:customers": deliveryPlanDigest('2')},
			BaseRelationExecution: map[string]string{"model:orders": deliveryPlanDigest('1'), "model:customers": deliveryPlanDigest('2')},
		},
	}
	input := deployment.DeliveryCandidateBuildInput{
		ProjectID: projectID, OwnerID: "owner_1", ArtifactDigest: sourceDigest, Operation: deployment.DeliveryOperationRestatement, PipelinePlan: &pipelinePlan,
		Candidate: deployment.Candidate{ID: "candidate_pipeline", TargetID: "target_prod", Scope: deployment.CandidateScope{ProjectID: projectID, Environment: "prod", BaseGenerationID: "generation_0"}},
	}
	base, err := CandidatePlanRequest(input, artifacts, "runtime:v1", time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	baseContext, err := base.Execution.ContextDigest()
	if err != nil {
		t.Fatal(err)
	}
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, base.CreatedAt, &deployment.DeliveryReuseInput{
		BaseContextDigest: baseContext, CatalogDigest: deliveryPlanDigest('c'), BaseCatalogDigest: deliveryPlanDigest('c'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('d'), BaseCompatibilityDigest: deliveryPlanDigest('d'), Deterministic: true,
	})
	if err != nil {
		t.Fatalf("plan scoped refresh with an exact sealed sibling: %v", err)
	}
	if len(request.Evidence.Reuse) != 2 || request.Evidence.Reuse[0].ResourceID != "model:customers" || !request.Evidence.Reuse[0].Reusable || request.Evidence.Reuse[1].ResourceID != "model:orders" || request.Evidence.Reuse[1].Reusable || !request.Evidence.Reuse[1].RetainBase {
		t.Fatalf("pipeline reuse evidence = %#v", request.Evidence.Reuse)
	}
}

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
			Identity: identity, DataRevision: "snapshot:7", DataMode: release.GenerationDataReuseBase, Deterministic: true,
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
			Project: "project_delivery", DependencyChanges: []projectcompiler.ProjectPlanDependencyChange{{From: "orders", To: "customers", Type: "uses_model", ResourceKind: string(projectgraph.KindModel), Action: "change"}, {From: "customers", To: "regions", Type: "uses_model", ResourceKind: string(projectgraph.KindModel), Action: "change"}},
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
	for _, affected := range request.Evidence.GraphImpact.IndirectlyAffected {
		if affected.Kind != string(projectgraph.KindModel) {
			t.Fatalf("indirectly affected resource = %#v, want model authorization kind", affected)
		}
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
			Identity: identity, DataRevision: "snapshot:7", DataMode: release.GenerationDataReuseBase, Deterministic: true,
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
	refreshRevision, err := release.CandidateSourcesDataRevision(artifacts.Artifact.SourceDigest, artifacts.Generation.ManagedDataPins)
	if err != nil {
		t.Fatal(err)
	}
	refreshConfig := candidateDataConfigDigest(input.Candidate.TargetID, input.Candidate.Scope.Environment, release.GenerationDataRefreshSources, refreshRevision)
	if baseRequest.Execution.ConfigDigest != refreshConfig || !strings.Contains(baseRequest.Evidence.PhysicalWorkStatement, "refreshes compiled project relations") || !strings.Contains(baseRequest.Evidence.ReuseStatement, "does not reuse") {
		t.Fatalf("unavailable reuse identity left stale plan execution/evidence: config=%q physical=%q reuse=%q", baseRequest.Execution.ConfigDigest, baseRequest.Evidence.PhysicalWorkStatement, baseRequest.Evidence.ReuseStatement)
	}
	// Reuse is evaluated against the hypothetical retained-snapshot execution;
	// a rejected decision is reconciled to refresh_sources before persistence.
	baseExecutionInputs := baseRequest.Execution
	baseExecutionInputs.ConfigDigest = candidateDataConfigDigest(input.Candidate.TargetID, input.Candidate.Scope.Environment, release.GenerationDataReuseBase, artifacts.Generation.DataRevision)
	baseExecution, err := baseExecutionInputs.ExecutionDigest()
	if err != nil {
		t.Fatal(err)
	}
	baseContext, err := baseRequest.Execution.ContextDigest()
	if err != nil {
		t.Fatal(err)
	}
	reuse := &deployment.DeliveryReuseInput{
		BaseExecutionDigest: baseExecution, BaseContextDigest: baseContext, CatalogDigest: deliveryPlanDigest('d'), BaseCatalogDigest: deliveryPlanDigest('d'),
		PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('e'), BaseCompatibilityDigest: deliveryPlanDigest('e'), Deterministic: true,
	}
	exact, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, baseRequest.CreatedAt, reuse)
	if err != nil {
		t.Fatal(err)
	}
	if len(exact.Evidence.Reuse) != 1 || !exact.Evidence.Reuse[0].Reusable || exact.Evidence.Reuse[0].ReuseKeyDigest == "" {
		t.Fatalf("exact active identity reuse = %#v", exact.Evidence.Reuse)
	}
	if exact.Execution.ConfigDigest != baseExecutionInputs.ConfigDigest || !strings.Contains(exact.Evidence.ReuseStatement, "reuses") {
		t.Fatalf("exact reuse execution/evidence = config:%q reuse:%q", exact.Execution.ConfigDigest, exact.Evidence.ReuseStatement)
	}
	missingContext := *reuse
	missingContext.BaseContextDigest = ""
	if _, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, baseRequest.CreatedAt, &missingContext); err == nil || !strings.Contains(err.Error(), "execution context identity is incomplete") {
		t.Fatalf("missing active context identity error = %v, want fail-closed rejection", err)
	}
	changed := *reuse
	changed.BaseCatalogDigest = deliveryPlanDigest('f')
	mismatch, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, baseRequest.CreatedAt, &changed)
	if err != nil {
		t.Fatal(err)
	}
	if len(mismatch.Evidence.Reuse) != 1 || mismatch.Evidence.Reuse[0].Reusable {
		t.Fatalf("mismatching active identity unexpectedly reused: %#v", mismatch.Evidence.Reuse)
	}
	if mismatch.Execution.ConfigDigest != refreshConfig || !strings.Contains(mismatch.Evidence.PhysicalWorkStatement, "refreshes compiled project relations") || !strings.Contains(mismatch.Evidence.ReuseStatement, "does not reuse") {
		t.Fatalf("mismatching identity left stale execution/evidence: config=%q physical=%q reuse=%q", mismatch.Execution.ConfigDigest, mismatch.Evidence.PhysicalWorkStatement, mismatch.Evidence.ReuseStatement)
	}
	undeclared := artifacts
	undeclared.Generation.Deterministic = false
	nondeterministic, err := CandidatePlanRequestWithPolicyAndReuse(input, undeclared, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, baseRequest.CreatedAt, reuse)
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
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, artifacts, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, base.CreatedAt, &deployment.DeliveryReuseInput{BaseExecutionDigest: baseExecution, BaseContextDigest: baseContext, CatalogDigest: deliveryPlanDigest('d'), BaseCatalogDigest: deliveryPlanDigest('d'), PhysicalPoolID: "pool-1", BasePhysicalPoolID: "pool-1", CompatibilityDigest: deliveryPlanDigest('e'), BaseCompatibilityDigest: deliveryPlanDigest('e'), Deterministic: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(request.Evidence.Reuse) != 2 || request.Evidence.Reuse[0].ResourceID != "model_customers" || request.Evidence.Reuse[0].Reusable || !request.Evidence.Reuse[0].RetainBase || request.Evidence.Reuse[1].ResourceID != "model_orders" || !request.Evidence.Reuse[1].Reusable {
		t.Fatalf("relation reuse evidence = %#v", request.Evidence.Reuse)
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
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, changed, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, baseRequest.CreatedAt, &deployment.DeliveryReuseInput{
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
	request, err := CandidatePlanRequestWithPolicyAndReuse(input, changed, "runtime:v1", CandidateDeliveryPolicy{ApprovalPolicyRevision: CurrentApprovalPolicyRevision}, baseRequest.CreatedAt, &deployment.DeliveryReuseInput{
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
	pathLocation := &projectcontracts.PathSourceLocation{Value: &projectcontracts.CSVPathSourceLocation{
		PathSourceLocationBase: projectcontracts.PathSourceLocationBase{Type: "path", Path: "orders.csv", Format: "csv"},
		Format:                 "csv",
	}}
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
		Connections:          map[string]semanticmodel.Connection{"connection:warehouse": {Kind: "managed"}},
		Sources:              map[string]semanticmodel.Source{"source:orders": {Connection: "connection:warehouse", Format: "csv", Path: "orders.csv", PathLocation: pathLocation, EffectivePathLocation: pathLocation}},
		Models:               map[string]semanticmodel.Table{"model:orders": {Execution: semanticmodel.ExecutionDefinition{Source: "source:orders"}}},
		SemanticModels:       map[string]*semanticmodel.Model{"semantic:sales": {Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {Execution: semanticmodel.ExecutionDefinition{Source: "orders"}}}}},
		DashboardDefinitions: map[string]dashboarddefinition.Definition{"dashboard:sales": {ID: "dashboard:sales", Title: dashboardTitle, SemanticModel: "semantic:sales"}},
		DashboardSources:     map[string]projectmanifest.DashboardSource{"dashboard:sales": {Document: dashboarddocument.DashboardDocument{APIVersion: dashboarddocument.DashboardApiVersionLeapviewDevV1, Kind: dashboarddocument.DashboardResourceKindDashboard, Metadata: dashboarddocument.DashboardMetadata{ID: "dashboard:sales", Name: "sales_dashboard"}, Spec: dashboarddocument.DashboardSpec{SemanticModel: "semantic:sales"}}}},
		Access:               access,
	})
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}

func deliveryPlanDigest(char byte) string { return "sha256:" + strings.Repeat(string(char), 64) }
