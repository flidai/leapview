package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Exercise the complete generated HTTP route and its transactional policy
// writer with the same native device credential used by local authoring.
func TestLocalAuthoringClaimBootstrapPolicyJourney(t *testing.T) {
	const instanceID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	f := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{TargetID: instanceID, BrowserSessionAuth: true, ProjectClaimBootstrap: true})
	repo, ctx := f.Graph.Access, t.Context()
	initial, err := repo.InitializeInstance(ctx, access.InstanceInitializationInput{InstanceID: instanceID, Email: "local-author@example.test", Environment: "prod"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := repo.CredentialForAPIToken(ctx, initial.ProjectClaimToken)
	if err != nil {
		t.Fatal(err)
	}
	owner := claim.Principal.ID
	if _, err := repo.ChangeLocalPassword(ctx, owner, initial.TemporaryPassword, "replacement-password-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Graph.DeploymentRepository.ClaimProject(ctx, deployment.ProjectClaimInput{ProjectID: postgresJourneyProject, Environment: "prod", ClaimedBy: owner, ClaimedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	permissions, err := access.ProjectPermissionPairsForActions(postgresJourneyProject, []access.Action{access.ActionProjectAccessRead, access.ActionProjectAccessManage})
	if err != nil {
		t.Fatal(err)
	}
	scope := access.AuthoringScope{TargetID: instanceID, ProjectID: postgresJourneyProject, Permissions: permissions}
	token := issueBootstrapJourneyDeviceCredential(t, f, owner, scope, "owner")
	path := "/api/v1/projects/" + postgresJourneyProject.String()
	request := func(method, endpoint, body, credential, key string, want int) map[string]any {
		t.Helper()
		req := httptest.NewRequest(method, path+endpoint, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+credential)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		response := httptest.NewRecorder()
		f.Handler.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s: status=%d want=%d body=%s", method, endpoint, response.Code, want, response.Body)
		}
		var result map[string]any
		if want < 400 && json.Unmarshal(response.Body.Bytes(), &result) != nil {
			t.Fatalf("invalid response: %s", response.Body)
		}
		return result
	}
	ownerBody := fmt.Sprintf(`{"id":"project-bootstrap-owner","name":"Project bootstrap owner","subjectType":"principal","subjectId":%q,"role":"project_admin","expectedRevision":0}`, owner)
	readOnly := scope
	readOnly.Permissions, err = access.ProjectPermissionPairsForActions(postgresJourneyProject, []access.Action{access.ActionProjectAccessRead})
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/role-bindings", ownerBody, issueBootstrapJourneyDeviceCredential(t, f, owner, readOnly, "read-only"), "denied-read-only", http.StatusForbidden)
	wrongTarget := scope
	wrongTarget.TargetID = "lvinst_another"
	request(http.MethodPost, "/role-bindings", ownerBody, issueBootstrapJourneyDeviceCredential(t, f, owner, wrongTarget, "other-target"), "denied-target", http.StatusForbidden)
	wrongProject := scope
	wrongProject.ProjectID = projectgraph.ResourceID("project:other")
	wrongProject.Permissions, err = access.ProjectPermissionPairsForActions(wrongProject.ProjectID, []access.Action{access.ActionProjectAccessManage})
	if err != nil {
		t.Fatal(err)
	}
	request(http.MethodPost, "/role-bindings", ownerBody, issueBootstrapJourneyDeviceCredential(t, f, owner, wrongProject, "other-project"), "denied-project", http.StatusForbidden)
	other, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{Email: "other-admin@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	otherToken := issueBootstrapJourneyDeviceCredential(t, f, other.ID, scope, "other-admin")
	request(http.MethodPost, "/role-bindings", strings.ReplaceAll(ownerBody, owner, other.ID), otherToken, "denied-claim-owner", http.StatusForbidden)
	request(http.MethodPost, "/role-bindings", strings.ReplaceAll(ownerBody, "project-bootstrap-owner", "arbitrary-owner"), token, "denied-noncanonical", http.StatusForbidden)
	request(http.MethodPost, "/role-bindings", strings.ReplaceAll(ownerBody, owner, other.ID), token, "denied-recipient", http.StatusForbidden)

	bindings := []struct {
		id, name string
		role     access.PermissionRole
	}{
		{access.BootstrapOwnerBindingID, access.BootstrapOwnerBindingName, access.PermissionRoleProjectAdmin},
		{access.BootstrapEditorBindingID, access.BootstrapEditorBindingName, access.PermissionRoleEditor},
		{access.BootstrapReleaseOperatorBindingID, access.BootstrapReleaseOperatorBindingName, access.PermissionRoleReleaseOperator},
	}
	for index, binding := range bindings {
		body := fmt.Sprintf(`{"id":%q,"name":%q,"subjectType":"principal","subjectId":%q,"role":%q,"expectedRevision":%d}`, binding.id, binding.name, owner, binding.role, index)
		created := request(http.MethodPost, "/role-bindings", body, token, binding.id, http.StatusCreated)
		listed := request(http.MethodGet, "/role-bindings?limit=200", "", token, "", http.StatusOK)
		grants := request(http.MethodGet, "/grants?limit=200", "", token, "", http.StatusOK)
		if created["policyRevision"] != float64(index+1) || listed["policyDigest"] != created["policyDigest"] || grants["policyDigest"] != listed["policyDigest"] {
			t.Fatalf("inconsistent bootstrap policy: created=%v listed=%v grants=%v", created, listed, grants)
		}
		request(http.MethodPost, "/role-bindings", body, token, binding.id, http.StatusCreated)
	}
	grant := fmt.Sprintf(`{"id":"bootstrap-dashboard-read","resourceKind":%q,"resourceId":"dashboard:sample","subjectType":"principal","subjectId":%q,"capability":"RESOURCE_READ","expectedRevision":3}`, projectgraph.KindDashboard, owner)
	created := request(http.MethodPost, "/grants", grant, token, "bootstrap-dashboard-read", http.StatusCreated)
	listed := request(http.MethodGet, "/grants?limit=200", "", token, "", http.StatusOK)
	if created["policyRevision"] != float64(4) || created["policyDigest"] != listed["policyDigest"] {
		t.Fatalf("grant policy did not advance consistently: created=%v listed=%v", created, listed)
	}
	policy, err := repo.AuthorizationPolicy(ctx, access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: postgresJourneyProject.String(), Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(policy.RoleBindings) != 3 || len(policy.Grants) != 1 {
		t.Fatalf("persisted bootstrap policy: %+v", policy)
	}
}

func issueBootstrapJourneyDeviceCredential(t *testing.T, f *PostgresJourneyFixture, principal string, scope access.AuthoringScope, suffix string) string {
	t.Helper()
	repo, ctx := f.Graph.Access, t.Context()
	now := time.Now().UTC()
	hash := func(value string) string {
		digest := sha256.Sum256([]byte(value))
		return hex.EncodeToString(digest[:])
	}
	device := "device-" + suffix
	if err := repo.CreateDeviceAuthorization(ctx, access.DeviceAuthorization{ID: device, ClientID: access.AuthoringCLIClientID, DeviceCodeHash: hash(device), UserCodeHash: hash("user-" + suffix), Scope: scope, Status: access.DeviceAuthorizationPending, CreatedAt: now, ExpiresAt: now.Add(time.Hour), PollInterval: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApproveDeviceAuthorization(ctx, device, principal, now); err != nil {
		t.Fatal(err)
	}
	token := "lv_cli_access_" + suffix
	if _, err := repo.IssueDeviceCredential(ctx, access.DeviceCredentialIssue{DeviceCodeHash: hash(device), ClientID: access.AuthoringCLIClientID, Now: now, SessionID: "session-" + suffix, CredentialID: "credential-" + suffix, AccessTokenHash: hash(token), RefreshTokenHash: hash("refresh-" + suffix), AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	return token
}
