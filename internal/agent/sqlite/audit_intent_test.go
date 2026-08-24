package sqlite

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/transaction"
)

type recordingAgentAuditIntent struct {
	intent access.AuditIntent
}

func (r *recordingAgentAuditIntent) RecordAuditIntent(_ context.Context, _ transaction.Transaction, intent access.AuditIntent) error {
	r.intent = intent
	return nil
}

func TestRecordAgentRunAuditIntentUsesRunResourceIdentity(t *testing.T) {
	recorder := &recordingAgentAuditIntent{}
	repo := &Repository{audit: recorder}
	intent := &access.AuditIntent{
		EventID: "pending", Source: "agent", Operation: "createAgentRun",
		ResourceKind: "conversation", ResourceID: "conversation-1",
		MetadataJSON: `{"schemaVersion":1,"retention":"security","payloadSchema":"AgentRunAuditPayload","payload":{"resourceKind":"conversation","resourceId":"conversation-1"}}`,
	}

	if err := repo.recordAuditIntent(t.Context(), nil, intent, "run-1", "run-1"); err != nil {
		t.Fatal(err)
	}
	if recorder.intent.ResourceKind != "agent_run" || recorder.intent.ResourceID != "run-1" {
		t.Fatalf("run resource = %s/%s, want agent_run/run-1", recorder.intent.ResourceKind, recorder.intent.ResourceID)
	}
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(recorder.intent.MetadataJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Payload["resourceKind"] != "agent_run" || envelope.Payload["resourceId"] != "run-1" {
		t.Fatalf("run audit payload = %#v", envelope.Payload)
	}
}

func TestRecordConversationCreateAuditIntentUsesGeneratedResourceIdentity(t *testing.T) {
	recorder := &recordingAgentAuditIntent{}
	repo := &Repository{audit: recorder}
	intent := &access.AuditIntent{
		EventID: "pending", Source: "agent", Operation: "createAgentConversation",
		ResourceKind: "conversation",
		MetadataJSON: `{"schemaVersion":1,"retention":"security","payloadSchema":"AgentCommandAuditPayload","payload":{"resourceKind":"conversation","resourceId":""}}`,
	}

	if err := repo.recordAuditIntent(t.Context(), nil, intent, "conversation-1", "conversation-1"); err != nil {
		t.Fatal(err)
	}
	if recorder.intent.ResourceKind != "conversation" || recorder.intent.ResourceID != "conversation-1" {
		t.Fatalf("conversation resource = %s/%s, want conversation/conversation-1", recorder.intent.ResourceKind, recorder.intent.ResourceID)
	}
	var envelope struct {
		Payload map[string]any `json:"payload"`
	}
	if err := json.Unmarshal([]byte(recorder.intent.MetadataJSON), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Payload["resourceKind"] != "conversation" || envelope.Payload["resourceId"] != "conversation-1" {
		t.Fatalf("conversation audit payload = %#v", envelope.Payload)
	}
}
