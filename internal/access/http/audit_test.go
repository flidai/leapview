package http

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

type auditedMutationRepository struct {
	access.Repository
	called bool
}

func (r *auditedMutationRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	r.called = true
	_, err := mutation(r.Repository)
	return err
}

func TestRunAuditedMutationUsesRepositoryTransaction(t *testing.T) {
	repo := &auditedMutationRepository{}
	request := httptest.NewRequest(stdhttp.MethodPost, "/", nil)
	mutationCalled := false

	err := runAuditedMutation(request, repo, func(access.Repository) (access.AuditEventInput, error) {
		mutationCalled = true
		return access.AuditEventInput{Action: "grant.created"}, nil
	})
	if err != nil {
		t.Fatalf("run audited mutation: %v", err)
	}
	if !repo.called || !mutationCalled {
		t.Fatalf("transaction called = %v, mutation called = %v", repo.called, mutationCalled)
	}
}

func TestRunAuditedMutationRejectsRepositoryWithoutTransactionBeforeMutation(t *testing.T) {
	repo := struct{ access.Repository }{}
	request := httptest.NewRequest(stdhttp.MethodPost, "/", nil)
	mutationCalled := false

	err := runAuditedMutation(request, repo, func(access.Repository) (access.AuditEventInput, error) {
		mutationCalled = true
		return access.AuditEventInput{Action: "grant.created"}, nil
	})
	if !errors.Is(err, access.ErrAuditTransaction) {
		t.Fatalf("run audited mutation error = %v, want %v", err, access.ErrAuditTransaction)
	}
	if mutationCalled {
		t.Fatal("mutation ran without transactional audit support")
	}
}

func TestCreatePrincipalAuditsDuplicateRejectionSeparately(t *testing.T) {
	ctx := t.Context()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	admin, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: "principal-admin", Email: "admin@example.com", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repo, nil }, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
	}}

	request := func(displayName string) *stdhttp.Request {
		r := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/principals", strings.NewReader(
			`{"email":"duplicate@example.com","displayName":"`+displayName+`"}`,
		))
		r.Header.Set("Content-Type", "application/json")
		return r
	}
	first := httptest.NewRecorder()
	handler.CreatePrincipal(first, request("Original"))
	if first.Code != stdhttp.StatusCreated {
		t.Fatalf("first status = %d, body=%s", first.Code, first.Body.String())
	}
	duplicate := httptest.NewRecorder()
	handler.CreatePrincipal(duplicate, request("Replacement"))
	if duplicate.Code != stdhttp.StatusConflict {
		t.Fatalf("duplicate status = %d, body=%s", duplicate.Code, duplicate.Body.String())
	}

	created, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "principal.local_user.created"})
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "principal.local_user.create_rejected"})
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 1 || len(rejected) != 1 || rejected[0].Status != "conflict" {
		t.Fatalf("created/rejected audit events = %d/%d (%+v)", len(created), len(rejected), rejected)
	}
}

func TestUpdateAndDeletePrincipalPersistRequiredSuccessAudits(t *testing.T) {
	ctx := t.Context()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "access.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repo := accesssqlite.NewRepository(store.SQLDB())
	if _, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "principal-admin", Kind: access.PrincipalKindUser,
		Email: "admin@example.com", DisplayName: "Admin",
	}); err != nil {
		t.Fatalf("create audit actor: %v", err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: "principal-admin", Email: "admin@example.com", Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatalf("set audit actor role: %v", err)
	}
	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{
		Email: "mutable@example.com", DisplayName: "Original", MustChange: true,
	})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repo, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal-admin"}, true
		},
	}

	updateRequest := requestWithRouteParam(stdhttp.MethodPatch, "/api/v1/principals/"+created.Principal.ID, "principal", created.Principal.ID)
	updateRequest.Body = io.NopCloser(strings.NewReader(`{"displayName":"Updated"}`))
	updateRequest.Header.Set("Content-Type", "application/json")
	updateRequest.Header.Set("If-Match", resourceETag(principalDTO(created.Principal)))
	updated := httptest.NewRecorder()
	handler.UpdatePrincipal(updated, updateRequest)
	if updated.Code != stdhttp.StatusOK {
		t.Fatalf("update status = %d, body=%s", updated.Code, updated.Body.String())
	}

	deleteRequest := requestWithRouteParam(stdhttp.MethodDelete, "/api/v1/principals/"+created.Principal.ID, "principal", created.Principal.ID)
	deleted := httptest.NewRecorder()
	handler.DeletePrincipal(deleted, deleteRequest)
	if deleted.Code != stdhttp.StatusNoContent {
		t.Fatalf("delete status = %d, body=%s", deleted.Code, deleted.Body.String())
	}

	for _, action := range []string{"principal.updated", "principal.deleted"} {
		events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: action})
		if err != nil {
			t.Fatalf("list %s audits: %v", action, err)
		}
		if len(events) != 1 || events[0].PrincipalID != "principal-admin" || events[0].ResourceID != created.Principal.ID || events[0].Status != "success" {
			t.Fatalf("%s audits = %#v", action, events)
		}
	}
}
