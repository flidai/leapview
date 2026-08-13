package module

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
)

const testSCIMToken = "test-scim-token"

func TestSCIMRoutesRequireBearerAndServeMetadata(t *testing.T) {
	store := testStore(t)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))

	missingToken := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
	missingRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(missingRec, missingToken)
	if missingRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token status = %d, want %d body=%s", missingRec.Code, http.StatusUnauthorized, missingRec.Body.String())
	}

	for _, path := range []string{"/scim/v2/ServiceProviderConfig", "/scim/v2/Schemas", "/scim/v2/ResourceTypes"} {
		req := scimRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want %d body=%s", path, rec.Code, http.StatusOK, rec.Body.String())
		}
	}

	for _, header := range []string{
		"bearer " + testSCIMToken,
		"BEARER " + testSCIMToken,
		"Bearer   " + testSCIMToken + "  ",
	} {
		req := httptest.NewRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
		req.Header.Set("Authorization", header)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("SCIM status with Authorization %q = %d, want %d body=%s", header, rec.Code, http.StatusOK, rec.Body.String())
		}
	}
}

func TestSCIMRoutesUseAPIRateLimit(t *testing.T) {
	store := testStore(t)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{

		WorkspaceID:     "test",
		SCIMBearerToken: testSCIMToken,
		RateLimits:      RateLimitConfig{Enabled: true, APILimit: 1, APIWindow: time.Minute},
	}))
	handler := server.Routes()

	for i := 0; i < 2; i++ {
		req := scimRequest(http.MethodGet, "/scim/v2/ServiceProviderConfig", nil)
		req.RemoteAddr = "192.0.2.10:1234"
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if i == 0 && rec.Code != http.StatusOK {
			t.Fatalf("first SCIM status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		if i == 1 && rec.Code != http.StatusTooManyRequests {
			t.Fatalf("second SCIM status = %d, want %d body=%s", rec.Code, http.StatusTooManyRequests, rec.Body.String())
		}
	}
}

func TestSCIMUserAndGroupProvisioningDriveGrantAccess(t *testing.T) {
	store := testStore(t)
	repo := testAccessRepository(store)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: repo, WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))
	ctx := context.Background()

	userID := createSCIMUser(t, server, "user-ext-1", "analyst@example.com", "Analyst User")
	groupID := createSCIMGroup(t, server, "group-ext-1", "Analysts", []string{userID})
	groups, err := repo.ListSCIMGroups(ctx, access.SCIMGroupFilter{})
	if err != nil {
		t.Fatalf("list directory groups: %v", err)
	}
	if !hasSCIMGroup(groups, groupID) {
		t.Fatalf("directory groups = %#v, want SCIM group %s", groups, groupID)
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object:      access.WorkspaceObject("test"),
		SubjectType: access.SubjectGroup,
		SubjectID:   groupID,
		Privilege:   access.PrivilegeUseWorkspace,
	}); err != nil {
		t.Fatalf("create group grant: %v", err)
	}

	decision, err := repo.Authorize(ctx, userID, access.PrivilegeUseWorkspace, access.WorkspaceObject("test"))
	if err != nil {
		t.Fatalf("authorize provisioned group member: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("provisioned group member was not allowed: %#v", decision)
	}

	removeBody := map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "remove", "path": `members[value eq "` + userID + `"]`}},
	}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, scimRequest(http.MethodPatch, "/scim/v2/Groups/"+url.PathEscape(groupID), removeBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("remove group member status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	decision, err = repo.Authorize(ctx, userID, access.PrivilegeUseWorkspace, access.WorkspaceObject("test"))
	if err != nil {
		t.Fatalf("authorize removed member: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("removed SCIM member still has group grant access: %#v", decision)
	}
}

func hasSCIMGroup(groups []access.Group, id string) bool {
	for _, group := range groups {
		if group.ID == id && group.Provider == "scim" && group.WorkspaceID == "" {
			return true
		}
	}
	return false
}

func TestSCIMDisableRevokesCredentialsAndBlocksAuthorization(t *testing.T) {
	store := testStore(t)
	repo := testAccessRepository(store)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: repo, WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))
	ctx := context.Background()

	userID := createSCIMUser(t, server, "user-ext-2", "disabled@example.com", "Disabled User")
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object:      access.WorkspaceObject("test"),
		SubjectType: access.SubjectPrincipal,
		SubjectID:   userID,
		Privilege:   access.PrivilegeUseWorkspace,
	}); err != nil {
		t.Fatalf("create direct grant: %v", err)
	}
	sessionToken, err := repo.CreateSession(ctx, userID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	apiToken, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: userID,
		WorkspaceID: "test",
		Name:        "disabled-user-token",
		Privileges:  []access.Privilege{access.PrivilegeUseWorkspace},
	})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	patchBody := map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "replace", "path": "active", "value": false}},
	}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, scimRequest(http.MethodPatch, "/scim/v2/Users/"+url.PathEscape(userID), patchBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("disable user status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	if _, err := repo.PrincipalForToken(ctx, sessionToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled principal session err = %v, want sql.ErrNoRows", err)
	}
	if _, err := repo.CredentialForAPIToken(ctx, apiToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled principal api token err = %v, want sql.ErrNoRows", err)
	}
	decision, err := repo.Authorize(ctx, userID, access.PrivilegeUseWorkspace, access.WorkspaceObject("test"))
	if err != nil {
		t.Fatalf("authorize disabled principal: %v", err)
	}
	if decision.Allowed || decision.Reason != access.ReasonPrincipalDisabled {
		t.Fatalf("disabled principal decision = %#v, want denied principal_disabled", decision)
	}
}

