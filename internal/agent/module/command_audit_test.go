package module

import (
	"context"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agenthttp "github.com/flidai/leapview/internal/agent/http"
)

func TestRecordCommandAuditDerivesGeneratedActionAndPrivilege(t *testing.T) {
	wantActions := map[string]string{
		"createAgentConversation":  "agent.conversation.created",
		"archiveAgentConversation": "agent.conversation.archived",
		"updateAgentConversation":  "agent.conversation.updated",
		"createAgentRun":           "agent.run.created",
		"cancelAgentRun":           "agent.run.cancelled",
	}
	var recorded []access.AuditEventInput
	m := &Module{recordAudit: func(_ context.Context, input access.AuditEventInput) error {
		recorded = append(recorded, input)
		return nil
	}}
	for operationID := range wantActions {
		if err := m.recordCommandAudit(t.Context(), agenthttp.CommandAuditInput{
			OperationID: operationID,
			Scope:       agent.Scope{WorkspaceID: "sales", PrincipalID: "principal-1"},
			TargetType:  "conversation",
			TargetID:    "conversation-1",
			RequestID:   "request-1",
		}); err != nil {
			t.Fatalf("record %s: %v", operationID, err)
		}
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
		if !operationMatched || event.Privilege != access.PrivilegeUseAgent || event.Status != "success" || event.PrincipalID != "principal-1" || event.RequestID != "request-1" {
			t.Fatalf("derived agent command audit = %#v", event)
		}
	}
}
