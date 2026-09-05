package module

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type generatedDeliveryAuthorizer struct{}

func (generatedDeliveryAuthorizer) Protect(_ string, next http.Handler) (http.Handler, bool) {
	return next, true
}

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
		Governance: deployment.DeliveryGovernance{PolicyDigest: "sha256:" + strings.Repeat("2", 64), AuthorizationDigest: "sha256:" + strings.Repeat("3", 64), QualificationDigest: "sha256:" + strings.Repeat("4", 64), ApprovalPolicyRevision: 1, ExpiresAt: time.Now().UTC().Add(time.Hour)},
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