func TestSCIMFiltersUsersAndGroups(t *testing.T) {
	store := testStore(t)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))
	userID := createSCIMUser(t, server, "filter-user", "filter@example.com", "Filter User")
	groupID := createSCIMGroup(t, server, "filter-group", "Filter Analysts", []string{userID})

	userRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(userRec, scimRequest(http.MethodGet, `/scim/v2/Users?filter=userName%20eq%20%22filter@example.com%22`, nil))
	if userRec.Code != http.StatusOK {
		t.Fatalf("user filter status = %d body=%s", userRec.Code, userRec.Body.String())
	}
	if totalResults(t, userRec.Body.Bytes()) != 1 {
		t.Fatalf("user filter body=%s, want one result", userRec.Body.String())
	}

	groupRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(groupRec, scimRequest(http.MethodGet, `/scim/v2/Groups?filter=displayName%20eq%20%22Filter%20Analysts%22`, nil))
	if groupRec.Code != http.StatusOK {
		t.Fatalf("group filter status = %d body=%s", groupRec.Code, groupRec.Body.String())
	}
	if totalResults(t, groupRec.Body.Bytes()) != 1 {
		t.Fatalf("group filter body=%s, want one result for group %s", groupRec.Body.String(), groupID)
	}
}

func TestSCIMUserRoundTripsExternalIDAndNestedPatch(t *testing.T) {
	store := testStore(t)
	repo := testAccessRepository(store)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: repo, WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))
	userID := createSCIMUser(t, server, "nested-user-ext", "old@example.com", "Old User")

	getRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(getRec, scimRequest(http.MethodGet, "/scim/v2/Users/"+url.PathEscape(userID), nil))
	if getRec.Code != http.StatusOK {
		t.Fatalf("get SCIM user status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	if externalID(t, getRec.Body.Bytes()) != "nested-user-ext" {
		t.Fatalf("SCIM user externalId = %q, want nested-user-ext body=%s", externalID(t, getRec.Body.Bytes()), getRec.Body.String())
	}

	patchBody := map[string]any{
		"schemas": []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{
			{"op": "replace", "path": "name.formatted", "value": "New User"},
			{"op": "replace", "path": "displayName", "value": "Display User"},
			{"op": "replace", "path": "userName", "value": "new@example.com"},
			{"op": "replace", "path": "emails", "value": []map[string]any{{"value": "new-primary@example.com", "primary": true}}},
			{"op": "replace", "path": "active", "value": true},
		},
	}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, scimRequest(http.MethodPatch, "/scim/v2/Users/"+url.PathEscape(userID), patchBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("patch SCIM user status = %d body=%s", rec.Code, rec.Body.String())
	}
	principal, err := repo.PrincipalByID(context.Background(), userID)
	if err != nil {
		t.Fatalf("get principal: %v", err)
	}
	if principal.Email != "new-primary@example.com" || principal.DisplayName != "Display User" || principal.DisabledAt != "" {
		t.Fatalf("patched principal = %#v", principal)
	}
	if externalID(t, rec.Body.Bytes()) != "nested-user-ext" {
		t.Fatalf("patched SCIM user externalId = %q, want nested-user-ext body=%s", externalID(t, rec.Body.Bytes()), rec.Body.String())
	}
}

