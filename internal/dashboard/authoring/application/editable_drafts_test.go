package application_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/authoring/catalog"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

type editableDraftsRepository struct {
	*applicationRepository
	lifecycles []authoring.DashboardLifecycle
	byID       map[authoring.DashboardID]authoring.DashboardLifecycle
	getErrors  map[authoring.DashboardID]error
	listErr    error
}

func (r *editableDraftsRepository) List(_ context.Context, project projectgraph.ResourceID) ([]authoring.DashboardLifecycle, error) {
	if r.listErr != nil {
		return nil, r.listErr
	}
	if project != "project" {
		return nil, errors.New("unexpected project")
	}
	return append([]authoring.DashboardLifecycle(nil), r.lifecycles...), nil
}

func (r *editableDraftsRepository) Get(_ context.Context, _ projectgraph.ResourceID, id authoring.DashboardID) (authoring.DashboardLifecycle, error) {
	if err := r.getErrors[id]; err != nil {
		return authoring.DashboardLifecycle{}, err
	}
	lifecycle, ok := r.byID[id]
	if !ok {
		return authoring.DashboardLifecycle{}, authoring.ErrNotFound
	}
	return lifecycle, nil
}

type editableDraftsAuthorizer struct {
	calls   []authoringservice.AuthorizationRequest
	results map[authoring.DashboardID]error
}

func (a *editableDraftsAuthorizer) Authorize(_ context.Context, request authoringservice.AuthorizationRequest) error {
	a.calls = append(a.calls, request)
	return a.results[request.DashboardID]
}

