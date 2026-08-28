package module

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	apigenruntime "github.com/flidai/leapview/internal/app/api/apigenruntime"
	"github.com/flidai/leapview/internal/deployment"
	deploymentapi "github.com/flidai/leapview/internal/deployment/api"
	deploymentgen "github.com/flidai/leapview/internal/deployment/api/gen"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	"github.com/flidai/leapview/internal/deployment/sealedcontrol"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

func TestDeliveryMutationErrorsUseTypedPublicContracts(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "forbidden", err: ErrDeliveryForbidden, status: http.StatusForbidden, code: "DELIVERY_FORBIDDEN"},
		{name: "input unavailable", err: ErrDeliveryInputUnavailable, status: http.StatusServiceUnavailable, code: "DELIVERY_INPUT_UNAVAILABLE"},
		{name: "idempotency drift", err: ErrDeliveryIdempotencyDrift, status: http.StatusConflict, code: "DELIVERY_IDEMPOTENCY_DRIFT"},
		{name: "approval required", err: errors.Join(ErrDeliveryApprovalRequired, errors.New("policy")), status: http.StatusConflict, code: "DELIVERY_APPROVAL_REQUIRED"},
		{name: "approval expired", err: deployment.ErrApprovalExpired, status: http.StatusConflict, code: "DELIVERY_PLAN_EXPIRED"},
		{name: "plan expired", err: deployment.ErrDeliveryPlanExpired, status: http.StatusConflict, code: "DELIVERY_PLAN_EXPIRED"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			m := &Module{}
			m.writeDeliveryMutationError(recorder, httptest.NewRequest(http.MethodPost, "/", nil), test.err)
			if recorder.Code != test.status || !strings.Contains(recorder.Body.String(), test.code) {
				t.Fatalf("response = %d %s, want %d %s", recorder.Code, recorder.Body.String(), test.status, test.code)
			}
		})
	}
}

type deliveryReadFixture struct {
	candidate        deployment.DeliveryCandidate
	publication      deployment.DeliveryPublication
	operatorSnapshot deployment.DeliveryOperatorSnapshot
	operatorErr      error
}

func (f deliveryReadFixture) PlanByID(context.Context, string) (deployment.DeliveryPlan, error) {
	return deployment.DeliveryPlan{}, sql.ErrNoRows
}
func (f deliveryReadFixture) DeliveryBuildAttemptByID(context.Context, string) (deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryBuildAttempt{}, sql.ErrNoRows
}
func (f deliveryReadFixture) DeliveryCatalogSealByID(context.Context, string) (deployment.CatalogSeal, error) {
	return deployment.CatalogSeal{}, sql.ErrNoRows
}
func (f deliveryReadFixture) DeliveryCandidateByID(context.Context, string) (deployment.DeliveryCandidate, error) {
	return f.candidate, nil
}
func (f deliveryReadFixture) DeliveryGenerationByID(context.Context, string) (deployment.DeliveryGeneration, error) {
	return deployment.DeliveryGeneration{}, sql.ErrNoRows
}
func (f deliveryReadFixture) DeliveryPublicationByID(context.Context, string) (deployment.DeliveryPublication, error) {
	if f.publication.ID == "" {
		return deployment.DeliveryPublication{}, sql.ErrNoRows
	}
	return f.publication, nil
}
func (f deliveryReadFixture) DeliveryOperatorSnapshot(context.Context, string, string) (deployment.DeliveryOperatorSnapshot, error) {
	if f.operatorErr != nil {
		return deployment.DeliveryOperatorSnapshot{}, f.operatorErr
	}
	if f.operatorSnapshot.ProjectID != "" {
		return f.operatorSnapshot, nil
	}
	return deployment.DeliveryOperatorSnapshot{}, sql.ErrNoRows
}

type boundedDeliveryReadDiagnostic struct {
	err error
}

func (e boundedDeliveryReadDiagnostic) Error() string { return e.err.Error() }
func (e boundedDeliveryReadDiagnostic) Unwrap() error { return e.err }
func (boundedDeliveryReadDiagnostic) DeliveryReadStage() string {
	return "physical_pool_admission_timestamp"
}
func (boundedDeliveryReadDiagnostic) DeliveryReadCategory() string { return "timestamp_parse" }

func deliveryTestModule(reader deployment.DeliveryReader, principal bool) *Module {
	return &Module{
		deliveryReader: reader,
		handler: deploymenthttp.NewHandler(deploymenthttp.Options{
			InstanceEnvironment: "prod",
			CurrentPrincipal: func(*http.Request) (deploymenthttp.Principal, bool) {
				return deploymenthttp.Principal{ID: "operator"}, principal
			},
		}),
	}
}

func TestDeliveryReadRequiresPrincipal(t *testing.T) {
	projectID, _ := projectgraph.NewResourceID("finance")
	m := deliveryTestModule(deliveryReadFixture{candidate: deployment.DeliveryCandidate{ProjectID: projectID}}, false)
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", "candidate-1")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
}

