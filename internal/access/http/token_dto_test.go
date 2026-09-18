package http

import (
	"context"
	"encoding/json"
	nethttp "net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type scopedTokenHTTPRepository struct {
	access.Repository
	input      access.ScopedAPITokenInput
	auditEvent access.AuditEventInput
}

func (r *scopedTokenHTTPRepository) RunAuditedMutation(_ context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(r)
	r.auditEvent = event
	return err
}

func (r *scopedTokenHTTPRepository) CreateScopedAPITokenWithMetadata(_ context.Context, input access.ScopedAPITokenInput) (string, access.APIToken, error) {
	r.input = input
	return "secret", access.APIToken{
		ID: "token_1", PrincipalID: input.PrincipalID, Name: input.Name,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: input.Permissions,
	}, nil
}

func TestAPITokenDTOEmitsExplicitEmptyCapabilities(t *testing.T) {
	dto := apiTokenDTO(access.APIToken{ID: "token_1", PrincipalID: "principal_1", Name: "identity only"})
	capabilities, ok := dto["capabilities"]
	if !ok {
		t.Fatal("apiTokenDTO omitted required capabilities")
	}
	if !reflect.DeepEqual(capabilities, []string{}) {
		t.Fatalf("capabilities = %#v, want explicit empty array", capabilities)
	}
}

func TestAPITokenDTOPreservesCapabilityOrder(t *testing.T) {
	dto := apiTokenDTO(access.APIToken{
		ID: "token_1", PrincipalID: "principal_1", Name: "scoped",
		Capabilities: []access.Capability{access.CapabilityResourceRead, access.CapabilityResourceUse},
	})
	if got, want := dto["capabilities"], []string{"RESOURCE_READ", "RESOURCE_USE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("capabilities = %#v, want %#v", got, want)
	}
}

func TestAPITokenDTOEmitsTypedProfileAndExactPermissionPairs(t *testing.T) {
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project_1", mustResourceRef(t, "dashboard_1", projectgraph.KindDashboard))
	if err != nil {
		t.Fatal(err)
	}
	dto := apiTokenDTO(access.APIToken{
		ID: "token_1", PrincipalID: "principal_1", Name: "dashboard reader",
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair},
	})
	if got, want := dto["permissionProfile"], access.PermissionCatalogProfile; got != want {
		t.Fatalf("permissionProfile = %#v, want %q", got, want)
	}
	permissions, ok := dto["permissions"].([]map[string]any)
	if !ok || len(permissions) != 1 {
		t.Fatalf("permissions = %#v, want one pair", dto["permissions"])
	}
	if got, want := permissions[0], map[string]any{
		"action":  string(access.ActionDashboardRead),
		"profile": access.PermissionCatalogProfile,
		"target":  map[string]any{"scope": "resource", "projectId": "project_1", "resourceKind": "dashboard", "resourceId": "dashboard_1"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("typed permission = %#v, want %#v", got, want)
	}
}

func TestAPITokenDTOEmitsEmptyTypedPermissionsForLegacyRows(t *testing.T) {
	dto := apiTokenDTO(access.APIToken{ID: "token_1", PrincipalID: "principal_1", Name: "legacy"})
	permissions, ok := dto["permissions"].([]map[string]any)
	if !ok || !reflect.DeepEqual(permissions, []map[string]any{}) {
		t.Fatalf("permissions = %#v, want explicit empty array", dto["permissions"])
	}
	if got := dto["permissionProfile"]; got != nil {
		t.Fatalf("permissionProfile = %#v, want nil for legacy row", got)
	}
}