func TestSCIMCannotMutateNonSCIMPrincipal(t *testing.T) {
	store := testStore(t)
	repo := accesssqlite.NewRepository(store.SQLDB())
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: repo, WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))
	ctx := context.Background()
	local, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "local_user", Email: "local@example.com", DisplayName: "Local"})
	if err != nil {
		t.Fatalf("create local principal: %v", err)
	}

	deleteRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(deleteRec, scimRequest(http.MethodDelete, "/scim/v2/Users/"+url.PathEscape(local.ID), nil))
	if deleteRec.Code != http.StatusNotFound {
		t.Fatalf("delete non-SCIM status = %d, want %d body=%s", deleteRec.Code, http.StatusNotFound, deleteRec.Body.String())
	}
	after, err := repo.PrincipalByID(ctx, local.ID)
	if err != nil {
		t.Fatalf("get local principal: %v", err)
	}
	if after.DisabledAt != "" {
		t.Fatalf("non-SCIM principal was disabled: %#v", after)
	}
}

func TestSCIMGroupPatchReplaceAndClearMembers(t *testing.T) {
	store := testStore(t)
	repo := testAccessRepository(store)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: repo, WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))
	first := createSCIMUser(t, server, "replace-member-1", "member1@example.com", "Member 1")
	second := createSCIMUser(t, server, "replace-member-2", "member2@example.com", "Member 2")
	groupID := createSCIMGroup(t, server, "replace-group", "Replace Group", []string{first})

	replaceBody := map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "replace", "path": "members", "value": []map[string]any{{"value": second}}}},
	}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, scimRequest(http.MethodPatch, "/scim/v2/Groups/"+url.PathEscape(groupID), replaceBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("replace members status = %d body=%s", rec.Code, rec.Body.String())
	}
	members, err := repo.ListSCIMGroupMembers(context.Background(), groupID)
	if err != nil {
		t.Fatalf("list members: %v", err)
	}
	if len(members) != 1 || members[0].PrincipalID != second {
		t.Fatalf("members after replace = %#v, want only %s", members, second)
	}

	clearBody := map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:api:messages:2.0:PatchOp"},
		"Operations": []map[string]any{{"op": "remove", "path": "members"}},
	}
	clearRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(clearRec, scimRequest(http.MethodPatch, "/scim/v2/Groups/"+url.PathEscape(groupID), clearBody))
	if clearRec.Code != http.StatusOK {
		t.Fatalf("clear members status = %d body=%s", clearRec.Code, clearRec.Body.String())
	}
	members, err = repo.ListSCIMGroupMembers(context.Background(), groupID)
	if err != nil {
		t.Fatalf("list members after clear: %v", err)
	}
	if len(members) != 0 {
		t.Fatalf("members after clear = %#v, want none", members)
	}
}

