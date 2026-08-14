package module

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

func TestPrincipalIsHumanExcludesServicePrincipals(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal Principal
		want      bool
	}{
		{name: "user", principal: Principal{Kind: access.PrincipalKindUser}, want: true},
		{name: "local developer", principal: Principal{DevBypass: true}, want: true},
		{name: "service principal", principal: Principal{Kind: access.PrincipalKindServicePrincipal}, want: false},
		{name: "publication", principal: Principal{Kind: access.PrincipalKindDashboardPublication}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := test.principal.IsHuman(); got != test.want {
				t.Fatalf("IsHuman() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPrivilegeWorkspaceIDFailsClosedWhenRouteHasNoScope(t *testing.T) {
	auth := &Auth{}

	request := httptest.NewRequest("POST", "/api/v1/principals", nil)
	if got := auth.privilegeWorkspaceID(request); got != "" {
		t.Fatalf("unscoped route workspace = %q, want empty", got)
	}
}

func TestPrivilegeWorkspaceIDPreservesExplicitAPIWorkspace(t *testing.T) {
	auth := &Auth{}

	request := httptest.NewRequest("GET", "/api/v1/workspaces/acme/groups", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("workspace", "acme")
	request = request.WithContext(contextWithRouteContext(request, routeContext))

	if got := auth.privilegeWorkspaceID(request); got != "acme" {
		t.Fatalf("workspace API route workspace = %q, want acme", got)
	}
}

func TestPrivilegeWorkspaceIDPreservesExplicitConnectionAssetWorkspace(t *testing.T) {
	auth := &Auth{}
	request := httptest.NewRequest("GET", "/updates?route=connection_asset&assetWorkspace=acme", nil)

	if got := auth.privilegeWorkspaceID(request); got != "acme" {
		t.Fatalf("connection asset workspace = %q, want acme", got)
	}
}

func TestAuthoringProjectScopeUsesCredentialProjectForProjectAgnosticRoute(
	t *testing.T,
) {
	request := httptest.NewRequest("GET", "/api/v1/capabilities", nil)
	session := &access.AuthoringSession{
		Scope: access.AuthoringScope{ProjectID: "leapview-evaluation"},
	}

	if got := authoringProjectScope(request, session); got != "leapview-evaluation" {
		t.Fatalf("project-agnostic authoring scope = %q", got)
	}
}

func TestAuthoringProjectScopePreservesExplicitRouteProject(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/v1/projects/finance/candidates", nil)
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("project", "finance")
	request = request.WithContext(contextWithRouteContext(request, routeContext))
	session := &access.AuthoringSession{
		Scope: access.AuthoringScope{ProjectID: "leapview-evaluation"},
	}

	if got := authoringProjectScope(request, session); got != "finance" {
		t.Fatalf("explicit authoring route project = %q", got)
	}
}

func contextWithRouteContext(request *http.Request, routeContext *chi.Context) context.Context {
	return context.WithValue(request.Context(), chi.RouteCtxKey, routeContext)
}

func TestAuthorizationObjectsIncludePlatformForSessionAuthentication(t *testing.T) {
	objects := authorizationObjects(nil, nil, access.PrivilegeViewAudit)
	if len(objects) != 1 || objects[0] != access.PlatformObject() {
		t.Fatalf("authorization objects = %#v, want platform object", objects)
	}
}

func TestAuthorizationObjectsDoNotExpandWorkspaceScopedAPITokenToPlatform(t *testing.T) {
	credential := &access.APICredential{Token: access.APIToken{
		WorkspaceID: "acme",
		Privileges:  []access.Privilege{access.PrivilegeViewAudit},
	}}
	objects := authorizationObjects([]string{"acme"}, credential, access.PrivilegeViewAudit)
	if len(objects) != 1 || objects[0] != access.WorkspaceObject("acme") {
		t.Fatalf("authorization objects = %#v, want only acme workspace", objects)
	}
}

func TestAuthorizationObjectsIgnoreUnregisteredWorkspace(t *testing.T) {
	credential := &access.APICredential{Token: access.APIToken{
		WorkspaceID: "test",
		Privileges:  []access.Privilege{access.PrivilegeViewAudit},
	}}
	objects := authorizationObjects(nil, credential, access.PrivilegeViewAudit)
	if len(objects) != 0 {
		t.Fatalf("authorization objects = %#v, want no phantom workspace", objects)
	}
}

func TestAuthorizationDenialAuditInputIdentifiesTheDeniedObject(t *testing.T) {
	request := httptest.NewRequest("POST", "/api/v1/workspaces/acme/semantic-models/sales/query", nil)
	request.Header.Set("X-Request-ID", "request-1")
	request.Header.Set("X-Correlation-ID", "correlation-1")
	input := authorizationDenialAuditInput(
		request,
		"principal-1",
		"acme",
		access.PrivilegeQueryData,
		[]access.ObjectRef{access.ItemObject(access.SecurableSemanticModel, "acme", "sales")},
		access.ReasonMissingPrivilege,
	)
	if input.Action != "authorization.denied" || input.Status != "denied" {
		t.Fatalf("denial audit action/status = %q/%q", input.Action, input.Status)
	}
	if input.WorkspaceID != "acme" || input.PrincipalID != "principal-1" {
		t.Fatalf("denial audit identity = %#v", input)
	}
	if input.TargetType != "semantic_model" || input.TargetID != "semantic_model:acme:sales" {
		t.Fatalf("denial audit target = %q/%q", input.TargetType, input.TargetID)
	}
	if input.Privilege != access.PrivilegeQueryData || input.RequestID != "request-1" || input.CorrelationID != "correlation-1" {
		t.Fatalf("denial audit request contract = %#v", input)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(input.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["reason"] != string(access.ReasonMissingPrivilege) {
		t.Fatalf("denial audit metadata = %#v", metadata)
	}
}

func TestAuthorizationAllowedAuditInputIdentifiesConnectionDecision(t *testing.T) {
	request := httptest.NewRequest(
		"POST",
		"/api/v1/workspaces/acme/targets/prod/environments/prod/connection-bindings/warehouse/test",
		nil,
	)
	request.Header.Set("X-Request-ID", "request-1")
	input := authorizationAllowedAuditInput(
		request,
		"operator-1",
		"acme",
		access.PrivilegeTestConnection,
		[]access.ObjectRef{access.WorkspaceObject("acme")},
	)
	if input.Action != "authorization.allowed" || input.Status != "allowed" ||
		input.WorkspaceID != "acme" || input.PrincipalID != "operator-1" ||
		input.TargetType != "workspace" || input.TargetID != "workspace:acme" ||
		input.Privilege != access.PrivilegeTestConnection || input.RequestID != "request-1" {
		t.Fatalf("allowed audit input = %#v", input)
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(input.MetadataJSON), &metadata); err != nil {
		t.Fatal(err)
	}
	if metadata["reason"] != "granted" {
		t.Fatalf("allowed audit metadata = %#v", metadata)
	}
}

func TestAuthorizeCredentialEvidenceFailsAfterTokenRevocation(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	module, err := Build(t.Context(), Config{Database: store.SQLDB()})
	require.NoError(t, err)
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{
		PrincipalID: "activator", Email: "activator@example.test",
		DisplayName: "Activator", Role: access.RoleDeploymentActivator,
	})
	require.NoError(t, err)
	if _, err := repository.UpsertSecurableObject(
		t.Context(),
		access.ProjectEnvironmentObject("finance", "production"),
		"",
	); err != nil {
		t.Fatal(err)
	}
	_, token, err := repository.CreateAPITokenWithMetadata(
		t.Context(),
		access.APITokenInput{
			PrincipalID: principal.ID, Name: "activation",
			Privileges: []access.Privilege{access.PrivilegeActivateDeployment},
			ExpiresAt:  time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		},
	)
	require.NoError(t, err)
	expiresAt, err := time.Parse(time.RFC3339Nano, token.ExpiresAt)
	require.NoError(t, err)
	evidence := access.CredentialEvidence{
		Class: "api_token", ID: token.ID, PrincipalID: principal.ID,
		ExpiresAt: expiresAt,
	}
	allowed, err := module.AuthorizeCredentialEvidence(
		t.Context(),
		evidence,
		"finance",
		"production",
		access.PrivilegeActivateDeployment,
	)
	if err != nil || !allowed {
		t.Fatalf("initial authorization = %t, %v", allowed, err)
	}
	if err := repository.RevokeAPIToken(t.Context(), token.ID); err != nil {
		t.Fatal(err)
	}
	allowed, err = module.AuthorizeCredentialEvidence(
		t.Context(),
		evidence,
		"finance",
		"production",
		access.PrivilegeActivateDeployment,
	)
	require.NoError(t, err)
	if allowed {
		t.Fatal("revoked activation credential remained authorized")
	}
}

func TestAuthorizeCredentialEvidenceUsesProjectEnvironmentGrant(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = store.Close() })
	module, err := Build(t.Context(), Config{Database: store.SQLDB()})
	require.NoError(t, err)
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		ID: "scoped-reviewer", Email: "reviewer@example.test",
		DisplayName: "Scoped Reviewer",
	})
	require.NoError(t, err)
	if _, err := repository.CreateGrant(t.Context(), access.GrantInput{
		Object:      access.ProjectEnvironmentObject("finance", "production"),
		SubjectType: access.SubjectPrincipal, SubjectID: principal.ID,
		Privilege: access.PrivilegeApproveDeployment,
	}); err != nil {
		t.Fatal(err)
	}
	_, token, err := repository.CreateAPITokenWithMetadata(
		t.Context(),
		access.APITokenInput{
			PrincipalID: principal.ID, Name: "approval",
			Privileges: []access.Privilege{access.PrivilegeApproveDeployment},
			ExpiresAt:  time.Now().UTC().Add(time.Hour).Truncate(time.Second),
		},
	)
	require.NoError(t, err)
	expiresAt, err := time.Parse(time.RFC3339Nano, token.ExpiresAt)
	require.NoError(t, err)
	evidence := access.CredentialEvidence{
		Class: "api_token", ID: token.ID, PrincipalID: principal.ID,
		ExpiresAt: expiresAt,
	}
	for _, test := range []struct {
		name, projectID, environment string
		want                         bool
	}{
		{"intended scope", "finance", "production", true},
		{"other project", "operations", "production", false},
		{"other environment", "finance", "staging", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			allowed, err := module.AuthorizeCredentialEvidence(
				t.Context(), evidence, test.projectID, test.environment,
				access.PrivilegeApproveDeployment,
			)
			require.NoError(t, err)
			if allowed != test.want {
				t.Fatalf("allowed = %t, want %t", allowed, test.want)
			}
		})
	}
}