func TestDeliveryReadReturnsTypedUnavailableFailure(t *testing.T) {
	m := deliveryTestModule(nil, true)
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", "candidate-1")
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "DELIVERY_READ_UNAVAILABLE") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
}

func TestDeliveryReadUnavailableLogsOnlyBoundedDiagnostic(t *testing.T) {
	const sensitiveCause = "parse /var/lib/leapview/private.db value tenant-secret"
	var logs bytes.Buffer
	m := deliveryTestModule(deliveryReadFixture{operatorErr: boundedDeliveryReadDiagnostic{err: errors.New(sensitiveCause)}}, true)
	m.logger = slog.New(slog.NewTextHandler(&logs, nil))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/v1/projects/finance/delivery/operator", nil)
	m.GetDeliveryOperatorSnapshot(recorder, request, "finance")

	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "DELIVERY_READ_UNAVAILABLE") {
		t.Fatalf("response = %d %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), sensitiveCause) || strings.Contains(logs.String(), sensitiveCause) {
		t.Fatalf("delivery read leaked raw cause: body=%q logs=%q", recorder.Body.String(), logs.String())
	}
	for _, expected := range []string{
		"canonical delivery read failed",
		"stage=physical_pool_admission_timestamp",
		"category=timestamp_parse",
	} {
		if !strings.Contains(logs.String(), expected) {
			t.Fatalf("diagnostic log = %q, missing %q", logs.String(), expected)
		}
	}
}

func TestDeliveryCandidateStatusRedactsObjectAuthorityAndInputs(t *testing.T) {
	projectID, _ := projectgraph.NewResourceID("finance")
	fixture := deliveryReadFixture{candidate: deployment.DeliveryCandidate{
		ID: "candidate-1", PlanID: "plan-1", PlanDigest: "sha256:" + strings.Repeat("a", 64), TargetID: "target-1", ProjectID: projectID, Environment: "prod",
		SourceDigest: "sha256:" + strings.Repeat("b", 64), ExecutionDigest: "sha256:" + strings.Repeat("c", 64), SealID: "seal-1", CatalogDigest: "sha256:" + strings.Repeat("d", 64), CompatibilityDigest: "sha256:" + strings.Repeat("e", 64),
		CatalogObjectKey: "private/credentials.parquet", PhysicalPoolID: "sha256:" + strings.Repeat("f", 64), ServingArtifactID: "artifact-1", ServingArtifactDigest: "sha256:" + strings.Repeat("1", 64), ServingStateID: "state-1", Status: deployment.DeliveryCandidateReady,
		ResolvedInputs: deployment.DeliveryResolvedBuildInputs{EvidenceDigest: "sha256:" + strings.Repeat("2", 64)}, CreatedAt: time.Now().UTC(),
	}}
	m := deliveryTestModule(fixture, true)
	recorder := httptest.NewRecorder()
	m.GetDeliveryCandidateStatus(recorder, httptest.NewRequest(http.MethodGet, "/", nil), "finance", "candidate-1")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	encoded := string(recorder.Body.Bytes())
	if strings.Contains(encoded, "credentials.parquet") || strings.Contains(encoded, "rawObservedValue") {
		t.Fatalf("response leaked object authority or raw inputs: %s", encoded)
	}
	if body["servingStateId"] != "state-1" {
		t.Fatalf("servingStateId = %v", body["servingStateId"])
	}
}