func TestSCIMAuditIncludesRequestMetadataOnSuccessAndFailure(t *testing.T) {
	store := testStore(t)
	repo := testAccessRepository(store)
	server := assembleSCIMTestHarness(fakeMetrics{}, testStoreOptions(store, assemblyConfig{AccessRepo: repo, WorkspaceID: "test", SCIMBearerToken: testSCIMToken}))

	req := scimRequest(http.MethodPost, "/scim/v2/Users", map[string]any{
		"schemas":    []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"externalId": "audit-user",
		"userName":   "audit@example.com",
		"active":     true,
	})
	req.Header.Set("X-Request-ID", "scim_req")
	req.Header.Set("X-Correlation-ID", "scim_corr")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create audited user status = %d body=%s", rec.Code, rec.Body.String())
	}

	badReq := httptest.NewRequest(http.MethodGet, "/scim/v2/Users", nil)
	badReq.Header.Set("Authorization", "Bearer wrong")
	badReq.Header.Set("X-Request-ID", "bad_req")
	badReq.Header.Set("X-Correlation-ID", "bad_corr")
	badRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status = %d body=%s", badRec.Code, badRec.Body.String())
	}

	events, err := repo.ListAuditEvents(context.Background(), access.AuditEventFilter{Action: "scim.user.create"})
	if err != nil {
		t.Fatalf("list create audit events: %v", err)
	}
	if len(events) != 1 || events[0].RequestID != "scim_req" || events[0].CorrelationID != "scim_corr" || events[0].PrincipalID != "" {
		t.Fatalf("create audit events = %#v", events)
	}
	events, err = repo.ListAuditEvents(context.Background(), access.AuditEventFilter{Action: "scim.auth"})
	if err != nil {
		t.Fatalf("list auth audit events: %v", err)
	}
	if len(events) != 1 || events[0].Status != "denied" || events[0].RequestID != "bad_req" || events[0].CorrelationID != "bad_corr" || events[0].PrincipalID != "" {
		t.Fatalf("auth audit events = %#v", events)
	}
}

func createSCIMUser(t *testing.T, server *scimTestHarness, externalID, email, displayName string) string {
	t.Helper()
	body := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:User"},
		"externalId":  externalID,
		"userName":    email,
		"displayName": displayName,
		"name":        map[string]any{"formatted": displayName},
		"active":      true,
		"emails":      []map[string]any{{"value": email, "type": "work", "primary": true}},
	}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, scimRequest(http.MethodPost, "/scim/v2/Users", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create SCIM user status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	return resourceID(t, rec.Body.Bytes())
}

func createSCIMGroup(t *testing.T, server *scimTestHarness, externalID, displayName string, members []string) string {
	t.Helper()
	memberAttrs := make([]map[string]any, 0, len(members))
	for _, member := range members {
		memberAttrs = append(memberAttrs, map[string]any{"value": member})
	}
	body := map[string]any{
		"schemas":     []string{"urn:ietf:params:scim:schemas:core:2.0:Group"},
		"externalId":  externalID,
		"displayName": displayName,
		"members":     memberAttrs,
	}
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, scimRequest(http.MethodPost, "/scim/v2/Groups", body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create SCIM group status = %d, want %d body=%s", rec.Code, http.StatusCreated, rec.Body.String())
	}
	return resourceID(t, rec.Body.Bytes())
}

func scimRequest(method, path string, body any) *http.Request {
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		payload, _ := json.Marshal(body)
		reader = bytes.NewReader(payload)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+testSCIMToken)
	req.Header.Set("Content-Type", "application/scim+json")
	req.Header.Set("Accept", "application/scim+json")
	return req
}

func resourceID(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode resource: %v body=%s", err, string(body))
	}
	if decoded.ID == "" {
		t.Fatalf("resource id missing: %s", string(body))
	}
	return decoded.ID
}

func totalResults(t *testing.T, body []byte) int {
	t.Helper()
	var decoded struct {
		TotalResults int `json:"totalResults"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode list response: %v body=%s", err, string(body))
	}
	return decoded.TotalResults
}

func externalID(t *testing.T, body []byte) string {
	t.Helper()
	var decoded struct {
		ExternalID string `json:"externalId"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode externalId response: %v body=%s", err, string(body))
	}
	return decoded.ExternalID
}
