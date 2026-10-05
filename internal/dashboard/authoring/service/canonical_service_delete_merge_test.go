package service_test

import (
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
)

func TestCanonicalServiceDeleteOnlyRemovesPrivateDraft(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-delete", "draft-delete", "revision-delete")
	created, err := svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "actor", Title: "Orders", Slug: "orders", SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-delete"})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := svc.Execute(t.Context(), "project:test", authoring.Command{ID: "delete-1", DashboardID: created.Lifecycle.ID, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Delete: &authoring.DeletePayload{}})
	if err != nil {
		t.Fatal(err)
	}
	if deleted.Revision != created.Revision || repository.lifecycle.ID != "" {
		t.Fatalf("deleted result = %#v, repository lifecycle = %#v", deleted, repository.lifecycle)
	}

	repository, authorizer, compiler = newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc = newCanonicalService(t, repository, authorizer, compiler, "dashboard-restricted", "draft-restricted", "revision-restricted")
	created, err = svc.Create(t.Context(), service.CreateRequest{ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "actor", Title: "Restricted", Slug: "restricted", SemanticModel: "model:test", Visibility: authoring.VisibilityRestricted, Origin: authoring.OriginUI, IdempotencyKey: "create-restricted"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Execute(t.Context(), "project:test", authoring.Command{ID: "delete-2", DashboardID: created.Lifecycle.ID, Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Delete: &authoring.DeletePayload{}}); !errors.Is(err, authoring.ErrConflict) {
		t.Fatalf("restricted delete error = %v, want conflict", err)
	}
	if repository.lifecycle.ID == "" {
		t.Fatal("restricted dashboard was deleted")
	}
}

func TestCanonicalServiceDeleteReplayRequiresCurrentAuthorization(t *testing.T) {
	repository, authorizer, compiler := newCanonicalRepository(), &canonicalAuthorizer{}, &canonicalCompiler{}
	svc := newCanonicalService(t, repository, authorizer, compiler, "dashboard-delete-replay", "draft-delete-replay", "revision-delete-replay")
	created, err := svc.Create(t.Context(), service.CreateRequest{
		ProjectID: "project:test", ActorID: "actor", OwnerPrincipalID: "owner", Title: "Orders", Slug: "orders",
		SemanticModel: "model:test", Visibility: authoring.VisibilityPrivate, Origin: authoring.OriginUI, IdempotencyKey: "create-delete-replay",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := authoring.Command{
		ID: "delete-replay", DashboardID: created.Lifecycle.ID,
		Provenance: authoring.Provenance{Origin: authoring.OriginUI, ActorID: "actor"}, Delete: &authoring.DeletePayload{},
	}
	if _, err := svc.Execute(t.Context(), "project:test", command); err != nil {
		t.Fatal(err)
	}

	// The lifecycle has been removed, so this authorization must use the owner
	// retained in the delete fence. Simulate the actor losing access after the
	// first request and ensure a replay cannot disclose or return the result.
	authorizer.denied = true
	if _, err := svc.Execute(t.Context(), "project:test", command); err == nil || err.Error() != "denied" {
		t.Fatalf("revoked delete replay error = %v", err)
	}
	if len(authorizer.calls) == 0 || authorizer.calls[len(authorizer.calls)-1].OwnerPrincipalID != "owner" || authorizer.calls[len(authorizer.calls)-1].Action != authoring.AuthorizationActionDelete {
		t.Fatalf("replay authorization = %#v", authorizer.calls)
	}
}
