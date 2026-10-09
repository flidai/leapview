package http

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	"github.com/go-chi/chi/v5"
)

func TestHydratePreviewActionIgnoresRenderPayload(t *testing.T) {
	// The encoder emits nested page lists that its TOON decoder cannot read.
	// Navigation needs retained arguments and the preview outcome, not chart data.
	const preview = `definition:
  pages[1]:
    -
      canvas:
        height: 0
      id: overview
pagePatch:
  visuals:
    chart:
      rows[1]:
        -
          values[2]: 1,2
revision:
  number: 4
  revisionId: revision-4
semanticEvidence:
  identity:
    generationId: generation-1
`
	for _, failure := range []string{"", "error: preview failed\n", "visualErrors:\n  chart: query failed\n"} {
		t.Run(failure, func(t *testing.T) {
			item := agent.ChatTranscriptItem{RunID: "run-1", ToolCallID: "call-1", Name: "preview_dashboard_draft"}
			hydrateRetainedTool(&item, []agent.Message{
				{RunID: "other-run", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: `{"error":"wrong run"}`},
				{RunID: "run-1", ContentJSON: `{"tool_calls":[{"id":"call-1","arguments":{"dashboardId":"dashboard-1","page":"overview"}}]}`},
				{RunID: "run-1", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: preview + failure},
			})
			var result map[string]json.RawMessage
			if err := json.Unmarshal([]byte(item.ResultJSON), &result); err != nil {
				t.Fatalf("preview navigation result is not JSON: %v", err)
			}
			if len(result["revision"]) == 0 || item.ArgumentsJSON == "" {
				t.Fatal("missing retained revision or arguments")
			}
			if failure != "" && len(result["error"]) == 0 && len(result["visualErrors"]) == 0 {
				t.Fatal("preview failure was discarded")
			}
		})
	}
}

func TestActionFromCompletedEventsBeforeTranscriptCommit(t *testing.T) {
	events := []agent.Event{
		{RunID: "run-1", EventType: "tool_execution_start", PayloadJSON: `{"tool_call_id":"call-1","tool_name":"read_dashboard_source","tool_arguments":"{\"dashboardId\":\"dashboard-1\"}"}`},
		{RunID: "run-1", EventType: "tool_execution_end", PayloadJSON: `{"tool_call_id":"call-1","tool_name":"read_dashboard_source","tool_result":"dashboardId: dashboard-1\ndraftId: draft-1\n"}`},
	}
	item := actionFromCompletedEvents(events, "run-1", "call-1")
	if item == nil || item.Status != "complete" || item.ArgumentsJSON != `{"dashboardId":"dashboard-1"}` || !json.Valid([]byte(item.ResultJSON)) {
		t.Fatalf("item=%+v", item)
	}
	if actionFromCompletedEvents(events, "other-run", "call-1") != nil {
		t.Fatal("cross-run action accepted")
	}
	if actionFromCompletedEvents(events[:1], "run-1", "call-1") != nil {
		t.Fatal("unfinished tool accepted")
	}
	events[1].PayloadJSON = `{"tool_call_id":"call-1","tool_name":"read_dashboard_source","error":"failed","tool_result":"error: failed"}`
	if actionFromCompletedEvents(events, "run-1", "call-1") != nil {
		t.Fatal("failed tool accepted")
	}
}

func TestDraftActionIgnoresUndecodableDocument(t *testing.T) {
	item := agent.ChatTranscriptItem{RunID: "run-1", ToolCallID: "call-1", Name: "get_dashboard_draft"}
	hydrateRetainedTool(&item, []agent.Message{{RunID: "run-1", ToolCallID: "call-1", Role: agent.MessageRoleTool, ContentText: `lifecycle:
  id: dashboard-1
  draft:
    id: draft-1
revision:
  dashboardId: dashboard-1
  document:
    pages[1]:
      -
        id: overview
  number: 3
error: failed
`}})
	var result map[string]json.RawMessage
	if err := json.Unmarshal([]byte(item.ResultJSON), &result); err != nil {
		t.Fatal(err)
	}
	if len(result["lifecycle"]) == 0 || len(result["revision"]) == 0 || len(result["error"]) == 0 {
		t.Fatalf("lost receipt: %s", item.ResultJSON)
	}
}

