package app

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
	"github.com/flidai/leapview/internal/workspace"
)

func TestAuthSpecItemSharingAndDataPrivileges(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	analyst := authSpecPrincipal(t, ctx, repo, "analyst@example.com")
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableDashboard, "sales", "executive-sales"), access.SubjectPrincipal, analyst.ID, access.PrivilegeViewItem)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, analyst.ID, access.PrivilegeQueryData)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: analyst.ID, WorkspaceID: "sales", Name: "analyst"})

	status, body := h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards/executive-sales", token, "")
	if status != http.StatusOK {
		t.Fatalf("dashboard metadata status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards", token, "")
	if status != http.StatusForbidden {
		t.Fatalf("workspace dashboard list status=%d want=403 body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/dashboards/executive-sales/pages/overview/query", token, `{}`)
	if status != http.StatusOK {
		t.Fatalf("dashboard query via semantic model grant status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/datasets/orders/preview", token, `{"dimensions":[{"field":"orders.status"}],"limit":1}`)
	if status != http.StatusNotFound {
		t.Fatalf("raw preview status=%d want=404 body=%s", status, body)
	}
}

func TestAuthSpecWorkspaceViewerCanOpenDashboardCatalog(t *testing.T) {
	h, repo := newAuthSpecHarnessWithAuthWorkspace(t, AuthConfig{}, "default")
	ctx := context.Background()

	viewer := authSpecPrincipal(t, ctx, repo, "workspace-viewer@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject(h.workspaceID), access.SubjectPrincipal, viewer.ID, access.PrivilegeViewItem)
	session, err := repo.CreateSession(ctx, viewer.ID, time.Hour)
	if err != nil {
		t.Fatalf("create viewer session: %v", err)
	}
	req, err := http.NewRequest(http.MethodGet, h.serverURL(t)+"/", nil)
	if err != nil {
		t.Fatalf("create dashboard catalog request: %v", err)
	}
	req.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get dashboard catalog: %v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK {
		t.Fatalf("dashboard catalog status=%d want=200 body=%s", res.StatusCode, body)
	}

	updatesCtx, cancelUpdates := context.WithCancel(context.Background())
	updatesReq, err := http.NewRequestWithContext(updatesCtx, http.MethodGet, h.serverURL(t)+"/updates?route=catalog", nil)
	if err != nil {
		t.Fatalf("create dashboard catalog updates request: %v", err)
	}
	updatesReq.AddCookie(&http.Cookie{Name: "lv_session", Value: session})
	updatesRes, err := http.DefaultClient.Do(updatesReq)
	if err != nil {
		cancelUpdates()
		t.Fatalf("get dashboard catalog updates: %v", err)
	}
	if updatesRes.StatusCode != http.StatusOK {
		defer updatesRes.Body.Close()
		updatesBody, _ := io.ReadAll(updatesRes.Body)
		cancelUpdates()
		t.Fatalf("dashboard catalog updates status=%d want=200 body=%s", updatesRes.StatusCode, updatesBody)
	}
	client := &streamClient{
		cancel:  cancelUpdates,
		body:    updatesRes.Body,
		patches: make(chan map[string]any, 1),
		errs:    make(chan error, 1),
	}
	go client.read()
	t.Cleanup(client.close)
	patch := patchString(client.nextPatch(t))
	if !strings.Contains(patch, `"href":"/workspaces/sales/dashboards/executive-sales"`) {
		t.Fatalf("dashboard catalog updates missing visible dashboard: %s", patch)
	}
}

func TestAuthSpecItemManagerCanShareAndRevokeDashboardAccess(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	manager := authSpecPrincipal(t, ctx, repo, "item-manager@example.com")
	viewer := authSpecPrincipal(t, ctx, repo, "shared-viewer@example.com")
	dashboard := access.ItemObject(access.SecurableDashboard, "sales", "executive-sales")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeUseWorkspace)
	authSpecGrant(t, ctx, repo, dashboard, access.SubjectPrincipal, manager.ID, access.PrivilegeViewItem)
	authSpecGrant(t, ctx, repo, dashboard, access.SubjectPrincipal, manager.ID, access.PrivilegeManageGrants)
	managerToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: manager.ID, WorkspaceID: "sales", Name: "item-manager"})
	viewerToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: viewer.ID, WorkspaceID: "sales", Name: "viewer"})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/grants", managerToken, `{"objectType":"dashboard","objectId":"executive-sales","subjectType":"principal","subjectId":"`+viewer.ID+`","privilege":"VIEW_ITEM"}`)
	if status != http.StatusCreated {
		t.Fatalf("item manager create grant status=%d body=%s", status, body)
	}
	var createdGrant struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &createdGrant); err != nil {
		t.Fatalf("decode created grant: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards/executive-sales", viewerToken, "")
	if status != http.StatusOK {
		t.Fatalf("shared viewer dashboard status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards", viewerToken, "")
	if status != http.StatusForbidden {
		t.Fatalf("shared viewer dashboard list status=%d want=403 body=%s", status, body)
	}

	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/grants/"+createdGrant.ID, managerToken, "")
	if status != http.StatusNoContent {
		t.Fatalf("item manager delete grant status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards/executive-sales", viewerToken, "")
	if status != http.StatusNotFound {
		t.Fatalf("revoked shared viewer dashboard status=%d want=404 body=%s", status, body)
	}
}

func TestAuthSpecGroupSharingFollowsMembershipChanges(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	admin := authSpecPrincipal(t, ctx, repo, "sharing-admin@example.com")
	member := authSpecPrincipal(t, ctx, repo, "group-member@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeManageGrants)
	adminToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: admin.ID, WorkspaceID: "sales", Name: "sharing-admin"})
	memberToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: member.ID, WorkspaceID: "sales", Name: "group-member"})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/groups", adminToken, `{"name":"analysts","displayName":"Analysts"}`)
	if status != http.StatusCreated {
		t.Fatalf("create group status=%d body=%s", status, body)
	}
	var group struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &group); err != nil {
		t.Fatalf("decode group: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodPut, "/api/v1/workspaces/sales/groups/"+group.ID+"/members/"+member.ID, adminToken, "")
	if status != http.StatusOK {
		t.Fatalf("add group member status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/grants", adminToken, `{"objectType":"dashboard","objectId":"executive-sales","subjectType":"group","subjectId":"`+group.ID+`","privilege":"VIEW_ITEM"}`)
	if status != http.StatusCreated {
		t.Fatalf("create group grant status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards/executive-sales", memberToken, "")
	if status != http.StatusOK {
		t.Fatalf("group member dashboard status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/groups/"+group.ID+"/members/"+member.ID, adminToken, "")
	if status != http.StatusNoContent {
		t.Fatalf("remove group member status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards/executive-sales", memberToken, "")
	if status != http.StatusNotFound {
		t.Fatalf("removed group member dashboard status=%d want=404 body=%s", status, body)
	}
}

func TestAuthSpecWorkspaceGroupAPIExposesGlobalSCIMGroupReadOnly(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	admin := authSpecPrincipal(t, ctx, repo, "scim-boundary-admin@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeManageGrants)
	adminToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: admin.ID, WorkspaceID: "sales", Name: "scim-boundary-admin"})
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{
		ID: "scim_group_sales_global", ExternalID: "directory-sales", Name: "Directory Sales",
	})
	if err != nil {
		t.Fatalf("upsert SCIM group: %v", err)
	}

	status, body := h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/groups/"+group.ID, adminToken, "")
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("delete global SCIM group through workspace API status=%d want=422 body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/groups", adminToken, "")
	if status != http.StatusOK {
		t.Fatalf("list workspace groups status=%d body=%s", status, body)
	}
	if !strings.Contains(body, group.ID) || !strings.Contains(body, `"provider":"scim"`) || !strings.Contains(body, `"canManageMembers":false`) {
		t.Fatalf("workspace group collection did not expose read-only SCIM group: %s", body)
	}
	scimGroups, err := repo.ListSCIMGroups(ctx, access.SCIMGroupFilter{ID: group.ID})
	if err != nil {
		t.Fatalf("list SCIM group after workspace delete attempt: %v", err)
	}
	if len(scimGroups) != 1 || scimGroups[0].ID != group.ID {
		t.Fatalf("SCIM group after workspace delete attempt = %#v, want preserved", scimGroups)
	}
}

func TestAuthSpecWorkspaceRoleSharingCompilesToGrants(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	admin := authSpecPrincipal(t, ctx, repo, "role-admin@example.com")
	viewer := authSpecPrincipal(t, ctx, repo, "role-viewer@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeManageGrants)
	adminToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: admin.ID, WorkspaceID: "sales", Name: "role-admin"})
	viewerToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: viewer.ID, WorkspaceID: "sales", Name: "role-viewer"})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/role-bindings", adminToken, `{"subjectType":"principal","subjectId":"`+viewer.ID+`","role":"viewer"}`)
	if status != http.StatusCreated {
		t.Fatalf("create viewer role binding status=%d body=%s", status, body)
	}
	var binding struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &binding); err != nil {
		t.Fatalf("decode role binding: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards", viewerToken, "")
	if status != http.StatusOK {
		t.Fatalf("viewer list dashboards status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/dashboards/executive-sales/pages/overview/query", viewerToken, `{}`)
	if status != http.StatusOK {
		t.Fatalf("viewer dashboard query status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/datasets/orders/preview", viewerToken, `{"dimensions":[{"field":"orders.status"}],"limit":1}`)
	if status != http.StatusNotFound {
		t.Fatalf("viewer raw preview status=%d want=404 body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/grants", viewerToken, `{"objectType":"dashboard","objectId":"executive-sales","subjectType":"principal","subjectId":"email_other","privilege":"VIEW_ITEM"}`)
	if status != http.StatusForbidden {
		t.Fatalf("viewer create grant status=%d want=403 body=%s", status, body)
	}

	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/role-bindings/"+binding.ID, adminToken, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete viewer role binding status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/dashboards", viewerToken, "")
	if status != http.StatusForbidden {
		t.Fatalf("viewer list after role delete status=%d want=403 body=%s", status, body)
	}
}

func TestAuthSpecEffectiveAccessExplainsInheritedGrants(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	principal := authSpecPrincipal(t, ctx, repo, "effective@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, principal.ID, access.PrivilegeUseWorkspace)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, principal.ID, access.PrivilegeQueryData)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, principal.ID, access.PrivilegeManageGrants)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: principal.ID, WorkspaceID: "sales", Name: "effective"})

	status, body := h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/effective-privileges?objectType=dataset&objectId=sales/orders", token, "")
	if status != http.StatusOK {
		t.Fatalf("effective privileges status=%d body=%s", status, body)
	}
	var decoded struct {
		Privileges      []string `json:"privileges"`
		EffectiveGrants []struct {
			Privilege     string `json:"privilege"`
			Reason        string `json:"reason"`
			Inherited     bool   `json:"inherited"`
			GrantObjectID string `json:"grantObjectId"`
		} `json:"effectiveGrants"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode effective access: %v body=%s", err, body)
	}
	if !authSpecHas(decoded.Privileges, string(access.PrivilegeQueryData)) {
		t.Fatalf("privileges=%#v missing QUERY_DATA", decoded.Privileges)
	}
	for _, grant := range decoded.EffectiveGrants {
		if grant.Privilege == string(access.PrivilegeQueryData) {
			if grant.Reason != string(access.ReasonGrant) || !grant.Inherited || grant.GrantObjectID != "semantic_model:sales:sales" {
				t.Fatalf("query grant provenance=%#v, want inherited semantic model grant", grant)
			}
			return
		}
	}
	t.Fatalf("effectiveGrants=%#v missing QUERY_DATA provenance", decoded.EffectiveGrants)
}

func TestAuthSpecShowGrantsIncludesInheritedObjectProvenance(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	manager := authSpecPrincipal(t, ctx, repo, "grant-inspector@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeUseWorkspace)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeManageGrants)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeQueryData)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: manager.ID, WorkspaceID: "sales", Name: "grant-inspector"})

	status, body := h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/grants?objectType=dataset&objectId=sales/orders&includeInherited=true", token, "")
	if status != http.StatusOK {
		t.Fatalf("list inherited grants status=%d body=%s", status, body)
	}
	var decoded struct {
		Items []struct {
			ObjectID  string `json:"objectId"`
			Privilege string `json:"privilege"`
			Inherited bool   `json:"inherited"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode inherited grants: %v body=%s", err, body)
	}
	for _, item := range decoded.Items {
		if item.Privilege == string(access.PrivilegeQueryData) {
			if !item.Inherited || item.ObjectID != "semantic_model:sales:sales" {
				t.Fatalf("inherited grant item=%#v, want semantic model provenance", item)
			}
			return
		}
	}
	t.Fatalf("inherited grants=%#v missing QUERY_DATA", decoded.Items)
}

func TestAuthSpecDataPolicyAPIRowFilterAppliesAndDeletes(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	manager := authSpecPrincipal(t, ctx, repo, "data-policy-manager@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeUseWorkspace)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeManageGrants)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeQueryData)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: manager.ID, WorkspaceID: "sales", Name: "data-policy-manager"})

	if got := h.authSpecQueryRevenue(t, token); got != 165 {
		t.Fatalf("baseline revenue = %v, want 165", got)
	}
	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/data-policies", token, `{"objectType":"dataset","objectId":"sales/orders","policyType":"row_filter","expression":{"field":"orders.status","operator":"equals","values":["delivered"]}}`)
	if status != http.StatusCreated {
		t.Fatalf("create row filter policy status=%d body=%s", status, body)
	}
	var created struct {
		ID         string `json:"id"`
		ObjectID   string `json:"objectId"`
		PolicyType string `json:"policyType"`
	}
	if err := json.Unmarshal([]byte(body), &created); err != nil {
		t.Fatalf("decode data policy: %v body=%s", err, body)
	}
	if created.ID == "" || created.ObjectID != "dataset:sales:sales/orders" || created.PolicyType != "row_filter" {
		t.Fatalf("created data policy = %#v", created)
	}
	if got := h.authSpecQueryRevenue(t, token); got != 110 {
		t.Fatalf("filtered revenue = %v, want 110", got)
	}

	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/data-policies?objectType=dataset&objectId=sales/orders", token, "")
	if status != http.StatusOK {
		t.Fatalf("list data policies status=%d body=%s", status, body)
	}
	if !strings.Contains(body, created.ID) {
		t.Fatalf("list data policies missing created policy %q: %s", created.ID, body)
	}
	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/data-policies/"+created.ID, token, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete data policy status=%d body=%s", status, body)
	}
	if got := h.authSpecQueryRevenue(t, token); got != 165 {
		t.Fatalf("revenue after policy delete = %v, want 165", got)
	}
}

func TestAuthSpecDataPolicySubjectScopeAppliesOnlyToMatchingPrincipal(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	manager := authSpecPrincipal(t, ctx, repo, "subject-policy-manager@example.com")
	analyst := authSpecPrincipal(t, ctx, repo, "subject-policy-analyst@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeUseWorkspace)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeManageGrants)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, manager.ID, access.PrivilegeQueryData)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, analyst.ID, access.PrivilegeQueryData)
	managerToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: manager.ID, WorkspaceID: "sales", Name: "subject-policy-manager"})
	analystToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: analyst.ID, WorkspaceID: "sales", Name: "subject-policy-analyst"})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/data-policies", managerToken, `{"objectType":"dataset","objectId":"sales/orders","policyType":"row_filter","subjectType":"principal","subjectId":"`+analyst.ID+`","expression":{"field":"orders.status","operator":"equals","values":["delivered"]}}`)
	if status != http.StatusCreated {
		t.Fatalf("create subject row filter policy status=%d body=%s", status, body)
	}
	if got := h.authSpecQueryRevenue(t, managerToken); got != 165 {
		t.Fatalf("manager revenue = %v, want unaffected 165", got)
	}
	if got := h.authSpecQueryRevenue(t, analystToken); got != 110 {
		t.Fatalf("analyst revenue = %v, want subject-filtered 110", got)
	}
	if !strings.Contains(body, `"subjectType":"principal"`) || !strings.Contains(body, `"subjectId":"`+analyst.ID+`"`) {
		t.Fatalf("created data policy missing subject scope: %s", body)
	}
}

