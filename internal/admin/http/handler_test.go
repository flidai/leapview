package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/admin/ui"
	"github.com/flidai/leapview/internal/agent/api"
)

func TestDirectoryListReadsSkipUnrelatedAdminProviders(t *testing.T) {
	agentCalls, publicationCalls := 0, 0
	model := ReadModel{
		AgentDetails: func(context.Context) (api.AdminAgentResponse, error) {
			agentCalls++
			return api.AdminAgentResponse{}, nil
		},
		Publications: func(*http.Request) ([]ui.AdminPublication, bool, error) {
			publicationCalls++
			return nil, false, nil
		},
	}
	for _, path := range []string{"/admin/principals?q=analyst", "/admin/groups?q=ops"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		var err error
		if strings.Contains(path, "principals") {
			_, err = model.PrincipalsListData(req)
		} else {
			_, err = model.GroupsListData(req)
		}
		if err != nil {
			t.Fatalf("list data for %s: %v", path, err)
		}
	}
	if agentCalls != 0 || publicationCalls != 0 {
		t.Fatalf("unrelated provider calls = agent:%d publications:%d, want zero", agentCalls, publicationCalls)
	}
}

func TestAdminRootRedirectsToDefaultProfile(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)

	Handler{ReadModel: ReadModel{}}.AdminRoot(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d, body = %s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin/profile" {
		t.Fatalf("location = %q, want /admin/profile", location)
	}
}

func TestProfileRendersAdminOwnedPageAdapter(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/admin/profile", nil)

	Handler{ReadModel: ReadModel{}}.Profile(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, "<lv-admin-page") || !strings.Contains(body, "section=profile") {
		t.Fatalf("profile handler did not render the profile route shell:\n%s", body)
	}
}

func TestPersonalSettingsRejectAuthoringCredentials(t *testing.T) {
	handler := Handler{ReadModel: ReadModel{}, CurrentCredential: func(*http.Request) (access.APICredential, bool) {
		return access.APICredential{Authoring: &access.AuthoringSession{ID: "authoring-1"}}, true
	}}
	for _, path := range []string{"/admin/profile", "/admin/security", "/admin/api-tokens"} {
		recorder := httptest.NewRecorder()
		handler.Profile(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s status = %d, want %d", path, recorder.Code, http.StatusForbidden)
		}
	}
}

func TestBuildAdminPrincipalsKeepsEmailDuplicatesIDDistinct(t *testing.T) {
	principals := []ui.AdminPrincipal{
		{ID: "principal-old", Email: "analyst@example.com", DisplayName: "Sales Analyst", CreatedAt: "2026-08-06T00:00:00Z"},
		{ID: "principal-new", Email: "ANALYST@example.com", DisplayName: "", CreatedAt: "2026-08-08T00:00:00Z"},
		{ID: "other", Email: "other@example.com", DisplayName: "Other"},
	}
	bindings := []adminRoleBindingView{
		{SubjectType: "principal", PrincipalID: "principal-old", Role: "viewer"},
		{SubjectType: "principal", PrincipalID: "principal-new", Role: "editor"},
	}
	groups := map[string]access.Group{
		"sales": {ID: "sales", Name: "Sales"},
	}
	members := map[string][]ui.AdminPrincipalRef{
		"sales": {
			{ID: "principal-old"},
			{ID: "principal-new"},
		},
	}

	got := buildAdminPrincipals(principals, bindings, groups, members)
	if len(got) != 3 {
		t.Fatalf("principal count = %d, want 3: %#v", len(got), got)
	}
	oldPrincipal, newPrincipal := got[0], got[1]
	if oldPrincipal.ID != "principal-old" || newPrincipal.ID != "principal-new" {
		t.Fatalf("duplicate-email principals = %#v, want distinct IDs", got[:2])
	}
	if len(oldPrincipal.DirectRoles) != 1 || oldPrincipal.DirectRoles[0] != "viewer" {
		t.Fatalf("old principal roles = %#v, want viewer", oldPrincipal.DirectRoles)
	}
	if len(newPrincipal.DirectRoles) != 1 || newPrincipal.DirectRoles[0] != "editor" {
		t.Fatalf("new principal roles = %#v, want editor", newPrincipal.DirectRoles)
	}
	if len(oldPrincipal.Groups) != 1 || len(newPrincipal.Groups) != 1 {
		t.Fatalf("principal groups = %#v / %#v, want independent Sales memberships", oldPrincipal.Groups, newPrincipal.Groups)
	}
}
