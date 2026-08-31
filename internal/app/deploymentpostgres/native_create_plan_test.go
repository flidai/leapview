package deploymentpostgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	projectbundle "github.com/flidai/leapview/internal/project/bundle"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/google/uuid"
)

func createPlanTestDigest(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }

func TestNativeCreatePlanBuildFailsClosed(t *testing.T) {
	_, err := (&NativeCreatePlanCoordinator{}).BuildPlan(context.Background(), deploymentmodule.NativeDeliveryBuildRequest{})
	if !errors.Is(err, deploymentmodule.ErrDeliveryInputUnavailable) {
		t.Fatalf("BuildPlan error = %v, want input unavailable", err)
	}
}

func TestSameNativeJSONPreservesLargeIntegerIdentity(t *testing.T) {
	if !sameNativeJSON([]byte(`{"nested":{"snapshot":42,"attempt":"a1"},"version":1}`), []byte(`{"version":1,"nested":{"attempt":"a1","snapshot":42}}`)) {
		t.Fatal("semantically identical native JSON compared unequal")
	}
	if sameNativeJSON([]byte(`{"snapshot":9007199254740992}`), []byte(`{"snapshot":9007199254740993}`)) {
		t.Fatal("native JSON comparison rounded distinct int64 values")
	}
	if sameNativeJSON([]byte(`{"snapshot":42} trailing`), []byte(`{"snapshot":42}`)) {
		t.Fatal("native JSON comparison accepted trailing input")
	}
}

func TestNativeCreatePlanOperationDispositionRequiresUUIDv7AndExactLease(t *testing.T) {
	op := "0198f2c0-7c7a-7f00-8a11-000000000101"
	owner := "0198f2c0-7c7a-7f00-8a11-000000000100"
	input := deploymentmodule.NativeOperationAcquireInput{Scope: "target", OperationType: nativePlanOperationType, IdempotencyKey: "key", RequestDigest: createPlanTestDigest('a'), OwnerID: owner}
	acquired := deploymentmodule.NativeOperationAcquireResult{
		Status:    deploymentmodule.NativeOperationAcquired,
		Operation: deploymentmodule.NativeOperationRecord{Scope: input.Scope, OperationType: input.OperationType, IdempotencyKey: input.IdempotencyKey, RequestDigest: input.RequestDigest, OwnerID: input.OwnerID, OperationID: op},
		Lease:     deploymentmodule.NativeOperationLease{Scope: input.Scope, IdempotencyKey: input.IdempotencyKey, OperationID: op, OwnerID: input.OwnerID, FencingGeneration: 1, LeaseExpiresAt: time.Now().UTC().Add(time.Minute)},
	}
	if replay, err := nativePlanOperationDisposition(acquired, input); err != nil || replay {
		t.Fatalf("acquired disposition = replay %v err %v", replay, err)
	}
	acquired.Operation.OperationID = "0198f2c0-7c7a-8f00-8a11-000000000101"
	if _, err := nativePlanOperationDisposition(acquired, input); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("non-v7 operation error = %v", err)
	}
}

func TestDecodeNativePlanOutcomeStrictlyBindsRequest(t *testing.T) {
	op := "0198f2c0-7c7a-7f00-8a11-000000000102"
	event, err := nativePlanConsequenceID(op, "event")
	if err != nil {
		t.Fatal(err)
	}
	audit, err := nativePlanConsequenceID(op, "audit")
	if err != nil {
		t.Fatal(err)
	}
	input := deploymentmodule.NativeOperationAcquireInput{Scope: "target", OperationType: nativePlanOperationType, IdempotencyKey: "key", RequestDigest: createPlanTestDigest('a'), OwnerID: "actor"}
	raw, err := json.Marshal(nativePlanOutcome{OperationID: op, PlanID: op, EventID: event, AuditID: audit, ProjectID: "project", TargetID: input.Scope, SourceDigest: createPlanTestDigest('b'), SourceAttestationDigest: createPlanTestDigest('c'), Status: "accepted", PlanDigest: createPlanTestDigest('d')})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativePlanOutcome(raw, input); err != nil {
		t.Fatalf("valid outcome rejected: %v", err)
	}
	var tampered nativePlanOutcome
	if err := json.Unmarshal(raw, &tampered); err != nil {
		t.Fatal(err)
	}
	tampered.PlanID = "0198f2c0-7c7a-7f00-8a11-000000000105"
	tamperedRaw, _ := json.Marshal(tampered)
	if _, err := decodeNativePlanOutcome(tamperedRaw, input); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("plan identity drift accepted: %v", err)
	}
}