func TestDeliveryPlanPreviewExposesImmutableReviewEvidence(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	plan := deployment.DeliveryPlan{
		ID: "plan-1", ProjectID: projectID, TargetID: "target-1", Environment: "prod",
		Operation: deployment.DeliveryOperationCodeChange, SourceDigest: "sha256:" + strings.Repeat("a", 64),
		ExecutionDigest: "sha256:" + strings.Repeat("b", 64), ProvenanceDigest: "sha256:" + strings.Repeat("c", 64),
		GovernanceDigest: "sha256:" + strings.Repeat("d", 64), EvidenceDigest: "sha256:" + strings.Repeat("e", 64),
		Digest: "sha256:" + strings.Repeat("f", 64), Status: deployment.DeliveryPlanPlanned,
		Provenance: deployment.DeliveryProvenance{AttestationDigest: "sha256:" + strings.Repeat("1", 64)},
		Execution:  deployment.DeliveryExecutionInputs{DataInputs: []deployment.DeliveryDataInput{{ID: "orders", Mode: deployment.DeliveryDataPinned, Revision: "rev-7"}}},
		Evidence: deployment.DeliveryPlanEvidence{
			ImpactStatement: "graph impact is bounded", PhysicalWorkStatement: "one qualification step", ReuseStatement: "unchanged nodes are reusable",
			GraphImpact:   deployment.DeliveryGraphImpact{DirectlyModified: []deployment.DeliveryImpactResource{{ID: "model-orders", Kind: "model", Change: "modified"}}},
			Compatibility: deployment.DeliveryCompatibilityImpact{Breaking: true},
			Qualification: deployment.DeliveryQualificationEvidence{Policy: "required", Steps: []deployment.DeliveryQualificationStep{{ID: "runtime", Kind: "runtime", Description: "check runtime", Required: true, Blocking: true}}},
			StalePolicy:   deployment.DeliveryStalePolicy{Mode: "reject"},
			Rollback:      deployment.DeliveryRollbackEvidence{Class: deployment.DeliveryRollbackSafe},
		},
		Governance: deployment.DeliveryGovernance{PolicyDigest: "sha256:" + strings.Repeat("2", 64), AuthorizationDigest: "sha256:" + strings.Repeat("3", 64), QualificationDigest: "sha256:" + strings.Repeat("4", 64), ExpiresAt: time.Now().UTC().Add(time.Hour)},
		CreatedAt:  time.Now().UTC(),
	}
	response := planPreviewResponse(plan)
	if response.SourceDigest != plan.SourceDigest || response.SourceAttestationDigest != plan.Provenance.AttestationDigest ||
		response.ExecutionDigest != plan.ExecutionDigest || response.ProvenanceDigest != plan.ProvenanceDigest || response.GovernanceDigest != plan.GovernanceDigest ||
		response.EvidenceDigest != plan.EvidenceDigest || response.PlanDigest != plan.Digest || response.TargetId != plan.TargetID || response.ProjectId != projectID.String() {
		t.Fatalf("plan identity response = %#v", response)
	}
	if response.Evidence.ImpactStatement == nil || *response.Evidence.ImpactStatement != plan.Evidence.ImpactStatement ||
		response.Evidence.QualificationPolicy != "required" || len(response.Evidence.PlannedInputs) != 1 ||
		response.Evidence.PlannedInputs[0].Revision == nil || *response.Evidence.PlannedInputs[0].Revision != "rev-7" ||
		len(response.Evidence.QualificationSteps) != 1 || response.Evidence.RollbackClass == nil || string(*response.Evidence.RollbackClass) != "rollback_safe" {
		t.Fatalf("plan review evidence = %#v", response.Evidence)
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "credentials") || strings.Contains(string(encoded), "rawObservedValue") {
		t.Fatalf("plan response leaked secret/object authority: %s", encoded)
	}
}

type canonicalApprovalRepository struct {
	approval deployment.Approval
}

type handlerPublicationStore struct {
	publication deployment.PublicationIntent
	requested   int
	activated   int
}

func (s *handlerPublicationStore) RequestPublication(_ context.Context, publication deployment.PublicationIntent, _ ...deployment.CatalogRoot) (deployment.PublicationIntent, error) {
	s.requested++
	if s.publication.ID != "" {
		return s.publication, nil
	}
	s.publication = publication
	return publication, nil
}

func (s *handlerPublicationStore) ActivatePublication(_ context.Context, id string, now time.Time) (deployment.PublicationIntent, error) {
	s.activated++
	if s.publication.ID != id {
		return deployment.PublicationIntent{}, deployment.ErrDeliveryConflict
	}
	s.publication.Status = deployment.DeliveryPublicationCommitted
	s.publication.ResultTargetRevision = s.publication.ExpectedTargetRevision + 1
	s.publication.CompletedAt = now.UTC()
	return s.publication, nil
}

type coordinatorDeliveryMutation struct {
	coordinator *sealedcontrol.Coordinator
	request     sealedcontrol.PublishRequest
}

func (m coordinatorDeliveryMutation) CreatePlan(context.Context, DeliveryPlanIntent, string) (deployment.DeliveryPlan, error) {
	return deployment.DeliveryPlan{}, deployment.ErrDeliveryInvalid
}
func (m coordinatorDeliveryMutation) BuildPlan(context.Context, string, string, string, string) (deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryBuildAttempt{}, deployment.ErrDeliveryInvalid
}
func (m coordinatorDeliveryMutation) PublishCandidate(ctx context.Context, _ string, _ string, principalID, _ string) (deployment.DeliveryPublication, error) {
	request := m.request
	request.ActorID = principalID
	return m.coordinator.Publish(ctx, request)
}
func (m coordinatorDeliveryMutation) RollbackGeneration(context.Context, string, string, string, string) (deployment.DeliveryPublication, error) {
	return deployment.DeliveryPublication{}, deployment.ErrDeliveryInvalid
}

func (r *canonicalApprovalRepository) CreateApproval(_ context.Context, approval deployment.Approval) (deployment.Approval, error) {
	if r.approval.ID != "" {
		return deployment.Approval{}, deployment.ErrApprovalConflict
	}
	r.approval = approval
	return approval, nil
}