func TestAuthSpecAPITokenAllowlistReducesEffectiveDataPrivileges(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	principal := authSpecPrincipal(t, ctx, repo, "token-scope@example.com")
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, principal.ID, access.PrivilegeQueryData)
	authSpecGrant(t, ctx, repo, access.ItemObject(access.SecurableSemanticModel, "sales", "sales"), access.SubjectPrincipal, principal.ID, access.PrivilegePreviewData)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{
		PrincipalID: principal.ID,
		WorkspaceID: "sales",
		Name:        "query-only",
		Privileges:  []access.Privilege{access.PrivilegeQueryData},
	})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", token, `{"measures":[{"field":"revenue"}],"limit":1}`)
	if status != http.StatusOK {
		t.Fatalf("query with QUERY_DATA token status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/datasets/orders/preview", token, `{"dimensions":[{"field":"orders.status"}],"limit":1}`)
	if status != http.StatusNotFound {
		t.Fatalf("preview with query-only token status=%d want=404 body=%s", status, body)
	}
}

func TestAuthSpecColumnGrantAllowsOnlyGrantedPreviewColumns(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	principal := authSpecPrincipal(t, ctx, repo, "column-preview@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, principal.ID, access.PrivilegeUseWorkspace)
	statusColumn := access.ItemObjectWithParent(
		access.SecurableColumn,
		"sales",
		"sales/orders/status",
		access.ItemObjectWithParent(access.SecurableDataset, "sales", "sales/orders", access.ItemObject(access.SecurableSemanticModel, "sales", "sales")),
	)
	authSpecGrant(t, ctx, repo, statusColumn, access.SubjectPrincipal, principal.ID, access.PrivilegePreviewData)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: principal.ID, WorkspaceID: "sales", Name: "column-preview"})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/datasets/orders/preview", token, `{"dimensions":[{"field":"orders.status"}],"limit":1}`)
	if status != http.StatusOK {
		t.Fatalf("preview granted column status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/datasets/orders/preview", token, `{"dimensions":[{"field":"orders.status"},{"field":"orders.revenue"}],"limit":1}`)
	if status != http.StatusNotFound {
		t.Fatalf("preview ungranted column status=%d want=404 body=%s", status, body)
	}
}

