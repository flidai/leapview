package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/agent"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	agentcore "github.com/flidai/leapview/pkg/agent"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
)

// TestPostgresAgentAdminJourney is the bounded PostgreSQL-native application
// journey for agent identity isolation, one durable model turn, and platform
// administration. It deliberately assembles a second access surface with
// explicit bearer authentication because the shared journey fixture's default
// disabled-auth surface is intended for route smoke tests, not identity tests.
func TestPostgresAgentAdminJourney(t *testing.T) {
	fixture := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{SkipRouteAssembly: true})
	ctx := t.Context()

	owner, err := fixture.SeedPrincipal(ctx, access.PrincipalInput{
		ID: journeyUUIDv7(t), Kind: access.PrincipalKindUser,
		Email: "journey-owner@example.com", DisplayName: "Journey Owner",
	})
	if err != nil {
		t.Fatalf("seed owner principal: %v", err)
	}
	viewer, err := fixture.SeedPrincipal(ctx, access.PrincipalInput{
		ID: journeyUUIDv7(t), Kind: access.PrincipalKindUser,
		Email: "journey-viewer@example.com", DisplayName: "Journey Viewer",
	})
	if err != nil {
		t.Fatalf("seed viewer principal: %v", err)
	}
	if _, err := fixture.Graph.Access.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: owner.ID, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatalf("grant owner platform-admin role: %v", err)
	}
	ownerToken, _, err := fixture.Graph.Access.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: owner.ID, Name: "journey-owner", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create owner API token: %v", err)
	}
	viewerToken, _, err := fixture.Graph.Access.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: viewer.ID, Name: "journey-viewer", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatalf("create viewer API token: %v", err)
	}

	service := agent.NewService(fixture.Graph.AgentRepository, agent.Config{APIKey: "journey-key", Model: "journey-fake-model"}, agent.WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{
			Content: "bounded PostgreSQL answer", FinishReason: agentcore.FinishReasonStop,
			Usage: agentcore.Usage{InputTokens: 3, OutputTokens: 4, TotalTokens: 7},
		}, nil
	})))
	auth := accessmodule.NewAuth(fixture.AccessPersistence.Repository, accessmodule.AuthConfig{
		APITokenOnly: true, CSRFKey: strings.Repeat("journey-auth", 4),
	})
	accessSurface, err := accessmodule.Build(ctx, accessmodule.Config{
		ExistingAuth: auth,
		CurrentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return postgresJourneyProject, nil
		},
	})
	if err != nil {
		t.Fatalf("build authenticated PostgreSQL access surface: %v", err)
	}

	routes, runtime, platform, policy, err := buildApplicationSurfaces(ctx, nil,
		dataAssemblyInputs{
			PlatformHealth: fixture.RuntimePool, ServingStateRepo: fixture.Graph.ServingState,
			AccessRepo: fixture.Graph.Access, APIIdempotency: fixture.Graph.Idempotency,
			CursorSigning: fixture.Graph.CursorSigning, RequireExplicitAPIProtocol: true,
		},
		capabilityAssemblyInputs{
			JobModule: fixture.JobsModule, AccessModule: accessSurface,
			AgentPersistence: fixture.Graph.AgentPersistence, Agent: service,
		},
		workflowAssemblyInputs{
			// Leave the optional system-prompt settings provider unset: the
			// fixture's bootstrap authority intentionally has no seeded setting,
			// and the agent service's built-in default prompt is sufficient for
			// this bounded fake-model turn.
			AgentConfig: agentmodule.ModelConfig{APIKey: "journey-key", Model: "journey-fake-model"},
			Auth:        auth, Workload: fixture.Workload,
		},
		runtimeAssemblyInputs{
			ProjectID:               postgresJourneyProject,
			ProjectIDResolver:       func(context.Context) (projectgraph.ResourceID, error) { return postgresJourneyProject, nil },
			ServingSnapshotResolver: func(context.Context) (string, error) { return "journey-snapshot", nil },
			InstanceID:              postgresJourneyTargetID, DefaultEnvironment: "prod",
		},
		httpAssemblyInputs{PublicURL: "http://localhost"},
	)
	if err != nil {
		t.Fatalf("assemble authenticated PostgreSQL journey surfaces: %v", err)
	}
	handler := Routes(routes, runtime, platform, policy)

	createPath := "/api/v1/agent/conversations"
	create := journeyAgentRequest(t, http.MethodPost, createPath, ownerToken, `{"title":"Owner conversation"}`)
	create.Header.Set("Idempotency-Key", journeyUUIDv7(t))
	createResponse := httptest.NewRecorder()
	handler.ServeHTTP(createResponse, create)
	if createResponse.Code != http.StatusCreated {
		t.Fatalf("owner conversation create status=%d body=%s", createResponse.Code, createResponse.Body.String())
	}
	var conversation struct {
		ID          string `json:"id"`
		PrincipalID string `json:"principalId"`
		Title       string `json:"title"`
	}
	if err := json.Unmarshal(createResponse.Body.Bytes(), &conversation); err != nil {
		t.Fatalf("decode owner conversation response: %v", err)
	}
	if conversation.ID == "" || conversation.PrincipalID != owner.ID || conversation.Title != "Owner conversation" {
		t.Fatalf("owner conversation response=%#v", conversation)
	}
	stored, err := fixture.Graph.AgentRepository.GetConversation(ctx, owner.ID, conversation.ID)
	if err != nil {
		t.Fatalf("read owner conversation from PostgreSQL authority: %v", err)
	}
	if stored.PrincipalID != owner.ID || stored.Title != conversation.Title {
		t.Fatalf("stored owner conversation=%#v", stored)
	}

	ownerList := journeyAgentRequest(t, http.MethodGet, createPath, ownerToken, "")
	ownerListResponse := httptest.NewRecorder()
	handler.ServeHTTP(ownerListResponse, ownerList)
	if ownerListResponse.Code != http.StatusOK || !strings.Contains(ownerListResponse.Body.String(), conversation.ID) {
		t.Fatalf("owner conversation list status=%d body=%s", ownerListResponse.Code, ownerListResponse.Body.String())
	}
	viewerList := journeyAgentRequest(t, http.MethodGet, createPath, viewerToken, "")
	viewerListResponse := httptest.NewRecorder()
	handler.ServeHTTP(viewerListResponse, viewerList)
	if viewerListResponse.Code != http.StatusOK || strings.Contains(viewerListResponse.Body.String(), conversation.ID) {
		t.Fatalf("viewer isolation status=%d body=%s", viewerListResponse.Code, viewerListResponse.Body.String())
	}
	if conversations, err := fixture.Graph.AgentRepository.ListConversations(ctx, viewer.ID); err != nil {
		t.Fatalf("list viewer conversations from PostgreSQL authority: %v", err)
	} else if len(conversations) != 0 {
		t.Fatalf("viewer PostgreSQL conversations=%#v, want empty", conversations)
	}

	// The generated create-run command is asynchronous. Start the fixture's
	// bounded PostgreSQL worker only after the identity assertions above, then
	// wait for one deterministic fake-model completion.
	if err := fixture.JobsModule.Start(ctx); err != nil {
		t.Fatalf("start PostgreSQL agent worker: %v", err)
	}
	runRequest := journeyAgentRequest(t, http.MethodPost, "/api/v1/agent/conversations/"+conversation.ID+"/runs", ownerToken, `{"input":"What is persisted?"}`)
	runRequest.Header.Set("Idempotency-Key", journeyUUIDv7(t))
	runResponse := httptest.NewRecorder()
	handler.ServeHTTP(runResponse, runRequest)
	if runResponse.Code != http.StatusAccepted {
		t.Fatalf("agent run create status=%d body=%s", runResponse.Code, runResponse.Body.String())
	}
	var run struct {
		ID             string `json:"id"`
		ConversationID string `json:"conversationId"`
		Status         string `json:"status"`
	}
	if err := json.Unmarshal(runResponse.Body.Bytes(), &run); err != nil {
		t.Fatalf("decode agent run response: %v", err)
	}
	if run.ID == "" || run.ConversationID != conversation.ID || run.Status != agent.RunStatusRunning {
		t.Fatalf("queued run response=%#v", run)
	}

	deadline := time.Now().Add(8 * time.Second)
	var completed agent.Run
	for time.Now().Before(deadline) {
		completed, err = fixture.Graph.AgentRepository.GetRun(ctx, owner.ID, conversation.ID, run.ID)
		if err == nil && completed.Status == agent.RunStatusCompleted {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if err != nil || completed.Status != agent.RunStatusCompleted {
		t.Fatalf("completed PostgreSQL run=%#v err=%v", completed, err)
	}
	messages, err := fixture.Graph.AgentRepository.ListMessages(ctx, owner.ID, conversation.ID)
	if err != nil {
		t.Fatalf("list PostgreSQL agent messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != agent.MessageRoleUser || messages[0].ContentText != "What is persisted?" || messages[1].Role != agent.MessageRoleAssistant || messages[1].ContentText != "bounded PostgreSQL answer" {
		t.Fatalf("PostgreSQL agent messages=%#v", messages)
	}
	events, err := fixture.Graph.AgentRepository.ListEvents(ctx, owner.ID, run.ID)
	if err != nil {
		t.Fatalf("list PostgreSQL agent events: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("PostgreSQL agent run emitted no durable events")
	}
	job, err := fixture.JobsModule.Get(ctx, "agent:"+run.ID+":run")
	if err != nil {
		t.Fatalf("read completed PostgreSQL agent job: %v", err)
	}
	if job.Status != jobs.StatusSucceeded || job.Attempts != 1 || job.FinishedAt == "" {
		t.Fatalf("completed PostgreSQL agent job=%#v", job)
	}
	if err := fixture.JobsModule.Stop(ctx); err != nil {
		t.Fatalf("stop PostgreSQL agent worker before direct fence assertions: %v", err)
	}

	// Platform administration is durable and independent of any serving or
	// DuckLake runtime. A viewer receives 403 while the owner gets the storage
	// shell, whose signal bootstrap points at the canonical updates URL.
	viewerAdmin := journeyAgentRequest(t, http.MethodGet, "/admin/storage", viewerToken, "")
	viewerAdminResponse := httptest.NewRecorder()
	handler.ServeHTTP(viewerAdminResponse, viewerAdmin)
	if viewerAdminResponse.Code != http.StatusForbidden {
		t.Fatalf("viewer admin storage status=%d body=%s", viewerAdminResponse.Code, viewerAdminResponse.Body.String())
	}
	ownerAdmin := journeyAgentRequest(t, http.MethodGet, "/admin/storage", ownerToken, "")
	ownerAdminResponse := httptest.NewRecorder()
	handler.ServeHTTP(ownerAdminResponse, ownerAdmin)
	if ownerAdminResponse.Code != http.StatusOK || !strings.Contains(ownerAdminResponse.Body.String(), "section=\"storage\"") || !strings.Contains(ownerAdminResponse.Body.String(), "/updates?route=admin&amp;section=storage") {
		t.Fatalf("owner admin storage status=%d body=%s", ownerAdminResponse.Code, ownerAdminResponse.Body.String())
	}
}

func journeyUUIDv7(t *testing.T) string {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("generate UUIDv7: %v", err)
	}
	return id.String()
}

func journeyAgentRequest(t *testing.T, method, path, token, body string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Host = "localhost"
	request.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	return request
}
