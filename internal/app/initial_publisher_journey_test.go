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
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/gorilla/csrf"
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

func TestInitialOwnerAssignsIndependentReviewerBeforePublication(t *testing.T) {
	const instanceID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	f := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{TargetID: instanceID, BrowserSessionAuth: true, ProjectClaimBootstrap: true})
	f.AccessModule.SetCurrentProjectID(func(context.Context) (projectgraph.ResourceID, error) { return "", runtimehost.ErrNoActiveServingState })
	repo, ctx := f.Graph.Access, t.Context()
	initial, err := repo.InitializeInstance(ctx, access.InstanceInitializationInput{InstanceID: instanceID, Email: "initial@example.test", Environment: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := repo.CredentialForAPIToken(ctx, initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.ChangeLocalPassword(ctx, owner.Principal.ID, initial.TemporaryPassword, "replacement-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.Graph.DeploymentRepository.ClaimProject(ctx, deployment.ProjectClaimInput{ProjectID: postgresJourneyProject, Environment: "prod", ClaimedBy: owner.Principal.ID, ClaimedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	scope := access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: postgresJourneyProject.String(), Environment: "prod"}
	binding, err := access.NewTypedRoleBinding(access.BootstrapOwnerBindingID, access.BootstrapOwnerBindingName, access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: owner.Principal.ID}, access.PermissionRoleProjectAdmin, postgresJourneyProject)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.UpsertAuthorizationRoleBinding(ctx, access.AuthorizationRoleBindingInput{Scope: scope, Binding: binding, ExpectedRevision: 0, IdempotencyKey: "test-initial-owner"}); err != nil {
		t.Fatal(err)
	}
	reviewer, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "reviewer@example.test", DisplayName: "Independent reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	session, err := repo.CreateSession(ctx, owner.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	csrfResponse := httptest.NewRecorder()
	var csrfToken string
	f.AccessModule.Auth().CSRFMiddleware(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) { csrfToken = csrf.Token(r) })).ServeHTTP(csrfResponse, httptest.NewRequest(http.MethodGet, "http://localhost/admin/principals", nil))
	send := func(token, subject, role, bindingID string, revision int64) *httptest.ResponseRecorder {
		t.Helper()
		body := fmt.Sprintf(`{"adminAccessCommand":{"action":"grant_role","bindingId":%q,"subjectType":"principal","subjectId":%q,"role":%q,"expectedRevision":%d}}`, bindingID, subject, role, revision)
		req := httptest.NewRequest(http.MethodPost, "http://localhost/admin/access/command?section=principals", strings.NewReader(body))
		req.AddCookie(&http.Cookie{Name: f.AccessModule.Auth().SessionCookieName(), Value: token})
		for _, cookie := range csrfResponse.Result().Cookies() {
			req.AddCookie(cookie)
		}
		req.Header.Set("X-CSRF-Token", csrfToken)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", bindingID)
		req.Header.Set(uicommand.HeaderOperationID, "createProjectRoleBinding")
		res := httptest.NewRecorder()
		f.Handler.ServeHTTP(res, req)
		return res
	}
	res := send(session, reviewer.Principal.ID, "release_approver", "initial-reviewer", 1)
	policy, err := repo.AuthorizationPolicy(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if res.Code != http.StatusOK || len(policy.RoleBindings) != 2 {
		t.Fatalf("initial reviewer grant: status=%d bindings=%d body=%s", res.Code, len(policy.RoleBindings), res.Body)
	}
	if !strings.Contains(res.Body.String(), `"rolePresets":[{"role":"release_approver"`) || !strings.Contains(res.Body.String(), "Pending activation") {
		t.Fatal("fresh owner UI did not expose reviewer nomination with pending policy bindings")
	}
	for _, b := range policy.RoleBindings {
		if b.ID == "initial-reviewer" && (b.Subject.ID != reviewer.Principal.ID || b.PermissionRole != access.PermissionRoleReleaseApprover) {
			t.Fatalf("wrong reviewer binding: %+v", b)
		}
	}
	approve, err := access.NewProjectPermissionPair(access.ActionDeliveryApprove, postgresJourneyProject)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range policy.RoleBindings {
		if b.Subject.ID == owner.Principal.ID && access.PermissionSetAllows(b.Permissions, approve) {
			t.Fatal("nomination added approval to owner runtime permissions")
		}
	}
	for _, action := range []string{"grant_admin_envelope.issued", "role_binding.created"} {
		events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{ProjectID: postgresJourneyProject.String(), PrincipalID: owner.Principal.ID, Action: action, Limit: 10})
		if err != nil || len(events) != 1 {
			t.Fatalf("nomination audit %s: count=%d err=%v", action, len(events), err)
		}
		if action == "role_binding.created" && (events[0].ResourceID != "initial-reviewer" || !strings.Contains(events[0].MetadataJSON, reviewer.Principal.ID) || !strings.Contains(events[0].MetadataJSON, "release_approver")) {
			t.Fatalf("reviewer audit did not preserve exact recipient and role")
		}
	}
	denied := func(name, token, subject, role, bindingID string, revision int64) {
		t.Helper()
		t.Run(name, func(t *testing.T) {
			before, err := repo.AuthorizationPolicy(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			result := send(token, subject, role, bindingID, revision)
			after, err := repo.AuthorizationPolicy(ctx, scope)
			if err != nil {
				t.Fatal(err)
			}
			if after.Revision != before.Revision || after.Digest != before.Digest {
				t.Fatalf("denied nomination changed policy: status=%d", result.Code)
			}
		})
	}
	denied("cannot nominate self", session, owner.Principal.ID, "release_approver", "self-reviewer", 2)
	denied("cannot grant broader role", session, reviewer.Principal.ID, "project_admin", "reviewer-admin", 2)
	denied("cannot replace canonical owner", session, reviewer.Principal.ID, "release_approver", access.BootstrapOwnerBindingID, 2)
	denied("stale policy rejected", session, reviewer.Principal.ID, "release_approver", "stale-reviewer", 0)
	permissions, err := access.InitialProjectPublisherPermissions(postgresJourneyProject)
	if err != nil {
		t.Fatal(err)
	}
	apiToken, _, err := repo.CreateScopedAPITokenWithMetadata(ctx, access.ScopedAPITokenInput{PrincipalID: owner.Principal.ID, Name: "ordinary-owner", Permissions: permissions, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	apiRequest := httptest.NewRequest(http.MethodPost, "/api/v1/projects/"+postgresJourneyProject.String()+"/role-bindings", strings.NewReader(fmt.Sprintf(`{"id":"api-reviewer","name":"API reviewer","subjectType":"principal","subjectId":%q,"role":"release_approver","expectedRevision":2}`, reviewer.Principal.ID)))
	apiRequest.Header.Set("Authorization", "Bearer "+apiToken)
	apiRequest.Header.Set("Content-Type", "application/json")
	apiRequest.Header.Set("Idempotency-Key", "api-reviewer")
	apiResponse := httptest.NewRecorder()
	f.Handler.ServeHTTP(apiResponse, apiRequest)
	if apiResponse.Code != http.StatusForbidden {
		t.Fatalf("ordinary API credential nominated reviewer: %d", apiResponse.Code)
	}
	other, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "other-admin@example.test", DisplayName: "Other admin"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: other.Principal.ID, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	otherSession, err := repo.CreateSession(ctx, other.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	denied("other platform admin is not claimant", otherSession, reviewer.Principal.ID, "release_approver", "other-admin-reviewer", 2)
	if _, err = repo.ResetLocalPassword(ctx, owner.Principal.ID); err != nil {
		t.Fatal(err)
	}
	denied("password reset closes nomination", session, reviewer.Principal.ID, "release_approver", "reset-reviewer", 2)

}
