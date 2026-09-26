package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
)

// This is the production qualifier's pre-password bootstrap: real PostgreSQL
// initialization and exchange, then real authenticated generated API routes.
func TestInitialPublisherPasswordSetupJourney(t *testing.T) {
	const instanceID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	f := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{TargetID: instanceID, BrowserSessionAuth: true, ProjectClaimBootstrap: true})
	repo := f.Graph.Access
	ctx := t.Context()
	initial, err := repo.InitializeInstance(ctx, access.InstanceInitializationInput{InstanceID: instanceID, Email: "initial@example.test", Environment: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.CredentialForAPIToken(ctx, initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Graph.DeploymentRepository.ClaimProject(ctx, deployment.ProjectClaimInput{ProjectID: postgresJourneyProject, Environment: "prod", ClaimedBy: claim.Principal.ID, ClaimedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	var publisher access.ProjectClaimPublisherCredentials
	err = repo.RunAuditedMutationBatch(ctx, func(tx access.Repository) ([]access.AuditEventInput, error) {
		var e error
		publisher, e = tx.(access.ProjectClaimPublisherRepository).ExchangeProjectClaimPublisher(ctx, access.ProjectClaimPublisherExchangeInput{InstanceID: instanceID, ProjectID: postgresJourneyProject.String(), PrincipalID: claim.Principal.ID, ClaimCredentialID: claim.Token.ID, ClaimedProjectID: postgresJourneyProject.String(), ClaimedBy: claim.Principal.ID})
		return []access.AuditEventInput{{PrincipalID: claim.Principal.ID, Action: "project.claim.publisher.exchanged", ResourceKind: "api_token", ResourceID: publisher.PublisherCredentialID, Status: "success"}}, e
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body, token string, want int) {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", "initial-publisher-"+method)
		res := httptest.NewRecorder()
		f.Handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, path, res.Code, want, res.Body)
		}
	}
	path := "/api/v1/projects/" + postgresJourneyProject.String() + "/role-bindings"
	body := fmt.Sprintf(`{"id":"project-bootstrap-owner","name":"Project bootstrap owner","subjectType":"principal","subjectId":%q,"role":"project_admin","expectedRevision":0}`, claim.Principal.ID)
	request(http.MethodPost, path, body, publisher.PublisherToken, http.StatusCreated)
	request(http.MethodGet, path+"?limit=200", "", publisher.PublisherToken, http.StatusOK)
	// ACK revokes the initial claim, not the publisher's setup window.
	err = repo.RunAuditedMutationBatch(ctx, func(tx access.Repository) ([]access.AuditEventInput, error) {
		err := tx.(access.ProjectClaimPublisherRepository).AcknowledgeProjectClaimPublisher(ctx, access.ProjectClaimPublisherAcknowledgeInput{InstanceID: instanceID, ProjectID: postgresJourneyProject.String(), PrincipalID: claim.Principal.ID, ClaimCredentialID: claim.Token.ID, PublisherCredentialID: publisher.PublisherCredentialID, ClaimedProjectID: postgresJourneyProject.String(), ClaimedBy: claim.Principal.ID})
		return []access.AuditEventInput{{PrincipalID: claim.Principal.ID, Action: "project.claim.publisher.acknowledged", ResourceKind: "api_token", ResourceID: publisher.PublisherCredentialID, Status: "success"}}, err
	})
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, path+"?limit=200", "", publisher.PublisherToken, http.StatusOK)
	permissions, err := access.InitialProjectPublisherPermissions(postgresJourneyProject)
	if err != nil {
		t.Fatal(err)
	}
	ordinary, _, err := repo.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{PrincipalID: claim.Principal.ID, Name: access.InitialProjectClaimPublisherTokenName(claim.Token.ID), Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, path+"?limit=200", "", ordinary, http.StatusForbidden)
	if _, err := repo.ChangeLocalPassword(ctx, claim.Principal.ID, initial.TemporaryPassword, "replacement-password-123"); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, path+"?limit=200", "", publisher.PublisherToken, http.StatusOK)
	request(http.MethodGet, path+"?limit=200", "", ordinary, http.StatusOK)
	if _, err := repo.ResetLocalPassword(ctx, claim.Principal.ID); err != nil {
		t.Fatal(err)
	}
	request(http.MethodGet, path+"?limit=200", "", publisher.PublisherToken, http.StatusForbidden)
	request(http.MethodGet, path+"?limit=200", "", ordinary, http.StatusForbidden)

}

func TestInitialAdministratorDirectoryBeforeFirstPublication(t *testing.T) {
	f := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{TargetID: "lvinst_0123456789abcdefghijklmnopqrstuv", BrowserSessionAuth: true, ProjectClaimBootstrap: true})
	f.AccessModule.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) {
		return "", fmt.Errorf("resolve active project: %w", runtimehost.ErrNoActiveServingState)
	})
	repo := f.Graph.Access
	initial, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{InstanceID: "lvinst_0123456789abcdefghijklmnopqrstuv", Email: "initial@example.test", Environment: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	credential, err := repo.CredentialForAPIToken(t.Context(), initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ChangeLocalPassword(t.Context(), credential.Principal.ID, initial.TemporaryPassword, "replacement-password-123"); err != nil {
		t.Fatal(err)
	}
	session, err := repo.CreateSession(t.Context(), credential.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}

	checkDirectory := func(want int) string {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/updates?route=admin&section=principals", nil).WithContext(ctx)
		req.AddCookie(&http.Cookie{Name: f.AccessModule.Auth().SessionCookieName(), Value: session})
		req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "initial-admin-directory"})
		res := httptest.NewRecorder()
		f.Handler.ServeHTTP(res, req)
		if res.Code != want {
			t.Fatalf("initial user directory = %d want=%d body=%s", res.Code, want, res.Body)
		}
		return res.Body.String()
	}
	if body := checkDirectory(http.StatusOK); !strings.Contains(body, "initial@example.test") {
		t.Fatalf("directory missing initial administrator: %s", body)
	}
	// A genuine resolver failure must not be treated as an empty fresh install.
	f.AccessModule.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) { return "", errors.New("resolver unavailable") })
	checkDirectory(http.StatusInternalServerError)
}
