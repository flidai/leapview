package application_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/application"
	authoringservice "github.com/flidai/leapview/internal/dashboard/authoring/service"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

func TestDraftDeniedBeforeRevisionLookup(t *testing.T) {
	repository, lifecycle, _ := previewRepository(t)
	authorizer := &recordingAuthorizer{err: access.ErrForbidden}
	app := newApplication(t, repository, authorizer, func(context.Context, string) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})

	_, err := app.Draft(context.Background(), application.DraftRequest{ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("denied draft error = %v, want forbidden", err)
	}
	if repository.getRevisionCalls != 0 {
		t.Fatalf("denied draft looked up revision %d times", repository.getRevisionCalls)
	}
}

func TestRevisionDeniedBeforeRevisionLookup(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	authorizer := &recordingAuthorizer{err: access.ErrForbidden}
	app := newApplication(t, repository, authorizer, func(context.Context, string) (runtimehost.Lease, error) {
		return nil, errors.New("runtime should not be acquired")
	})

	_, err := app.Revision(context.Background(), application.RevisionRequest{
		ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
		DraftID: lifecycle.Draft.ID, RevisionID: revision.ID, Action: authoring.AuthorizationActionEdit,
	})
	if !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("denied revision error = %v, want forbidden", err)
	}
	if repository.getRevisionCalls != 0 {
		t.Fatalf("denied revision looked up revision %d times", repository.getRevisionCalls)
	}
}

func TestRevisionRequiresExactCurrentDraftPointer(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	app := newApplication(t, repository, &recordingAuthorizer{}, func(context.Context, string) (runtimehost.Lease, error) { return nil, nil })

	for _, test := range []struct {
		name      string
		draftID   authoring.DraftID
		revision  authoring.RevisionID
		wantCalls int
	}{
		{name: "wrong draft", draftID: "other-draft", revision: revision.ID},
		{name: "wrong revision", draftID: lifecycle.Draft.ID, revision: "other-revision"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository.getRevisionCalls = 0
			_, err := app.Revision(context.Background(), application.RevisionRequest{
				ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
				DraftID: test.draftID, RevisionID: test.revision, Action: authoring.AuthorizationActionEdit,
			})
			if !errors.Is(err, authoring.ErrNotFound) {
				t.Fatalf("error = %v, want not found", err)
			}
			if repository.getRevisionCalls != test.wantCalls {
				t.Fatalf("revision lookups = %d, want %d", repository.getRevisionCalls, test.wantCalls)
			}
		})
	}

	got, err := app.Revision(context.Background(), application.RevisionRequest{
		ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
		DraftID: lifecycle.Draft.ID, RevisionID: revision.ID, Action: authoring.AuthorizationActionEdit,
	})
	if err != nil || got.ID != revision.ID {
		t.Fatalf("exact draft revision = %#v err=%v", got, err)
	}
}

func TestRevisionRequiresExactPublishedPointerForView(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	published := lifecycle
	published.Status = authoring.LifecycleStatusPublished
	published.Published = &authoring.Published{
		Revision: revision.Token(),
		Compilation: authoring.CompiledRevisionToken{
			AuthoredRevision: revision.Token(),
			DefinitionHash:   "sha256:" + strings.Repeat("a", 64),
			SemanticIdentity: func() projectgraph.ServingIdentity {
				identity, _ := projectgraph.NewServingIdentity("project", "production", "state-1")
				return identity
			}(),
		},
		PublishedAt: time.Date(2026, 8, 15, 13, 0, 0, 0, time.UTC),
		Provenance:  revision.Provenance,
	}
	if err := published.Validate(); err != nil {
		t.Fatalf("published fixture invalid: %v", err)
	}
	repository.lifecycles[lifecycle.ID] = published
	authApp := newApplication(t, repository, &recordingAuthorizer{}, func(context.Context, string) (runtimehost.Lease, error) { return nil, nil })

	_, err := authApp.Revision(context.Background(), application.RevisionRequest{
		ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
		RevisionID: "other-revision", Action: authoring.AuthorizationActionView,
	})
	if !errors.Is(err, authoring.ErrNotFound) || repository.getRevisionCalls != 0 {
		t.Fatalf("wrong published pointer error=%v lookups=%d", err, repository.getRevisionCalls)
	}
	repository.getRevisionCalls = 0
	got, err := authApp.Revision(context.Background(), application.RevisionRequest{
		ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
		RevisionID: revision.ID, Action: authoring.AuthorizationActionView,
	})
	if err != nil || got.ID != revision.ID || repository.getRevisionCalls != 1 {
		t.Fatalf("exact published revision=%#v err=%v lookups=%d", got, err, repository.getRevisionCalls)
	}
}

func TestArchivedRevisionDoesNotDiscloseRetainedPointer(t *testing.T) {
	repository, lifecycle, revision := previewRepository(t)
	archived := lifecycle
	archived.Status = authoring.LifecycleStatusArchived
	repository.lifecycles[lifecycle.ID] = archived
	app := newApplication(t, repository, &recordingAuthorizer{}, func(context.Context, string) (runtimehost.Lease, error) { return nil, nil })

	_, err := app.Revision(context.Background(), application.RevisionRequest{
		ProjectID: "project", ActorID: "actor", DashboardID: lifecycle.ID,
		DraftID: lifecycle.Draft.ID, RevisionID: revision.ID, Action: authoring.AuthorizationActionEdit,
	})
	if !errors.Is(err, authoring.ErrNotFound) || repository.getRevisionCalls != 0 {
		t.Fatalf("archived revision error=%v lookups=%d", err, repository.getRevisionCalls)
	}
}

var _ authoringservice.Authorizer = (*recordingAuthorizer)(nil)