func newEditableDraftsApplication(t *testing.T, repository *editableDraftsRepository, authorizer *editableDraftsAuthorizer) *application.Application {
	t.Helper()
	svc, err := authoringservice.NewService(authoringservice.Options{
		Repository: repository, Authorizer: authorizer, Compiler: applicationCompiler{},
		Now:            func() time.Time { return time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC) },
		NewDashboardID: func() (authoring.DashboardID, error) { return "dashboard-generated", nil },
		NewDraftID:     func() (authoring.DraftID, error) { return "draft-generated", nil },
		NewRevisionID:  func() (authoring.RevisionID, error) { return "revision-generated", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	app, err := application.New(application.Options{
		Authoring: svc, Repository: repository, Authorizer: authorizer,
		AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func newEditableDraftLifecycle(t *testing.T, id authoring.DashboardID) (authoring.DashboardLifecycle, authoring.Revision) {
	t.Helper()
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: "owner", ConversationID: "conversation", ToolCallID: "tool-call"}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: id.String(), Name: id.String()},
		Spec: document.DashboardSpec{
			SemanticModel: "sales", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}},
		},
	}
	revision, err := authoring.NewRevision(authoring.RevisionID("revision-"+id.String()), id, 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project", ID: id, OwnerPrincipalID: "owner", Slug: id.String(), Title: id.String(),
		SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: authoring.DraftID("draft-" + id.String()), DashboardID: id, Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle, revision
}

func TestEditableDraftsReturnsOnlyCurrentDraftsAuthorizedForEdit(t *testing.T) {
	editable, editableRevision := newEditableDraftLifecycle(t, "dashboard-editable")
	forbidden, forbiddenRevision := newEditableDraftLifecycle(t, "dashboard-forbidden")
	tracedOut, _ := newEditableDraftLifecycle(t, "dashboard-raced-out")
	withoutDraft := editable
	withoutDraft.ID = "dashboard-no-draft"
	withoutDraft.Draft = nil
	archived := editable
	archived.ID = "dashboard-archived"
	archived.Status = authoring.LifecycleStatusArchived

	repository := &editableDraftsRepository{
		applicationRepository: &applicationRepository{revisions: map[authoring.RevisionID]authoring.Revision{
			editableRevision.ID: editableRevision, forbiddenRevision.ID: forbiddenRevision,
		}},
		lifecycles: []authoring.DashboardLifecycle{editable, forbidden, tracedOut, withoutDraft, archived},
		byID: map[authoring.DashboardID]authoring.DashboardLifecycle{
			editable.ID: editable, forbidden.ID: forbidden,
		},
		getErrors: map[authoring.DashboardID]error{tracedOut.ID: authoring.ErrNotFound},
	}
	authorizer := &editableDraftsAuthorizer{results: map[authoring.DashboardID]error{forbidden.ID: access.ErrForbidden}}
	app := newEditableDraftsApplication(t, repository, authorizer)

	got, err := app.EditableDrafts(t.Context(), catalog.ListRequest{ProjectID: "project", ActorID: " owner "})
	if err != nil {
		t.Fatalf("EditableDrafts() error = %v", err)
	}
	if len(got) != 1 || got[0].Lifecycle.ID != editable.ID || got[0].Revision.Token() != editableRevision.Token() {
		t.Fatalf("editable drafts = %#v, want only %q at the current revision", got, editable.ID)
	}
	if len(authorizer.calls) != 2 {
		t.Fatalf("EDIT authorization calls = %d, want editable and forbidden drafts only", len(authorizer.calls))
	}
	for _, call := range authorizer.calls {
		if call.ActorID != "owner" || call.ProjectID != "project" || call.Action != authoring.AuthorizationActionEdit || call.Target != authoringservice.AuthorizationTargetAuthoredDashboard {
			t.Fatalf("draft authorization request = %#v, want an authored dashboard EDIT decision", call)
		}
	}
}

func TestEditableDraftsPropagatesUnexpectedAuthorizationAndListErrors(t *testing.T) {
	lifecycle, _ := newEditableDraftLifecycle(t, "dashboard-editable")
	authorizationErr := errors.New("authorizer unavailable")
	tests := []struct {
		name       string
		repository *editableDraftsRepository
		authorizer *editableDraftsAuthorizer
		wantErr    error
	}{
		{
			name:       "authorization error",
			repository: &editableDraftsRepository{applicationRepository: &applicationRepository{}, lifecycles: []authoring.DashboardLifecycle{lifecycle}, byID: map[authoring.DashboardID]authoring.DashboardLifecycle{lifecycle.ID: lifecycle}},
			authorizer: &editableDraftsAuthorizer{results: map[authoring.DashboardID]error{lifecycle.ID: authorizationErr}},
			wantErr:    authorizationErr,
		},
		{
			name:       "list error",
			repository: &editableDraftsRepository{applicationRepository: &applicationRepository{}, listErr: authorizationErr},
			authorizer: &editableDraftsAuthorizer{},
			wantErr:    authorizationErr,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			app := newEditableDraftsApplication(t, test.repository, test.authorizer)
			if _, err := app.EditableDrafts(t.Context(), catalog.ListRequest{ProjectID: "project", ActorID: "owner"}); !errors.Is(err, test.wantErr) {
				t.Fatalf("EditableDrafts() error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestEditableDraftsRequiresConfiguredApplicationProjectAndActor(t *testing.T) {
	request := catalog.ListRequest{ProjectID: "project", ActorID: "owner"}
	var unconfigured *application.Application
	if _, err := unconfigured.EditableDrafts(t.Context(), request); err == nil {
		t.Fatal("EditableDrafts() accepted an unconfigured application")
	}

	repository := &editableDraftsRepository{applicationRepository: &applicationRepository{}}
	app := newEditableDraftsApplication(t, repository, &editableDraftsAuthorizer{})
	for _, test := range []struct {
		name    string
		request catalog.ListRequest
	}{
		{name: "project", request: catalog.ListRequest{ActorID: "owner"}},
		{name: "actor", request: catalog.ListRequest{ProjectID: "project"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := app.EditableDrafts(t.Context(), test.request); err == nil {
				t.Fatalf("EditableDrafts() accepted missing %s", test.name)
			}
		})
	}
}

var _ authoring.Repository = (*editableDraftsRepository)(nil)
var _ authoringservice.Authorizer = (*editableDraftsAuthorizer)(nil)
