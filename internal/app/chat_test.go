package app

import (
	"context"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Yacobolo/toolbelt/pagestream"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/agent"
	"github.com/flidai/leapview/internal/agent/api"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	agentuiaction "github.com/flidai/leapview/internal/agent/uiaction"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardfilter "github.com/flidai/leapview/internal/dashboard/filter"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	visualizationruntime "github.com/flidai/leapview/internal/dashboard/visualization/runtime"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	workspacecompiler "github.com/flidai/leapview/internal/project/compiler"
	servingstate "github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workspace"
	productsearch "github.com/flidai/leapview/internal/workspace/search"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

func TestTypedChatArtifactsPreserveTabularTypeAcrossJSON(t *testing.T) {
	for _, visualType := range []string{"table", "matrix", "pivot"} {
		t.Run(visualType, func(t *testing.T) {
			kind := map[string]string{"table": "data_table", "matrix": "matrix_table", "pivot": "pivot_table"}[visualType]
			table := dashboard.Table{
				Kind: kind, Title: "Orders", Style: dashboard.TableStyle{}.WithDefaults(),
				Interaction: dashboard.InteractionConfig{}, Selection: []dashboard.InteractionSelectionEntry{},
				Columns: []dashboard.TableColumn{{Key: "value", Label: "Value", Role: "measure"}}, Cardinality: dashboard.ExactCardinality(0), Blocks: map[string]dashboard.TableBlock{},
			}
			authored := reportdef.TableVisual{Title: "Orders", Columns: table.Columns, Query: reportdef.TableQuery{Table: "table", Fields: []string{"value"}}}
			if visualType != "table" {
				authored.Query.Fields = nil
				authored.Query.Rows = []reportdef.FieldRef{{Field: "label", Alias: "label"}}
				authored.Query.Measures = []reportdef.FieldRef{{Field: "value", Alias: "value"}}
				table.Columns = []dashboard.TableColumn{{Key: "label", Label: "Label"}, {Key: "value", Label: "Value", Role: "measure"}}
				authored.Columns = table.Columns
			}
			definitions, err := workspacecompiler.CompileVisualizationDefinitions(&reportdef.Dashboard{
				ID: "test", SemanticModel: "model",
				Visuals: reportdef.TabularVisualizations(visualType, map[string]reportdef.TableVisual{"orders": authored}),
			})
			if err != nil {
				t.Fatal(err)
			}
			envelope, err := visualizationruntime.WindowEnvelopeFromDefinition(definitions["orders"], table, 1, 1)
			if err != nil {
				t.Fatal(err)
			}
			stored, err := json.Marshal(envelope)
			if err != nil {
				t.Fatal(err)
			}
			var persisted any
			if err := json.Unmarshal(stored, &persisted); err != nil {
				t.Fatal(err)
			}
			visuals := agentmodule.TypedChatArtifacts(agent.ChatArtifactSignals{Visuals: map[string]any{"orders": persisted}})
			encoded, err := json.Marshal(visuals["orders"])
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(encoded), `"visualID":"orders"`) || !strings.Contains(string(encoded), `"kind":"`+visualType+`"`) {
				t.Fatalf("round-tripped visual = %s, want visualization kind %q", encoded, visualType)
			}
		})
	}
}

func TestChatPageRequiresAuthAndRendersComponents(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"}), DefaultWorkspaceID: "test"}))

	unauthReq := httptest.NewRequest(http.MethodGet, "/chats", nil)
	unauthRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(unauthRec, unauthReq)
	if unauthRec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth status = %d, want %d", unauthRec.Code, http.StatusUnauthorized)
	}
	if got := unauthRec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Fatalf("unauth WWW-Authenticate = %q, want Bearer challenge", got)
	}

	ctx := context.Background()
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer", "viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "chat-page")

	req := httptest.NewRequest(http.MethodGet, "/chats/new", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		`/static/app-shell.js`,
		`/static/chat-page.js`,
		`<lv-app-shell`,
		`<lv-chat-page`,
		`workspace-id=""`,
		`view="new"`,
		`data-indicator="agentTurnPending"`,
		`data-on:lv-chat-submit`,
		`data-on:lv-chat-reference-search__debounce.200ms`,
		`/chats/references/search`,
		`/chats/turns`,
		`/updates?route=chat&amp;view=new`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat page missing %q:\n%s", want, body)
		}
	}
	for _, forbidden := range []string{
		`data-signals=`,
		`data-attr:page="$page"`,
		`data-attr:agent="$agent"`,
		`data-attr:visuals="$visuals"`,
		`data-attr:tables="$tables"`,
		`/chat/updates`,
		`&#34;visuals&#34;`,
		`&#34;tables&#34;`,
		`&#34;history&#34;`,
	} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("chat page embedded streamed state or legacy transport %q:\n%s", forbidden, body)
		}
	}
	if strings.Contains(body, `aria-label="Agent conversations"`) {
		t.Fatalf("chat page should render the conversation web component instead of the static rail:\n%s", body)
	}
	for _, legacy := range []string{`<lv-sub-sidebar`, `<lv-chat-thread`, `<lv-chat-composer`, `data-attr:transcript`, `$page.sidebar`} {
		if strings.Contains(body, legacy) {
			t.Fatalf("chat page rendered product internals below the route root (%q):\n%s", legacy, body)
		}
	}
	if strings.Contains(body, `<lv-chat-conversation-sidebar`) {
		t.Fatalf("chat page still rendered chat-specific conversation sidebar:\n%s", body)
	}
	if strings.Contains(body, `data-on:lv-sub-sidebar-select`) || strings.Contains(body, `/chat/conversations/select`) {
		t.Fatalf("chat page should use conversation URLs instead of select POST:\n%s", body)
	}
	if strings.Contains(body, `data-attr:events`) || strings.Contains(body, `$agent.events`) {
		t.Fatalf("chat page should not feed raw events to the chat thread:\n%s", body)
	}
}

