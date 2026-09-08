package app

import (
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/analytics/queryaudit"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/pagestream"
)

type synchronizedResponseRecorder struct {
	mu sync.RWMutex
	*httptest.ResponseRecorder
}

func newSynchronizedResponseRecorder() *synchronizedResponseRecorder {
	return &synchronizedResponseRecorder{ResponseRecorder: httptest.NewRecorder()}
}

func (r *synchronizedResponseRecorder) Write(body []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.ResponseRecorder.Write(body)
}

func (r *synchronizedResponseRecorder) WriteHeader(status int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.WriteHeader(status)
}

func (r *synchronizedResponseRecorder) Flush() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ResponseRecorder.Flush()
}

func (r *synchronizedResponseRecorder) BodyString() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.Body.String()
}

func TestAdminRoutesExposeOnlyPersonalSettingsToViewer(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	viewer := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer")
	token := testAPIToken(t, ctx, store, viewer.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	for _, tc := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{method: http.MethodGet, path: "/admin", status: http.StatusSeeOther},
		{method: http.MethodGet, path: "/admin/profile", status: http.StatusOK},
		{method: http.MethodGet, path: "/admin/security", status: http.StatusOK},
		{method: http.MethodGet, path: "/admin/api-tokens", status: http.StatusOK},
		{method: http.MethodGet, path: "/admin/agent", status: http.StatusForbidden},
		{method: http.MethodGet, path: "/admin/storage", status: http.StatusForbidden},
		{method: http.MethodGet, path: "/admin/storage/tables/model/orders", status: http.StatusForbidden},
		{method: http.MethodGet, path: "/updates?route=admin&section=storage", status: http.StatusForbidden},
		{method: http.MethodGet, path: "/updates?route=admin&section=storage-detail&schema=model&table=orders", status: http.StatusForbidden},
	} {
		req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)

		if rec.Code != tc.status {
			t.Fatalf("%s status = %d, want %d body=%s", tc.path, rec.Code, tc.status, rec.Body.String())
		}
	}
}

func TestAdminPagesRenderAccessAdministrationShells(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	analyst := testPrincipal(t, ctx, store, "analyst@example.com", "Analyst")
	repo := testAccessRepository(store)
	group, err := repo.UpsertGroup(ctx, access.GroupInput{ID: "group_finance", Provider: "local", ExternalID: "finance", Name: "Finance"})
	if err != nil {
		t.Fatalf("seed group: %v", err)
	}
	if err := repo.AddGroupMember(ctx, group.ID, analyst.ID); err != nil {
		t.Fatalf("seed group member: %v", err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})}))

	cases := []struct {
		path   string
		status int
		want   []string
	}{
		{path: "/admin", status: http.StatusSeeOther, want: []string{"/admin/profile"}},
		{path: "/admin/profile", want: []string{"<lv-admin-page", `section="profile"`, `/updates?route=admin&amp;section=profile`}},
		{path: "/admin/principals", want: []string{"<lv-admin-page", `section="principals"`, `/updates?route=admin&amp;section=principals`, "/admin/access/command", "createPrincipal"}},
		{path: "/admin/principals/" + analyst.ID, want: []string{"<lv-admin-page", `section="principal-detail"`, `/updates?principal=` + analyst.ID + `&amp;route=admin&amp;section=principal-detail`, "/admin/access/command", "resetPrincipalPassword"}},
		{path: "/admin/groups", want: []string{"<lv-admin-page", `section="groups"`, `/updates?route=admin&amp;section=groups`, "/admin/access/command", "createGroup"}},
		{path: "/admin/groups/group_finance", want: []string{"<lv-admin-page", `section="group-detail"`, `/updates?group=group_finance&amp;route=admin&amp;section=group-detail`, "/admin/access/command", "addGroupMember"}},
		{path: "/admin/agent", want: []string{"<lv-admin-page", `section="agent"`, `/updates?route=admin&amp;section=agent`, "/admin/agent/config", "updateAgentConfig"}},
		{path: "/admin/storage", want: []string{"<lv-admin-page", `section="storage"`, `/updates?route=admin&amp;section=storage`}},
		{path: "/admin/queries", want: []string{"<lv-admin-page", `section="queries"`, `/updates?route=admin&amp;section=queries`, "/admin/queries/command"}},
		{path: "/admin/publications", want: []string{"<lv-admin-page", `section="publications"`, `/updates?route=admin&amp;section=publications`, "/admin/publications/command"}},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		wantStatus := tc.status
		if wantStatus == 0 {
			wantStatus = http.StatusOK
		}
		if rec.Code != wantStatus {
			t.Fatalf("%s status = %d want %d body=%s", tc.path, rec.Code, wantStatus, rec.Body.String())
		}
		body := rec.Body.String()
		for _, want := range tc.want {
			if !strings.Contains(body, want) && !strings.Contains(rec.Header().Get("Location"), want) {
				t.Fatalf("%s missing %q:\n%s", tc.path, want, body)
			}
		}
		if tc.status != 0 {
			continue
		}
		for _, notWant := range []string{"Assign role", "Remove access", "<form", "data-on:lv-project-access-upsert", "refresh-materializations"} {
			if strings.Contains(body, notWant) {
				t.Fatalf("%s rendered write control %q:\n%s", tc.path, notWant, body)
			}
		}
	}
}

