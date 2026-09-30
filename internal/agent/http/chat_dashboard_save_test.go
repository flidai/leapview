package http

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	agentpostgres "github.com/flidai/leapview/internal/agent/postgres"
	agenttools "github.com/flidai/leapview/internal/agent/tools"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
	"github.com/go-chi/chi/v5"
)

type dashboardSaveRepository struct {
	chatCopyHTTPRepository
	creates int
	intent  access.AuditIntent
}

func (r *dashboardSaveRepository) Create(ctx context.Context, input authoring.CreateInput) (authoring.DashboardLifecycle, error) {
	r.creates++
	r.operation, r.target, r.revision = input.Operation, input.Lifecycle, input.Revision
	r.retained = authoring.CreateOperationResult{DashboardID: input.Lifecycle.ID, Revision: input.Revision.Token(), Fingerprint: input.Operation.Fingerprint}
	r.intent, _ = authoring.AuditIntentFromContext(ctx)
	return input.Lifecycle, nil
}

func TestSaveChatDashboardCreatesAllCardsOnceAndEnforcesPreviewRevision(t *testing.T) {
	fixture := openAgentHTTPPostgresFixture(t, agentpostgres.Options{})
	owner, err := fixture.Access.UpsertPrincipal(t.Context(), access.PrincipalInput{Email: "dashboard-chat-save@example.com", DisplayName: "Dashboard owner"})
	if err != nil {
		t.Fatal(err)
	}
	scope := agent.Scope{PrincipalID: owner.ID, ProjectID: "project:chat-save"}
	agentService := agent.NewService(fixture.Agent, agent.Config{APIKey: "test", Model: "test"})
	conversation, err := agentService.CreateConversation(t.Context(), scope, "Build a dashboard")
	if err != nil {
		t.Fatal(err)
	}
	revision := appendHTTPDashboardDraft(t, fixture.Agent, scope, conversation.ID)
	repository := &dashboardSaveRepository{}
	authorizer := &chatCopyHTTPAuthorizer{}
	service, err := authoringservice.NewService(authoringservice.Options{Repository: repository, Authorizer: authorizer, Compiler: chatCopyHTTPCompiler{}, Now: func() time.Time { return time.Now().UTC() },
		NewDashboardID: func() (authoring.DashboardID, error) { return "saved-dashboard", nil },
		NewDraftID:     func() (authoring.DraftID, error) { return "saved-draft", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return "saved-revision", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.New(application.Options{Authoring: service, Repository: repository, Authorizer: authorizer, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return nil, nil }})
	if err != nil {
		t.Fatal(err)
	}
	semanticAllowed := true
	handler := NewHandler(Options{Service: agentService, ActiveProjectID: scope.ProjectID, CurrentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: scope.PrincipalID}, true },
		DashboardAuthoring: app, AuthorizeSemanticModel: func(_ context.Context, _ agent.Scope, model string) error {
			if model != "semantic-model:finance" {
				t.Fatalf("unexpected model %q", model)
			}
			if !semanticAllowed {
				return access.ErrForbidden
			}
			return nil
		},
	})
	router := chi.NewRouter()
	router.Post("/chats/{conversation}/dashboard", handler.SaveChatDashboardDraftUI)
	save := func(revision, title string, key string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"revision": revision, "title": title})
		request := httptest.NewRequest(http.MethodPost, "/chats/"+conversation.ID+"/dashboard", strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", key)
		request.Header.Set(uicommand.HeaderOperationID, saveChatDashboardDraftOperation.APIGenOperationID())
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		return response
	}
	if response := save("stale", "Performance", chatVisualDashboardTestCommandID); response.Code != http.StatusConflict {
		t.Fatalf("stale preview status=%d body=%s", response.Code, response.Body.String())
	}
	if repository.creates != 0 {
		t.Fatal("stale preview created dashboard")
	}
	for range 2 {
		response := save(revision, "Performance", chatVisualDashboardTestCommandID)
		if response.Code != http.StatusOK {
			t.Fatalf("save status=%d body=%s", response.Code, response.Body.String())
		}
	}
	if repository.creates != 1 || len(repository.revision.Document.Spec.Visuals) != 2 || len(repository.revision.Document.Spec.Pages[0].Components) != 2 {
		t.Fatalf("create count/cards=%d/%d", repository.creates, len(repository.revision.Document.Spec.Visuals))
	}
	if repository.target.Visibility != authoring.VisibilityPrivate || repository.revision.Provenance.ConversationID != conversation.ID || repository.revision.Provenance.ToolCallID != "compose-test" {
		t.Fatal("save lost privacy or conversation provenance")
	}
	if repository.intent.Action != "agent.dashboard.saved" || repository.intent.Operation != "saveChatDashboardDraft" {
		t.Fatalf("save audit intent=%#v", repository.intent)
	}
	if response := save(revision, "Changed title", chatVisualDashboardTestCommandID); response.Code != http.StatusConflict {
		t.Fatalf("changed retry accepted: %d %s", response.Code, response.Body.String())
	}
	semanticAllowed = false
	if response := save(revision, "Performance", chatVisualDashboardTestCommandID); response.Code != http.StatusForbidden {
		t.Fatalf("revoked model retry accepted: %d %s", response.Code, response.Body.String())
	}
	if repository.creates != 1 {
		t.Fatal("retry created duplicate dashboard")
	}
	handler.options.CurrentPrincipal = func(*http.Request) (Principal, bool) { return Principal{ID: "different-owner"}, true }
	if response := save(revision, "Performance", chatVisualDashboardTestCommandID); response.Code != http.StatusNotFound {
		t.Fatalf("another principal accessed draft: %d %s", response.Code, response.Body.String())
	}
}

