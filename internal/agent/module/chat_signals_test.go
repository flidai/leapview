package module

import (
	"encoding/json"
	"testing"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/ui"
)

func TestChatSignalWithDerivesRunningFromDurableRun(t *testing.T) {
	fixture := newModuleJobFixture(t)
	conversation, run := fixture.run(t, "durable-running", agent.RunStatusRunning)

	signal := fixture.mod.ChatSignalWith(
		t.Context(), fixture.scope(), conversation.ID, nil, agent.ChatArtifactSignals{}, "", false,
	)

	if !signal.Agent.Status.Running {
		t.Fatal("signal status running = false, want true from durable run")
	}
	if signal.Agent.Status.RunID == nil || *signal.Agent.Status.RunID != run.ID {
		t.Fatalf("signal run ID = %v, want %q", signal.Agent.Status.RunID, run.ID)
	}
	if !signal.Agent.Composer.Disabled {
		t.Fatal("composer disabled = false, want true while durable run is active")
	}
	if signal.Agent.Composer.Placeholder != "Waiting for the current answer..." {
		t.Fatalf("composer placeholder = %q, want running placeholder", signal.Agent.Composer.Placeholder)
	}

	preparingConversation, err := fixture.repo.CreateConversation(t.Context(), agent.ConversationInput{
		PrincipalID: fixture.owner.ID,
		Title:       "durable-preparing",
	})
	if err != nil {
		t.Fatalf("create preparing conversation: %v", err)
	}
	preparingRun, err := fixture.repo.CreateRun(t.Context(), agent.RunInput{
		PrincipalID:    fixture.owner.ID,
		ConversationID: preparingConversation.ID,
		RunID:          "durable-preparing-run",
		Status:         agent.RunStatusPreparing,
	})
	if err != nil {
		t.Fatalf("create preparing run: %v", err)
	}

	preparingSignal := fixture.mod.ChatSignalWith(
		t.Context(), fixture.scope(), preparingConversation.ID, nil, agent.ChatArtifactSignals{}, "", false,
	)
	if !preparingSignal.Agent.Status.Running {
		t.Fatal("preparing signal status running = false, want true from durable run")
	}
	if preparingSignal.Agent.Status.RunID == nil || *preparingSignal.Agent.Status.RunID != preparingRun.ID {
		t.Fatalf("preparing signal run ID = %v, want %q", preparingSignal.Agent.Status.RunID, preparingRun.ID)
	}
}

func TestChatSignalWithClearsRunningFromDurableTerminalRun(t *testing.T) {
	fixture := newModuleJobFixture(t)
	conversation, run := fixture.run(t, "durable-completed", agent.RunStatusCompleted)

	signal := fixture.mod.ChatSignalWith(
		t.Context(), fixture.scope(), conversation.ID, nil, agent.ChatArtifactSignals{}, "", true,
	)

	if signal.Agent.Status.Running {
		t.Fatal("signal status running = true, want false from terminal durable run")
	}
	if signal.Agent.Status.RunID != nil {
		t.Fatalf("signal run ID = %v, want nil for terminal durable run %q", signal.Agent.Status.RunID, run.ID)
	}
	if signal.Agent.Composer.Disabled {
		t.Fatal("composer disabled = true, want false after durable run completion")
	}
	if signal.Agent.Composer.Placeholder != "Ask about dashboards, metrics, or models..." {
		t.Fatalf("composer placeholder = %q, want idle placeholder", signal.Agent.Composer.Placeholder)
	}
}

func TestChatWorkerSignalPatchPreservesAndClearsRunStatus(t *testing.T) {
	for _, test := range []struct {
		name   string
		status ui.ChatStatus
		want   map[string]any
	}{
		{"canceled", ui.ChatStatus{Enabled: true, CanContinue: ui.Pointer(true)}, map[string]any{"enabled": true, "running": false, "canContinue": true, "runId": nil, "error": nil}},
		{"running", ui.ChatStatus{Enabled: true, Running: true, RunID: ui.Pointer("current-run")}, map[string]any{"enabled": true, "running": true, "canContinue": nil, "runId": "current-run", "error": nil}},
		{"completed", ui.ChatStatus{Enabled: true}, map[string]any{"enabled": true, "running": false, "canContinue": nil, "runId": nil, "error": nil}},
		{"failed", ui.ChatStatus{Enabled: true, Error: ui.Pointer("Please try again.")}, map[string]any{"enabled": true, "running": false, "canContinue": nil, "runId": nil, "error": "Please try again."}},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := json.Marshal(chatSignalPatch(ui.ChatViewState{Agent: ui.ChatSignal{Status: test.status}}))
			if err != nil {
				t.Fatal(err)
			}
			var patch map[string]any
			if err := json.Unmarshal(encoded, &patch); err != nil {
				t.Fatal(err)
			}
			status := patch["agent"].(map[string]any)["status"].(map[string]any)
			for key, want := range test.want {
				if value, present := status[key]; !present || value != want {
					t.Errorf("status[%q] = %#v (present %v), want %#v", key, value, present, want)
				}
			}
		})
	}
}