func TestRichPlanFromRequestCarriesActorSourceOwnerAndBaseFence(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	d := createPlanTestDigest
	request := deployment.DeliveryPlanRequest{
		ActorID: "reviewer", TargetID: "target", ProjectID: "project", Environment: "prod", Operation: deployment.DeliveryOperationCodeChange, SourceDigest: d('a'), ServingArtifactDigest: d('6'), CreatedAt: now,
		Provenance: deployment.DeliveryProvenance{Builder: "test", AttestationDigest: d('b')},
		Execution:  deployment.DeliveryExecutionInputs{SourceArtifactDigest: d('a'), CompilerDigest: d('c'), ExecutableDigest: d('d'), DependencyDigest: d('e'), ConfigDigest: d('f'), BindingDigest: d('0'), RuntimeDigest: d('1'), CapabilityDigest: d('2')},
		Governance: deployment.DeliveryGovernance{PolicyDigest: d('3'), AuthorizationDigest: d('4'), QualificationDigest: d('5'), ExpiresAt: now.Add(time.Hour)},
		Evidence:   deployment.DeliveryPlanEvidence{ImpactStatement: "impact", PhysicalWorkStatement: "work", ReuseStatement: "reuse", Qualification: deployment.DeliveryQualificationEvidence{Policy: "policy", Steps: []deployment.DeliveryQualificationStep{{ID: "schema", Kind: "contract", Description: "schema", Required: true, Blocking: true}}}, StalePolicy: deployment.DeliveryStalePolicy{Mode: "reject"}, Rollback: deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryServingSafe}},
	}
	plan, err := richPlanFromRequest(request, "author", "0198f2c0-7c7a-7f00-8a11-000000000106", "0198f2c0-7c7a-7f00-8a11-000000000107", 9)
	if err != nil {
		t.Fatal(err)
	}
	if plan.ActorID != "reviewer" || plan.SourceOwnerID != "author" || plan.ServingArtifactDigest != d('6') || plan.BaseGenerationID == "" || plan.BaseTargetRevision != 9 || plan.Status != deployment.DeliveryPlanPlanned {
		t.Fatalf("rich plan omitted immutable actor/source/base evidence: %#v", plan)
	}
	if _, err := uuid.Parse(plan.ID); err != nil || projectgraph.ResourceID(plan.ProjectID) != "project" {
		t.Fatalf("rich plan identity = %#v", plan)
	}
	otherOwner, err := richPlanFromRequest(request, "different-author", "0198f2c0-7c7a-7f00-8a11-000000000106", "0198f2c0-7c7a-7f00-8a11-000000000107", 9)
	if err != nil {
		t.Fatal(err)
	}
	if otherOwner.Digest == plan.Digest {
		t.Fatal("source owner namespace was omitted from canonical plan identity")
	}
}

func TestValidateNativePlanInspectionRejectsCrossProjectEvidence(t *testing.T) {
	request := nativePlanRequest()
	source, inspected := nativePlanPostgresFixture(t, request.SourceDigest, request.SourceAttestationDigest)
	requestDigest, err := nativePlanRequestDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	inspectID := "inspect-" + strings.TrimPrefix(requestDigest, "sha256:")
	digest := sha256.Sum256([]byte(inspectID))
	inspected.Generation.Identity = projectgraph.ServingIdentity{ProjectID: request.ProjectID, Environment: request.Environment, GenerationID: "inspect-" + hex.EncodeToString(digest[:])}
	manifest, servingDigest, err := projectbundle.PackCompiledProject(inspected.Compiler.Artifact, inspected.Compiler.Plan, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	inspected.Artifact.ContentDigest = servingDigest
	inspected.Generation.ArtifactDigest = servingDigest
	inspected.Generation.ServingArtifactID = nativePlannedServingArtifactID(servingDigest)
	inspected.Generation.BundleManifestJSON = string(manifestJSON)
	if err := validateNativePlanInspection(request, source, inspected, inspectID); err != nil {
		t.Fatalf("valid inspection rejected: %v", err)
	}
	tamperedServing := inspected
	tamperedServing.Generation.ArtifactDigest = createPlanTestDigest('e')
	if err := validateNativePlanInspection(request, source, tamperedServing, inspectID); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("tampered serving digest accepted: %v", err)
	}
	tamperedManifest := inspected
	tamperedManifest.Generation.BundleManifestJSON = "{}"
	if err := validateNativePlanInspection(request, source, tamperedManifest, inspectID); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("tampered serving manifest accepted: %v", err)
	}
	tamperedCompiler := inspected
	tamperedCompiler.Compiler.Plan.Deterministic = !tamperedCompiler.Compiler.Plan.Deterministic
	if err := validateNativePlanInspection(request, source, tamperedCompiler, inspectID); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("tampered compiler evidence accepted: %v", err)
	}
	inspected.Artifact.ProjectDigest = createPlanTestDigest('f')
	if err := validateNativePlanInspection(request, source, inspected, inspectID); !errors.Is(err, deployment.ErrDeliveryConflict) {
		t.Fatalf("cross-project inspection error = %v, want conflict", err)
	}
}