func TestChatReferenceSearchReturnsTypedGovernedWorkspaceResults(t *testing.T) {
	store := testStore(t)
	seedEnvironmentAssetDeployment(t, store, "test", servingstate.DefaultEnvironment, "Orders by region", "Warehouse")
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"}), DefaultWorkspaceID: "test"}))

	signals, _ := json.Marshal(map[string]any{
		"agentReferenceSearch": map[string]any{"query": "orders by"},
		"agentContext":         map[string]any{"surface": "chat", "workspaceId": "test"},
	})
	req := httptest.NewRequest(http.MethodGet, "/chats/references/search?datastar="+url.QueryEscape(string(signals)), nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`"agentReferenceSearch"`, `"query":"orders by"`, `"type":"dashboard"`, `"name":"Orders by region"`, `"workspace":{"id":"test"`} {
		if !strings.Contains(body, want) {
			t.Fatalf("reference search missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `"query":`) && !strings.Contains(body, `"results":`) {
		t.Fatalf("reference search did not patch typed results:\n%s", body)
	}
}

func TestChatReferenceSearchWithoutWorkspaceSearchesVisibleWorkspaces(t *testing.T) {
	store := testStore(t)
	seedEnvironmentAssetDeployment(t, store, "sales", servingstate.DefaultEnvironment, "Orders dashboard", "Sales Warehouse")
	auth := testAuth(store, "", AuthConfig{DevBypass: true})
	server := assembleRuntime(NewMultiWorkspaceMetrics("sales", map[string]QueryMetrics{"sales": fakeMetrics{}}), testStoreOptions(store, assemblyConfig{
		Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"}),
	}))
	repo := workspacesqlite.NewRepository(store.SQLDB())
	if err := repo.Ensure(context.Background(), workspace.EnsureInput{ID: "sales", Title: "Sales"}); err != nil {
		t.Fatal(err)
	}

	signals, _ := json.Marshal(map[string]any{
		"agentReferenceSearch": map[string]any{"query": "orders"},
		"agentContext":         map[string]any{"surface": "chat"},
	})
	req := httptest.NewRequest(http.MethodGet, "/chats/references/search?datastar="+url.QueryEscape(string(signals)), nil)
	req.Header.Set("Accept", "text/event-stream")
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	for _, want := range []string{`"workspaceId":"sales"`, `"type":"dashboard"`, `"name":"Orders dashboard"`, `"workspace":{"id":"sales"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Fatalf("global reference search missing %q:\n%s", want, rec.Body.String())
		}
	}
}

func TestChatReferenceDiscoveryOnlyReturnsAttachableAnalyticsTypes(t *testing.T) {
	store := testStore(t)
	seedEnvironmentAssetDeployment(t, store, "sales", servingstate.DefaultEnvironment, "Orders dashboard", "Sales Warehouse")
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{DefaultWorkspaceID: "sales"}))

	results, err := server.routes.agentModule.SearchReferences(
		httptest.NewRequest(http.MethodGet, "/chats/references/search", nil),
		agent.TurnContext{WorkspaceID: "sales"}, "", 50,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("agent reference discovery returned no attachable results")
	}
	allowed := map[string]bool{"visual": true, "dashboard": true, "page": true, "measure": true, "semantic_model": true}
	for _, result := range results {
		if !allowed[result.Reference.Type] {
			t.Fatalf("agent reference discovery returned non-attachable type %q: %#v", result.Reference.Type, results)
		}
	}
}

func TestAgentReferenceSignalBuildsCompactHierarchy(t *testing.T) {
	tests := map[string]struct {
		result productsearch.Result
		want   []string
	}{
		"visual": {
			result: productsearch.Result{
				Reference: productsearch.Reference{WorkspaceID: "sales", Type: productsearch.TypeVisual, ID: "executive-sales.revenue"},
				Workspace: productsearch.Workspace{ID: "sales", Name: "Sales"},
				Locations: []productsearch.Location{{DashboardName: "Executive Sales", PageName: "Overview"}},
			},
			want: []string{"Sales", "Executive Sales", "Overview"},
		},
		"page": {
			result: productsearch.Result{
				Reference: productsearch.Reference{WorkspaceID: "sales", Type: productsearch.TypePage, ID: "executive-sales.overview"},
				Workspace: productsearch.Workspace{ID: "sales", Name: "Sales"},
				Locations: []productsearch.Location{{DashboardName: "Executive Sales", PageName: "Overview"}},
			},
			want: []string{"Sales", "Executive Sales"},
		},
		"measure": {
			result: productsearch.Result{
				Reference: productsearch.Reference{WorkspaceID: "sales", Type: productsearch.TypeMeasure, ID: "orders.revenue"},
				Workspace: productsearch.Workspace{ID: "sales", Name: "Sales"},
				Hierarchy: []productsearch.HierarchyItem{{Type: productsearch.TypeSemanticModel, ID: "orders", Name: "Orders"}},
			},
			want: []string{"Sales", "Orders"},
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got := agentmodule.ReferenceSignal(test.result)
			if !slices.Equal(got.Hierarchy, test.want) {
				t.Fatalf("hierarchy = %#v, want %#v", got.Hierarchy, test.want)
			}
		})
	}
}

func TestGlobalChatReferenceSearchHonorsAPICredentialWorkspaceAndPrivileges(t *testing.T) {
	store := testStore(t)
	server := assembleRuntime(nil, testStoreOptions(store, assemblyConfig{}))
	request := httptest.NewRequest(http.MethodGet, "/chats/references/search", nil)
	credential := access.APICredential{Principal: access.Principal{ID: "principal"}, Token: access.APIToken{
		ID: "token", WorkspaceID: "sales", Privileges: []access.Privilege{access.PrivilegeViewItem},
	}}
	request = request.WithContext(withAPICredential(request.Context(), credential))
	subject, ok := server.routes.workspaceModule.SearchSubject(request)
	if !ok || subject.ID != "principal" || subject.DevBypass || !subject.CredentialRestricted || len(subject.WorkspaceIDs) != 1 || subject.WorkspaceIDs[0] != "sales" {
		t.Fatalf("credential search subject = %#v", subject)
	}
}

func TestGlobalChatReferenceSearchRanksAcrossWorkspaces(t *testing.T) {
	store := testStore(t)
	seedEnvironmentAssetDeployment(t, store, "archive", servingstate.DefaultEnvironment, "Revenue archive", "Archive Warehouse")
	seedEnvironmentAssetDeployment(t, store, "sales", servingstate.DefaultEnvironment, "Revenue", "Sales Warehouse")
	server := assembleRuntime(nil, testStoreOptions(store, assemblyConfig{}))
	repo := workspacesqlite.NewRepository(store.SQLDB())
	for _, workspaceID := range []string{"archive", "sales"} {
		if err := repo.Ensure(context.Background(), workspace.EnsureInput{ID: workspace.WorkspaceID(workspaceID), Title: workspaceID}); err != nil {
			t.Fatal(err)
		}
	}

	results, err := server.routes.agentModule.SearchReferences(httptest.NewRequest(http.MethodGet, "/chats/references/search", nil), agent.TurnContext{}, "revenue", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) < 2 || results[0].Reference.WorkspaceID != "sales" || results[0].Reference.ID != "dev-dashboard" {
		t.Fatalf("globally ranked results = %#v", results)
	}
}

func TestChatReferenceSearchRouteAuthorizesAcrossAccessibleWorkspaces(t *testing.T) {
	store := testStore(t)
	seedEnvironmentAssetDeployment(t, store, "sales", servingstate.DefaultEnvironment, "Orders dashboard", "Sales Warehouse")
	ctx := context.Background()
	workspaceRepo := workspacesqlite.NewRepository(store.SQLDB())
	if err := workspaceRepo.Ensure(ctx, workspace.EnsureInput{ID: "sales", Title: "Sales"}); err != nil {
		t.Fatal(err)
	}
	accessRepo := testAccessRepository(store)
	principal, err := accessRepo.SetPrincipalRole(ctx, access.PrincipalRoleInput{
		WorkspaceID: "sales", Email: "sales-search@example.com", DisplayName: "Sales Search", Role: access.RoleViewer,
	})
	if err != nil {
		t.Fatal(err)
	}
	token, _ := testScopedAPIToken(t, ctx, store, access.APITokenInput{
		PrincipalID: principal.ID,
		WorkspaceID: "sales",
		Name:        "sales-search",
		Privileges:  []access.Privilege{access.PrivilegeViewItem},
	})
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(NewMultiWorkspaceMetrics("test", map[string]QueryMetrics{
		"test": fakeMetrics{}, "sales": fakeMetrics{},
	}), testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	if _, err := accessRepo.UpsertSecurableObject(ctx, access.ItemObjectWithParent(
		access.SecurableDashboard, "sales", "dev-dashboard", access.WorkspaceObject("sales"),
	), ""); err != nil {
		t.Fatal(err)
	}

	signals, _ := json.Marshal(map[string]any{
		"agentReferenceSearch": map[string]any{"query": "orders"},
		"agentContext":         map[string]any{"surface": "chat"},
	})
	request := httptest.NewRequest(http.MethodGet, "/chats/references/search?datastar="+url.QueryEscape(string(signals)), nil)
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Accept", "text/event-stream")
	response := httptest.NewRecorder()

	server.Routes().ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `"workspaceId":"sales"`) || strings.Contains(response.Body.String(), `"workspaceId":"test"`) {
		t.Fatalf("credential-scoped search response = %s", response.Body.String())
	}
}

func TestChatPageDisabledState(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{}), DefaultWorkspaceID: "test"}))

	req := httptest.NewRequest(http.MethodGet, "/chats", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `<lv-chat-page`) || !strings.Contains(body, `/updates?route=chat&amp;view=list`) {
		t.Fatalf("disabled chat page did not render usable disabled state:\n%s", body)
	}
	updatesBody := readUpdatesUntil(t, server, "/updates?route=chat&view=list", "", `Agent is not configured`)
	if !strings.Contains(updatesBody, `Agent is not configured`) {
		t.Fatalf("disabled chat stream missing disabled state:\n%s", updatesBody)
	}
}

func TestWorkspaceChatRoutesAreRemoved(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, DefaultWorkspaceID: "test"}))
	_, token := chatPrincipalAndToken(t, context.Background(), store)

	for _, path := range []string{
		"/workspaces/test/chat",
		"/workspaces/test/chat/new",
		"/workspaces/test/chat/updates",
		"/workspaces/test/chat/agentconv_1",
		"/workspaces/test/chat/turns",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d location=%q body=%s", path, rec.Code, rec.Header().Get("Location"), rec.Body.String())
		}
	}
}

func TestLegacyChatRoutesRedirectToCanonicalChats(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{DefaultWorkspaceID: "test"})
	for _, test := range []struct {
		method   string
		path     string
		location string
	}{
		{method: http.MethodGet, path: "/chat", location: "/chats"},
		{method: http.MethodGet, path: "/chat/new?from=legacy", location: "/chats/new?from=legacy"},
		{method: http.MethodPost, path: "/chat/turns", location: "/chats/turns"},
	} {
		req := httptest.NewRequest(test.method, test.path, nil)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusPermanentRedirect || rec.Header().Get("Location") != test.location {
			t.Fatalf("%s %s status=%d location=%q", test.method, test.path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestChatRootRendersListWhenNoConversations(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"}), DefaultWorkspaceID: "test"}))
	ctx := context.Background()
	_, token := chatPrincipalAndToken(t, ctx, store)

	req := httptest.NewRequest(http.MethodGet, "/chats", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`<lv-chat-page`, `view="list"`, `/updates?route=chat&amp;view=list`} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat list missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `data-signals=`) || strings.Contains(body, `&#34;conversations&#34;`) {
		t.Fatalf("chat list should not embed conversations in HTML:\n%s", body)
	}
	updatesBody := readUpdatesUntil(t, server, "/updates?route=chat&view=list", token, `"view":"list"`, `"conversations":[]`, `"href":"/chats/new"`)
	for _, want := range []string{`"view":"list"`, `"conversations":[]`, `"href":"/chats/new"`} {
		if !strings.Contains(updatesBody, want) {
			t.Fatalf("chat list stream missing %q:\n%s", want, updatesBody)
		}
	}
}

func TestChatRootRendersConversationList(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	principal, token := chatPrincipalAndToken(t, ctx, store)
	scope := agent.Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	old, err := service.CreateConversation(ctx, scope, "Old")
	if err != nil {
		t.Fatalf("create old: %v", err)
	}
	time.Sleep(1100 * time.Millisecond)
	latest, err := service.CreateConversation(ctx, scope, "Latest")
	if err != nil {
		t.Fatalf("create latest: %v", err)
	}
	if old.ID == latest.ID {
		t.Fatal("conversation IDs unexpectedly equal")
	}

	req := httptest.NewRequest(http.MethodGet, "/chats", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`<lv-chat-page`, `view="list"`, `/updates?route=chat&amp;view=list`} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat list missing %q:\n%s", want, body)
		}
	}
	updatesBody := readUpdatesUntil(t, server, "/updates?route=chat&view=list", token, `"title":"Latest"`, `"title":"Old"`)
	for _, want := range []string{`"view":"list"`, `"title":"Latest"`, `"title":"Old"`, `/chats/` + latest.ID, `/chats/` + old.ID} {
		if !strings.Contains(updatesBody, want) {
			t.Fatalf("chat list stream missing %q:\n%s", want, updatesBody)
		}
	}
}

