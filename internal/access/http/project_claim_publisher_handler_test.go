package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type projectClaimPublisherHTTPRepository struct {
	access.Repository
	credentials access.ProjectClaimPublisherCredentials
	exchange    access.ProjectClaimPublisherExchangeInput
	acknowledge access.ProjectClaimPublisherAcknowledgeInput
	event       access.AuditEventInput
	exchangeErr error
	ackErr      error
	called      bool
}

func (r *projectClaimPublisherHTTPRepository) RunAuditedMutation(_ context.Context, mutation func(access.Repository) (access.AuditEventInput, error)) error {
	event, err := mutation(r)
	if err == nil {
		r.event = event
	}
	return err
}

func (r *projectClaimPublisherHTTPRepository) ExchangeProjectClaimPublisher(_ context.Context, input access.ProjectClaimPublisherExchangeInput) (access.ProjectClaimPublisherCredentials, error) {
	r.called = true
	r.exchange = input
	return r.credentials, r.exchangeErr
}

func (r *projectClaimPublisherHTTPRepository) AcknowledgeProjectClaimPublisher(_ context.Context, input access.ProjectClaimPublisherAcknowledgeInput) error {
	r.called = true
	r.acknowledge = input
	return r.ackErr
}

func projectClaimPublisherHandler(repository *projectClaimPublisherHTTPRepository, token access.APIToken, claimProject, claimPrincipal string) Handler {
	return Handler{
		Repository: func() (access.Repository, error) { return repository, nil },
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: token.PrincipalID}, true
		},
		CurrentCredential: func(*http.Request) (access.APICredential, bool) {
			return access.APICredential{Principal: access.Principal{ID: token.PrincipalID}, Token: token}, true
		},
		DurableGrantInstanceID: "instance_demo",
		ProjectClaim:           func(context.Context) (string, string, error) { return claimProject, claimPrincipal, nil },
	}
}