func TestAuthSpecServicePrincipalRESTAndMCPOAuthTokensAreSeparated(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	admin := authSpecPlatformAdmin(t, ctx, repo, "platform-admin@example.com")
	adminToken := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: admin.ID, Name: "platform-admin", Privileges: []access.Privilege{access.PrivilegeManagePlatform, access.PrivilegeManageGrants}})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/service-principals", adminToken, `{"id":"sp_ci","displayName":"CI"}`)
	if status != http.StatusCreated {
		t.Fatalf("create service principal status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/service-principals/sp_ci/secrets", adminToken, `{"name":"ci"}`)
	if status != http.StatusCreated {
		t.Fatalf("create service principal secret status=%d body=%s", status, body)
	}
	var secretResponse struct {
		Secret       string `json:"secret"`
		ClientSecret struct {
			ID string `json:"id"`
		} `json:"clientSecret"`
	}
	if err := json.Unmarshal([]byte(body), &secretResponse); err != nil {
		t.Fatalf("decode service principal secret: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/grants", adminToken, `{"objectType":"semantic_model","objectId":"sales","subjectType":"service_principal","subjectId":"sp_ci","privilege":"QUERY_DATA"}`)
	if status != http.StatusCreated {
		t.Fatalf("share semantic model with service principal status=%d body=%s", status, body)
	}
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectServicePrincipal, "sp_ci", access.PrivilegeUseAgent)

	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", "sp_ci")
	form.Set("client_secret", secretResponse.Secret)
	form.Set("workspace_id", "sales")
	status, body = h.authSpecForm(t, "/oauth/token", form)
	if status != http.StatusBadRequest {
		t.Fatalf("REST OAuth token empty scope status=%d want=400 body=%s", status, body)
	}
	form.Set("scope", string(access.PrivilegeQueryData))
	status, body = h.authSpecForm(t, "/oauth/token", form)
	if status != http.StatusOK {
		t.Fatalf("REST OAuth token status=%d body=%s", status, body)
	}
	var restTokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &restTokenResponse); err != nil || restTokenResponse.AccessToken == "" {
		t.Fatalf("decode REST oauth token: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", restTokenResponse.AccessToken, `{"measures":[{"field":"revenue"}],"limit":1}`)
	if status != http.StatusOK {
		t.Fatalf("service principal REST query status=%d body=%s", status, body)
	}

	mcpForm := url.Values{}
	mcpForm.Set("grant_type", "client_credentials")
	mcpForm.Set("client_id", "sp_ci")
	mcpForm.Set("client_secret", secretResponse.Secret)
	mcpForm.Set("scope", "mcp:use")
	mcpForm.Set("resource", "http://localhost:8080/mcp")
	status, body = h.authSpecForm(t, "/oauth/token", mcpForm)
	if status != http.StatusOK {
		t.Fatalf("MCP OAuth token status=%d body=%s", status, body)
	}
	var mcpTokenResponse struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal([]byte(body), &mcpTokenResponse); err != nil || mcpTokenResponse.AccessToken == "" {
		t.Fatalf("decode MCP oauth token: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", mcpTokenResponse.AccessToken, `{"measures":[{"field":"revenue"}],"limit":1}`)
	if status != http.StatusUnauthorized {
		t.Fatalf("MCP OAuth token used as REST token status=%d want=401 body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPost, "/mcp", mcpTokenResponse.AccessToken, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"integration-test","version":"1"}}}`)
	if status != http.StatusOK || !strings.Contains(body, `"protocolVersion":"2025-11-25"`) {
		t.Fatalf("service principal MCP initialize status=%d body=%s", status, body)
	}

	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/service-principals/sp_ci/secrets/"+secretResponse.ClientSecret.ID, adminToken, "")
	if status != http.StatusNoContent {
		t.Fatalf("revoke service principal secret status=%d body=%s", status, body)
	}
	status, body = h.authSpecForm(t, "/oauth/token", mcpForm)
	if status != http.StatusUnauthorized {
		t.Fatalf("MCP OAuth token after service secret revoke status=%d want=401 body=%s", status, body)
	}
}

func TestAuthSpecAuditIncludesGrantRequestMetadata(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	admin := authSpecPrincipal(t, ctx, repo, "grant-admin@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeManageGrants)
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeViewAudit)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: admin.ID, WorkspaceID: "sales", Name: "grant-admin"})

	req, err := http.NewRequest(http.MethodPost, h.serverURL(t)+"/api/v1/workspaces/sales/grants", strings.NewReader(`{"objectType":"dashboard","objectId":"executive-sales","subjectType":"principal","subjectId":"email_audited","privilege":"VIEW_ITEM"}`))
	if err != nil {
		t.Fatalf("create grant request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "auth-spec-request")
	req.Header.Set("X-Correlation-ID", "auth-spec-correlation")
	req.Header.Set("Idempotency-Key", "auth-spec-audited-grant")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create grant: %v", err)
	}
	bodyBytes, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create grant status=%d body=%s", res.StatusCode, string(bodyBytes))
	}

	status, body := h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/audit-events?action=grant.created&limit=10", token, "")
	if status != http.StatusOK {
		t.Fatalf("list audit status=%d body=%s", status, body)
	}
	if !strings.Contains(body, `"requestId":"auth-spec-request"`) ||
		!strings.Contains(body, `"correlationId":"auth-spec-correlation"`) ||
		!strings.Contains(body, `"privilege":"VIEW_ITEM"`) ||
		!strings.Contains(body, `"status":"success"`) {
		t.Fatalf("audit response missing auth metadata: %s", body)
	}
}

func TestAuthSpecAuditCoversLocalAccessMutations(t *testing.T) {
	h, repo := newAuthSpecHarness(t)
	ctx := context.Background()

	admin := authSpecPrincipal(t, ctx, repo, "access-audit-admin@example.com")
	member := authSpecPrincipal(t, ctx, repo, "access-audit-member@example.com")
	viewer := authSpecPrincipal(t, ctx, repo, "access-audit-viewer@example.com")
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeManageGrants)
	authSpecGrant(t, ctx, repo, access.WorkspaceObject("sales"), access.SubjectPrincipal, admin.ID, access.PrivilegeViewAudit)
	token := authSpecToken(t, ctx, repo, access.APITokenInput{PrincipalID: admin.ID, WorkspaceID: "sales", Name: "access-audit-admin"})

	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/groups", token, `{"name":"audit-analysts","displayName":"Audit Analysts"}`)
	if status != http.StatusCreated {
		t.Fatalf("create group status=%d body=%s", status, body)
	}
	var group struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &group); err != nil {
		t.Fatalf("decode group: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodPatch, "/api/v1/workspaces/sales/groups/"+group.ID, token, `{"displayName":"Audit Analysts Updated"}`)
	if status != http.StatusOK {
		t.Fatalf("update group status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodPut, "/api/v1/workspaces/sales/groups/"+group.ID+"/members/"+member.ID, token, "")
	if status != http.StatusOK {
		t.Fatalf("add group member status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/groups/"+group.ID+"/members/"+member.ID, token, "")
	if status != http.StatusNoContent {
		t.Fatalf("remove group member status=%d body=%s", status, body)
	}

	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/role-bindings", token, `{"subjectType":"principal","subjectId":"`+viewer.ID+`","role":"viewer"}`)
	if status != http.StatusCreated {
		t.Fatalf("create role binding status=%d body=%s", status, body)
	}
	var binding struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &binding); err != nil {
		t.Fatalf("decode role binding: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodPatch, "/api/v1/workspaces/sales/role-bindings/"+binding.ID, token, `{"subjectType":"principal","subjectId":"`+viewer.ID+`","role":"contributor"}`)
	if status != http.StatusOK {
		t.Fatalf("update role binding status=%d body=%s", status, body)
	}
	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/role-bindings/"+binding.ID, token, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete role binding status=%d body=%s", status, body)
	}

	status, body = h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/grants", token, `{"objectType":"dashboard","objectId":"executive-sales","subjectType":"principal","subjectId":"`+viewer.ID+`","privilege":"VIEW_ITEM"}`)
	if status != http.StatusCreated {
		t.Fatalf("create grant status=%d body=%s", status, body)
	}
	var grant struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(body), &grant); err != nil {
		t.Fatalf("decode grant: %v body=%s", err, body)
	}
	status, body = h.authSpecDo(t, http.MethodDelete, "/api/v1/workspaces/sales/grants/"+grant.ID, token, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete grant status=%d body=%s", status, body)
	}

	status, body = h.authSpecDo(t, http.MethodGet, "/api/v1/workspaces/sales/audit-events?limit=50", token, "")
	if status != http.StatusOK {
		t.Fatalf("list audit status=%d body=%s", status, body)
	}
	actions := authSpecAuditActions(t, body)
	for _, want := range []string{
		"group.created",
		"group.updated",
		"group.member_added",
		"group.member_removed",
		"role_binding.created",
		"role_binding.updated",
		"role_binding.deleted",
		"grant.created",
		"grant.deleted",
	} {
		if !actions[want] {
			t.Fatalf("audit actions missing %q: %#v body=%s", want, actions, body)
		}
	}
}

func newAuthSpecHarness(t *testing.T) (*harness, *accesssqlite.Repository) {
	return newAuthSpecHarnessWithAuthWorkspace(t, AuthConfig{APITokenOnly: true}, "")
}

func newAuthSpecHarnessWithAuthWorkspace(t *testing.T, authConfig AuthConfig, authWorkspaceID string) (*harness, *accesssqlite.Repository) {
	t.Helper()
	h, metrics, catalogPath := newHarnessWithMetrics(t)
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	workspaceID := metrics.Catalog().Workspace.ID
	if workspaceID == "" {
		t.Fatal("auth-spec runtime catalog has no workspace ID")
	}
	workspaceRepo := projectsqlite.NewRepository(store.SQLDB())
	if err := workspaceRepo.Ensure(ctx, workspace.EnsureInput{ID: workspace.WorkspaceID(workspaceID), Title: metrics.Catalog().Workspace.Title, Description: metrics.Catalog().Workspace.Description}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if authWorkspaceID != "" && authWorkspaceID != workspaceID {
		if err := workspaceRepo.Ensure(ctx, workspace.EnsureInput{ID: workspace.WorkspaceID(authWorkspaceID), Title: "Auth Workspace"}); err != nil {
			t.Fatalf("ensure auth workspace: %v", err)
		}
	}
	seedIntegrationActiveDeployment(t, store, workspaceID, catalogPath)
	repo := accesssqlite.NewRepository(store.SQLDB())
	if authWorkspaceID == "" {
		authWorkspaceID = workspaceID
	}
	auth := NewAuth(repo, authConfig)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: workspaceID}))
	h.store = store
	h.handler = server.Routes()
	h.server = httptestNewServer(t, h.handler)
	h.workspaceID = workspaceID
	return h, repo
}

func httptestNewServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func authSpecPrincipal(t *testing.T, ctx context.Context, repo *accesssqlite.Repository, email string) access.Principal {
	t.Helper()
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: access.PrincipalIDForEmail(email), Email: email, DisplayName: email})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	return principal
}

func authSpecPlatformAdmin(t *testing.T, ctx context.Context, repo *accesssqlite.Repository, email string) access.Principal {
	t.Helper()
	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: access.PrincipalIDForEmail(email), Email: email, DisplayName: email, Role: access.RolePlatformAdmin})
	if err != nil {
		t.Fatalf("set platform role: %v", err)
	}
	return principal
}

func authSpecGrant(t *testing.T, ctx context.Context, repo *accesssqlite.Repository, object access.ObjectRef, subjectType access.SubjectType, subjectID string, privilege access.Privilege) {
	t.Helper()
	if _, err := repo.CreateGrant(ctx, access.GrantInput{Object: object, SubjectType: subjectType, SubjectID: subjectID, Privilege: privilege}); err != nil {
		t.Fatalf("create %s grant on %s: %v", privilege, object.CanonicalID(), err)
	}
}

func authSpecToken(t *testing.T, ctx context.Context, repo *accesssqlite.Repository, input access.APITokenInput) string {
	t.Helper()
	token, _, err := repo.CreateAPITokenWithMetadata(ctx, input)
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	return token
}