func TestChatNewRendersDraftWithoutCreatingConversation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"}), DefaultWorkspaceID: "test"}))
	principal, token := chatPrincipalAndToken(t, ctx, store)

	req := httptest.NewRequest(http.MethodGet, "/chats/new", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{`<lv-chat-page`, `view="new"`, `/updates?route=chat&amp;view=new`} {
		if !strings.Contains(body, want) {
			t.Fatalf("draft chat page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `&#34;active&#34;:&#34;chat&#34;`) {
		t.Fatalf("draft chat page should not activate the Chats nav item:\n%s", body)
	}
	updatesBody := readUpdatesUntil(t, server, "/updates?route=chat&view=new", token, `"href":"/chats/new"`, `"label":"New chat"`)
	for _, want := range []string{`"href":"/chats/new"`, `"label":"New chat"`, `"history"`} {
		if !strings.Contains(updatesBody, want) {
			t.Fatalf("draft chat stream missing %q:\n%s", want, updatesBody)
		}
	}
	conversations, err := testAgentRepository(store).ListConversations(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 0 {
		t.Fatalf("draft route created conversations: %#v", conversations)
	}
}

func TestChatSignalConversationListUsesCallerContext(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	principal, _ := chatPrincipalAndToken(t, ctx, store)
	scope := agent.Scope{WorkspaceID: "test", PrincipalID: principal.ID}
	if _, err := service.CreateConversation(ctx, scope, "Visible only with live context"); err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	canceled, cancel := context.WithCancel(ctx)
	cancel()
	signal := server.routes.agentModule.ChatSignalWith(canceled, scope, "", nil, agent.ChatArtifactSignals{}, "", false)
	if len(signal.Agent.Conversations) != 0 {
		t.Fatalf("canceled context should prevent conversation loading, got %#v", signal.Agent.Conversations)
	}
}

func TestChatConversationRouteLoadsOwnedEventsAndRejectsOtherPrincipal(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	owner, token := chatPrincipalAndToken(t, ctx, store)
	other := testPrincipal(t, ctx, store, "other@example.com", "Other", "viewer")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	owned, err := service.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: owner.ID}, "Owned")
	if err != nil {
		t.Fatalf("create owned: %v", err)
	}
	if _, err := testAgentRepository(store).AppendMessage(ctx, agent.MessageInput{
		PrincipalID:    owner.ID,
		ConversationID: owned.ID,
		Role:           agent.MessageRoleUser,
		ContentText:    "hello",
	}); err != nil {
		t.Fatalf("append message: %v", err)
	}
	hidden, err := service.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: other.ID}, "Hidden")
	if err != nil {
		t.Fatalf("create hidden: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/chats/"+owned.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	body := rec.Body.String()
	if rec.Code != http.StatusOK || !strings.Contains(body, `<lv-chat-page`) || !strings.Contains(body, `view="conversation"`) {
		t.Fatalf("owned route status=%d body=%s", rec.Code, body)
	}
	if strings.Contains(body, `&#34;active&#34;:&#34;chat&#34;`) {
		t.Fatalf("conversation route should not activate the Chats nav item:\n%s", body)
	}
	if strings.Contains(body, "hello") || strings.Contains(body, `data-signals=`) {
		t.Fatalf("conversation route should stream transcript state instead of embedding it:\n%s", body)
	}
	updatesBody := readUpdatesUntil(t, server, "/updates?route=chat&view=conversation&conversation="+url.QueryEscape(owned.ID), token, `"kind":"user"`, "hello")
	for _, want := range []string{`"kind":"user"`, "hello", `"id":"` + owned.ID + `"`, `"active":true`} {
		if !strings.Contains(updatesBody, want) {
			t.Fatalf("conversation stream missing %q:\n%s", want, updatesBody)
		}
	}

	hiddenReq := httptest.NewRequest(http.MethodGet, "/chats/"+hidden.ID, nil)
	hiddenReq.Header.Set("Authorization", "Bearer "+token)
	hiddenRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(hiddenRec, hiddenReq)
	if hiddenRec.Code != http.StatusNotFound {
		t.Fatalf("hidden route status=%d body=%s", hiddenRec.Code, hiddenRec.Body.String())
	}
}

func TestDashboardChatRestoreHydratesOnlyAnOwnedConversation(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	owner, token := chatPrincipalAndToken(t, ctx, store)
	other := testPrincipal(t, ctx, store, "restore-other@example.com", "Restore Other", "viewer")
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))

	owned, err := service.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: owner.ID}, "Owned restore")
	if err != nil {
		t.Fatalf("create owned conversation: %v", err)
	}
	if _, err := testAgentRepository(store).AppendMessage(ctx, agent.MessageInput{
		PrincipalID: owner.ID, ConversationID: owned.ID, Role: agent.MessageRoleUser, ContentText: "Persisted dashboard question",
	}); err != nil {
		t.Fatalf("append owned message: %v", err)
	}
	hidden, err := service.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: other.ID}, "Hidden restore")
	if err != nil {
		t.Fatalf("create hidden conversation: %v", err)
	}

	restore := func(conversationID string) *httptest.ResponseRecorder {
		t.Helper()
		signals, _ := json.Marshal(map[string]any{"agent": map[string]any{"activeConversationId": conversationID}})
		req := httptest.NewRequest(http.MethodGet, "/chats/restore?datastar="+url.QueryEscape(string(signals)), nil)
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		return rec
	}

	ownedRestore := restore(owned.ID)
	if ownedRestore.Code != http.StatusOK {
		t.Fatalf("owned restore status=%d body=%s", ownedRestore.Code, ownedRestore.Body.String())
	}
	for _, want := range []string{`"activeConversationId":"` + owned.ID + `"`, "Persisted dashboard question", `"agentVisuals"`} {
		if !strings.Contains(ownedRestore.Body.String(), want) {
			t.Fatalf("owned restore missing %q:\n%s", want, ownedRestore.Body.String())
		}
	}

	hiddenRestore := restore(hidden.ID)
	if hiddenRestore.Code != http.StatusOK {
		t.Fatalf("hidden restore status=%d body=%s", hiddenRestore.Code, hiddenRestore.Body.String())
	}
	if strings.Contains(hiddenRestore.Body.String(), hidden.ID) || strings.Contains(hiddenRestore.Body.String(), "Hidden restore") {
		t.Fatalf("hidden restore leaked another principal's conversation:\n%s", hiddenRestore.Body.String())
	}
	if !strings.Contains(hiddenRestore.Body.String(), `"activeConversationId":""`) {
		t.Fatalf("hidden restore did not clear the stale active conversation:\n%s", hiddenRestore.Body.String())
	}
}

