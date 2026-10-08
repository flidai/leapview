package agent

import (
	"context"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"testing"
)

func TestServiceRecoversTruncationAndRecordsFinalOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		final     agentcore.FinishReason
		status    string
		wantError bool
	}{
		{"recovered", agentcore.FinishReasonStop, RunStatusCompleted, false},
		{"still truncated", agentcore.FinishReasonTruncated, RunStatusFailed, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			store := openAgentAppStore(t, ctx)
			defer store.Close()
			principal := createAgentAppPrincipal(t, ctx, store, "recovery@example.com")
			model := newRecordingAgentModel(agentcore.ModelResponse{FinishReason: agentcore.FinishReasonTruncated}, agentcore.ModelResponse{Content: "result", FinishReason: tc.final})
			service := NewService(store, Config{APIKey: "key", Model: "fake-model"}, WithModel(model))
			scope := Scope{ProjectID: "test", PrincipalID: principal.ID}
			conversation, err := service.CreateConversation(ctx, scope, "Recovery")
			if err != nil {
				t.Fatal(err)
			}
			_, err = service.Prompt(ctx, PromptInput{Scope: scope, ConversationID: conversation.ID, Input: "Build dashboard"})
			if (err != nil) != tc.wantError {
				t.Fatalf("prompt error=%v, wantError=%t", err, tc.wantError)
			}
			runs, err := store.ListRuns(ctx, principal.ID, conversation.ID)
			if err != nil || len(runs) != 1 || runs[0].Status != tc.status {
				t.Fatalf("runs=%+v err=%v", runs, err)
			}
			if len(model.Requests()) != 2 {
				t.Fatalf("model requests=%d, want 2", len(model.Requests()))
			}
		})
	}
}
