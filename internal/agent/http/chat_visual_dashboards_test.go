package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	authoring "github.com/flidai/leapview/internal/dashboard/authoring"
	dashboardauthoringapplication "github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

var chatVisualDashboardTestCommandID = uuid.Must(uuid.NewV7()).String()

type chatVisualDashboardHTTPFixture struct {
	service        *agent.Service
	ownerID        string
	otherID        string
	conversationID string
	artifactID     string
}

func newChatVisualDashboardHTTPFixture(t *testing.T) chatVisualDashboardHTTPFixture {
	t.Helper()
	fixture := openAgentHTTPPostgresFixture(t, agentpostgres.Options{})
	owner, err := fixture.Access.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "chat-visual-owner@example.com", DisplayName: "Chat Visual Owner"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := fixture.Access.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "chat-visual-other@example.com", DisplayName: "Chat Visual Other"})
	if err != nil {
		t.Fatal(err)
	}
	service := agent.NewService(fixture.Agent, agent.Config{APIKey: "test", Model: "test"})
	scope := agent.Scope{ProjectID: "project:chat-visual", PrincipalID: owner.ID}
	conversation, err := service.CreateConversation(t.Context(), scope, "Visual source")
	if err != nil {
		t.Fatal(err)
	}
	artifactID := "agent_visual_http"
	appendHTTPChatVisualArtifact(t, fixture.Agent, scope, conversation.ID, "call_visual_http", artifactID)
	return chatVisualDashboardHTTPFixture{service: service, ownerID: owner.ID, otherID: other.ID, conversationID: conversation.ID, artifactID: artifactID}
}

