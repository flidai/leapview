package http

import (
	"context"
	"encoding/json"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/go-chi/chi/v5"
)

func TestGlobalGroupAPIIncludesSourceAwareSCIMGroups(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	admin, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: "principal-admin", Email: "admin@example.test", Role: access.PlatformRoleAdmin})
	if err != nil {
		t.Fatal(err)
	}
	member, err := repository.UpsertSCIMUser(t.Context(), access.SCIMUserInput{ExternalID: "directory-member", UserName: "member@example.test", DisplayName: "Directory Member", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	scimGroup, err := repository.UpsertSCIMGroup(t.Context(), access.SCIMGroupInput{ExternalID: "directory-analysts", Name: "Directory Analysts", MemberIDs: []string{member.Principal.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.UpsertGroup(t.Context(), access.GroupInput{Provider: "local", ExternalID: "local-analysts", Name: "Local Analysts"}); err != nil {
		t.Fatal(err)
	}
	handler := Handler{Repository: func() (access.Repository, error) { return repository, nil }, CurrentEffectiveCapabilities: allowProjectAdmin, CurrentPrincipal: func(*stdhttp.Request) (Principal, bool) {
		return Principal{ID: admin.ID, Kind: access.PrincipalKindUser}, true
	}}
	request := func(method, path, groupID string, principalID ...string) *stdhttp.Request {
		r := httptest.NewRequest(method, path, nil)
		ctx := chi.NewRouteContext()
		if groupID != "" {
			ctx.URLParams.Add("group", groupID)
		}
		if len(principalID) > 0 && principalID[0] != "" {
			ctx.URLParams.Add("principal", principalID[0])
		}
		return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, ctx))
	}
	response := httptest.NewRecorder()
	handler.ListGroups(response, request(stdhttp.MethodGet, "/api/v1/groups", ""))
	if response.Code != stdhttp.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	groups := repositoryGroups(t, response.Body.Bytes())
	var found bool
	for _, group := range groups {
		if group.ID == scimGroup.ID {
			found = true
			if group.Provider != "scim" || group.Capabilities["canUpdate"] || group.Capabilities["canDelete"] || group.Capabilities["canManageMembers"] {
				t.Fatalf("SCIM group response=%#v", group)
			}
		}
	}
	if !found {
		t.Fatalf("SCIM group missing: %#v", groups)
	}
	membersResponse := httptest.NewRecorder()
	handler.ListGroupMembers(membersResponse, request(stdhttp.MethodGet, "/api/v1/groups/"+scimGroup.ID+"/members", scimGroup.ID))
	if membersResponse.Code != stdhttp.StatusOK {
		t.Fatalf("list SCIM members status=%d body=%s", membersResponse.Code, membersResponse.Body.String())
	}
	var members struct {
		Items []struct {
			PrincipalID string `json:"principalId"`
		} `json:"items"`
	}
	if err := json.Unmarshal(membersResponse.Body.Bytes(), &members); err != nil {
		t.Fatal(err)
	}
	if len(members.Items) != 1 || members.Items[0].PrincipalID != member.Principal.ID {
		t.Fatalf("SCIM members=%#v, want principal %q", members.Items, member.Principal.ID)
	}
	update := request(stdhttp.MethodPatch, "/api/v1/groups/"+scimGroup.ID, scimGroup.ID)
	update.Body = io.NopCloser(strings.NewReader(`{"displayName":"Changed"}`))
	update.Header.Set("Content-Type", "application/json")
	for _, mutation := range []struct {
		name string
		call func(stdhttp.ResponseWriter, *stdhttp.Request)
		req  *stdhttp.Request
	}{
		{name: "update", call: handler.UpdateGroup, req: update},
		{name: "delete", call: handler.DeleteGroup, req: request(stdhttp.MethodDelete, "/api/v1/groups/"+scimGroup.ID, scimGroup.ID)},
		{name: "add member", call: handler.AddGroupMember, req: request(stdhttp.MethodPost, "/api/v1/groups/"+scimGroup.ID+"/members/"+member.Principal.ID, scimGroup.ID, member.Principal.ID)},
		{name: "remove member", call: handler.RemoveGroupMember, req: request(stdhttp.MethodDelete, "/api/v1/groups/"+scimGroup.ID+"/members/"+member.Principal.ID, scimGroup.ID, member.Principal.ID)},
	} {
		recorder := httptest.NewRecorder()
		mutation.call(recorder, mutation.req)
		if recorder.Code != stdhttp.StatusUnprocessableEntity {
			t.Fatalf("%s status=%d body=%s", mutation.name, recorder.Code, recorder.Body.String())
		}
	}
}

type groupResponse struct {
	ID           string          `json:"id"`
	Provider     string          `json:"provider"`
	Capabilities map[string]bool `json:"capabilities"`
}

func repositoryGroups(t *testing.T, body []byte) []groupResponse {
	t.Helper()
	var response struct {
		Items []groupResponse `json:"items"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		t.Fatal(err)
	}
	return response.Items
}