func TestChatPreviewOpensCompactCreationReceiptAndChecksOwnership(t *testing.T) {
	for _, edited := range []bool{false, true} {
		t.Run(fmt.Sprint(edited), func(t *testing.T) {
			fixture := openAgentHTTPPostgresFixture(t, agentpostgres.Options{})
			owner, err := fixture.Access.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "preview-owner@example.com", DisplayName: "Owner"})
			if err != nil {
				t.Fatal(err)
			}
			other, err := fixture.Access.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "preview-other@example.com", DisplayName: "Other"})
			if err != nil {
				t.Fatal(err)
			}
			service := agent.NewService(fixture.Agent, agent.Config{APIKey: "test", Model: "test"})
			scope := agent.Scope{PrincipalID: owner.ID, ProjectID: "project:preview"}
			conversation, err := service.CreateConversation(t.Context(), scope, "Build dashboard")
			if err != nil {
				t.Fatal(err)
			}
			name, selector, args := "create_dashboard_draft", "createdBy", `{"title":"Sales","semanticModelId":"semantic-model:sales"}`
			if edited {
				name, selector, args = "edit_dashboard_source", "authoredBy", `{"dashboardId":"dashboard-1","source":"`+strings.Repeat("x", 5000)+`"}`
			}
			for _, tool := range []struct{ name, id, args, result string }{
				{name, "create-call", args, `{"id":"dashboard-1","status":"draft"}`},
				{"preview_dashboard_draft", "preview-call", `{"dashboardId":"dashboard-1","draftId":"draft-1","page":"overview"}`, `{"visualErrors":{}}`},
			} {
				content, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"id": tool.id, "name": tool.name, "arguments": json.RawMessage(tool.args)}}})
				_, err = fixture.Agent.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: owner.ID, ConversationID: conversation.ID, Role: agent.MessageRoleAssistant, ContentJSON: string(content)})
				if err != nil {
					t.Fatal(err)
				}
				_, err = fixture.Agent.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: owner.ID, ConversationID: conversation.ID, Role: agent.MessageRoleTool, ToolCallID: tool.id, ToolName: tool.name, ContentText: tool.result})
				if err != nil {
					t.Fatal(err)
				}
			}
			for _, tc := range []struct {
				principal, creation string
				want                int
			}{
				{owner.ID, "create-call", http.StatusSeeOther},
				{owner.ID, "wrong-call", http.StatusNotFound},
				{other.ID, "create-call", http.StatusNotFound},
			} {
				handler := NewHandler(Options{Service: service, ActiveProjectID: "project:preview", CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: tc.principal}, true }})
				router := chi.NewRouter()
				router.Get("/chats/{conversation}/actions/{toolcall}/open", handler.ChatActionOpen)
				response := httptest.NewRecorder()
				router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/chats/"+conversation.ID+"/actions/preview-call/open?embed=chat&"+selector+"="+tc.creation, nil))
				if response.Code != tc.want {
					t.Fatalf("principal=%s creation=%s status=%d body=%s", tc.principal, tc.creation, response.Code, response.Body.String())
				}
				if tc.want == http.StatusSeeOther && !strings.HasPrefix(response.Header().Get("Location"), "/dashboards/dashboard-1/edit?") {
					t.Fatalf("preview location=%s", response.Header().Get("Location"))
				}
			}
		})
	}
}

func TestChatAutomaticPreviewValidatesRetainedErrorsWithoutCreation(t *testing.T) {
	fixture := openAgentHTTPPostgresFixture(t, agentpostgres.Options{})
	owner, err := fixture.Access.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "recovery-owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	service := agent.NewService(fixture.Agent, agent.Config{APIKey: "test", Model: "test"})
	scope := agent.Scope{PrincipalID: owner.ID, ProjectID: "project:preview"}
	for _, tc := range []struct {
		name, receipt, query string
		want                 int
	}{
		{"valid recovery", "revision:\n  number: 4\n", "?embed=chat&mode=preview", http.StatusSeeOther},
		{"invalid recovery", "visualErrors:\n  chart: Missing measure\n", "?embed=chat&mode=preview", http.StatusNotFound},
		{"manual repair", "visualErrors:\n  chart: Missing measure\n", "?embed=chat", http.StatusSeeOther},
	} {
		t.Run(tc.name, func(t *testing.T) {
			conversation, err := service.CreateConversation(t.Context(), scope, "Recover dashboard")
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.Agent.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: owner.ID, ConversationID: conversation.ID, Role: agent.MessageRoleAssistant, ContentJSON: `{"tool_calls":[{"id":"preview-call","name":"preview_dashboard_draft","arguments":{"dashboardId":"dashboard-1","draftId":"draft-1","page":"details"}}]}`})
			if err != nil {
				t.Fatal(err)
			}
			_, err = fixture.Agent.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: owner.ID, ConversationID: conversation.ID, Role: agent.MessageRoleTool, ToolCallID: "preview-call", ToolName: "preview_dashboard_draft", ContentText: tc.receipt})
			if err != nil {
				t.Fatal(err)
			}
			handler := NewHandler(Options{Service: service, ActiveProjectID: scope.ProjectID, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: owner.ID}, true }})
			router := chi.NewRouter()
			router.Get("/chats/{conversation}/actions/{toolcall}/open", handler.ChatActionOpen)
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/chats/"+conversation.ID+"/actions/preview-call/open"+tc.query, nil))
			if response.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", response.Code, tc.want, response.Body.String())
			}
			if tc.want == http.StatusSeeOther && !strings.Contains(response.Header().Get("Location"), "page=details") {
				t.Fatalf("selected page lost: %s", response.Header().Get("Location"))
			}
		})
	}
}