func (r *canonicalApprovalRepository) ApprovalByDeployment(_ context.Context, deploymentID string) (deployment.Approval, error) {
	if r.approval.ID == "" || r.approval.DeploymentID != deploymentID {
		return deployment.Approval{}, deployment.ErrApprovalNotFound
	}
	return r.approval, nil
}

func (r *canonicalApprovalRepository) SaveApproval(_ context.Context, approval deployment.Approval, expectedRevision int64) (deployment.Approval, error) {
	if r.approval.ID == "" || r.approval.Revision != expectedRevision || r.approval.ID != approval.ID {
		return deployment.Approval{}, deployment.ErrApprovalConflict
	}
	r.approval = approval
	return approval, nil
}

func TestCanonicalPublicationApprovalEndpointsPreservePublicationAndReleaseScope(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	publication := deployment.DeliveryPublication{
		ID: "publication-1", ProjectID: projectID, Environment: "prod", TargetID: "target-1",
		RequestDigest: "request-digest", CandidateID: "candidate-1", GenerationID: "generation-1",
	}
	candidate := deployment.DeliveryCandidate{
		ID: "candidate-1", ProjectID: projectID, Environment: "prod", TargetID: "target-1",
		ServingArtifactID: "artifact-1", Status: deployment.DeliveryCandidateReady,
	}
	reader := &deliveryReadFixture{candidate: candidate, publication: publication}
	repository := &canonicalApprovalRepository{}
	approvals, err := deployment.NewApprovalService(repository, deployment.ApprovalServiceConfig{
		Now: nowFunc(now), NewID: func() (string, error) { return "approval-1", nil }, Lifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := deliveryTestModule(reader, true)
	m.approvals = approvals
	m.handler = deploymenthttp.NewHandler(deploymenthttp.Options{
		InstanceEnvironment: "prod",
		CurrentPrincipal: func(r *http.Request) (deploymenthttp.Principal, bool) {
			principal := r.Header.Get("X-Principal")
			if principal == "" {
				principal = "publisher"
			}
			return deploymenthttp.Principal{ID: principal}, true
		},
	})
	m.currentApprovalActor = func(r *http.Request) (deployment.ApprovalActor, bool) {
		principal := r.Header.Get("X-Principal")
		if principal == "" {
			principal = "publisher"
		}
		return deployment.ApprovalActor{PrincipalID: principal, CredentialClass: deployment.CredentialClassHuman, CredentialID: "cred-" + principal, CredentialExpiresAt: now.Add(time.Hour)}, true
	}

	request := httptest.NewRequest(http.MethodPost, "/", nil)
	request.Header.Set("X-Principal", "publisher")
	created := httptest.NewRecorder()
	m.RequestDeliveryPublicationApproval(created, request, "finance", publication.ID, "request-key")
	if created.Code != http.StatusCreated {
		t.Fatalf("request status = %d: %s", created.Code, created.Body.String())
	}
	var requested deploymentapi.ApprovalResponse
	if err := json.Unmarshal(created.Body.Bytes(), &requested); err != nil {
		t.Fatal(err)
	}
	if requested.ID != "approval-1" || requested.DeploymentID != publication.ID || requested.ReleaseID != candidate.ServingArtifactID || requested.Status != string(deployment.ApprovalPending) {
		t.Fatalf("requested approval = %#v", requested)
	}

	approve := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"expectedRevision":1}`))
	approve.Header.Set("X-Principal", "reviewer")
	approved := httptest.NewRecorder()
	m.ApproveDeliveryPublicationApproval(approved, approve, "finance", publication.ID, requested.ID, "approve-key")
	if approved.Code != http.StatusOK || !strings.Contains(approved.Body.String(), `"status":"approved"`) {
		t.Fatalf("approve response = %d %s", approved.Code, approved.Body.String())
	}

	get := httptest.NewRecorder()
	m.GetDeliveryPublicationApproval(get, httptest.NewRequest(http.MethodGet, "/", nil), "finance", publication.ID, requested.ID)
	if get.Code != http.StatusOK || !strings.Contains(get.Body.String(), `"releaseId":"artifact-1"`) {
		t.Fatalf("get response = %d %s", get.Code, get.Body.String())
	}

	// Carrying the approval forward to a different candidate artifact is
	// rejected before the transition, even though the approval row itself is
	// otherwise valid.
	reader.candidate.ServingArtifactID = "artifact-2"
	denied := httptest.NewRecorder()
	m.DenyDeliveryPublicationApproval(denied, httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"expectedRevision":2}`)), "finance", publication.ID, requested.ID, "deny-key")
	if denied.Code != http.StatusNotFound {
		t.Fatalf("scope drift status = %d, want 404: %s", denied.Code, denied.Body.String())
	}
}

