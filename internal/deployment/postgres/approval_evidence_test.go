package postgres

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestApprovalEvidencePayloadRequestLeavesDecisionActorEmpty(t *testing.T) {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	request := ApprovalRequest{
		RequestID: "0198f2c0-7c7a-7f00-8a11-000000001111", PublicationID: "0198f2c0-7c7a-7f00-8a11-000000001112",
		TargetID: "target_prod", CandidateID: "0198f2c0-7c7a-7f00-8a11-000000001113", GenerationID: "0198f2c0-7c7a-7f00-8a11-000000001114",
		RequestDigest: "sha256:" + strings.Repeat("a", 64), ExpectedTargetRevision: 4, PolicyRevision: 2,
		RequestedBy: ApprovalActor{PrincipalID: "publisher", CredentialClass: "session", CredentialID: "session-1", CredentialExpiresAt: now.Add(time.Hour)},
		ExpiresAt:   now.Add(time.Hour),
	}
	evidence := ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000001115", EventID: "0198f2c0-7c7a-7f00-8a11-000000001116", AuditID: "0198f2c0-7c7a-7f00-8a11-000000001117", Metadata: []byte(`{"source":"test"}`)}
	raw, err := ApprovalEvidencePayload(ApprovalActionRequest, request, nil, evidence)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"decided_by", "decision_credential_class", "decision_credential_id", "decision_credential_expires_at", "decided_at"} {
		if got, ok := payload[key].(string); !ok || got != "" {
			t.Fatalf("request payload %s = %#v, want empty string", key, payload[key])
		}
	}
	if got := payload["requested_by"]; got != "publisher" {
		t.Fatalf("requested_by = %#v", got)
	}
	if got := payload["decision_revision"]; got != float64(0) {
		t.Fatalf("decision_revision = %#v", got)
	}
}

func TestApprovalEvidencePayloadDecisionUsesReviewerEvidence(t *testing.T) {
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	request := ApprovalRequest{RequestID: "0198f2c0-7c7a-7f00-8a11-000000001111", RequestedBy: ApprovalActor{PrincipalID: "publisher"}, ExpiresAt: now.Add(time.Hour)}
	decision := &ApprovalDecision{DecisionID: "0198f2c0-7c7a-7f00-8a11-000000001112", Revision: 1, DecidedBy: ApprovalActor{PrincipalID: "reviewer", CredentialClass: "human", CredentialID: "cred-1", CredentialExpiresAt: now.Add(time.Hour)}, DecidedAt: now}
	evidence := ApprovalEvidence{OperationID: "0198f2c0-7c7a-7f00-8a11-000000001113", EventID: "0198f2c0-7c7a-7f00-8a11-000000001114", AuditID: "0198f2c0-7c7a-7f00-8a11-000000001115", Metadata: []byte(`{}`)}
	raw, err := ApprovalEvidencePayload(ApprovalActionApprove, request, decision, evidence)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["decided_by"] != "reviewer" || payload["decision_id"] != decision.DecisionID || payload["decision_revision"] != float64(1) {
		t.Fatalf("decision payload = %#v", payload)
	}
}
