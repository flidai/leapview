package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apigenclient "github.com/Yacobolo/toolbelt/apigen/runtime/client"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	"github.com/flidai/leapview/internal/platform/cliapi"
	projectcli "github.com/flidai/leapview/internal/project/cli"
	"github.com/flidai/leapview/internal/project/devloop"
)

func TestDeliveryLifecycleAdaptersUseGeneratedContracts(t *testing.T) {
	contracts := map[string]string{
		"createDeliveryPlan":         "/api/v1/projects/{project}/delivery",
		"buildDeliveryPlan":          "/api/v1/projects/{project}/delivery/plans/{plan}/build",
		"publishDeliveryCandidate":   "/api/v1/projects/{project}/delivery/candidates/{candidate}/publish",
		"rollbackDeliveryGeneration": "/api/v1/projects/{project}/delivery/generations/{generation}/rollback",
	}
	for operationID, path := range contracts {
		contract, ok := deploymentgen.GetAPIGenOperationContract(operationID)
		if !ok || contract.Path != path {
			t.Fatalf("generated contract %q = %#v, want path %q", operationID, contract, path)
		}
	}
	root := NewCommand(context.Background())
	for _, commandName := range []string{"plan", "build", "publish", "rollback"} {
		if command, _, err := root.Find([]string{commandName}); err != nil || command == root {
			t.Fatalf("canonical CLI command %q is not registered: command=%v err=%v", commandName, command, err)
		}
	}
	workflow, err := os.ReadFile(filepath.Join("..", "..", "..", ".github", "examples", "leapview-authoring.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), "build") || !strings.Contains(string(workflow), "publish") {
		t.Fatal("CI example does not invoke canonical build and publish operations")
	}
}

func TestDeliveryPlanResultPreservesReviewEvidence(t *testing.T) {
	impact := "one model changes and two dashboards are affected"
	physical := "reuse the admitted pool; qualify one materialization"
	reuse := "the unchanged source remains reusable"
	rollback := deploymentgen.DeliveryRollbackClassRollbackSafe
	plan := deliveryPlanResult(deploymentgen.DeliveryPlanPreviewResponse{
		Id: "plan_1", ProjectId: "finance", TargetId: "target_1", Environment: "prod",
		Operation: deploymentgen.DeliveryOperationKindCodeChange, SourceDigest: "sha256:source",
		SourceAttestationDigest: "sha256:attestation", PlanDigest: "sha256:plan",
		ExecutionDigest: "sha256:execution", ProvenanceDigest: "sha256:provenance",
		GovernanceDigest: "sha256:governance", EvidenceDigest: "sha256:evidence",
		Status: deploymentgen.DeliveryPlanStatusPlanned,
		Evidence: deploymentgen.DeliveryPlanEvidenceView{
			Digest: "sha256:evidence", CompatibilityBreaking: true, AddedCount: 1,
			RemovedCount: 2, DirectlyModifiedCount: 3, IndirectlyAffectedCount: 4,
			ReuseCount: 5, QualificationStepCount: 1, ImpactStatement: &impact,
			PhysicalWorkStatement: &physical, ReuseStatement: &reuse,
			RollbackClass: &rollback, QualificationPolicy: "required",
			PlannedInputs: []deploymentgen.DeliveryPlannedInputView{{
				Id: "orders", Mode: deploymentgen.DeliveryDataInputModePinned,
				Revision: stringPointer("rev-7"), Bound: stringPointer("2026-08-01"),
			}},
			QualificationSteps: []deploymentgen.DeliveryQualificationStepView{{
				Id: "duckdb", Kind: "runtime", Description: "run qualification",
				Required: true, Blocking: true,
			}},
			StalePolicy: deploymentgen.DeliveryStalePolicyView{
				Mode:        deploymentgen.DeliveryStalePolicyModeReject,
				Description: stringPointer("reject stale base"), AllowRetainedBase: false,
			},
			ReuseDecisions: []deploymentgen.DeliveryReuseDecisionView{{
				ResourceId: "orders", Reusable: true, Reason: "digest unchanged",
				ReuseKeyDigest: stringPointer("sha256:reuse"),
			}},
		},
	})
	if plan.SourceAttestationDigest != "sha256:attestation" || plan.Evidence.QualificationPolicy != "required" ||
		len(plan.Evidence.PlannedInputs) != 1 || plan.Evidence.PlannedInputs[0].Revision != "rev-7" ||
		len(plan.Evidence.QualificationSteps) != 1 || len(plan.Evidence.ReuseDecisions) != 1 ||
		plan.Evidence.RollbackClass != "rollback_safe" {
		t.Fatalf("plan evidence = %#v", plan)
	}
}

func TestDeliveryPlanInvocationsUseFreshOperationKeys(t *testing.T) {
	transport := &deliveryPlanSourceHandoffTransport{}
	operations := projectDeliveryPlanOperations{client: fixedTransportClient{transport: transport}}
	digest := "sha256:" + strings.Repeat("a", 64)
	options := projectcli.DeliveryPlanOptions{
		ProjectID: "project-1", TargetID: "target-1", Environment: "development",
		SourceDigest: digest, SourceAttestationDigest: digest,
	}
	if _, err := operations.Create(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if _, err := operations.Create(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if len(transport.planKeys) != 2 || transport.planKeys[0] == transport.planKeys[1] {
		t.Fatalf("plan operation keys = %#v, want a new plan identity per invocation", transport.planKeys)
	}
}

func stringPointer(value string) *string { return &value }

func TestDeliveryPlanRetainsSourceWhenCandidateIdentityIsIncomplete(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	snapshot, err := (devloop.FilesystemBuilder{ProjectPath: projectPath}).Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	attestation := "sha256:" + strings.Repeat("a", 64)
	transport := &deliveryPlanSourceHandoffTransport{retained: deploymentgen.CandidateSourceSnapshotResponse{
		ProjectId: snapshot.ProjectID.String(), SourceDigest: snapshot.Digest, SourceAttestationDigest: attestation,
		TargetId: "target-1", Environment: "development",
	}}
	result, err := (projectDeliveryPlanOperations{client: deliveryPlanSourceHandoffClient{transport: transport}}).Create(t.Context(), projectcli.DeliveryPlanOptions{
		ProjectPath: projectPath, CandidateID: "candidate-1", ProjectID: snapshot.ProjectID.String(), TargetID: "target-1", SourceDigest: snapshot.Digest,
		UploadConcurrency: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.SourceAttestationDigest != attestation || transport.retainCalls != 1 || transport.planCalls != 1 || transport.createCalls != 1 {
		t.Fatalf("result=%#v transport=%#v", result, transport)
	}
}

func TestDeliveryPlanResolvesThePlanAlreadyBuiltByDev(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	transport := &deliveryPlanSourceHandoffTransport{
		candidate: deploymentgen.DeliveryCandidateStatusResponse{
			Id: "candidate-1", PlanId: "plan-candidate-1", PlanDigest: digest,
			ProjectId: "project-1", TargetId: "target-1", Environment: "development",
			SourceDigest: digest, Status: deploymentgen.DeliveryCandidateStatusReady,
		},
		preview: deploymentgen.DeliveryPlanPreviewResponse{
			Id: "plan-candidate-1", PlanDigest: digest, ProjectId: "project-1",
			TargetId: "target-1", Environment: "development", SourceDigest: digest,
			Status: deploymentgen.DeliveryPlanStatusPlanned,
		},
	}
	result, err := (projectDeliveryPlanOperations{
		client: deliveryPlanSourceHandoffClient{transport: transport},
	}).Create(t.Context(), projectcli.DeliveryPlanOptions{
		ProjectID: "project-1", CandidateID: "candidate-1",
		TargetID: "target-1", Environment: "development", SourceDigest: digest,
		ResolveCandidatePlan: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.PlanID != "plan-candidate-1" || result.PlanDigest != digest ||
		transport.candidateCalls != 1 || transport.previewCalls != 1 ||
		transport.createCalls != 0 || transport.retainCalls != 0 {
		t.Fatalf("result=%#v transport=%#v", result, transport)
	}
}

func TestDeliveryBuildRotatesPoisonedOperationKeyAndRetries(t *testing.T) {
	checkpoints := projectcli.NewCandidateCheckpointStore(filepath.Join(t.TempDir(), "authoring.json"))
	if err := checkpoints.SavePlan(projectcli.DeliveryPlanCheckpoint{
		PlanID: "plan-1", ProjectID: "project-1", TargetOrigin: "https://target.example",
	}); err != nil {
		t.Fatal(err)
	}
	transport := &deliveryBuildRetryTransport{}
	result, err := (projectDeliveryBuildOperations{
		client: fixedTransportClient{transport: transport}, checkpoints: checkpoints,
	}).Build(t.Context(), projectcli.DeliveryBuildOptions{PlanID: "plan-1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != "sealed" || result.CandidateID != "candidate-2" {
		t.Fatalf("result = %#v", result)
	}
	if len(transport.keys) != 2 || transport.keys[0] == transport.keys[1] {
		t.Fatalf("build idempotency keys = %#v, want one rotated retry", transport.keys)
	}
	replay, err := (projectDeliveryBuildOperations{
		client: fixedTransportClient{transport: transport}, checkpoints: checkpoints,
	}).Build(t.Context(), projectcli.DeliveryBuildOptions{PlanID: "plan-1"})
	if err != nil {
		t.Fatal(err)
	}
	if replay.BuildID != result.BuildID || len(transport.keys) != 3 || transport.keys[2] != transport.keys[1] {
		t.Fatalf("replay = %#v keys = %#v, want persisted sealed operation", replay, transport.keys)
	}
	checkpoint, err := checkpoints.LoadPlan("plan-1")
	if err != nil {
		t.Fatal(err)
	}
	if checkpoint.BuildIdempotencyKey != transport.keys[1] {
		t.Fatalf("checkpoint build key = %q, want %q", checkpoint.BuildIdempotencyKey, transport.keys[1])
	}
}

type deliveryBuildRetryTransport struct {
	keys []string
}

func (transport *deliveryBuildRetryTransport) DoAPIGen(_ context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	if request.OperationID != deploymentgen.GenOperationBuildDeliveryPlan {
		return apigenclient.Response{}, fmt.Errorf("unexpected operation %q", request.OperationID)
	}
	transport.keys = append(transport.keys, request.Headers.Get("Idempotency-Key"))
	if len(transport.keys) == 1 {
		return apigenclient.Response{}, generatedProblemErrorForTest(http.StatusConflict, "DELIVERY_IDEMPOTENCY_DRIFT")
	}
	candidateID, sealID := "candidate-2", "seal-2"
	response := deploymentgen.DeliveryBuildStatusResponse{
		Id: "attempt-2", PlanId: "plan-1", PlanDigest: "sha256:plan", SourceDigest: "sha256:source",
		ExecutionDigest: "sha256:execution", PhysicalPoolId: "pool-1", WriterLeaseId: "writer-2",
		CandidateId: &candidateID, SealId: &sealID, Status: deploymentgen.DeliveryBuildStatusSealed, Revision: 2,
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return apigenclient.Response{}, err
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return apigenclient.Response{}, err
	}
	return apigenclient.Response{StatusCode: http.StatusOK, Headers: http.Header{}, ContentType: "application/json"}, nil
}

func TestDeliveryPlanRejectsRetainedSourceIdentityMismatch(t *testing.T) {
	projectPath := filepath.Join("..", "..", "..", "dashboards", "leapview.yaml")
	snapshot, err := (devloop.FilesystemBuilder{ProjectPath: projectPath}).Build(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	const retainedTarget = "target-1"
	const retainedEnvironment = "development"
	const retainedAttestation = "sha256:" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tests := []struct {
		name     string
		options  projectcli.DeliveryPlanOptions
		retained deploymentgen.CandidateSourceSnapshotResponse
		wantPart string
	}{
		{name: "project", options: projectcli.DeliveryPlanOptions{ProjectID: "project:asserted", TargetID: retainedTarget, SourceDigest: snapshot.Digest}, retained: deploymentgen.CandidateSourceSnapshotResponse{ProjectId: snapshot.ProjectID.String(), SourceDigest: snapshot.Digest, SourceAttestationDigest: retainedAttestation, TargetId: retainedTarget, Environment: retainedEnvironment}, wantPart: "project"},
		{name: "target", options: projectcli.DeliveryPlanOptions{ProjectID: snapshot.ProjectID.String(), TargetID: "target-asserted", SourceDigest: snapshot.Digest}, retained: deploymentgen.CandidateSourceSnapshotResponse{ProjectId: snapshot.ProjectID.String(), SourceDigest: snapshot.Digest, SourceAttestationDigest: retainedAttestation, TargetId: retainedTarget, Environment: retainedEnvironment}, wantPart: "target"},
		{name: "source digest", options: projectcli.DeliveryPlanOptions{ProjectID: snapshot.ProjectID.String(), TargetID: retainedTarget, SourceDigest: snapshot.Digest}, retained: deploymentgen.CandidateSourceSnapshotResponse{ProjectId: snapshot.ProjectID.String(), SourceDigest: "sha256:" + strings.Repeat("b", 64), SourceAttestationDigest: retainedAttestation, TargetId: retainedTarget, Environment: retainedEnvironment}, wantPart: "source digest"},
		{name: "source attestation", options: projectcli.DeliveryPlanOptions{ProjectID: snapshot.ProjectID.String(), SourceAttestationDigest: "sha256:" + strings.Repeat("c", 64), SourceDigest: snapshot.Digest}, retained: deploymentgen.CandidateSourceSnapshotResponse{ProjectId: snapshot.ProjectID.String(), SourceDigest: snapshot.Digest, SourceAttestationDigest: retainedAttestation, TargetId: retainedTarget, Environment: retainedEnvironment}, wantPart: "source attestation"},
		{name: "environment", options: projectcli.DeliveryPlanOptions{ProjectID: snapshot.ProjectID.String(), SourceDigest: snapshot.Digest, Environment: "production"}, retained: deploymentgen.CandidateSourceSnapshotResponse{ProjectId: snapshot.ProjectID.String(), SourceDigest: snapshot.Digest, SourceAttestationDigest: retainedAttestation, TargetId: retainedTarget, Environment: retainedEnvironment}, wantPart: "environment"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.options.ProjectPath = projectPath
			tt.options.CandidateID = "candidate-1"
			tt.options.UploadConcurrency = 1
			transport := &deliveryPlanSourceHandoffTransport{retained: tt.retained}
			_, err := (projectDeliveryPlanOperations{client: deliveryPlanSourceHandoffClient{transport: transport}}).Create(t.Context(), tt.options)
			if err == nil || !strings.Contains(err.Error(), tt.wantPart) {
				t.Fatalf("error=%v, want retained %s mismatch", err, tt.wantPart)
			}
			if transport.createCalls != 0 {
				t.Fatal("created a delivery plan after retained identity mismatch")
			}
		})
	}
}

type deliveryPlanSourceHandoffClient struct {
	transport *deliveryPlanSourceHandoffTransport
}

func (client deliveryPlanSourceHandoffClient) Resolve(context.Context, cliapi.Credentials) (cliapi.Credentials, error) {
	return cliapi.Credentials{Target: "https://target.example", Token: "token"}, nil
}

func (client deliveryPlanSourceHandoffClient) Environment(context.Context, cliapi.Credentials, string) (string, error) {
	return "development", nil
}

func (client deliveryPlanSourceHandoffClient) Transport(context.Context, cliapi.Credentials) (apigenclient.Transport, error) {
	return client.transport, nil
}

type deliveryPlanSourceHandoffTransport struct {
	retained       deploymentgen.CandidateSourceSnapshotResponse
	candidate      deploymentgen.DeliveryCandidateStatusResponse
	preview        deploymentgen.DeliveryPlanPreviewResponse
	planCalls      int
	retainCalls    int
	createCalls    int
	candidateCalls int
	previewCalls   int
	planKeys       []string
}

func (transport *deliveryPlanSourceHandoffTransport) DoAPIGen(_ context.Context, request apigenclient.Request, out any) (apigenclient.Response, error) {
	var response any
	status := http.StatusOK
	switch request.OperationID {
	case deploymentgen.GenOperationPlanProjectCandidateSynchronization:
		transport.planCalls++
		body := request.Body.(deploymentgen.CandidateSynchronizationRequest)
		response = deploymentgen.CandidateSynchronizationPlanResponse{PlanId: "plan-delivery", ArtifactDigest: body.ArtifactDigest}
	case deploymentgen.GenOperationRetainProjectCandidateSource:
		transport.retainCalls++
		status = http.StatusCreated
		response = transport.retained
	case deploymentgen.GenOperationCreateDeliveryPlan:
		transport.createCalls++
		transport.planKeys = append(transport.planKeys, request.Headers.Get("Idempotency-Key"))
		body := request.Body.(deploymentgen.DeliveryPlanRequest)
		response = deploymentgen.DeliveryPlanPreviewResponse{Id: "plan-1", ProjectId: request.PathParams["project"], TargetId: body.TargetId, Environment: "development", SourceDigest: body.SourceDigest, SourceAttestationDigest: body.SourceAttestationDigest, Status: deploymentgen.DeliveryPlanStatusPlanned}
	case deploymentgen.GenOperationGetDeliveryCandidateStatus:
		transport.candidateCalls++
		response = transport.candidate
	case deploymentgen.GenOperationGetDeliveryPlanPreview:
		transport.previewCalls++
		response = transport.preview
	default:
		return apigenclient.Response{}, fmt.Errorf("unexpected operation %q", request.OperationID)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		return apigenclient.Response{}, err
	}
	if err := json.Unmarshal(encoded, out); err != nil {
		return apigenclient.Response{}, err
	}
	return apigenclient.Response{StatusCode: status, Headers: make(http.Header), ContentType: "application/json"}, nil
}