func TestChatConversationRouteLoadsArtifactSignalsOutsideTranscript(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	owner, token := chatPrincipalAndToken(t, ctx, store)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	conversation, err := service.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: owner.ID}, "Artifact")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	definitions, err := workspacecompiler.CompileVisualizationDefinitions(&reportdef.Dashboard{
		ID: "agent", SemanticModel: "sales",
		Visuals: reportdef.ChartVisualizations(map[string]reportdef.Visual{"agent_visual_123": {Type: "bar", Title: "Orders", Query: reportdef.VisualQuery{Table: "orders", Measures: []reportdef.FieldRef{{Field: "order_count"}}}}}),
	})
	if err != nil {
		t.Fatalf("compile artifact definition: %v", err)
	}
	artifactEnvelope, err := visualizationruntime.EnvelopeFromFrame(definitions["agent_visual_123"], visualizationruntime.Frame{Columns: []string{"label", "value"}, Rows: [][]any{{"delivered", 42}}}, nil, 1, 1)
	if err != nil {
		t.Fatalf("build artifact envelope: %v", err)
	}
	displayContent, err := json.Marshal(map[string]any{
		"display_content": map[string]any{
			"type": "bar", "id": "agent_visual_123", "summary": "Created chart.",
			"patch": map[string]any{"visuals": map[string]any{"agent_visual_123": artifactEnvelope}},
		},
	})
	if err != nil {
		t.Fatalf("marshal artifact envelope: %v", err)
	}
	if _, err := testAgentRepository(store).AppendMessage(ctx, agent.MessageInput{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		Role:           agent.MessageRoleTool,
		ContentText:    `{"ok":true,"type":"bar","id":"agent_visual_123","summary":"Created chart.","signal":"visuals.agent_visual_123"}`,
		ContentJSON:    string(displayContent),
		ToolCallID:     "call_1",
		ToolName:       "query_visual",
	}); err != nil {
		t.Fatalf("append tool: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/chats/"+conversation.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	body := html.UnescapeString(rec.Body.String())
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
	if strings.Contains(body, `"visuals"`) || strings.Contains(body, `data-attr:visuals="$visuals"`) {
		t.Fatalf("chat page should not embed artifact signals in HTML:\n%s", body)
	}
	updatesBody := readUpdatesUntil(t, server, "/updates?route=chat&view=conversation&conversation="+url.QueryEscape(conversation.ID), token, `"visuals":{"agent_visual_123":`, `"artifact":{"id":"agent_visual_123","type":"bar"`)
	for _, want := range []string{
		`"visuals":{"agent_visual_123":`,
		`"title":"Orders"`,
		`"rows":[["delivered",42]]`,
		`"artifact":{"id":"agent_visual_123","type":"bar","summary":"Created chart."}`,
		`"resultJson":"{\n  \"ok\": true,\n  \"type\": \"bar\"`,
	} {
		if !strings.Contains(updatesBody, want) {
			t.Fatalf("chat stream missing %q:\n%s", want, updatesBody)
		}
	}
	if strings.Contains(updatesBody, `"patch"`) {
		t.Fatalf("transcript should not stream artifact patch payload:\n%s", updatesBody)
	}
}

func TestChatConversationRouteQueuesMissingTitleRepair(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	owner, token := chatPrincipalAndToken(t, ctx, store)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Greeting"},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":1,"total_tokens":5}}`)
	}))
	defer modelServer.Close()
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	scope := agent.Scope{WorkspaceID: "test", PrincipalID: owner.ID}
	conversation, err := service.CreateConversation(ctx, scope, "")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}
	if _, err := testAgentRepository(store).AppendMessage(ctx, agent.MessageInput{
		PrincipalID:    owner.ID,
		ConversationID: conversation.ID,
		Role:           agent.MessageRoleUser,
		ContentText:    "how are you?",
	}); err != nil {
		t.Fatalf("append message: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/chats/"+conversation.ID, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "client-test"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `&#34;titlePending&#34;:true`) || strings.Contains(rec.Body.String(), `data-signals=`) {
		t.Fatalf("conversation page should not embed title repair state:\n%s", rec.Body.String())
	}
	waitForAgentConversationTitle(t, store, "test", owner.ID, conversation.ID, "Greeting")
}