func TestCreateCurrentAPITokenUsesTypedPermissionPairs(t *testing.T) {
	pair, err := access.NewExactPermissionPair(access.ActionDashboardRead, "project_1", mustResourceRef(t, "dashboard_1", projectgraph.KindDashboard))
	if err != nil {
		t.Fatal(err)
	}
	repository := &scopedTokenHTTPRepository{}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*nethttp.Request) (Principal, bool) {
			return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
		},
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return []access.PermissionPair{pair}, nil
		},
	}
	body, err := json.Marshal(map[string]any{
		"name": "dashboard reader", "permissions": []access.PermissionPair{pair},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(nethttp.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.CreateCurrentAPIToken(response, request)
	if response.Code != nethttp.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if len(repository.input.Permissions) != 1 || !reflect.DeepEqual(repository.input.Permissions[0], pair) {
		t.Fatalf("repository permissions = %#v, want %#v", repository.input.Permissions, []access.PermissionPair{pair})
	}
	if repository.auditEvent.Action != "api_token.created" || repository.auditEvent.ResourceID != "token_1" || repository.auditEvent.Status != "success" {
		t.Fatalf("audit event = %#v, want successful typed-token creation", repository.auditEvent)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	var result struct {
		APIToken struct {
			PermissionProfile string           `json:"permissionProfile"`
			Permissions       []map[string]any `json:"permissions"`
		} `json:"apiToken"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.APIToken.PermissionProfile != access.PermissionCatalogProfile || len(result.APIToken.Permissions) != 1 {
		t.Fatalf("response typed token = %#v, want profile and one permission", result.APIToken)
	}
}

func TestCreateCurrentAPITokenRejectsUnauthorizedSessionPairsAndCrossResourceCombinations(t *testing.T) {
	allowed := mustTokenResourcePair(t, access.ActionDashboardRead, "dashboard_a")
	otherResource := mustTokenResourcePair(t, access.ActionDashboardRead, "dashboard_b")
	unauthorizedAction := mustTokenResourcePair(t, access.ActionDashboardUpdate, "dashboard_a")
	repository := &scopedTokenHTTPRepository{}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*nethttp.Request) (Principal, bool) {
			return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
		},
		CurrentEffectivePermissionOptions: func(context.Context, string) ([]access.PermissionPair, error) {
			return []access.PermissionPair{allowed}, nil
		},
	}
	response := createSessionTypedTokenRequest(t, handler, []access.PermissionPair{allowed, otherResource, unauthorizedAction})
	if response.Code != nethttp.StatusForbidden {
		t.Fatalf("unauthorized typed session issuance status = %d body = %s, want %d", response.Code, response.Body.String(), nethttp.StatusForbidden)
	}
	if repository.input.Permissions != nil {
		t.Fatalf("unauthorized typed session request reached persistence: %#v", repository.input.Permissions)
	}
}

func TestCreateCurrentAPITokenAllowsExplicitEmptySessionAuthority(t *testing.T) {
	repository := &scopedTokenHTTPRepository{}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*nethttp.Request) (Principal, bool) {
			return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
		},
	}
	response := createSessionTypedTokenRequest(t, handler, []access.PermissionPair{})
	if response.Code != nethttp.StatusCreated {
		t.Fatalf("empty typed session issuance status = %d body = %s, want %d", response.Code, response.Body.String(), nethttp.StatusCreated)
	}
	if repository.input.Permissions == nil || len(repository.input.Permissions) != 0 {
		t.Fatalf("persisted empty permissions = %#v, want explicit empty set", repository.input.Permissions)
	}
}

func createSessionTypedTokenRequest(t *testing.T, handler Handler, permissions []access.PermissionPair) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(map[string]any{"name": "session child", "permissions": permissions})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(nethttp.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.CreateCurrentAPIToken(response, request)
	return response
}

func TestCreateCurrentAPITokenUsesExplicitEmptyTypedPermissionArray(t *testing.T) {
	repository := &scopedTokenHTTPRepository{}
	handler := Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*nethttp.Request) (Principal, bool) {
			return Principal{ID: "principal_1", Kind: access.PrincipalKindUser}, true
		},
	}
	request := httptest.NewRequest(nethttp.MethodPost, "/api/v1/me/api-tokens", strings.NewReader(`{"name":"identity only","permissions":[]}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.CreateCurrentAPIToken(response, request)
	if response.Code != nethttp.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if repository.input.Permissions == nil || len(repository.input.Permissions) != 0 {
		t.Fatalf("repository permissions = %#v, want explicit empty array", repository.input.Permissions)
	}
}

func mustResourceRef(t *testing.T, id string, kind projectgraph.Kind) access.ResourceRef {
	t.Helper()
	resource, err := access.NewResourceRef(projectgraph.ResourceID(id), kind)
	if err != nil {
		t.Fatal(err)
	}
	return resource
}
