package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

// The source catalog and all mutation ports are absent: a successful retry
// must need only the retained target and the original conversation artifact.
type chatCopyHTTPRepository struct {
	authoring.Repository
	operation authoring.CreateOperation
	retained  authoring.CreateOperationResult
	target    authoring.DashboardLifecycle
	revision  authoring.Revision
}

func (r *chatCopyHTTPRepository) LookupCreateOperation(_ context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	if operation.ProjectID == r.operation.ProjectID && operation.ActorID == r.operation.ActorID && operation.Kind == r.operation.Kind && operation.IdempotencyKey == r.operation.IdempotencyKey {
		return r.retained, true, nil
	}
	return authoring.CreateOperationResult{}, false, nil
}
func (r *chatCopyHTTPRepository) Get(_ context.Context, project graph.ResourceID, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	if project == r.operation.ProjectID && id == r.target.ID {
		return r.target, nil
	}
	return authoring.DashboardLifecycle{}, authoring.ErrNotFound
}
func (r *chatCopyHTTPRepository) GetRevision(_ context.Context, project graph.ResourceID, id authoring.DashboardID, revision authoring.RevisionID) (authoring.Revision, error) {
	if project == r.operation.ProjectID && id == r.target.ID && revision == r.revision.ID {
		return r.revision, nil
	}
	return authoring.Revision{}, authoring.ErrNotFound
}

type chatCopyHTTPAuthorizer struct {
	calls []authoringservice.AuthorizationRequest
}

func (a *chatCopyHTTPAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.calls = append(a.calls, request)
	return nil
}

type chatCopyHTTPCompiler struct{ authoringservice.Compiler }

func TestChatVisualDashboardCopyRetryDoesNotReloadSource(t *testing.T) {
	fixture := newChatVisualDashboardHTTPFixture(t)
	scope := agent.Scope{ProjectID: "project:chat-visual", PrincipalID: fixture.ownerID}
	handler := fixture.handler(scope.PrincipalID, func(context.Context, agent.Scope, string) error { return nil })
	artifact, err := handler.loadChatVisualArtifact(t.Context(), fixture.service, scope, fixture.conversationID, fixture.artifactID)
	if err != nil {
		t.Fatal(err)
	}
	imported := applicationChatVisualImport(artifact)
	doc := newChatVisualDashboardDocument(imported.SemanticModelID)
	doc.Metadata.ID, doc.Metadata.Name = "saved-copy", "saved-copy"
	if _, err := application.AddChatVisualToDocument(&doc, "overview", imported, authoring.CommandID(chatVisualDashboardTestCommandID)); err != nil {
		t.Fatal(err)
	}
	identity, err := graph.NewServingIdentity(graph.ResourceID(scope.ProjectID), "production", "previous-generation")
	if err != nil {
		t.Fatal(err)
	}
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: scope.PrincipalID, ConversationID: fixture.conversationID, ToolCallID: artifact.ToolCallID, ForkedFrom: &authoring.ForkEvidence{Kind: authoring.ForkSourceProject, Project: &authoring.ProjectForkEvidence{SourceProjectID: graph.ResourceID(scope.ProjectID), SourceDashboardID: "missing-source", Identity: identity}}}
	revision, err := authoring.NewRevision("copy-revision", "saved-copy", 1, time.Now().UTC(), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	target, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{ProjectID: graph.ResourceID(scope.ProjectID), ID: "saved-copy", OwnerPrincipalID: scope.PrincipalID, Slug: "saved-copy", Title: "Original dashboard (copy)", SemanticModel: imported.SemanticModelID, Visibility: authoring.VisibilityPrivate, Draft: &authoring.Draft{ID: "copy-draft", DashboardID: "saved-copy", Revision: revision.Token(), Provenance: provenance}})
	if err != nil {
		t.Fatal(err)
	}
	repo := &chatCopyHTTPRepository{operation: authoring.CreateOperation{ProjectID: graph.ResourceID(scope.ProjectID), ActorID: scope.PrincipalID, Kind: "fork", IdempotencyKey: chatVisualDashboardTestCommandID}, retained: authoring.CreateOperationResult{DashboardID: target.ID, Revision: revision.Token()}, target: target, revision: revision}
	auth := &chatCopyHTTPAuthorizer{}
	svc, err := authoringservice.NewService(authoringservice.Options{Repository: repo, Authorizer: auth, Compiler: chatCopyHTTPCompiler{}, Now: time.Now,
		NewDashboardID: func() (authoring.DashboardID, error) { t.Fatal("replay allocated another dashboard"); return "", nil },
		NewDraftID:     func() (authoring.DraftID, error) { t.Fatal("replay allocated another draft"); return "", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { t.Fatal("replay allocated another revision"); return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.New(application.Options{Authoring: svc, Repository: repo, Authorizer: auth, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) {
		t.Fatal("HTTP replay reloaded the unavailable source")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	handler.options.DashboardAuthoring = app
	request := chatVisualAddRequest(fixture, `{"dashboardId":"missing-source","pageId":"overview"}`)
	request.Header.Set(uicommand.HeaderOperationID, addChatVisualToDashboardOperation.APIGenOperationID())
	request.Header.Set("Idempotency-Key", chatVisualDashboardTestCommandID)
	response := httptest.NewRecorder()
	chatVisualDashboardRouter(handler).ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("retry status=%d body=%s", response.Code, response.Body.String())
	}
	var result struct {
		DashboardID string `json:"dashboardId"`
		PageID      string `json:"pageId"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.DashboardID != "saved-copy" || result.PageID != "overview" {
		t.Fatalf("retry returned %#v", result)
	}
	if len(auth.calls) != 1 || auth.calls[0].DashboardID != "saved-copy" || auth.calls[0].Action != authoring.AuthorizationActionEdit {
		t.Fatalf("replay authorization=%#v", auth.calls)
	}
}