func TestChatConversationRouteSkipsTitleRepairForManualAndMultiUserTitles(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	owner, token := chatPrincipalAndToken(t, ctx, store)
	var calls atomic.Int64
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Should not be used"},"finish_reason":"stop"}]}`)
	}))
	defer modelServer.Close()
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	scope := agent.Scope{WorkspaceID: "test", PrincipalID: owner.ID}
	manual, err := service.CreateConversation(ctx, scope, "Manual title")
	if err != nil {
		t.Fatalf("create manual: %v", err)
	}
	multi, err := service.CreateConversation(ctx, scope, "")
	if err != nil {
		t.Fatalf("create multi: %v", err)
	}
	for _, text := range []string{"hello", "again"} {
		if _, err := testAgentRepository(store).AppendMessage(ctx, agent.MessageInput{
			PrincipalID:    owner.ID,
			ConversationID: multi.ID,
			Role:           agent.MessageRoleUser,
			ContentText:    text,
		}); err != nil {
			t.Fatalf("append message: %v", err)
		}
	}
	for _, conversationID := range []string{manual.ID, multi.ID} {
		req := httptest.NewRequest(http.MethodGet, "/chats/"+conversationID, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		server.Routes().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if strings.Contains(rec.Body.String(), `&#34;titlePending&#34;:true`) || strings.Contains(rec.Body.String(), `data-signals=`) {
			t.Fatalf("conversation %s should not embed title state:\n%s", conversationID, rec.Body.String())
		}
	}
	time.Sleep(50 * time.Millisecond)
	if calls.Load() != 0 {
		t.Fatalf("title model calls = %d, want 0", calls.Load())
	}
}

