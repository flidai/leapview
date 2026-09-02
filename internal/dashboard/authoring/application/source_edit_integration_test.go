package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	"github.com/flidai/leapview/internal/dashboard/document"
	projectruntime "github.com/flidai/leapview/internal/project/runtime"
)

func TestSourceEditCommitsCanonicalYAMLAtomicallyAndReplays(t *testing.T) {
	provenance := authoring.Provenance{Origin: authoring.OriginAgent, ActorID: "agent", ConversationID: "conversation", ToolCallID: "edit-source"}
	doc := document.DashboardDocument{
		APIVersion: document.DashboardApiVersionLeapviewDevV1,
		Kind:       document.DashboardResourceKindDashboard,
		Metadata:   document.DashboardMetadata{ID: "dashboard", Name: "dashboard"},
		Spec: document.DashboardSpec{
			SemanticModel: "sales", Filters: []document.DashboardFilter{}, Visuals: map[string]document.DashboardVisual{},
			Pages: []document.DashboardPage{{ID: "overview", Title: "Overview", Components: []document.DashboardPageComponent{}}},
		},
	}
	revision, err := authoring.NewRevision("revision-1", "dashboard", 1, time.Date(2026, 8, 18, 0, 0, 0, 0, time.UTC), doc, provenance)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := authoring.NewDashboardLifecycle(authoring.NewDashboardLifecycleInput{
		ProjectID: "project", ID: "dashboard", OwnerPrincipalID: "agent", Slug: "dashboard", Title: "Dashboard",
		SemanticModel: "sales", Visibility: authoring.VisibilityPrivate,
		Draft: &authoring.Draft{ID: "draft", DashboardID: "dashboard", Revision: revision.Token(), Provenance: provenance},
	})
	if err != nil {
		t.Fatal(err)
	}
	repo := &applicationRepository{
		lifecycle: lifecycle, revisions: map[authoring.RevisionID]authoring.Revision{revision.ID: revision},
		commands: map[authoring.CommandID]authoring.CommandResult{}, fingerprints: map[authoring.CommandID]string{},
	}
	app, err := application.New(application.Options{
		Authoring: newApplicationService(t, repo, &applicationAuthorizer{}), Repository: repo, Authorizer: &applicationAuthorizer{},
		AcquireRuntime: func(context.Context) (projectruntime.Lease, error) { return nil, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	read, err := app.ReadSource(t.Context(), application.DraftRequest{ProjectID: "project", ActorID: "agent", DashboardID: "dashboard"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(read.YAML, "title: Overview") || read.Revision != revision.Token() {
		t.Fatalf("source read = %#v", read)
	}
	request := application.SourceEditRequest{
		ProjectID: "project", ActorID: "agent", DashboardID: "dashboard", DraftID: "draft", ExpectedRevision: revision.Token(),
		Edits:     []application.SourceEdit{{OldText: "title: Overview", NewText: "title: Executive overview"}},
		CommandID: "edit-source", Provenance: provenance,
	}
	result, err := app.EditSource(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Revision.Number != 2 || repo.appendCalls != 1 || !strings.Contains(result.YAML, "title: Executive overview") || !strings.Contains(result.Diff, "+          title: Executive overview") {
		t.Fatalf("source edit = %#v, appends=%d", result, repo.appendCalls)
	}
	replay, err := app.EditSource(t.Context(), request)
	if err != nil || replay.Revision != result.Revision || repo.appendCalls != 1 {
		t.Fatalf("source edit replay = %#v, err=%v, appends=%d", replay, err, repo.appendCalls)
	}
	stale := request
	stale.CommandID = "different-command"
	stale.Provenance.ToolCallID = "different-command"
	if _, err := app.EditSource(t.Context(), stale); !errors.Is(err, authoring.ErrStaleRevision) {
		t.Fatalf("stale source edit error = %v", err)
	}
	if repo.appendCalls != 1 {
		t.Fatalf("stale edit appended %d revisions", repo.appendCalls)
	}
}