func authSpecAuditActions(t *testing.T, body string) map[string]bool {
	t.Helper()
	var decoded struct {
		Items []struct {
			Action string `json:"action"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode audit response: %v body=%s", err, body)
	}
	actions := map[string]bool{}
	for _, item := range decoded.Items {
		actions[item.Action] = true
	}
	return actions
}

func (h *harness) authSpecDo(t *testing.T, method, path, token, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, h.serverURL(t)+path, reader)
	if err != nil {
		t.Fatalf("create %s %s: %v", method, path, err)
	}
	req.Header.Set("Accept", "application/json")
	if path == "/mcp" {
		req.Header.Set("Accept", "application/json, text/event-stream")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if method == http.MethodPatch {
		req.Header.Set("If-Match", "*")
	}
	if method == http.MethodPost && strings.HasPrefix(path, "/api/v1/") {
		req.Header.Set("Idempotency-Key", "auth-spec-"+strings.ReplaceAll(path, "/", "-"))
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	bytes, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(bytes)
}

func (h *harness) authSpecQueryRevenue(t *testing.T, token string) float64 {
	t.Helper()
	status, body := h.authSpecDo(t, http.MethodPost, "/api/v1/workspaces/sales/semantic-models/sales/query", token, `{"measures":[{"field":"revenue"}],"limit":1}`)
	if status != http.StatusOK {
		t.Fatalf("semantic revenue query status=%d body=%s", status, body)
	}
	var decoded struct {
		Rows [][]string `json:"rows"`
	}
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("decode semantic revenue query: %v body=%s", err, body)
	}
	if len(decoded.Rows) != 1 || len(decoded.Rows[0]) != 1 {
		t.Fatalf("semantic revenue rows = %#v, want one cell", decoded.Rows)
	}
	value, err := strconv.ParseFloat(decoded.Rows[0][0], 64)
	if err != nil {
		t.Fatalf("parse semantic revenue %q: %v", decoded.Rows[0][0], err)
	}
	return value
}

func (h *harness) authSpecForm(t *testing.T, path string, form url.Values) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.serverURL(t)+path, bytes.NewBufferString(form.Encode()))
	if err != nil {
		t.Fatalf("create form request %s: %v", path, err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	defer res.Body.Close()
	bytes, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(bytes)
}

func authSpecHas(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