func TestCoordinatorApprovalCauseReachesPublishHandlerAndRetry(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	digest := func(ch byte) string { return "sha256:" + strings.Repeat(string(ch), 64) }
	seal := deployment.VerifiedSeal{
		SealID: "seal-handler", CatalogDigest: digest('a'), CatalogObjectKey: "catalogs/a.ducklake", ObjectSize: 1,
		PhysicalPoolID: "pool-1", CompatibilityDigest: digest('b'), ClosureDigest: digest('c'), QualificationDigest: digest('d'),
		ServingArtifactID: "artifact-handler", ServingArtifactDigest: digest('e'),
	}
	generation, err := deployment.NewCatalogRoot(deployment.CatalogRoot{
		ID: "generation-handler", CandidateID: "candidate-handler", PlanID: "plan-handler", PlanDigest: digest('f'),
		TargetID: "target-1", ProjectID: projectID, Environment: "prod", CatalogDigest: seal.CatalogDigest, CatalogObjectKey: seal.CatalogObjectKey,
		PhysicalPoolID: seal.PhysicalPoolID, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest,
		ServingStateID: "state-handler", CompatibilityDigest: seal.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackSafe, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	publication, err := deployment.NewPublicationIntent(deployment.PublicationIntent{
		ID: "publication-handler", RequestDigest: digest('1'), TargetID: "target-1", ProjectID: projectID, Environment: "prod",
		PlanID: generation.PlanID, PlanDigest: generation.PlanDigest, CandidateID: generation.CandidateID, GenerationID: generation.ID,
		ExpectedTargetRevision: 0, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := deployment.DeliveryCandidate{ID: generation.CandidateID, ProjectID: projectID, Environment: "prod", TargetID: "target-1", PlanID: generation.PlanID, PlanDigest: generation.PlanDigest, ServingArtifactID: seal.ServingArtifactID, Status: deployment.DeliveryCandidateReady}
	reader := &deliveryReadFixture{candidate: candidate, publication: publication}
	approvalRepository := &canonicalApprovalRepository{}
	approvals, err := deployment.NewApprovalService(approvalRepository, deployment.ApprovalServiceConfig{Now: nowFunc(now), Lifetime: time.Hour, NewID: func() (string, error) { return "approval-handler", nil }})
	if err != nil {
		t.Fatal(err)
	}
	store := &handlerPublicationStore{}
	coordinator := &sealedcontrol.Coordinator{
		Publications:     store,
		VerifySeal:       func(context.Context, sealedcontrol.SealBinding) error { return nil },
		Authorize:        func(context.Context, sealedcontrol.SealBinding) error { return nil },
		ApprovalVerifier: sealedcontrol.DurableApprovalVerifier(approvals),
		Now:              nowFunc(now),
	}
	m := deliveryTestModule(reader, true)
	m.approvals = approvals
	m.deliveryMutations = coordinatorDeliveryMutation{coordinator: coordinator, request: sealedcontrol.PublishRequest{Publication: publication, Generation: generation, Seal: seal, ApprovalReleaseID: candidate.ServingArtifactID}}
	m.handler = deploymenthttp.NewHandler(deploymenthttp.Options{
		InstanceEnvironment: "prod",
		CurrentPrincipal: func(r *http.Request) (deploymenthttp.Principal, bool) {
			principal := r.Header.Get("X-Principal")
			if principal == "" {
				principal = "publisher"
			}
			return deploymenthttp.Principal{ID: principal}, true
		},
	})
	m.currentApprovalActor = func(r *http.Request) (deployment.ApprovalActor, bool) {
		principal := r.Header.Get("X-Principal")
		if principal == "" {
			principal = "publisher"
		}
		return deployment.ApprovalActor{PrincipalID: principal, CredentialClass: deployment.CredentialClassHuman, CredentialID: "cred-" + principal, CredentialExpiresAt: now.Add(time.Hour)}, true
	}
	first := httptest.NewRecorder()
	firstRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	firstRequest.Header.Set("X-Principal", "publisher")
	m.PublishDeliveryCandidate(first, firstRequest, "finance", candidate.ID, "publish-key")
	if first.Code != http.StatusAccepted || store.requested != 1 || store.activated != 0 {
		t.Fatalf("protected first publish = %d %s requested=%d activated=%d", first.Code, first.Body.String(), store.requested, store.activated)
	}
	if !strings.Contains(first.Body.String(), `"status":"pending"`) || approvalRepository.approval.RequestedBy != "publisher" {
		t.Fatalf("first publish did not persist pending approval identity: %s approval=%#v", first.Body.String(), approvalRepository.approval)
	}

	// The publisher cannot approve its own publication; an independent
	// reviewer transitions the durable approval before the retry.
	selfApprove := httptest.NewRecorder()
	selfRequest := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"expectedRevision":1}`))
	selfRequest.Header.Set("X-Principal", "publisher")
	m.ApproveDeliveryPublicationApproval(selfApprove, selfRequest, "finance", publication.ID, approvalRepository.approval.ID, "approve-self")
	if selfApprove.Code != http.StatusConflict {
		t.Fatalf("self approval status = %d, want 409: %s", selfApprove.Code, selfApprove.Body.String())
	}
	reviewer := httptest.NewRecorder()
	reviewerRequest := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{"expectedRevision":1}`))
	reviewerRequest.Header.Set("X-Principal", "reviewer")
	m.ApproveDeliveryPublicationApproval(reviewer, reviewerRequest, "finance", publication.ID, approvalRepository.approval.ID, "approve-reviewer")
	if reviewer.Code != http.StatusOK || approvalRepository.approval.ApprovedBy != "reviewer" {
		t.Fatalf("reviewer approval = %d %s approval=%#v", reviewer.Code, reviewer.Body.String(), approvalRepository.approval)
	}

	retry := httptest.NewRecorder()
	retryRequest := httptest.NewRequest(http.MethodPost, "/", nil)
	retryRequest.Header.Set("X-Principal", "publisher")
	m.PublishDeliveryCandidate(retry, retryRequest, "finance", candidate.ID, "publish-key")
	if retry.Code != http.StatusAccepted || !strings.Contains(retry.Body.String(), `"status":"committed"`) || store.activated != 1 {
		t.Fatalf("protected retry = %d %s activated=%d", retry.Code, retry.Body.String(), store.activated)
	}
}

func nowFunc(now time.Time) func() time.Time { return func() time.Time { return now } }

type generatedDeliveryMutation struct{ plan deployment.DeliveryPlan }

func (m generatedDeliveryMutation) CreatePlan(context.Context, DeliveryPlanIntent, string) (deployment.DeliveryPlan, error) {
	return m.plan, nil
}
func (generatedDeliveryMutation) BuildPlan(context.Context, string, string, string, string) (deployment.DeliveryBuildAttempt, error) {
	return deployment.DeliveryBuildAttempt{}, deployment.ErrDeliveryInvalid
}
func (generatedDeliveryMutation) PublishCandidate(context.Context, string, string, string, string) (deployment.DeliveryPublication, error) {
	return deployment.DeliveryPublication{}, deployment.ErrDeliveryInvalid
}
func (generatedDeliveryMutation) RollbackGeneration(context.Context, string, string, string, string) (deployment.DeliveryPublication, error) {
	return deployment.DeliveryPublication{}, deployment.ErrDeliveryInvalid
}

type generatedDeliveryAuthorizer struct{}

func (generatedDeliveryAuthorizer) Protect(_ string, next http.Handler) (http.Handler, bool) {
	return next, true
}

func TestGeneratedDeliveryRouterFlushesSuccessfulPlanAfterEvidenceCompletion(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	planDigest := "sha256:" + strings.Repeat("a", 64)
	plan := deployment.DeliveryPlan{ID: "plan-command", ProjectID: projectID, TargetID: "target-command", Environment: "prod", Digest: planDigest}
	reader := &deliveryEvidenceFixture{byRequest: map[string]deployment.DeliveryEvent{}, byObject: map[string][]deployment.DeliveryEvent{}}
	reader.byRequest[strings.Join([]string{plan.TargetID, plan.Digest, "plan_created", "plan", plan.ID}, "\x00")] = deployment.DeliveryEvent{TargetID: plan.TargetID, RequestDigest: plan.Digest, EventKind: "plan_created", ObjectKind: "plan", ObjectID: plan.ID, Outcome: "accepted"}
	m := deliveryTestModule(reader, true)
	m.deliveryMutations = generatedDeliveryMutation{plan: plan}

	dispatch := func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		if operationID != deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID() {
			return false
		}
		m.CreateDeliveryPlan(w, r, "finance", "plan-command-key")
		return true
	}
	handler, err := apigenruntime.Build(generatedDeliveryAuthorizer{}, dispatch, deploymentgen.GetAPIGenCommandRuntimeContract)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/finance/delivery/plans", strings.NewReader(`{"targetId":"target-command","sourceDigest":"source","sourceAttestationDigest":"attestation"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Idempotency-Key", "plan-command-key")
	route := chi.NewRouteContext()
	route.URLParams.Add("project", "finance")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.HandleAPIGen(deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID(), recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "COMMAND_CONTRACT_NOT_EXECUTED") {
		t.Fatalf("successful generated response was rejected: %s", recorder.Body.String())
	}
}

