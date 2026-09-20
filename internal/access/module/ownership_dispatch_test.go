package module

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesshttp "github.com/flidai/leapview/internal/access/http"
	"github.com/go-chi/chi/v5"
)

func TestDispatchAPIGenOperationReachesOwnershipResolutionHandler(t *testing.T) {
	module := &Module{handler: accesshttp.Handler{
		Repository: func() (access.Repository, error) { return nil, errors.New("test repository unavailable") },
		CurrentPrincipal: func(*http.Request) (accesshttp.Principal, bool) {
			return accesshttp.Principal{ID: "principal_admin", Kind: access.PrincipalKindUser}, true
		},
		PlatformAdmin: func(context.Context, string) (bool, error) { return true, nil },
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/principals/principal_owner/ownership", strings.NewReader(`{"action":"tombstone","targetPrincipalId":""}`))
	ctx := chi.NewRouteContext()
	ctx.URLParams.Add("principal", "principal_owner")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, ctx))
	response := httptest.NewRecorder()

	if !module.DispatchAPIGenOperation("resolvePrincipalOwnership", response, request) {
		t.Fatal("resolvePrincipalOwnership was not dispatched")
	}
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d body=%s, want handler repository error", response.Code, response.Body.String())
	}
}
