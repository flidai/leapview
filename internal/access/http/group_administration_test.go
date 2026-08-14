package http

import (
	"bytes"
	"context"
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/go-chi/chi/v5"
)

func TestWorkspaceGroupAPIIncludesReadOnlySCIMGroups(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.SQLDB().ExecContext(t.Context(), `INSERT INTO workspaces (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	repository := accesssqlite.NewRepository(store.SQLDB())
	member, err := repository.UpsertSCIMUser(t.Context(), access.SCIMUserInput{
		ExternalID: "directory-member", UserName: "member@example.test", DisplayName: "Directory Member", Active: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	scimGroup, err := repository.UpsertSCIMGroup(t.Context(), access.SCIMGroupInput{
		ExternalID: "directory-analysts", Name: "Directory Analysts", MemberIDs: []string{member.Principal.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertGroup(t.Context(), access.GroupInput{
		WorkspaceID: "sales", Provider: "local", ExternalID: "local-analysts", Name: "Local Analysts",
	}); err != nil {
		t.Fatal(err)
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repository, nil }}

	listRecorder := httptest.NewRecorder()
	handler.ListGroups(listRecorder, groupRequest(stdhttp.MethodGet, "/api/v1/workspaces/sales/groups", "sales", "", "", nil))
	if listRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("list status=%d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var list struct {
		Items []struct {
			ID           string          `json:"id"`
			Provider     string          `json:"provider"`
			Capabilities map[string]bool `json:"capabilities"`
		} `json:"items"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 2 {
		t.Fatalf("groups=%#v", list.Items)
	}
	var foundSCIM bool
	for _, item := range list.Items {
		if item.ID != scimGroup.ID {
			continue
		}
		foundSCIM = true
		if item.Provider != "scim" || item.Capabilities["canUpdate"] || item.Capabilities["canDelete"] || item.Capabilities["canManageMembers"] || !item.Capabilities["canManageAuthorization"] {
			t.Fatalf("SCIM group response=%#v", item)
		}
	}
	if !foundSCIM {
		t.Fatalf("SCIM group missing from response: %#v", list.Items)
	}

	membersRecorder := httptest.NewRecorder()
	handler.ListGroupMembers(membersRecorder, groupRequest(stdhttp.MethodGet, "/api/v1/workspaces/sales/groups/"+scimGroup.ID+"/members", "sales", scimGroup.ID, "", nil))
	if membersRecorder.Code != stdhttp.StatusOK {
		t.Fatalf("members status=%d body=%s", membersRecorder.Code, membersRecorder.Body.String())
	}
	var members struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(membersRecorder.Body.Bytes(), &members); err != nil {
		t.Fatal(err)
	}
	if len(members.Items) != 1 || members.Items[0]["id"] != member.Principal.ID {
		t.Fatalf("members=%#v", members.Items)
	}

	for _, mutation := range []struct {
		name      string
		method    string
		path      string
		principal string
		body      []byte
		call      func(stdhttp.ResponseWriter, *stdhttp.Request)
	}{
		{name: "update", method: stdhttp.MethodPatch, path: "/api/v1/workspaces/sales/groups/" + scimGroup.ID, body: []byte(`{"displayName":"Changed"}`), call: handler.UpdateGroup},
		{name: "delete", method: stdhttp.MethodDelete, path: "/api/v1/workspaces/sales/groups/" + scimGroup.ID, call: handler.DeleteGroup},
		{name: "add member", method: stdhttp.MethodPut, path: "/api/v1/workspaces/sales/groups/" + scimGroup.ID + "/members/" + member.Principal.ID, principal: member.Principal.ID, call: handler.AddGroupMember},
		{name: "remove member", method: stdhttp.MethodDelete, path: "/api/v1/workspaces/sales/groups/" + scimGroup.ID + "/members/" + member.Principal.ID, principal: member.Principal.ID, call: handler.RemoveGroupMember},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			mutation.call(recorder, groupRequest(mutation.method, mutation.path, "sales", scimGroup.ID, mutation.principal, mutation.body))
			if recorder.Code != stdhttp.StatusUnprocessableEntity {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func groupRequest(method, path, workspaceID, groupID, principalID string, body []byte) *stdhttp.Request {
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	routeContext := chi.NewRouteContext()
	routeContext.URLParams.Add("workspace", workspaceID)
	if groupID != "" {
		routeContext.URLParams.Add("group", groupID)
	}
	if principalID != "" {
		routeContext.URLParams.Add("principal", principalID)
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}