func TestAdminAccessCommandBlocksPrincipalAndReturnsSignalPatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	repo := testAccessRepository(store)
	target, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "member@example.com", DisplayName: "Member"})
	if err != nil {
		t.Fatal(err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	body := strings.NewReader(`{"adminAccessCommand":{"action":"block_principal","principalId":"` + target.Principal.ID + `"}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/access/command?section=principal-detail&principal="+target.Principal.ID, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(uicommand.HeaderOperationID, "disablePrincipal")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"adminAccess"`) || !strings.Contains(rec.Body.String(), `"blockedAt"`) {
		t.Fatalf("command response = %d %s", rec.Code, rec.Body.String())
	}
	stored, err := repo.PrincipalByID(ctx, target.Principal.ID)
	if err != nil || stored.BlockedAt == "" {
		t.Fatalf("blocked principal = %#v, %v", stored, err)
	}
}

func TestAdminAccessCommandDeletesPrincipalAndReturnsClientRedirectSignal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	repo := testAccessRepository(store)
	target, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "delete-me@example.com", DisplayName: "Delete Me"})
	if err != nil {
		t.Fatal(err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	body := strings.NewReader(`{"adminAccessCommand":{"action":"delete_principal","principalId":"` + target.Principal.ID + `"}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/access/command?section=principal-detail&principal="+target.Principal.ID, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(uicommand.HeaderOperationID, "deletePrincipal")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"redirectTo":"/admin/principals"`) {
		t.Fatalf("command response = %d %s", rec.Code, rec.Body.String())
	}
	if _, err := repo.PrincipalByID(ctx, target.Principal.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted principal lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestAdminAccessCommandCreatesGroupAndReturnsDetailRedirectSignal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	body := strings.NewReader(`{"adminAccessCommand":{"action":"create_group","projectId":"test","displayName":"Revenue analysts"}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/access/command?section=groups", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(uicommand.HeaderOperationID, "createGroup")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"redirectTo":"/admin/groups/group_`) {
		t.Fatalf("command response = %d %s", rec.Code, rec.Body.String())
	}
	groups, err := testAccessRepository(store).ListGroups(ctx)
	if err != nil || !slices.ContainsFunc(groups, func(group access.Group) bool { return group.Name == "Revenue analysts" }) {
		t.Fatalf("groups = %#v, err=%v", groups, err)
	}
}

func TestAdminAccessCommandAddsMultipleGroupMembers(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	repo := testAccessRepository(store)
	first, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "first@example.com", DisplayName: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "second@example.com", DisplayName: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{Name: "Analysts"})
	if err != nil {
		t.Fatal(err)
	}
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	body := strings.NewReader(`{"adminAccessCommand":{"action":"add_group_member","projectId":"test","groupId":"` + group.ID + `","principalIds":["` + first.Principal.ID + `","` + second.Principal.ID + `"]}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/access/command?section=group-detail&group="+group.ID, body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(uicommand.HeaderOperationID, "addGroupMember")
	rec := httptest.NewRecorder()

	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"message":"2 members added."`) {
		t.Fatalf("command response = %d %s", rec.Code, rec.Body.String())
	}
	members, err := repo.ListGroupMembers(ctx, group.ID)
	if err != nil || len(members) != 2 {
		t.Fatalf("members = %#v, err=%v", members, err)
	}
}

func TestAdminQueryHistoryCommandPublishesLoadMorePatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	repo := queryAuditRepositoryForTest(t, server)
	for _, event := range []queryaudit.EventInput{
		{ProjectID: projectgraph.ResourceID("project:test"), PrincipalID: owner.ID, Surface: "api", Operation: "api_query", QueryKind: "semantic_rows", ModelID: "sales", Target: "orders", Status: "success", SQL: "select 1"},
		{ProjectID: projectgraph.ResourceID("project:test"), PrincipalID: owner.ID, Surface: "dashboard", Operation: "dashboard_visual", QueryKind: "semantic_rows", ModelID: "sales", Target: "customers", Status: "success", SQL: "select 2"},
		{ProjectID: projectgraph.ResourceID("project:test"), PrincipalID: owner.ID, Surface: "agent", Operation: "agent_query", QueryKind: "semantic_rows", ModelID: "operations", Target: "reviews", Status: "error", SQL: "select 3"},
	} {
		if err := repo.RecordQueryEvent(ctx, event); err != nil {
			t.Fatalf("record query event: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	first, err := repo.ListQueryEvents(ctx, queryaudit.Filter{Limit: 2})
	if err != nil || len(first) != 2 {
		t.Fatalf("first page = %d, err=%v", len(first), err)
	}
	nextCursor := encodeAdminQueryCursor(first[1].CreatedAt, first[1].ID)
	expectedNext, err := repo.ListQueryEvents(ctx, queryaudit.Filter{PageToken: nextCursor, Limit: 2})
	if err != nil || len(expectedNext) != 1 {
		t.Fatalf("next page = %d, err=%v", len(expectedNext), err)
	}
	updates, unsubscribe, err := server.runtime.broker.Subscribe("admin-queries:test-client")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	body := strings.NewReader(`{"adminQueryHistory":{"table":{"rows":[{"id":"existing","query":{"label":"select 1","expandedContent":"select 1"}}]}},"adminQueryHistoryCommand":{"action":"load_more","pageToken":"` + nextCursor + `","limit":2}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/queries/command", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		history, ok := patch["adminQueryHistory"].(uisignals.AdminQueryHistorySignal)
		if !ok {
			t.Fatalf("patch missing adminQueryHistory: %#v", patch)
		}
		if len(history.Table.Rows) != 2 || history.Table.Rows[0]["id"] != "existing" || history.Table.Rows[1]["id"] != expectedNext[0].ID {
			t.Fatalf("rows were not appended correctly: %#v", history.Table.Rows)
		}
		if history.HasMore || history.NextCursor != "" || history.LoadedCountLabel != "2 queries loaded" || history.Loading {
			t.Fatalf("unexpected pagination state: %#v", history)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query history patch")
	}
}

func encodeAdminQueryCursor(createdAt, id string) string {
	if strings.TrimSpace(createdAt) == "" || strings.TrimSpace(id) == "" {
		return ""
	}
	return base64.RawURLEncoding.EncodeToString([]byte(createdAt + "\x00" + id))
}

func TestAdminQueryHistoryCommandPublishesFilteredResetPatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	repo := queryAuditRepositoryForTest(t, server)
	for _, event := range []queryaudit.EventInput{
		{ProjectID: projectgraph.ResourceID("project:sales"), PrincipalID: owner.ID, Surface: "api", Operation: "api_query", QueryKind: "semantic_rows", ModelID: "sales", Target: "orders", Status: "success", SQL: "select orders"},
		{ProjectID: projectgraph.ResourceID("project:operations"), PrincipalID: owner.ID, Surface: "agent", Operation: "agent_query", QueryKind: "semantic_rows", ModelID: "operations", Target: "reviews", Status: "error", SQL: "select reviews"},
	} {
		if err := repo.RecordQueryEvent(ctx, event); err != nil {
			t.Fatalf("record query event: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	updates, unsubscribe, err := server.runtime.broker.Subscribe("admin-queries:test-client")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	body := strings.NewReader(`{"adminQueryHistoryCommand":{"action":"reset","limit":50,"filters":{"projects":["project:sales"],"surfaces":["api"],"statuses":["success"],"search":"orders"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/queries/command", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		history, ok := patch["adminQueryHistory"].(uisignals.AdminQueryHistorySignal)
		if !ok {
			t.Fatalf("patch missing adminQueryHistory: %#v", patch)
		}
		if len(history.Table.Rows) != 1 || history.Table.Rows[0]["runtime"] != "project:sales" || history.Table.Rows[0]["target"] != "orders" {
			t.Fatalf("filtered reset rows = %#v", history.Table.Rows)
		}
		projects := uisignals.ValueOrZero(history.Filters.Projects)
		surfaces := uisignals.ValueOrZero(history.Filters.Surfaces)
		statuses := uisignals.ValueOrZero(history.Filters.Statuses)
		if len(projects) != 1 || projects[0] != "project:sales" || len(surfaces) != 1 || surfaces[0] != "api" || len(statuses) != 1 || statuses[0] != "success" || uisignals.ValueOrZero(history.Filters.Search) != "orders" {
			t.Fatalf("filters were not preserved: %#v", history.Filters)
		}
		filterMenus := uisignals.ValueOrZero(history.FilterMenus)
		if len(filterMenus) == 0 || uisignals.ValueOrZero(filterMenus[0].SummaryLabel) == "" {
			t.Fatalf("filter menus were not patched: %#v", history.FilterMenus)
		}
		command, ok := patch["adminQueryHistoryCommand"].(uisignals.AdminQueryHistoryCommand)
		if !ok || uisignals.ValueOrZero(command.PageToken) != history.NextCursor || command.Action != "load_more" {
			t.Fatalf("command patch = %#v", patch["adminQueryHistoryCommand"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query history patch")
	}
}

func TestAdminQueryHistoryCommandSearchesFilterMenuOptions(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	repo := queryAuditRepositoryForTest(t, server)
	for _, event := range []queryaudit.EventInput{
		{ProjectID: projectgraph.ResourceID("project:sales"), PrincipalID: owner.ID, Surface: "api", Operation: "api_query", QueryKind: "semantic_rows", ModelID: "sales", Target: "orders", Status: "success", SQL: "select orders"},
		{ProjectID: projectgraph.ResourceID("project:operations"), PrincipalID: owner.ID, Surface: "agent", Operation: "agent_query", QueryKind: "semantic_rows", ModelID: "operations", Target: "reviews", Status: "error", SQL: "select reviews"},
	} {
		if err := repo.RecordQueryEvent(ctx, event); err != nil {
			t.Fatalf("record query event: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	updates, unsubscribe, err := server.runtime.broker.Subscribe("admin-queries:test-client")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	body := strings.NewReader(`{"adminQueryHistory":{"filterMenus":[{"id":"project","label":"Project"}]},"adminQueryHistoryCommand":{"action":"filter_search","limit":50,"filterMenu":{"menuId":"project","action":"search","search":"oper"}}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/queries/command", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		history, ok := patch["adminQueryHistory"].(uisignals.AdminQueryHistorySignal)
		if !ok {
			t.Fatalf("patch missing adminQueryHistory: %#v", patch)
		}
		projectMenu := queryHistoryMenuForTest(uisignals.ValueOrZero(history.FilterMenus), "project")
		projectOptions := uisignals.ValueOrZero(projectMenu.Options)
		if uisignals.ValueOrZero(projectMenu.Search) != "oper" || len(projectOptions) != 1 || projectOptions[0].Value != "project:operations" {
			t.Fatalf("project menu = %#v", projectMenu)
		}
		if len(history.Table.Rows) != 0 {
			t.Fatalf("filter search should not patch table rows: %#v", history.Table.Rows)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query history patch")
	}
}

func TestAdminQueryHistoryCommandTogglesFilterAndResetsTable(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	repo := queryAuditRepositoryForTest(t, server)
	for _, event := range []queryaudit.EventInput{
		{ProjectID: projectgraph.ResourceID("project:test"), PrincipalID: owner.ID, Surface: "api", Operation: "api_query", QueryKind: "semantic_rows", ModelID: "sales", Target: "orders", Status: "success", SQL: "select orders"},
		{ProjectID: projectgraph.ResourceID("project:test"), PrincipalID: owner.ID, Surface: "agent", Operation: "agent_query", QueryKind: "semantic_rows", ModelID: "operations", Target: "reviews", Status: "error", SQL: "select reviews"},
	} {
		if err := repo.RecordQueryEvent(ctx, event); err != nil {
			t.Fatalf("record query event: %v", err)
		}
		time.Sleep(time.Millisecond)
	}
	updates, unsubscribe, err := server.runtime.broker.Subscribe("admin-queries:test-client")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	body := strings.NewReader(`{"adminQueryHistory":{"table":{"rows":[{"id":"old"}]}},"adminQueryHistoryCommand":{"action":"filter_toggle","limit":50,"filterMenu":{"menuId":"surface","action":"toggle","value":"agent","selected":[]}}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/queries/command", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		history, ok := patch["adminQueryHistory"].(uisignals.AdminQueryHistorySignal)
		if !ok {
			t.Fatalf("patch missing adminQueryHistory: %#v", patch)
		}
		surfaces := uisignals.ValueOrZero(history.Filters.Surfaces)
		if len(surfaces) != 1 || surfaces[0] != "agent" {
			t.Fatalf("surface filter = %#v", history.Filters)
		}
		if len(history.Table.Rows) != 1 || history.Table.Rows[0]["target"] != "reviews" {
			t.Fatalf("filtered table rows = %#v", history.Table.Rows)
		}
		surfaceMenu := queryHistoryMenuForTest(uisignals.ValueOrZero(history.FilterMenus), "surface")
		selected := uisignals.ValueOrZero(surfaceMenu.Selected)
		if uisignals.ValueOrZero(surfaceMenu.SummaryLabel) != "agent" || len(selected) != 1 || selected[0] != "agent" {
			t.Fatalf("surface menu = %#v", surfaceMenu)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query history patch")
	}
}

func TestAdminQueryHistoryCommandPublishesDetailPatch(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))
	repo := queryAuditRepositoryForTest(t, server)
	if err := repo.RecordQueryEvent(ctx, queryaudit.EventInput{
		ProjectID:     projectgraph.ResourceID("project:test"),
		PrincipalID:   owner.ID,
		Surface:       "api",
		Operation:     "api_query",
		QueryKind:     "semantic_rows",
		ModelID:       "sales",
		Target:        "orders",
		ObjectType:    "semantic_dataset",
		ObjectID:      "sales:orders",
		RequestID:     "req_detail",
		CorrelationID: "corr_detail",
		Status:        "success",
		DurationMS:    17,
		RowsReturned:  3,
		SQL:           "select * from orders",
		PlanText:      "orders plan",
		QueryJSON:     `{"target":"orders"}`,
	}); err != nil {
		t.Fatalf("record query event: %v", err)
	}
	events, err := repo.ListQueryEvents(ctx, queryaudit.Filter{Search: "orders", Limit: 1})
	if err != nil || len(events) != 1 {
		t.Fatalf("query events = %d, err=%v", len(events), err)
	}
	updates, unsubscribe, err := server.runtime.broker.Subscribe("admin-queries:test-client")
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer unsubscribe()

	body := strings.NewReader(`{"adminQueryHistoryCommand":{"action":"select_detail","eventId":"` + events[0].ID + `","limit":50}}`)
	req := httptest.NewRequest(http.MethodPost, "/admin/queries/command", body)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		detail, ok := patch["adminQueryDetail"].(uisignals.AdminQueryDetailSignal)
		if !ok {
			t.Fatalf("patch missing adminQueryDetail: %#v", patch)
		}
		if uisignals.ValueOrZero(detail.EventID) != events[0].ID || uisignals.ValueOrZero(detail.ProjectID) != "project:test" || uisignals.ValueOrZero(detail.SQL) != "select * from orders" || uisignals.ValueOrZero(detail.PlanText) != "orders plan" || uisignals.ValueOrZero(detail.QueryJSON) == "" {
			t.Fatalf("detail patch = %#v", detail)
		}
		if _, ok := patch["adminQueryHistory"]; ok {
			t.Fatalf("detail selection should not patch history: %#v", patch)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for query detail patch")
	}
}

func queryHistoryMenuForTest(menus []uisignals.FilterMenuSignal, id string) uisignals.FilterMenuSignal {
	for _, menu := range menus {
		if menu.ID == id {
			return menu
		}
	}
	return uisignals.FilterMenuSignal{}
}

func TestAdminQueryHistoryCommandRequiresCSRF(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, accessmodule.AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	req := httptest.NewRequest(http.MethodPost, "http://localhost:8150/admin/queries/command", strings.NewReader(`{"adminQueryHistoryCommand":{"action":"reset","limit":50}}`))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "http://localhost:8150/admin/queries")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF status = %d, want %d body=%s", rec.Code, http.StatusForbidden, rec.Body.String())
	}
}

func TestAdminPrincipalSearchCommandPatchesOnlyDirectoryRows(t *testing.T) {
	t.Setenv("LEAPVIEW_DEV_AUTH_BYPASS", "1")
	store := testStore(t)
	auth := testAuth(store, accessmodule.AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	signals := url.QueryEscape(`{"entityListQuery":"analyst","entityListFilter":"all"}`)
	req := httptest.NewRequest(http.MethodGet, "/admin/principals/search?datastar="+signals, nil)
	req.Header.Set("Authorization", "Bearer dev")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	patches := ssetest.PatchSignals(t, rec.Body.String())
	if len(patches) != 1 {
		t.Fatalf("patch count = %d, want 1: %s", len(patches), rec.Body.String())
	}
	page, ok := patches[0]["page"].(map[string]any)
	if !ok || len(patches[0]) != 1 || len(page) != 1 {
		t.Fatalf("search command patched more than directory results: %#v", patches[0])
	}
	directory, ok := page["directoryList"].(map[string]any)
	if !ok || len(directory) != 1 {
		t.Fatalf("directory patch = %#v", page["directoryList"])
	}
	if _, ok := directory["items"].([]any); !ok {
		t.Fatalf("directory items = %#v", directory["items"])
	}
}

func TestAdminQueryHistoryUpdatesForwardsPatches(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(reqCtx, http.MethodGet, "/updates?route=admin&section=queries", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := newSynchronizedResponseRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(rec, req)
	}()

	deadline := time.After(10 * time.Second)
	for !strings.Contains(rec.BodyString(), "sentinel") {
		server.runtime.broker.Publish("admin-queries:test-client", pagestream.SignalPatch{"adminQueryHistory": map[string]any{"loadedCountLabel": "sentinel"}})
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for forwarded patch:\n%s", rec.BodyString())
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	cancel()
	<-done
	if got := rec.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Fatalf("content type = %q, want text/event-stream", got)
	}
}

func TestAdminAccessRouteIsDropped(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	req := httptest.NewRequest(http.MethodGet, "/admin/access", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestAdminPrincipalDetailReturnsNotFoundForMissingPrincipal(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	req := httptest.NewRequest(http.MethodGet, "/admin/principals/missing", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestAdminGroupDetailReturnsNotFoundForMissingGroup(t *testing.T) {
	store := testStore(t)
	ctx := context.Background()
	owner := testPlatformPrincipal(t, ctx, store, "owner@example.com", "Owner")
	token := testAPIToken(t, ctx, store, owner.ID, "test")
	auth := testAuth(store, accessmodule.AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth}))

	req := httptest.NewRequest(http.MethodGet, "/admin/groups/missing", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestAdminDefaultsToProfileWithoutStore(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{})
	req := httptest.NewRequest(http.MethodGet, "/admin", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d want %d body=%s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	if location := rec.Header().Get("Location"); location != "/admin/profile" {
		t.Fatalf("location = %q, want /admin/profile", location)
	}
}

func TestAdminStorageRendersEmptyStateWithoutDuckDBFiles(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{DuckDBDir: t.TempDir()})
	req := httptest.NewRequest(http.MethodGet, "/admin/storage", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"<lv-admin-page", `section="storage"`, `/updates?route=admin&amp;section=storage`} {
		if !strings.Contains(body, want) {
			t.Fatalf("admin storage missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "No DuckLake catalog has been initialized.") || strings.Contains(body, "data-signals=") {
		t.Fatalf("admin storage should stream read-model state instead of embedding it:\n%s", body)
	}
}