func projectClaimPublisherRequest(method, path, body string) *http.Request {
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("project", "project_demo")
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func TestExchangeProjectClaimPublisherReturnsSecretOnlyInNoStoreResponse(t *testing.T) {
	claimPermissions, err := access.InitialProjectClaimPermissions("instance_demo")
	if err != nil {
		t.Fatal(err)
	}
	repository := &projectClaimPublisherHTTPRepository{credentials: access.ProjectClaimPublisherCredentials{
		ClaimCredentialID: "claim-credential", PublisherCredentialID: "publisher-credential", PublisherToken: "one-time-publisher-secret",
		PublisherTokenExpiresAt:       time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC),
		RevokedPublisherCredentialIDs: []string{"prior-publisher"},
	}}
	handler := projectClaimPublisherHandler(repository, access.APIToken{
		ID: "claim-credential", PrincipalID: "principal_admin", Name: access.APITokenNameInitialProjectClaim,
		PermissionProfile: access.PermissionCatalogProfile, Permissions: claimPermissions,
	}, "project_demo", "principal_admin")
	request := projectClaimPublisherRequest(http.MethodPost, "/api/v1/projects/project_demo/project-claim-publisher/exchange", "")
	response := httptest.NewRecorder()

	handler.ExchangeProjectClaimPublisher(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d body = %s", response.Code, response.Body.String())
	}
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Pragma") != "no-cache" {
		t.Fatalf("secret response cache headers = %v", response.Header())
	}
	var body struct {
		ClaimCredentialID       string    `json:"claimCredentialId"`
		PublisherToken          string    `json:"publisherToken"`
		PublisherTokenExpiresAt time.Time `json:"publisherTokenExpiresAt"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ClaimCredentialID != "claim-credential" || body.PublisherToken != "one-time-publisher-secret" || !body.PublisherTokenExpiresAt.Equal(repository.credentials.PublisherTokenExpiresAt) {
		t.Fatalf("exchange response = %#v", body)
	}
	if !repository.called || repository.exchange.ClaimedProjectID != "project_demo" || repository.exchange.ClaimedBy != "principal_admin" || repository.exchange.InstanceID != "instance_demo" {
		t.Fatalf("exchange repository input = %#v, called=%v", repository.exchange, repository.called)
	}
	if strings.Contains(repository.event.MetadataJSON, "one-time-publisher-secret") || !strings.Contains(repository.event.MetadataJSON, "prior-publisher") {
		t.Fatalf("audit metadata must contain only safe credential IDs: %s", repository.event.MetadataJSON)
	}
}

func TestExchangeProjectClaimPublisherRejectsNonExactClaimCredentialAndForeignPath(t *testing.T) {
	claimPermissions, err := access.InitialProjectClaimPermissions("instance_demo")
	if err != nil {
		t.Fatal(err)
	}
	otherPermission, err := access.NewInstancePermissionPair(access.ActionPlatformAccessRead, "instance_demo")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name       string
		token      access.APIToken
		claimPath  string
		claimActor string
	}{
		{name: "extra authority", token: access.APIToken{ID: "claim", PrincipalID: "principal_admin", Name: access.APITokenNameInitialProjectClaim, PermissionProfile: access.PermissionCatalogProfile, Permissions: append(claimPermissions, otherPermission)}, claimPath: "project_demo", claimActor: "principal_admin"},
		{name: "foreign claimed project", token: access.APIToken{ID: "claim", PrincipalID: "principal_admin", Name: access.APITokenNameInitialProjectClaim, PermissionProfile: access.PermissionCatalogProfile, Permissions: claimPermissions}, claimPath: "project_other", claimActor: "principal_admin"},
	} {
		t.Run(test.name, func(t *testing.T) {
			repository := &projectClaimPublisherHTTPRepository{}
			response := httptest.NewRecorder()
			request := projectClaimPublisherRequest(http.MethodPost, "/api/v1/projects/project_demo/project-claim-publisher/exchange", "")
			projectClaimPublisherHandler(repository, test.token, test.claimPath, test.claimActor).ExchangeProjectClaimPublisher(response, request)
			if response.Code != http.StatusForbidden || repository.called {
				t.Fatalf("status=%d called=%v body=%s; want forbidden before mutation", response.Code, repository.called, response.Body.String())
			}
		})
	}
}

func TestAcknowledgeProjectClaimPublisherRequiresTypedManageAndAuditsOnlyIDs(t *testing.T) {
	projectID := projectgraph.ResourceID("project_demo")
	manage, err := access.NewProjectPermissionPair(access.ActionProjectAccessManage, projectID)
	if err != nil {
		t.Fatal(err)
	}
	repository := &projectClaimPublisherHTTPRepository{}
	handler := projectClaimPublisherHandler(repository, access.APIToken{
		ID: "publisher-credential", PrincipalID: "principal_admin", Name: access.InitialProjectClaimPublisherTokenName("claim-credential"),
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{manage},
	}, "project_demo", "principal_admin")
	request := projectClaimPublisherRequest(http.MethodPost, "/api/v1/projects/project_demo/project-claim-publisher/acknowledge", `{"claimCredentialId":"claim-credential"}`)
	response := httptest.NewRecorder()

	handler.AcknowledgeProjectClaimPublisher(response, request)

	if response.Code != http.StatusNoContent || response.Body.Len() != 0 {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if !repository.called || repository.acknowledge.ClaimCredentialID != "claim-credential" || repository.acknowledge.PublisherCredentialID != "publisher-credential" {
		t.Fatalf("acknowledgement input = %#v, called=%v", repository.acknowledge, repository.called)
	}
	if strings.Contains(repository.event.MetadataJSON, "publisher-token-secret") || strings.Contains(repository.event.MetadataJSON, "publisherToken") {
		t.Fatalf("audit metadata leaked a secret: %s", repository.event.MetadataJSON)
	}
}

func TestAcknowledgeProjectClaimPublisherRejectsMissingTypedManage(t *testing.T) {
	repository := &projectClaimPublisherHTTPRepository{}
	handler := projectClaimPublisherHandler(repository, access.APIToken{
		ID: "publisher-credential", PrincipalID: "principal_admin", Name: access.InitialProjectClaimPublisherTokenName("claim-credential"),
		PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{},
	}, "project_demo", "principal_admin")
	request := projectClaimPublisherRequest(http.MethodPost, "/api/v1/projects/project_demo/project-claim-publisher/acknowledge", `{"claimCredentialId":"claim-credential"}`)
	response := httptest.NewRecorder()

	handler.AcknowledgeProjectClaimPublisher(response, request)

	if response.Code != http.StatusForbidden || repository.called {
		t.Fatalf("status=%d called=%v body=%s; want typed denial before mutation", response.Code, repository.called, response.Body.String())
	}
}
