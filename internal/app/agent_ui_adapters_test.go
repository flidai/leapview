package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agentui "github.com/flidai/leapview/internal/agent/ui"
	appshell "github.com/flidai/leapview/internal/app/shell"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
)

func TestApplicationLayoutUsesCurrentPrincipalIdentity(t *testing.T) {
	auth, err := accessmodule.NewAuth(nil, accessmodule.AuthConfig{DevBypass: true, CSRFKey: "0123456789abcdef0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	access, err := accessmodule.Build(t.Context(), accessmodule.Config{ExistingAuth: auth})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/profile", nil)
	request = request.WithContext(accessmodule.WithPrincipal(request.Context(), accessmodule.LocalDeveloperPrincipal()))
	layout := applicationLayout(access, nil, nil, staticasset.Resolver{}, request)(webpage.Context{Active: "admin", PageID: "profile"})
	chrome := layout.Signal.(appshell.Chrome)
	if chrome.Sidebar.UserName == nil || *chrome.Sidebar.UserName != "Local Developer" {
		t.Fatalf("sidebar user name = %v, want Local Developer", chrome.Sidebar.UserName)
	}
	if chrome.Sidebar.PrincipalID == nil || *chrome.Sidebar.PrincipalID != accessmodule.LocalDeveloperPrincipal().ID {
		t.Fatalf("sidebar principal ID = %v, want current principal", chrome.Sidebar.PrincipalID)
	}
}

func TestAdminLayoutRequestRecognizesDocumentsAndAdminStreams(t *testing.T) {
	tests := map[string]bool{
		"/admin/profile":                     true,
		"/connections":                       false,
		"/connections/warehouse":             false,
		"/updates?route=admin&section=audit": true,
		"/workspaces":                        false,
		"/updates?route=workspace":           false,
	}
	for target, want := range tests {
		request := httptest.NewRequest("GET", target, nil)
		if got := adminLayoutRequest(request); got != want {
			t.Errorf("adminLayoutRequest(%q) = %t, want %t", target, got, want)
		}
	}
}

func TestDashboardChatAdapterPreservesBrowserContract(t *testing.T) {
	source := agentui.ChatSignal{
		ActiveConversationID: "conversation-1",
		Composer: agentui.ComposerSignal{
			Disabled: true, Placeholder: "Ask", Value: "Question",
		},
		Conversations: []agentui.ChatConversationSummary{{
			ArchivedAt: agentui.Optional("2026-07-24T00:00:00Z"), CreatedAt: "2026-07-23T00:00:00Z",
			ID: "conversation-1", LastMessageText: agentui.Optional("Answer"), MessageCount: 2,
			PrincipalID: "principal-1", Status: "ready", Title: "Analysis",
			TitlePending: agentui.Pointer(true), UpdatedAt: "2026-07-25T00:00:00Z",
		}},
		Status: agentui.ChatStatus{
			Enabled: true, Error: agentui.Optional("warning"), Running: true,
		},
		Transcript: []agentui.ChatTranscriptItemSignal{{
			ID: "turn-1", Kind: "assistant", Text: agentui.Optional("Answer"),
			Artifact: &agentui.ChatArtifactSignal{
				ID: "visual-1", Type: "visual", Summary: agentui.Optional("Chart"),
			},
			References: &[]agentui.AgentReferenceSignal{{
				Reference: agentui.AgentReferenceKeySignal{
					Kind: "visual", ID: "visual-1",
				},
				Name: "Revenue", Description: agentui.Optional("Revenue chart"), VisualType: agentui.Optional("bar"),
				Hierarchy: []string{"Sales", "Executive"}, Href: "/visual-1",
				Locations: []agentui.AgentReferenceLocationSignal{{
					DashboardID: agentui.Optional("dashboard-1"), DashboardName: agentui.Optional("Executive"),
					PageID: agentui.Optional("page-1"), PageName: agentui.Optional("Overview"), Href: "/visual-1",
				}},
				Context: []string{"revenue"},
			}},
		}},
	}

	got := dashboardChatSignal(source)

	var sourceJSON any
	sourceBytes, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(sourceBytes, &sourceJSON); err != nil {
		t.Fatal(err)
	}
	var gotJSON any
	gotBytes, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(gotBytes, &gotJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(gotJSON, sourceJSON) {
		t.Fatalf("dashboard adapter changed browser contract:\nsource=%s\ngot=%s", sourceBytes, gotBytes)
	}
}

func TestDataExplorerAgentAdapterPreservesExplicitStatusResetsAndCommandScope(t *testing.T) {
	source := agentui.ChatViewState{Agent: agentui.ChatSignal{ActiveConversationID: "own-conversation", Status: agentui.ChatStatus{Enabled: true}, Composer: agentui.ComposerSignal{Placeholder: "Ask", Disabled: false}, Transcript: []agentui.ChatTranscriptItemSignal{{Kind: "assistant", Text: agentui.Optional("Own answer")}}}}
	projected := dataExplorerAgentBootstrap(source)
	raw, err := json.Marshal(projected.Agent)
	if err != nil {
		t.Fatal(err)
	}
	var state map[string]any
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state["activeConversationId"] != "own-conversation" || state["composer"].(map[string]any)["disabled"] != false {
		t.Fatalf("agent context/availability lost: %s", raw)
	}
	status := state["status"].(map[string]any)
	for _, key := range []string{"runId", "error", "canContinue"} {
		if value, present := status[key]; !present || value != nil {
			t.Fatalf("status reset %q missing: %s", key, raw)
		}
	}
	if status["enabled"] != true || status["running"] != false {
		t.Fatalf("agent status lost: %s", raw)
	}
	refreshJSON, err := json.Marshal(projected.Refresh)
	if err != nil {
		t.Fatal(err)
	}
	var refresh map[string]any
	if err := json.Unmarshal(refreshJSON, &refresh); err != nil {
		t.Fatal(err)
	}
	refreshStatus, ok := refresh["status"].(map[string]any)
	if len(refresh) != 2 || len(refreshStatus) != 1 || !ok || refreshStatus["enabled"] != true {
		t.Fatalf("reconnect refresh can replace active chat state: %s", refreshJSON)
	}
	if _, exists := refresh["conversations"]; !exists {
		t.Fatalf("reconnect must refresh conversation history: %s", refreshJSON)
	}

	bindings := dataExplorerAgentCommands(&agentmodule.Module{})
	if bindings.CreateConversation.OperationID() != "createAgentConversation" || bindings.CreateRun.OperationID() != "createAgentRun" || bindings.CancelRun.OperationID() != "cancelAgentRun" {
		t.Fatalf("agent command contract mismatch: %#v", bindings)
	}
	initialBindings := dataExplorerAgentCommands(nil)
	if initialBindings.CreateConversation.OperationID() != "createAgentConversation" || initialBindings.CreateRun.OperationID() != "createAgentRun" || initialBindings.CancelRun.OperationID() != "cancelAgentRun" {
		t.Fatal("static command identities must be available before agent initialization")
	}
}