func appendHTTPChatVisualArtifact(t *testing.T, repository *agentpostgres.Repository, scope agent.Scope, conversationID, callID, artifactID string) {
	t.Helper()
	const arguments = "{\"semanticModelId\":\"semantic_model_finance\",\"visual\":{\"type\":\"donut\",\"title\":\"Net sales by country\",\"query\":{\"type\":\"aggregate\",\"dimensions\":[\"country\"],\"metrics\":[\"net_sales\"]},\"presentation\":{\"type\":\"proportional\",\"legend\":\"bottom\"}},\"filters\":[{\"id\":\"region\",\"label\":\"Region\",\"dimension\":\"region\",\"control\":{\"type\":\"text\"}}]}"
	assistantContent, err := json.Marshal(map[string]any{
		"tool_calls": []any{map[string]any{"id": callID, "name": "query_visual", "arguments": json.RawMessage(arguments)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AppendMessage(t.Context(), agent.MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversationID,
		Role: agent.MessageRoleAssistant, ContentJSON: string(assistantContent),
	}); err != nil {
		t.Fatal(err)
	}
	toolContent, err := json.Marshal(map[string]any{
		"display_content": map[string]any{
			"type": "donut", "id": artifactID, "summary": "Created chart.",
			"patch": map[string]any{"visuals": map[string]any{artifactID: map[string]any{"type": "donut"}}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AppendMessage(t.Context(), agent.MessageInput{
		PrincipalID: scope.PrincipalID, ConversationID: conversationID,
		Role: agent.MessageRoleTool, ToolCallID: callID, ToolName: "query_visual", ContentJSON: string(toolContent),
	}); err != nil {
		t.Fatal(err)
	}
}

func (f chatVisualDashboardHTTPFixture) handler(principalID string, authorize func(context.Context, agent.Scope, string) error) *Handler {
	return NewHandler(Options{
		Service: f.service, ActiveProjectID: "project:chat-visual",
		CurrentPrincipal:       func(*http.Request) (Principal, bool) { return Principal{ID: principalID}, true },
		DashboardAuthoring:     &dashboardauthoringapplication.Application{},
		AuthorizeSemanticModel: authorize,
	})
}

func chatVisualDashboardRouter(handler *Handler) *chi.Mux {
	router := chi.NewRouter()
	router.Get("/chats/{conversation}/visuals/{artifact}/dashboards", handler.ListChatVisualDashboards)
	router.Post("/chats/{conversation}/visuals/{artifact}/dashboards", handler.AddChatVisualToDashboardUI)
	return router
}

func chatVisualAddRequest(f chatVisualDashboardHTTPFixture, body string) *http.Request {
	request := httptest.NewRequest(http.MethodPost, "/chats/"+f.conversationID+"/visuals/"+f.artifactID+"/dashboards", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	return request
}

func TestChatVisualDashboardRoutesEnforceConversationAndSemanticModelAccess(t *testing.T) {
	fixture := newChatVisualDashboardHTTPFixture(t)
	authorizeCalls := 0
	allow := func(context.Context, agent.Scope, string) error { authorizeCalls++; return nil }
	otherRouter := chatVisualDashboardRouter(fixture.handler(fixture.otherID, allow))

	getResponse := httptest.NewRecorder()
	otherRouter.ServeHTTP(getResponse, httptest.NewRequest(http.MethodGet, "/chats/"+fixture.conversationID+"/visuals/"+fixture.artifactID+"/dashboards", nil))
	if getResponse.Code != http.StatusNotFound {
		t.Fatalf("other owner's list status=%d body=%s, want 404", getResponse.Code, getResponse.Body.String())
	}

	addRequest := chatVisualAddRequest(fixture, "{\"title\":\"New dashboard\"}")
	addRequest.Header.Set(uicommand.HeaderOperationID, addChatVisualToDashboardOperation.APIGenOperationID())
	addRequest.Header.Set("Idempotency-Key", chatVisualDashboardTestCommandID)
	addResponse := httptest.NewRecorder()
	otherRouter.ServeHTTP(addResponse, addRequest)
	if addResponse.Code != http.StatusNotFound {
		t.Fatalf("other owner's add status=%d body=%s, want 404", addResponse.Code, addResponse.Body.String())
	}
	if authorizeCalls != 0 {
		t.Fatalf("semantic-model authorization ran for another owner's conversation %d times", authorizeCalls)
	}

	revoked := func(_ context.Context, _ agent.Scope, modelID string) error {
		authorizeCalls++
		if modelID != "semantic_model_finance" {
			t.Fatalf("semantic model authorization received %q", modelID)
		}
		return access.ErrForbidden
	}
	ownerRouter := chatVisualDashboardRouter(fixture.handler(fixture.ownerID, revoked))
	ownerGet := httptest.NewRecorder()
	ownerRouter.ServeHTTP(ownerGet, httptest.NewRequest(http.MethodGet, "/chats/"+fixture.conversationID+"/visuals/"+fixture.artifactID+"/dashboards", nil))
	if ownerGet.Code != http.StatusForbidden {
		t.Fatalf("revoked owner's list status=%d body=%s, want 403", ownerGet.Code, ownerGet.Body.String())
	}

	ownerAddRequest := chatVisualAddRequest(fixture, "{\"title\":\"New dashboard\"}")
	ownerAddRequest.Header.Set(uicommand.HeaderOperationID, addChatVisualToDashboardOperation.APIGenOperationID())
	ownerAddRequest.Header.Set("Idempotency-Key", chatVisualDashboardTestCommandID)
	ownerAdd := httptest.NewRecorder()
	ownerRouter.ServeHTTP(ownerAdd, ownerAddRequest)
	if ownerAdd.Code != http.StatusForbidden {
		t.Fatalf("revoked owner's add status=%d body=%s, want 403", ownerAdd.Code, ownerAdd.Body.String())
	}
	if authorizeCalls != 2 {
		t.Fatalf("semantic-model authorization calls=%d, want 2", authorizeCalls)
	}
}

func TestAddChatVisualToDashboardUIRejectsInvalidRequestIdentityAndSelection(t *testing.T) {
	fixture := newChatVisualDashboardHTTPFixture(t)
	authorizeCalls := 0
	handler := fixture.handler(fixture.ownerID, func(context.Context, agent.Scope, string) error { authorizeCalls++; return nil })
	router := chatVisualDashboardRouter(handler)

	for _, test := range []struct {
		name  string
		key   string
		claim string
		body  string
		want  int
	}{
		{name: "missing UUIDv7 key", claim: addChatVisualToDashboardOperation.APIGenOperationID(), body: "{\"title\":\"New dashboard\"}", want: http.StatusUnprocessableEntity},
		{name: "invalid UUIDv7 key", key: "not-a-uuid", claim: addChatVisualToDashboardOperation.APIGenOperationID(), body: "{\"title\":\"New dashboard\"}", want: http.StatusUnprocessableEntity},
		{name: "missing generated operation claim", key: chatVisualDashboardTestCommandID, body: "{\"title\":\"New dashboard\"}", want: http.StatusServiceUnavailable},
		{name: "mismatched generated operation claim", key: chatVisualDashboardTestCommandID, claim: "agent.createRun", body: "{\"title\":\"New dashboard\"}", want: http.StatusServiceUnavailable},
		{name: "new and existing dashboard fields together", body: "{\"dashboardId\":\"dash_sales\",\"pageId\":\"overview\",\"title\":\"New dashboard\"}", want: http.StatusUnprocessableEntity},
		{name: "incomplete existing dashboard selection", body: "{\"dashboardId\":\"dash_sales\"}", want: http.StatusUnprocessableEntity},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := chatVisualAddRequest(fixture, test.body)
			if test.key != "" {
				request.Header.Set("Idempotency-Key", test.key)
			}
			if test.claim != "" {
				request.Header.Set(uicommand.HeaderOperationID, test.claim)
			}
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code != test.want {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.want)
			}
		})
	}
	if authorizeCalls != 2 {
		t.Fatalf("semantic-model authorization ran %d times; only requests with a valid body and generated claim should reach artifact authorization", authorizeCalls)
	}
}

func TestChatVisualDashboardCreateAuditUsesGeneratedAuthoringContext(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/chats/conversation/visuals/artifact/dashboards", nil)
	request.Header.Set("Idempotency-Key", chatVisualDashboardTestCommandID)
	request.Header.Set("X-Request-ID", "request-123")
	request.Header.Set("X-Correlation-ID", "correlation-456")
	ctx, err := chatVisualAuditContext(context.Background(), request, agent.Scope{ProjectID: "project:chat-visual", PrincipalID: "principal-123"}, chatVisualDashboardTestCommandID, "pending-dashboard", "pending-draft")
	if err != nil {
		t.Fatalf("chatVisualAuditContext() error = %v", err)
	}
	intent, ok := authoring.AuditIntentFromContext(ctx)
	if !ok {
		t.Fatal("generated authoring audit intent was not attached to context")
	}
	if intent.Source != "dashboard.authoring" || intent.Operation != addChatVisualToDashboardOperation.APIGenOperationID() {
		t.Fatalf("generated audit identity = %q/%q", intent.Source, intent.Operation)
	}
	if intent.EventID != chatVisualDashboardTestCommandID || intent.ResourceKind != "dashboard" || intent.ResourceID != "pending-dashboard" {
		t.Fatalf("generated audit target = %#v", intent)
	}
	if intent.ActorID != "principal-123" || intent.PrincipalID != "principal-123" || intent.RequestID != "request-123" || intent.CorrelationID != "correlation-456" {
		t.Fatalf("generated audit request identity = %#v", intent)
	}
	if !strings.Contains(intent.MetadataJSON, "pending-dashboard") || !strings.Contains(intent.MetadataJSON, "pending-draft") {
		t.Fatalf("generated audit metadata omitted placeholder identities: %s", intent.MetadataJSON)
	}
}

func TestChatVisualDashboardReceiptRetainsComponentAndOriginatingChat(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/chats/conversation-1/visuals/chart-1/dashboards", nil)
	route := chi.NewRouteContext()
	route.URLParams.Add("conversation", "conversation-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	response := httptest.NewRecorder()
	handler := NewHandler(Options{})
	handler.writeChatVisualDashboardResult(response, request, agent.Scope{}, authoring.DashboardID("dashboard-1"), "Finance", "details", "imported-chart-component")
	var receipt struct {
		ComponentID string `json:"componentId"`
		Href        string `json:"href"`
		PageID      string `json:"pageId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &receipt); err != nil {
		t.Fatal(err)
	}
	href, err := url.Parse(receipt.Href)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ComponentID != "imported-chart-component" || receipt.PageID != "details" || href.Path != "/dashboards/dashboard-1/edit" || href.Query().Get("page") != "details" || href.Query().Get("returnChat") != "conversation-1" {
		t.Fatalf("dashboard receipt loses imported component, page, or originating chat: %+v", receipt)
	}
}
