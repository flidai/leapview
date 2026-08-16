package http

import (
	"context"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/go-chi/chi/v5"
)

func TestUpdateCurrentPrincipalIfMatchSemantics(t *testing.T) {
	for _, test := range []struct {
		name       string
		ifMatch    string
		wantStatus int
	}{
		{name: "exact", ifMatch: "etag", wantStatus: stdhttp.StatusOK},
		{name: "wildcard", ifMatch: "*", wantStatus: stdhttp.StatusOK},
		{name: "missing", wantStatus: stdhttp.StatusPreconditionFailed},
		{name: "mismatch", ifMatch: `"stale"`, wantStatus: stdhttp.StatusPreconditionFailed},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &currentUserRepository{
				principal: access.Principal{
					ID: "principal_me", Kind: access.PrincipalKindUser, Email: "me@example.com", DisplayName: "Before",
					CreatedAt: "2026-08-10T12:00:00Z", UpdatedAt: "2026-08-10T12:00:00Z",
				},
				management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
			}
			handler := Handler{
				Repository:                   func() (access.Repository, error) { return repository, nil },
				CurrentEffectiveCapabilities: allowProjectAdmin,
				CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
					return Principal{ID: repository.principal.ID, Kind: access.PrincipalKindUser}, true
				},
			}
			get := httptest.NewRecorder()
			handler.GetCurrentPrincipal(get, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me", nil))
			if test.ifMatch == "etag" {
				test.ifMatch = get.Header().Get("ETag")
			}
			request := httptest.NewRequest(stdhttp.MethodPatch, "/api/v1/me", strings.NewReader(`{"displayName":"After"}`))
			if test.ifMatch != "" {
				request.Header.Set("If-Match", test.ifMatch)
			}
			response := httptest.NewRecorder()
			handler.UpdateCurrentPrincipal(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
			if test.wantStatus == stdhttp.StatusPreconditionFailed && repository.principal.DisplayName != "Before" {
				t.Fatalf("stale write changed principal: %#v", repository.principal)
			}
		})
	}
}

type revisionRaceRepository struct {
	*currentUserRepository
	bumpOnTransaction bool
}

func (r *revisionRaceRepository) IsPlatformAdmin(context.Context, string) (bool, error) {
	return true, nil
}

func (r *revisionRaceRepository) RunAuditedMutation(ctx context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	if r.bumpOnTransaction {
		r.principal.UpdatedAt = "2026-08-10T12:30:00Z"
		r.bumpOnTransaction = false
	}
	return r.currentUserRepository.RunAuditedMutation(ctx, mutation)
}

func revisionRaceRequest(method, path, principalID string, body string) *stdhttp.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("principal", principalID)
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
}

func TestUpdatePrincipalRejectsTransactionTimeRevisionChange(t *testing.T) {
	base := &currentUserRepository{
		principal: access.Principal{
			ID: "target", Kind: access.PrincipalKindUser, Email: "target@example.com", DisplayName: "Before",
			CreatedAt: "2026-08-10T12:00:00Z", UpdatedAt: "2026-08-10T12:00:00Z",
		},
		management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
	}
	repository := &revisionRaceRepository{currentUserRepository: base, bumpOnTransaction: true}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repository, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "admin", Kind: access.PrincipalKindUser}, true
		},
	}
	revision, err := access.PrincipalRevision(base.principal)
	if err != nil {
		t.Fatal(err)
	}
	request := revisionRaceRequest(stdhttp.MethodPatch, "/api/v1/principals/target", "target", `{"displayName":"After"}`)
	request.Header.Set("If-Match", revision)
	response := httptest.NewRecorder()
	handler.UpdatePrincipal(response, request)
	if response.Code != stdhttp.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s, want 412", response.Code, response.Body.String())
	}
	if base.principal.DisplayName != "Before" {
		t.Fatalf("transaction-time stale write changed principal: %#v", base.principal)
	}
}

func TestUpdateCurrentPrincipalRejectsTransactionTimeRevisionChange(t *testing.T) {
	base := &currentUserRepository{
		principal: access.Principal{
			ID: "principal_me", Kind: access.PrincipalKindUser, Email: "me@example.com", DisplayName: "Before",
			CreatedAt: "2026-08-10T12:00:00Z", UpdatedAt: "2026-08-10T12:00:00Z",
		},
		management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal, HasLocalPassword: true},
	}
	repository := &revisionRaceRepository{currentUserRepository: base}
	handler := Handler{
		Repository:                   func() (access.Repository, error) { return repository, nil },
		CurrentEffectiveCapabilities: allowProjectAdmin,
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: base.principal.ID, Kind: access.PrincipalKindUser}, true
		},
	}
	get := httptest.NewRecorder()
	handler.GetCurrentPrincipal(get, httptest.NewRequest(stdhttp.MethodGet, "/api/v1/me", nil))
	repository.bumpOnTransaction = true
	request := httptest.NewRequest(stdhttp.MethodPatch, "/api/v1/me", strings.NewReader(`{"displayName":"After"}`))
	request.Header.Set("If-Match", get.Header().Get("ETag"))
	response := httptest.NewRecorder()
	handler.UpdateCurrentPrincipal(response, request)
	if response.Code != stdhttp.StatusPreconditionFailed {
		t.Fatalf("status=%d body=%s, want 412", response.Code, response.Body.String())
	}
	if base.principal.DisplayName != "Before" {
		t.Fatalf("transaction-time stale write changed current principal: %#v", base.principal)
	}
}

func TestUpdatePrincipalRevisionAcceptsWildcardAndRejectsMissingHeader(t *testing.T) {
	for _, test := range []struct {
		name       string
		ifMatch    string
		wantStatus int
	}{
		{name: "wildcard", ifMatch: "*", wantStatus: stdhttp.StatusOK},
		{name: "missing", wantStatus: stdhttp.StatusPreconditionFailed},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			base := &currentUserRepository{
				principal:  access.Principal{ID: "target", Kind: access.PrincipalKindUser, Email: "target@example.com", DisplayName: "Before", CreatedAt: "2026-08-10T12:00:00Z", UpdatedAt: "2026-08-10T12:00:00Z"},
				management: access.PrincipalIdentityManagement{Source: access.IdentityManagementLocal},
			}
			repository := &revisionRaceRepository{currentUserRepository: base}
			handler := Handler{Repository: func() (access.Repository, error) { return repository, nil }, CurrentEffectiveCapabilities: allowProjectAdmin, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "admin"}, true }}
			request := revisionRaceRequest(stdhttp.MethodPatch, "/api/v1/principals/target", "target", `{"displayName":"After"}`)
			if test.ifMatch != "" {
				request.Header.Set("If-Match", test.ifMatch)
			}
			response := httptest.NewRecorder()
			handler.UpdatePrincipal(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status=%d body=%s, want %d", response.Code, response.Body.String(), test.wantStatus)
			}
		})
	}
}