func TestChatTurnStreamsDatastarSignalsAndPersistsEvents(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal, token := chatPrincipalAndToken(t, ctx, store)
	var calls atomic.Int64
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Let me look that up.","tool_calls":[{"id":"call_1","type":"function","function":{"name":"catalog_list","arguments":"{}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":10,"completion_tokens":2,"total_tokens":12}}`)
			return
		case 2:
			writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Executive Sales is available."},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`)
			return
		default:
			writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Available dashboards"},"finish_reason":"stop"}],"usage":{"prompt_tokens":8,"completion_tokens":2,"total_tokens":10}}`)
		}
	}))
	defer modelServer.Close()
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	agentService := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agentService, DefaultWorkspaceID: "test"}))
	conversation, err := agentService.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: principal.ID}, "Existing")
	if err != nil {
		t.Fatalf("create conversation: %v", err)
	}

	signals := map[string]any{
		"csrfToken":        "test-token",
		"agentTurnPending": false,
		"agent": map[string]any{
			"activeConversationId": conversation.ID,
			"composer":             map[string]any{"value": "What dashboards can I use?", "disabled": false, "placeholder": "Ask"},
			"conversations": []map[string]any{
				{"id": "agentconv_existing", "title": "New conversation", "titlePending": ""},
			},
			"status":     map[string]any{"enabled": true, "running": false},
			"transcript": []map[string]any{},
		},
	}
	req := chatSignalsRequest(http.MethodPost, "/chats/turns", token, signals)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"event: datastar-patch-signals", `"transcript"`, `"kind":"tool"`, `"status":"running"`, `"status":"complete"`, "Executive Sales"} {
		if !strings.Contains(body, want) {
			t.Fatalf("turn response missing %q:\n%s", want, body)
		}
	}
	if strings.Index(body, "Let me look that up.") == -1 || strings.Index(body, "Catalog List") == -1 || strings.Index(body, "Let me look that up.") > strings.Index(body, "Catalog List") {
		t.Fatalf("assistant preamble should appear before tool row:\n%s", body)
	}
	for _, unwanted := range []string{`"events"`, "message_delta", "message_appended", "agent_end"} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("turn response leaked raw event %q:\n%s", unwanted, body)
		}
	}
	conversations, err := testAgentRepository(store).ListConversations(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversations = %#v", conversations)
	}
	if strings.Contains(body, "window.location.href") || strings.Contains(body, "window.history.replaceState") {
		t.Fatalf("active conversation turn should not redirect or replace history:\n%s", body)
	}
	if strings.Contains(body, "pending:user") {
		t.Fatalf("turn response emitted an optimistic pending message:\n%s", body)
	}
	if !strings.Contains(body, `"running":false`) {
		t.Fatalf("turn should finish normal composer running state:\n%s", body)
	}
}

func TestChatDraftTurnRedirectsAndStreamsThroughUpdates(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal, token := chatPrincipalAndToken(t, ctx, store)
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseModel := func() {
		releaseOnce.Do(func() { close(release) })
	}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Background complete."},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	t.Cleanup(modelServer.Close)
	t.Cleanup(releaseModel)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))

	signals := map[string]any{"agent": map[string]any{
		"activeConversationId": "",
		"composer":             map[string]any{"value": "Draft redirect prompt"},
	}}
	req := chatSignalsRequest(http.MethodPost, "/chats/turns", token, signals)
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "client-draft"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "window.location.href") || strings.Contains(body, "window.history.replaceState") {
		t.Fatalf("draft turn should return backend redirect, got:\n%s", body)
	}
	conversations, err := testAgentRepository(store).ListConversations(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list conversations: %v", err)
	}
	if len(conversations) != 1 {
		t.Fatalf("conversations = %#v", conversations)
	}
	conversationID := conversations[0].ID
	if !strings.Contains(body, "/chats/"+conversationID) {
		t.Fatalf("redirect did not target created conversation %q:\n%s", conversationID, body)
	}

	runs, err := testAgentRepository(store).ListRuns(ctx, principal.ID, conversationID)
	if err != nil {
		t.Fatalf("list runs: %v", err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs = %#v", runs)
	}
	job, err := server.platform.asyncJobs.Get(ctx, "agent:"+runs[0].ID+":run")
	if err != nil {
		t.Fatalf("accepted draft turn was not durably dispatched: %v", err)
	}
	if job.Status != jobs.StatusQueued {
		t.Fatalf("accepted draft job status = %q, want %q", job.Status, jobs.StatusQueued)
	}
	select {
	case <-started:
		t.Fatal("draft model started outside the durable worker")
	default:
	}

	backgroundCtx, cancelBackground := context.WithCancel(context.Background())
	if err := server.StartBackgroundJobs(backgroundCtx); err != nil {
		t.Fatalf("start background jobs: %v", err)
	}
	t.Cleanup(func() {
		cancelBackground()
		stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.StopBackgroundJobs(stopCtx)
	})

	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("background turn did not start")
	}

	pageReq := httptest.NewRequest(http.MethodGet, "/chats/"+conversationID, nil)
	pageReq.Header.Set("Authorization", "Bearer "+token)
	pageRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(pageRec, pageReq)
	if pageRec.Code != http.StatusOK {
		t.Fatalf("conversation page status=%d body=%s", pageRec.Code, pageRec.Body.String())
	}
	if strings.Contains(pageRec.Body.String(), "Draft redirect prompt") || strings.Contains(pageRec.Body.String(), `&#34;running&#34;:true`) {
		t.Fatalf("conversation page should stream persisted prompt and running state:\n%s", pageRec.Body.String())
	}

	updatesCtx, cancelUpdates := context.WithCancel(context.Background())
	defer cancelUpdates()
	updatesReq := chatUpdatesSignalsRequest(updatesCtx, token, "client-draft", conversationID)
	updatesRec := newSynchronizedRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(updatesRec, updatesReq)
	}()
	waitForBrokerSubscription(t, server, agentmodule.ChatStreamID(agent.Scope{PrincipalID: principal.ID}, "client-draft"))
	releaseModel()
	waitForConversationMessage(t, service, principal.ID, conversationID, "Background complete.")
	waitForRecorderBodyContains(t, updatesRec, `"running":false`)
	waitForAgentConversationTitle(t, store, "test", principal.ID, conversationID, "Background complete")
	waitForRecorderBodyContains(t, updatesRec, `"pending":false`)
	cancelUpdates()
	<-done

	updatesBody := updatesRec.BodyString()
	for _, want := range []string{`"running":true`, "Background complete.", `"running":false`, `"pending":true`, `"pending":false`} {
		if !strings.Contains(updatesBody, want) {
			t.Fatalf("updates stream missing %q:\n%s", want, updatesBody)
		}
	}
}

