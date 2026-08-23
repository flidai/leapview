package module

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentgen "github.com/flidai/leapview/internal/agent/api/gen"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
)

func TestBuildAuditIntentDerivesGeneratedActionAndCapability(t *testing.T) {
	wantActions := map[string]string{
		"createAgentConversation":  "agent.conversation.created",
		"archiveAgentConversation": "agent.conversation.archived",
		"updateAgentConversation":  "agent.conversation.updated",
		"createAgentRun":           "agent.run.created",
		"cancelAgentRun":           "agent.run.cancelled",
	}
	var recorded []access.AuditIntent
	for operationID := range wantActions {
		intent, err := BuildAuditIntent(t.Context(), agenthttp.CommandAuditInput{
			OperationID: operationID,
			Scope:       agent.Scope{ProjectID: "sales", PrincipalID: "principal-1"},
			TargetType:  "conversation",
			TargetID:    "conversation-1",
			RequestID:   "request-1",
		})
		if err != nil {
			t.Fatalf("record %s: %v", operationID, err)
		}
		recorded = append(recorded, *intent)
	}
	if len(recorded) != len(wantActions) {
		t.Fatalf("recorded audits = %#v", recorded)
	}
	for _, event := range recorded {
		operationMatched := false
		for _, action := range wantActions {
			if event.Action == action {
				operationMatched = true
				break
			}
		}
		if !operationMatched || event.Capability != access.CapabilityResourceUse || event.Outcome == "" || event.PrincipalID != "principal-1" || event.RequestID != "request-1" {
			t.Fatalf("derived agent command audit = %#v", event)
		}
		var envelope struct {
			SchemaVersion int               `json:"schemaVersion"`
			Retention     string            `json:"retention"`
			PayloadSchema string            `json:"payloadSchema"`
			Payload       map[string]string `json:"payload"`
		}
		if err := json.Unmarshal([]byte(event.MetadataJSON), &envelope); err != nil {
			t.Fatalf("decode agent audit metadata: %v", err)
		}
		if envelope.SchemaVersion != 1 || envelope.Retention != "security" || envelope.PayloadSchema != "AgentCommandAuditPayload" ||
			envelope.Payload["resourceKind"] != "conversation" || envelope.Payload["resourceId"] != "conversation-1" ||
			envelope.Payload["surface"] != "api" {
			t.Fatalf("agent audit envelope = %#v", envelope)
		}
	}
}

func TestAgentCommandAuditPayloadRedactsInternalFieldsForLogs(t *testing.T) {
	encoded, err := agentgen.EncodeGenCreateAgentConversationAuditPayloadForLog(agentgen.GenSchemaAgentCommandAuditPayload{
		OperationId: "createAgentConversation", ResourceKind: "conversation", ResourceId: "conversation-1", Surface: "api",
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(encoded, "sales") || strings.Contains(encoded, "conversation-1") {
		t.Fatalf("agent log payload leaked internal values: %s", encoded)
	}
	if !strings.Contains(encoded, `"resourceKind":"conversation"`) || !strings.Contains(encoded, `"surface":"api"`) {
		t.Fatalf("agent log payload omitted public values: %s", encoded)
	}
}
