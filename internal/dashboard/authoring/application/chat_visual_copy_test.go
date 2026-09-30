package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type chatCopyReplayRepository struct {
	*applicationRepository
	operation     authoring.CreateOperationResult
	revisionReads int
}

func (r *chatCopyReplayRepository) LookupCreateOperation(_ context.Context, operation authoring.CreateOperation) (authoring.CreateOperationResult, bool, error) {
	if err := operation.ValidateKey(); err != nil {
		return authoring.CreateOperationResult{}, false, err
	}
	if operation.ProjectID != "sales" || operation.ActorID != "owner" || operation.Kind != "fork" || operation.IdempotencyKey != string(chatVisualCommandID) {
		return authoring.CreateOperationResult{}, false, nil
	}
	return r.operation, true, nil
}
func (r *chatCopyReplayRepository) Get(ctx context.Context, project graph.ResourceID, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	if id != "dashboard" {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	return r.applicationRepository.Get(ctx, project, id)
}
func (r *chatCopyReplayRepository) GetRevision(ctx context.Context, project graph.ResourceID, id authoring.DashboardID, revision authoring.RevisionID) (authoring.Revision, error) {
	r.revisionReads++
	if id != "dashboard" {
		return authoring.Revision{}, authoring.ErrNotFound
	}
	return r.applicationRepository.GetRevision(ctx, project, id, revision)
}

type chatCopyReplayAuthorizer struct {
	deny  bool
	calls []service.AuthorizationRequest
}

func (a *chatCopyReplayAuthorizer) Authorize(_ context.Context, request service.AuthorizationRequest) error {
	a.calls = append(a.calls, request)
	if a.deny {
		return access.ErrForbidden
	}
	return nil
}

func newChatCopyReplayFixture(t *testing.T, kind authoring.ForkSourceKind) (*application.Application, *chatCopyReplayRepository, *chatCopyReplayAuthorizer, application.AddChatVisualRequest) {
	t.Helper()
	document := chatVisualIntegrationDocument()
	source := chatVisualIntegrationSource()
	if _, err := application.AddChatVisualToDocument(&document, "overview", source, chatVisualCommandID); err != nil {
		t.Fatal(err)
	}
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: "owner", ConversationID: "conversation", ToolCallID: source.ToolCallID}
	fork := &authoring.ForkEvidence{Kind: kind}
	if kind == authoring.ForkSourceProject {
		identity, err := graph.NewServingIdentity("sales", "production", "old-source-generation")
		if err != nil {
			t.Fatal(err)
		}
		fork.Project = &authoring.ProjectForkEvidence{SourceProjectID: "sales", SourceDashboardID: "source-dashboard", Identity: identity}
	} else {
		sourceDoc := chatVisualIntegrationDocument()
		sourceDoc.Metadata.ID = "source-dashboard"
		revision, err := authoring.NewRevision("source-revision", "source-dashboard", 1, time.Now().UTC(), sourceDoc, authoring.Provenance{Origin: authoring.OriginUI, ActorID: "owner"})
		if err != nil {
			t.Fatal(err)
		}
		fork.Instance = &authoring.InstanceForkEvidence{SourceProjectID: "sales", SourceDashboardID: "source-dashboard", SourceRevision: revision.Token()}
	}
	storedProvenance := provenance
	storedProvenance.ForkedFrom = fork
	revision, err := authoring.NewRevision("copy-revision", "dashboard", 1, time.Now().UTC(), document, storedProvenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "sales", ID: "dashboard", OwnerPrincipalID: "owner", Slug: "copy", Title: "Original source (copy)", SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "copy-draft", DashboardID: "dashboard", Revision: revision.Token(), Provenance: storedProvenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &chatCopyReplayRepository{applicationRepository: &applicationRepository{lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{revision.ID: revision}}, operation: authoring.CreateOperationResult{DashboardID: lifecycle.ID, Revision: revision.Token(), Fingerprint: "original-document-fingerprint"}}
	auth := &chatCopyReplayAuthorizer{}
	svc, err := service.NewService(service.Options{Repository: repo, Authorizer: auth, Compiler: applicationCompiler{},
		Now:            time.Now,
		NewDashboardID: func() (authoring.DashboardID, error) { t.Fatal("replay allocated a dashboard"); return "", nil },
		NewDraftID:     func() (authoring.DraftID, error) { t.Fatal("replay allocated a draft"); return "", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { t.Fatal("replay allocated a revision"); return "", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.New(application.Options{Authoring: svc, Repository: repo, Authorizer: auth, AcquireRuntime: func(context.Context) (projectruntime.Lease, error) {
		t.Fatal("replay tried to load the moving source runtime")
		return nil, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	return app, repo, auth, application.AddChatVisualRequest{ProjectID: "sales", ActorID: "owner", DashboardID: "source-dashboard", PageID: "overview", Source: source, CommandID: chatVisualCommandID, Provenance: provenance}
}

func TestChatVisualCopyReplayUsesRetainedResultWithoutSource(t *testing.T) {
	for _, kind := range []authoring.ForkSourceKind{authoring.ForkSourceInstance, authoring.ForkSourceProject} {
		t.Run(string(kind), func(t *testing.T) {
			app, repo, auth, request := newChatCopyReplayFixture(t, kind)
			// The source is deliberately unavailable; only the retained copy exists.
			// A later target edit must not change the revision returned by a retry.
			repo.lifecycle.Title = "Renamed copy"
			repo.lifecycle.Draft.Revision.RevisionID = "later-target-revision"
			result, found, err := app.LookupChatVisualCopyReplay(t.Context(), request)
			if err != nil || !found || result.Revision != repo.operation.Revision || result.Lifecycle.ID != "dashboard" {
				t.Fatalf("replay=%#v found=%v err=%v", result, found, err)
			}
			if len(auth.calls) != 1 || auth.calls[0].DashboardID != "dashboard" || auth.calls[0].Action != authoring.AuthorizationActionEdit {
				t.Fatalf("authorization=%#v", auth.calls)
			}
		})
	}
}

func TestChatVisualCopyReplayRejectsChangedIntent(t *testing.T) {
	for name, mutate := range map[string]func(*application.AddChatVisualRequest){
		"source dashboard": func(r *application.AddChatVisualRequest) { r.DashboardID = "another-source" },
		"page":             func(r *application.AddChatVisualRequest) { r.PageID = "summary" },
		"visual":           func(r *application.AddChatVisualRequest) { r.Source.Visual.Title = stringPtr("Changed visual") },
		"filters":          func(r *application.AddChatVisualRequest) { r.Source.Filters[0].Dimension = "country" },
		"conversation":     func(r *application.AddChatVisualRequest) { r.Provenance.ConversationID = "another-conversation" },
		"tool":             func(r *application.AddChatVisualRequest) { r.Provenance.ToolCallID = "another-tool" },
	} {
		t.Run(name, func(t *testing.T) {
			app, _, _, request := newChatCopyReplayFixture(t, authoring.ForkSourceProject)
			mutate(&request)
			if _, _, err := app.LookupChatVisualCopyReplay(t.Context(), request); !errors.Is(err, authoring.ErrCommandReuse) {
				t.Fatalf("changed intent error=%v", err)
			}
		})
	}
}

func TestChatVisualCopyReplayAuthorizesBeforeReadingOrComparingResult(t *testing.T) {
	app, repo, auth, request := newChatCopyReplayFixture(t, authoring.ForkSourceProject)
	auth.deny = true
	request.PageID = "changed"
	if _, _, err := app.LookupChatVisualCopyReplay(t.Context(), request); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("revoked access error=%v", err)
	}
	if repo.revisionReads != 0 {
		t.Fatalf("read %d revisions before authorization", repo.revisionReads)
	}
}
