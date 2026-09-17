package http

import (
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type tokenAttenuationRepository struct {
	access.Repository
	created bool
}

func (r *tokenAttenuationRepository) RunAuditedMutation(_ context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	_, err := mutation(r)
	return err
}

func (r *tokenAttenuationRepository) CreateScopedAPITokenWithMetadata(_ context.Context, input access.ScopedAPITokenInput) (string, access.APIToken, error) {
	r.created = true
	return "secret", access.APIToken{ID: "child", PrincipalID: input.PrincipalID, PermissionProfile: access.PermissionCatalogProfile, Permissions: input.Permissions}, nil
}

func TestCreateCurrentAPITokenTypedCredentialCannotCrossPairAuthority(t *testing.T) {
	readA := mustTokenResourcePair(t, access.ActionDashboardRead, "dashboard_a")
	updateB := mustTokenResourcePair(t, access.ActionDashboardUpdate, "dashboard_b")
	updateA := mustTokenResourcePair(t, access.ActionDashboardUpdate, "dashboard_a")
	repository := &tokenAttenuationRepository{}
	handler := tokenAttenuationHandler(repository, access.APIToken{
		ID: "parent", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{readA, updateB},
	})

	response := createTypedTokenRequest(t, handler, []access.PermissionPair{updateA})
	if response.Code != stdhttp.StatusForbidden {
		t.Fatalf("cross-paired issuance status = %d body = %s, want %d", response.Code, response.Body.String(), stdhttp.StatusForbidden)
	}
	if repository.created {
		t.Fatal("cross-paired token was persisted")
	}
}

func TestCreateCurrentAPITokenTypedCredentialCannotWidenToFutureResources(t *testing.T) {
	exact := mustTokenResourcePair(t, access.ActionDashboardRead, "dashboard_a")
	future, err := access.NewFutureProjectPermissionPair(access.ActionDashboardRead, "project_1", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	repository := &tokenAttenuationRepository{}
	handler := tokenAttenuationHandler(repository, access.APIToken{
		ID: "parent", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{exact},
	})

	response := createTypedTokenRequest(t, handler, []access.PermissionPair{future})
	if response.Code != stdhttp.StatusForbidden {
		t.Fatalf("future-widening issuance status = %d body = %s, want %d", response.Code, response.Body.String(), stdhttp.StatusForbidden)
	}
	if repository.created {
		t.Fatal("future-widening token was persisted")
	}
}

func TestCreateCurrentAPITokenRejectsLegacyCredentialForTypedIssuance(t *testing.T) {
	pair := mustTokenResourcePair(t, access.ActionDashboardRead, "dashboard_a")
	repository := &tokenAttenuationRepository{}
	handler := tokenAttenuationHandler(repository, access.APIToken{ID: "legacy", Capabilities: access.LegacyProjectCapabilities()})

	response := createTypedTokenRequest(t, handler, []access.PermissionPair{pair})
	if response.Code != stdhttp.StatusForbidden {
		t.Fatalf("legacy-scope issuance status = %d body = %s, want %d", response.Code, response.Body.String(), stdhttp.StatusForbidden)
	}
	if repository.created {
		t.Fatal("legacy-scope token was persisted")
	}
}

func TestCreateCurrentAPITokenRejectsLegacyCapabilityInput(t *testing.T) {
	for _, test := range []struct {
		name  string
		token access.APIToken
	}{
		{name: "legacy token", token: access.APIToken{ID: "legacy", Capabilities: access.LegacyProjectCapabilities()}},
		{name: "typed token", token: access.APIToken{ID: "typed", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &tokenAttenuationRepository{}
			handler := tokenAttenuationHandler(repository, test.token)
			request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(`{"name":"child","capabilities":["RESOURCE_READ"]}`))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			handler.CreateCurrentAPIToken(response, request)
			if response.Code != stdhttp.StatusBadRequest {
				t.Fatalf("legacy child issuance status = %d body = %s, want %d", response.Code, response.Body.String(), stdhttp.StatusBadRequest)
			}
			if repository.created {
				t.Fatal("legacy child token was persisted")
			}
		})
	}
}

func tokenAttenuationHandler(repository *tokenAttenuationRepository, token access.APIToken) Handler {
	return Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
			return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
		},
		CurrentCredential: func(*stdhttp.Request) (access.APICredential, bool) {
			return access.APICredential{Principal: access.Principal{ID: "principal_1"}, Token: token}, true
		},
	}
}

func createTypedTokenRequest(t *testing.T, handler Handler, permissions []access.PermissionPair) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": "child", "permissions": permissions})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.CreateCurrentAPIToken(response, request)
	return response
}

func mustTokenResourcePair(t *testing.T, action access.Action, id string) access.PermissionPair {
	t.Helper()
	resource, err := access.NewResourceRef(projectgraph.ResourceID(id), projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(action, "project_1", resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}