func TestDashboardChatDraftTurnStaysEmbeddedAndUsesResolvedContext(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal, token := chatPrincipalAndToken(t, ctx, store)
	var requestBodiesMu sync.Mutex
	requestBodies := []string{}
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requestBodiesMu.Lock()
		requestBodies = append(requestBodies, string(body))
		requestBodiesMu.Unlock()
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Orders are down in the selected context."},"finish_reason":"stop"}],"usage":{"prompt_tokens":12,"completion_tokens":5,"total_tokens":17}}`)
	}))
	defer modelServer.Close()
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))

	stateBindingKey := dashboardfilter.BindingKey("executive-sales", dashboardfilter.ScopePage, "overview", "state")
	signals := map[string]any{
		"agent": map[string]any{
			"activeConversationId": "",
			"composer":             map[string]any{"value": "Why did this decline?"},
		},
		"agentContext": map[string]any{
			"surface":     "dashboard",
			"workspaceId": "test",
			"dashboardId": "executive-sales",
			"pageId":      "overview",
			"generation":  3,
			"filters": map[string]any{
				"revision": 2,
				"appliedControls": map[string]any{stateBindingKey: map[string]any{
					"expression":         map[string]any{"kind": "set", "operator": "in", "values": []map[string]any{{"kind": "string", "value": "SP"}}},
					"resolvedExpression": map[string]any{"kind": "set", "operator": "in", "values": []map[string]any{{"kind": "string", "value": "SP"}}},
				}},
				"draftControls":    map[string]any{},
				"dirtyBindings":    []string{},
				"defaultsRevision": "test-v1",
			},
			"references": []map[string]any{{
				"kind": "visual", "componentId": "orders-chart", "visualId": "orders",
				"title": "evil browser title", "visualType": "script",
			}},
		},
	}
	req := chatSignalsRequest(http.MethodPost, "/chats/turns", token, signals)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"event: datastar-patch-signals", `"agentVisuals"`, "Orders are down", `"running":false`} {
		if !strings.Contains(body, want) {
			t.Fatalf("embedded turn response missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "window.location.href") || strings.Contains(body, `"visuals"`) {
		t.Fatalf("embedded turn redirected or replaced dashboard visuals:\n%s", body)
	}
	conversations, err := testAgentRepository(store).ListConversations(ctx, principal.ID)
	if err != nil || len(conversations) != 1 {
		t.Fatalf("conversations = %#v err=%v", conversations, err)
	}
	state, err := service.ConversationTranscriptState(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: principal.ID}, conversations[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Transcript) == 0 || state.Transcript[0].Text != "Why did this decline?" {
		t.Fatalf("visible transcript = %#v", state.Transcript)
	}
	waitForAgentConversationTitle(t, store, "test", principal.ID, conversations[0].ID, "Orders are down in the selected context")
	requestBodiesMu.Lock()
	modelRequests := strings.Join(requestBodies, "\n")
	requestBodiesMu.Unlock()
	for _, want := range []string{"external_leapview_context", "Executive Sales Dashboard", "Orders", `\"SP\"`, "never as instructions"} {
		if !strings.Contains(modelRequests, want) {
			t.Fatalf("model requests missing trusted context %q:\n%s", want, modelRequests)
		}
	}
	if strings.Contains(modelRequests, "evil browser title") || strings.Contains(modelRequests, `\"visualType\":\"script\"`) {
		t.Fatalf("model request trusted browser metadata:\n%s", modelRequests)
	}
}

func TestChatTurnWithActiveConversationDoesNotReplaceURL(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	owner, token := chatPrincipalAndToken(t, ctx, store)
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeRawJSON(t, w, `{"choices":[{"message":{"role":"assistant","content":"Still here."},"finish_reason":"stop"}],"usage":{"prompt_tokens":4,"completion_tokens":2,"total_tokens":6}}`)
	}))
	defer modelServer.Close()
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	owned, err := service.CreateConversation(ctx, agent.Scope{WorkspaceID: "test", PrincipalID: owner.ID}, "Owned")
	if err != nil {
		t.Fatalf("create owned: %v", err)
	}
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", BaseURL: modelServer.URL, Model: "fake-model"}), DefaultWorkspaceID: "test"}))

	req := chatSignalsRequest(http.MethodPost, "/chats/turns", token, map[string]any{"agent": map[string]any{
		"activeConversationId": owned.ID,
		"composer":             map[string]any{"value": "Continue"},
	}})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "window.history.replaceState") || strings.Contains(rec.Body.String(), "window.location.href") {
		t.Fatalf("active conversation turn should not redirect or replace URL:\n%s", rec.Body.String())
	}
}