// Persist an authored tool invocation and its valid renderer envelopes, just
// as run completion does. Production never accepts these objects from Save.
func appendHTTPDashboardDraft(t *testing.T, repository *agentpostgres.Repository, scope agent.Scope, conversationID string) string {
	t.Helper()
	var visual document.DashboardVisual
	if err := json.Unmarshal([]byte(`{"type":"bar","title":"Revenue","query":{"type":"aggregate","dimensions":["country"],"metrics":["revenue"]},"presentation":{"type":"cartesian"}}`), &visual); err != nil {
		t.Fatal(err)
	}
	normalized, err := agenttools.NormalizeChatVisualDefinition(visual)
	if err != nil {
		t.Fatal(err)
	}
	input := agenttools.ComposeChatDashboardInput{Title: "Performance", SemanticModelID: "semantic-model:finance", Visuals: []agenttools.ChatDashboardVisualInput{{ID: "revenue", Visual: normalized}, {ID: "comparison", Visual: normalized}}}
	callID, partID := "compose-test", "compose-part"
	revision, err := agenttools.ChatDashboardDraftRevision(callID, input)
	if err != nil {
		t.Fatal(err)
	}
	arguments, _ := json.Marshal(input)
	assistant, _ := json.Marshal(map[string]any{"tool_calls": []any{map[string]any{"id": callID, "name": "compose_chat_dashboard", "arguments": json.RawMessage(arguments), "output_part_id": partID}}})
	if _, err := repository.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: scope.PrincipalID, ConversationID: conversationID, Role: agent.MessageRoleAssistant, ContentJSON: string(assistant)}); err != nil {
		t.Fatal(err)
	}
	fixture, err := os.ReadFile("../../../api/visualization/conformance/cartesian-inline.json")
	if err != nil {
		t.Fatal(err)
	}
	envelopes := map[string]visualizationir.VisualizationEnvelope{}
	cards := []map[string]string{}
	for _, card := range input.Visuals {
		sum := sha256.Sum256([]byte(callID + "\x00" + card.ID))
		artifactID := "agent_visual_compose_" + hex.EncodeToString(sum[:16])
		var envelope visualizationir.VisualizationEnvelope
		if err := json.Unmarshal(fixture, &envelope); err != nil {
			t.Fatal(err)
		}
		envelope.VisualID = artifactID
		if err := visualizationir.ValidateEnvelope(envelope); err != nil {
			t.Fatal(err)
		}
		envelopes[artifactID] = envelope
		cards = append(cards, map[string]string{"id": card.ID, "artifactId": artifactID, "title": "Revenue"})
	}
	display := map[string]any{"type": "dashboard_draft", "id": callID, "revision": revision, "title": input.Title, "semanticModelId": input.SemanticModelID, "visuals": cards, "summary": "Dashboard composed", "patch": map[string]any{"visuals": envelopes}}
	tool, _ := json.Marshal(map[string]any{"display_content": display, "output_part_id": partID})
	if _, err := repository.AppendMessage(t.Context(), agent.MessageInput{PrincipalID: scope.PrincipalID, ConversationID: conversationID, Role: agent.MessageRoleTool, ToolCallID: callID, ToolName: "compose_chat_dashboard", ContentJSON: string(tool)}); err != nil {
		t.Fatal(fmt.Errorf("persist dashboard fixture: %w", err))
	}
	return revision
}

var _ authoring.CreateOperationRepository = (*dashboardSaveRepository)(nil)
