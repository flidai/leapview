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
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	"github.com/flidai/leapview/internal/deployment"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// Exercise the complete generated HTTP route and its transactional policy
// writer with the same native device credential used by local authoring.
func TestLocalAuthoringClaimBootstrapPolicyJourney(t *testing.T) {
	const instanceID = "lvinst_0123456789abcdefghijklmnopqrstuv"
	f := NewPostgresJourneyFixture(t, PostgresJourneyFixtureOptions{TargetID: instanceID, BrowserSessionAuth: true, ProjectClaimBootstrap: true, LocalDevelopment: true})
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
	// Declared local fixtures need exact upload authority in the next captured
	// policy; the editor role intentionally does not confer this permission.
	connection, err := access.NewResourceRef("connection:sample", projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	uploadPair, err := access.NewExactPermissionPair(access.ActionConnectionUpload, postgresJourneyProject, connection)
	if err != nil {
		t.Fatal(err)
	}
	uploadBody, err := json.Marshal(map[string]any{
		"id": "local-sample-upload", "resourceKind": connection.Kind(), "resourceId": connection.ID(),
		"subjectType": "principal", "subjectId": owner, "permissionProfile": access.PermissionCatalogProfile,
		"permissions": []access.PermissionPair{uploadPair}, "expectedRevision": 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	uploadGrant := request(http.MethodPost, "/grants", string(uploadBody), token, "local-sample-upload", http.StatusCreated)
	request(http.MethodPost, "/grants", string(uploadBody), token, "local-sample-upload", http.StatusCreated)
	if uploadGrant["permissionProfile"] != access.PermissionCatalogProfile || uploadGrant["policyRevision"] != float64(5) {
		t.Fatalf("typed fixture grant did not advance policy: %v", uploadGrant)
	}
	policy, err = repo.AuthorizationPolicy(ctx, access.AuthorizationPolicyScope{TargetID: instanceID, ProjectID: postgresJourneyProject.String(), Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	var retainedUpload *access.AuthorizationGrant
	for index := range policy.Grants {
		if policy.Grants[index].ID == "local-sample-upload" {
			retainedUpload = &policy.Grants[index]
		}
	}
	if retainedUpload == nil || retainedUpload.PermissionProfile != access.PermissionCatalogProfile || retainedUpload.Capability != "" || len(retainedUpload.Permissions) != 1 || retainedUpload.Permissions[0] != uploadPair {
		t.Fatalf("typed fixture upload permission was not durably retained: %+v", retainedUpload)
	}

	// The installed CLI reaches profile application immediately after staging
	// its declared fixture. The former local ceiling included policy management
	// but omitted both project-settings actions required by these generated routes.
	profilePath := "/targets/" + instanceID + "/development-profile-application"
	request(http.MethodGet, profilePath, "", token, "", http.StatusForbidden)
	profileDigest, err := connectionbinding.DevelopmentProfileDigest("local", nil)
	if err != nil {
		t.Fatal(err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	profileBody := fmt.Sprintf(`{"applicationId":"profile_journey","mode":"new","sourceDigest":%q,"graphDigest":%q,"profileDigest":%q,"connections":[]}`, digest, digest, profileDigest)
	request(http.MethodPost, profilePath, profileBody, token, "denied-profile-scope", http.StatusForbidden)
	profileScope := scope
	localActions := append(access.DefaultAuthoringActions(), access.ActionProjectAccessRead, access.ActionProjectAccessManage)
	profileScope.Permissions, err = access.ProjectPermissionPairsForActions(postgresJourneyProject, localActions)
	if err != nil {
		t.Fatal(err)
	}
	legacyToken := issueBootstrapJourneyDeviceCredential(t, f, owner, profileScope, "legacy-local")
	request(http.MethodGet, profilePath, "", legacyToken, "", http.StatusForbidden)
	request(http.MethodPost, profilePath, profileBody, legacyToken, "denied-legacy-profile", http.StatusForbidden)
	profileScope.Permissions, err = access.ProjectPermissionPairsForActions(postgresJourneyProject, append(localActions, access.ActionProjectSettingsRead, access.ActionProjectSettingsUpdate))
	if err != nil {
		t.Fatal(err)
	}
	profileToken := issueBootstrapJourneyDeviceCredential(t, f, owner, profileScope, "profile-owner")
	otherTargetProfile := profileScope
	otherTargetProfile.TargetID = "lvinst_another"
	request(http.MethodGet, profilePath, "", issueBootstrapJourneyDeviceCredential(t, f, owner, otherTargetProfile, "profile-other-target"), "", http.StatusForbidden)
	readProfile := profileScope
	readProfile.Permissions, err = access.ProjectPermissionPairsForActions(postgresJourneyProject, []access.Action{access.ActionProjectSettingsRead})
	if err != nil {
		t.Fatal(err)
	}
	readProfileToken := issueBootstrapJourneyDeviceCredential(t, f, owner, readProfile, "profile-read-only")
	request(http.MethodGet, profilePath, "", readProfileToken, "", http.StatusNotFound)
	request(http.MethodPost, profilePath, profileBody, readProfileToken, "denied-profile-read-only", http.StatusForbidden)
	request(http.MethodGet, profilePath, "", profileToken, "", http.StatusNotFound)
	request(http.MethodPost, profilePath, profileBody, profileToken, "profile-apply", http.StatusOK)
	profile := request(http.MethodGet, profilePath, "", profileToken, "", http.StatusOK)
	if profile["lastCompletedApplicationId"] != "profile_journey" {
		t.Fatalf("profile application was not durably completed: %v", profile)
	}

	// A synchronization attempt is persisted before a valid candidate exists;
	// native authentication must reach this exact owner-scoped session route.
	sessionPath := "/targets/" + instanceID + "/development-session/"
	request(http.MethodGet, sessionPath, "", profileToken, "", http.StatusNotFound)
	attempt := fmt.Sprintf(`{"revision":0,"attempted":{"artifactDigest":%q,"graphDigest":%q},"diagnostics":[{"code":"COMPILING","message":"First synchronization"}]}`, digest, digest)
	updated := request(http.MethodPut, sessionPath, attempt, profileToken, "", http.StatusOK)
	session := request(http.MethodGet, sessionPath, "", profileToken, "", http.StatusOK)
	if updated["revision"] != float64(1) || session["id"] != updated["id"] {
		t.Fatalf("development session did not retain its first attempt: updated=%v session=%v", updated, session)
	}
	request(http.MethodGet, sessionPath, "", otherToken, "", http.StatusNotFound)
	request(http.MethodGet, "/targets/lvinst_another/development-session/", "", profileToken, "", http.StatusForbidden)
	request(http.MethodGet, sessionPath+"candidate", "", profileToken, "", http.StatusNotFound)
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