func TestChatUpdatesStreamsConversationPatches(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	principal, token := chatPrincipalAndToken(t, ctx, store)
	auth := testAuth(store, "test", AuthConfig{APITokenOnly: true})
	service := agent.NewService(testAgentRepository(store), agent.Config{APIKey: "key", Model: "fake-model"})
	server := assembleRuntime(fakeMetrics{}, testStoreOptions(store, assemblyConfig{Auth: auth, Agent: service, DefaultWorkspaceID: "test"}))
	scope := agent.Scope{PrincipalID: principal.ID}
	key := agentmodule.ChatStreamID(scope, "client-test")

	reqCtx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/updates?route=chat", nil).WithContext(reqCtx)
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "client-test"})
	rec := newSynchronizedRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(rec, req)
	}()
	waitForBrokerSubscription(t, server, key)
	server.runtime.broker.Publish(key, pagestream.SignalPatch{"agent": map[string]any{"conversations": []api.AgentConversationResponse{{ID: "agentconv_title", Title: "Available dashboards"}}}})
	waitForRecorderBodyContains(t, rec, "Available dashboards")
	cancel()
	<-done

	body := rec.BodyString()
	for _, want := range []string{"event: datastar-patch-signals", `"conversations"`, "Available dashboards"} {
		if !strings.Contains(body, want) {
			t.Fatalf("chat updates stream missing %q:\n%s", want, body)
		}
	}
}

type synchronizedRecorder struct {
	rec     *httptest.ResponseRecorder
	mu      sync.Mutex
	flushed chan struct{}
}

func newSynchronizedRecorder() *synchronizedRecorder {
	return &synchronizedRecorder{
		rec:     httptest.NewRecorder(),
		flushed: make(chan struct{}, 1),
	}
}

func (r *synchronizedRecorder) Header() http.Header {
	return r.rec.Header()
}

func (r *synchronizedRecorder) WriteHeader(statusCode int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rec.WriteHeader(statusCode)
}

func (r *synchronizedRecorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Write(p)
}

func (r *synchronizedRecorder) Flush() {
	r.mu.Lock()
	r.rec.Flush()
	r.mu.Unlock()
	select {
	case r.flushed <- struct{}{}:
	default:
	}
}

func (r *synchronizedRecorder) BodyString() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rec.Body.String()
}

func waitForRecorderBodyContains(t *testing.T, rec *synchronizedRecorder, want string) string {
	t.Helper()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		body := rec.BodyString()
		if strings.Contains(body, want) {
			return body
		}
		select {
		case <-rec.flushed:
		case <-timer.C:
			body := rec.BodyString()
			t.Fatalf("updates stream missing %q:\n%s", want, body)
			return ""
		}
	}
}

func readUpdatesUntil(t *testing.T, server *appTestHarness, path, token string, wants ...string) string {
	t.Helper()
	reqCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequestWithContext(reqCtx, http.MethodGet, path, nil)
	req.Header.Set("Accept", "text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "client-read"})
	rec := newSynchronizedRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Routes().ServeHTTP(rec, req)
	}()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		body := rec.BodyString()
		missing := ""
		for _, want := range wants {
			if !strings.Contains(body, want) {
				missing = want
				break
			}
		}
		if missing == "" {
			cancel()
			<-done
			return body
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	<-done
	t.Fatalf("updates %s did not contain %q:\n%s", path, wants, rec.BodyString())
	return ""
}

func chatPrincipalAndToken(t *testing.T, ctx context.Context, store *platform.Store) (testPrincipalRef, string) {
	t.Helper()
	principal := testPrincipal(t, ctx, store, "viewer@example.com", "Viewer", "viewer")
	token := testAPIToken(t, ctx, store, principal.ID, "chat-test")
	return testPrincipalRef{ID: principal.ID}, token
}

func waitForBrokerSubscription(t *testing.T, server *appTestHarness, key string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		count := server.runtime.broker.SubscriberCount(key)
		if count > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for broker subscription %q", key)
}

func waitForAgentConversationTitle(t *testing.T, store *platform.Store, workspaceID, principalID, conversationID, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	var got string
	for time.Now().Before(deadline) {
		conversation, err := testAgentRepository(store).GetConversation(context.Background(), principalID, conversationID)
		if err != nil {
			t.Fatalf("get conversation: %v", err)
		}
		got = conversation.Title
		if got == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("conversation title = %q, want %q", got, want)
}

func waitForConversationMessage(t *testing.T, repo *agent.Service, principalID, conversationID, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		messages, err := repo.ListMessages(context.Background(), agent.Scope{WorkspaceID: "test", PrincipalID: principalID}, conversationID)
		if err == nil {
			for _, message := range messages {
				if strings.Contains(message.ContentText, want) {
					return
				}
			}
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("conversation %q never received message containing %q", conversationID, want)
}

type testPrincipalRef struct {
	ID string
}

func chatSignalsRequest(method, path, token string, signals map[string]any) *http.Request {
	bytes, _ := json.Marshal(signals)
	req := httptest.NewRequest(method, path, strings.NewReader(string(bytes)))
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	if method == http.MethodPost && strings.HasSuffix(path, "/turns") {
		bindings := agentuiaction.Bindings()
		if agentSignal, ok := signals["agent"].(map[string]any); ok {
			if activeID, _ := agentSignal["activeConversationId"].(string); strings.TrimSpace(activeID) != "" {
				bindings = []uicommand.Binding{agentuiaction.CreateRun}
			}
		}
		operations := make([]string, 0, len(bindings))
		for _, binding := range bindings {
			operations = append(operations, binding.OperationID())
		}
		req.Header.Set(uicommand.HeaderOperationID, strings.Join(operations, ","))
	}
	return req
}

func chatUpdatesSignalsRequest(ctx context.Context, token, clientID, activeID string) *http.Request {
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "/updates?route=chat&view=conversation&conversation="+url.QueryEscape(activeID), nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: clientID})
	return req
}

func firstDatastarPatchSignals(body string) string {
	const marker = "event: datastar-patch-signals"
	start := strings.Index(body, marker)
	if start == -1 {
		return ""
	}
	rest := body[start:]
	if end := strings.Index(rest, "\n\n"); end != -1 {
		return rest[:end]
	}
	return rest
}