func TestGeneratedDeliveryRouterFlushesApprovalRequestAfterEvidenceCompletion(t *testing.T) {
	projectID, err := projectgraph.NewResourceID("finance")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 19, 10, 0, 0, 0, time.UTC)
	requestDigest := "sha256:" + strings.Repeat("a", 64)
	publication := deployment.DeliveryPublication{
		ID: "publication-command", ProjectID: projectID, Environment: "prod", TargetID: "target-command",
		RequestDigest: requestDigest, CandidateID: "candidate-command",
	}
	candidate := deployment.DeliveryCandidate{
		ID: publication.CandidateID, ProjectID: projectID, Environment: "prod", TargetID: publication.TargetID,
		ServingArtifactID: "artifact-command", Status: deployment.DeliveryCandidateReady,
	}
	reader := &deliveryEvidenceFixture{
		deliveryReadFixture: deliveryReadFixture{candidate: candidate, publication: publication},
		byRequest: map[string]deployment.DeliveryEvent{
			strings.Join([]string{publication.TargetID, requestDigest, "approval_requested", "approval", "approval-command"}, "\x00"): {
				TargetID: publication.TargetID, RequestDigest: requestDigest, EventKind: "approval_requested", ObjectKind: "approval", ObjectID: "approval-command", Outcome: "accepted",
			},
		},
		byObject: map[string][]deployment.DeliveryEvent{},
	}
	repository := &canonicalApprovalRepository{}
	approvals, err := deployment.NewApprovalService(repository, deployment.ApprovalServiceConfig{
		Now: nowFunc(now), NewID: func() (string, error) { return "approval-command", nil }, Lifetime: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	m := deliveryTestModule(reader, true)
	m.approvals = approvals
	m.currentApprovalActor = func(*http.Request) (deployment.ApprovalActor, bool) {
		return deployment.ApprovalActor{PrincipalID: "operator", CredentialClass: deployment.CredentialClassHuman, CredentialID: "credential-command", CredentialExpiresAt: now.Add(time.Hour)}, true
	}
	dispatch := func(operationID string, w http.ResponseWriter, r *http.Request) bool {
		if operationID != deploymentgen.GenCommandOperationRequestDeliveryPublicationApproval().APIGenOperationID() {
			return false
		}
		m.RequestDeliveryPublicationApproval(w, r, "finance", publication.ID, "approval-command-key")
		return true
	}
	handler, err := apigenruntime.Build(generatedDeliveryAuthorizer{}, dispatch, deploymentgen.GetAPIGenCommandRuntimeContract)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/projects/finance/delivery/publications/publication-command/approval-requests", nil)
	request.Header.Set("Idempotency-Key", "approval-command-key")
	route := chi.NewRouteContext()
	route.URLParams.Add("project", "finance")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.HandleAPIGen(deploymentgen.GenCommandOperationRequestDeliveryPublicationApproval().APIGenOperationID(), recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "COMMAND_CONTRACT_NOT_EXECUTED") {
		t.Fatalf("successful generated response was rejected: %s", recorder.Body.String())
	}
}

type deliveryEvidenceFixture struct {
	deliveryReadFixture
	byRequest map[string]deployment.DeliveryEvent
	byObject  map[string][]deployment.DeliveryEvent
}

func (f *deliveryEvidenceFixture) DeliveryEventByRequest(_ context.Context, targetID, requestDigest, eventKind, objectKind, objectID string) (deployment.DeliveryEvent, error) {
	key := strings.Join([]string{targetID, requestDigest, eventKind, objectKind, objectID}, "\x00")
	event, ok := f.byRequest[key]
	if !ok {
		return deployment.DeliveryEvent{}, deployment.ErrNotFound
	}
	return event, nil
}

func (f *deliveryEvidenceFixture) DeliveryEventsByObject(_ context.Context, targetID, objectKind, objectID string) ([]deployment.DeliveryEvent, error) {
	key := strings.Join([]string{targetID, objectKind, objectID}, "\x00")
	events, ok := f.byObject[key]
	if !ok {
		return nil, deployment.ErrNotFound
	}
	return events, nil
}

func TestDeliveryCommandCompletionVerifiesTransactionalEvidence(t *testing.T) {
	const targetID = "target-command"
	const requestDigest = "sha256:request-command"
	accepted := deployment.DeliveryEvent{TargetID: targetID, RequestDigest: requestDigest, Outcome: "accepted"}
	buildAttempt := deployment.DeliveryBuildAttempt{ID: "attempt-command", CandidateID: "candidate-command"}
	buildEvent := deployment.DeliveryEvent{TargetID: targetID, ObjectKind: "candidate", ObjectID: buildAttempt.CandidateID, EventKind: "candidate_sealed", Outcome: "accepted"}
	reader := &deliveryEvidenceFixture{
		byRequest: map[string]deployment.DeliveryEvent{},
		byObject: map[string][]deployment.DeliveryEvent{
			strings.Join([]string{targetID, "candidate", buildAttempt.CandidateID}, "\x00"): {buildEvent},
		},
	}
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "plan_created", "plan", "plan-command"}, "\x00")] = accepted
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "publish_requested", "publication", "publication-command"}, "\x00")] = accepted
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "rollback_requested", "rollback", "rollback-command"}, "\x00")] = accepted
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "approval_requested", "approval", "approval-command"}, "\x00")] = accepted
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "approval_granted", "approval", "approval-command"}, "\x00")] = accepted
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "approval_rejected", "approval", "approval-command"}, "\x00")] = accepted
	reader.byRequest[strings.Join([]string{targetID, requestDigest, "approval_revoked", "approval", "approval-command"}, "\x00")] = accepted
	m := &Module{deliveryReader: reader}
	publication := deployment.DeliveryPublication{ID: "publication-command", TargetID: targetID, RequestDigest: requestDigest}
	approval := deployment.Approval{ID: "approval-command"}
	cases := []struct {
		name, operation, action, approvalEventKind string
		verify                                     func(context.Context, deliveryEventReader) error
	}{
		{name: "create", operation: deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID(), action: "delivery.plan.created", verify: func(ctx context.Context, events deliveryEventReader) error {
			event, err := events.DeliveryEventByRequest(ctx, targetID, requestDigest, "plan_created", "plan", "plan-command")
			if err != nil {
				return err
			}
			return acceptedDeliveryEvent(event, "createDeliveryPlan")
		}},
		{name: "build", operation: deploymentgen.GenCommandOperationBuildDeliveryPlan().APIGenOperationID(), action: "delivery.build.sealed", verify: func(ctx context.Context, events deliveryEventReader) error {
			return acceptedBuildEvidence(ctx, events, targetID, buildAttempt)
		}},
		{name: "publish", operation: deploymentgen.GenCommandOperationPublishDeliveryCandidate().APIGenOperationID(), action: "delivery.publication.requested", verify: func(ctx context.Context, events deliveryEventReader) error {
			event, err := events.DeliveryEventByRequest(ctx, targetID, requestDigest, "publish_requested", "publication", "publication-command")
			if err != nil {
				return err
			}
			return acceptedDeliveryEvent(event, "publishDeliveryCandidate")
		}},
		{name: "rollback", operation: deploymentgen.GenCommandOperationRollbackDeliveryGeneration().APIGenOperationID(), action: "delivery.rollback.requested", verify: func(ctx context.Context, events deliveryEventReader) error {
			event, err := events.DeliveryEventByRequest(ctx, targetID, requestDigest, "rollback_requested", "rollback", "rollback-command")
			if err != nil {
				return err
			}
			return acceptedDeliveryEvent(event, "rollbackDeliveryGeneration")
		}},
		{name: "approval-request", operation: deploymentgen.GenCommandOperationRequestDeliveryPublicationApproval().APIGenOperationID(), action: "delivery.publication.approval_requested", approvalEventKind: "approval_requested"},
		{name: "approval-grant", operation: deploymentgen.GenCommandOperationApproveDeliveryPublicationApproval().APIGenOperationID(), action: "delivery.publication.approved", approvalEventKind: "approval_granted"},
		{name: "approval-deny", operation: deploymentgen.GenCommandOperationDenyDeliveryPublicationApproval().APIGenOperationID(), action: "delivery.publication.denied", approvalEventKind: "approval_rejected"},
		{name: "approval-revoke", operation: deploymentgen.GenCommandOperationRevokeDeliveryPublicationApproval().APIGenOperationID(), action: "delivery.publication.approval_revoked", approvalEventKind: "approval_revoked"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(test.operation)
			if !ok {
				t.Fatalf("missing generated contract %q", test.operation)
			}
			ctx, guard, err := apigencommand.Begin(context.Background(), contract)
			if err != nil {
				t.Fatal(err)
			}
			if test.approvalEventKind != "" {
				err = m.completeDeliveryApprovalCommand(ctx, test.operation, test.action, publication, approval, test.approvalEventKind)
			} else {
				err = m.completeDeliveryCommand(ctx, test.operation, test.action, test.verify)
			}
			if err != nil {
				t.Fatal(err)
			}
			if !guard.Completed() {
				t.Fatal("generated command guard was not completed")
			}
		})
	}
}

func TestDeliveryCommandCompletionLeavesGuardIncompleteWithoutEvidence(t *testing.T) {
	operation := deploymentgen.GenCommandOperationCreateDeliveryPlan().APIGenOperationID()
	contract, ok := deploymentgen.GetAPIGenCommandRuntimeContract(operation)
	if !ok {
		t.Fatalf("missing generated contract %q", operation)
	}
	ctx, guard, err := apigencommand.Begin(context.Background(), contract)
	if err != nil {
		t.Fatal(err)
	}
	m := &Module{deliveryReader: &deliveryEvidenceFixture{byRequest: map[string]deployment.DeliveryEvent{}, byObject: map[string][]deployment.DeliveryEvent{}}}
	err = m.completeDeliveryCommand(ctx, operation, "delivery.plan.created", func(ctx context.Context, reader deliveryEventReader) error {
		_, err := reader.DeliveryEventByRequest(ctx, "target-command", "sha256:missing", "plan_created", "plan", "plan-command")
		return err
	})
	if err == nil {
		t.Fatal("expected durable evidence failure")
	}
	if guard.Completed() {
		t.Fatal("command guard completed despite missing durable evidence")
	}
}
