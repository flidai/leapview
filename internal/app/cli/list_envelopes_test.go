package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	cligen "github.com/flidai/leapview/internal/app/cli/gen"
	"github.com/spf13/cobra"
)

func TestFriendlyListCommandsPassPaginationQuery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command func(context.Context, *rootOptions) *cobra.Command
		args    []string
		path    string
	}{
		{
			name:    "workspaces",
			command: workspacesCommand,
			args:    []string{"list"},
			path:    "/api/v1/workspaces",
		},
		{
			name:    "dashboards",
			command: dashboardsCommand,
			args:    []string{"list", "--workspace", "test"},
			path:    "/api/v1/workspaces/test/dashboards",
		},
		{
			name:    "semantic-models",
			command: semanticModelsCommand,
			args:    []string{"list", "--workspace", "test"},
			path:    "/api/v1/workspaces/test/semantic-models",
		},
		{
			name:    "agent conversations",
			command: agentCommand,
			args:    []string{"conversations"},
			path:    "/api/v1/agent/conversations",
		},
		{
			name:    "search",
			command: searchCommand,
			args:    []string{"orders", "--workspace", "test", "--type", "visual"},
			path:    "/api/v1/search",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.path {
					t.Fatalf("path = %s want %s", r.URL.Path, tc.path)
				}
				if got := r.URL.Query().Get("limit"); got != "7" {
					t.Fatalf("limit = %q", got)
				}
				if got := r.URL.Query().Get("pageToken"); got != "cursor" {
					t.Fatalf("pageToken = %q", got)
				}
				if tc.name == "search" {
					if got := r.URL.Query().Get("q"); got != "orders" {
						t.Fatalf("q=%q", got)
					}
					if got := r.URL.Query().Get("type"); got != "visual" {
						t.Fatalf("type=%q", got)
					}
					if got := r.URL.Query().Get("workspace"); got != "test" {
						t.Fatalf("workspace=%q", got)
					}
				}
				writeCLIJSON(t, w, map[string]any{
					"items": []map[string]any{},
					"page":  map[string]any{"nextCursor": ""},
				})
			}))
			defer server.Close()

			opts := &rootOptions{}
			cmd := tc.command(context.Background(), opts)
			args := append([]string{}, tc.args...)
			args = append(args, "--target", server.URL, "--token", "token", "--limit", "7", "--page-token", "cursor")
			cmd.SetArgs(args)
			captureStdout(t, func() {
				if err := cmd.Execute(); err != nil {
					t.Fatalf("run command: %v", err)
				}
			})
		})
	}
}

func TestSearchCommandRendersConciseRows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/search" {
			t.Fatalf("path=%s", r.URL.Path)
		}
		if got := r.URL.Query().Get("q"); got != "orders" {
			t.Fatalf("q=%q", got)
		}
		writeCLIJSON(t, w, map[string]any{
			"items": []map[string]any{{
				"reference":   map[string]any{"workspaceId": "test", "type": "visual", "id": "executive-sales.orders"},
				"name":        "Orders",
				"description": "Orders visual on Overview.",
			}},
			"page": map[string]any{"nextCursor": ""},
		})
	}))
	defer server.Close()

	output := captureStdout(t, func() {
		cmd := searchCommand(context.Background(), &rootOptions{workspaceID: "test"})
		cmd.SetArgs([]string{"orders", "--target", server.URL, "--token", "token"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("run search: %v", err)
		}
	})
	for _, want := range []string{"WORKSPACE", "TYPE", "NAME", "DESCRIPTION", "ID", "test", "visual", "Orders", "Orders visual on Overview."} {
		if !strings.Contains(output, want) {
			t.Fatalf("search output missing %q:\n%s", want, output)
		}
	}
	if strings.Contains(output, "items") || strings.Contains(output, "nextCursor") {
		t.Fatalf("search output leaked envelope:\n%s", output)
	}
}

func TestSearchGeneratedCLIMetadataUsesQueryAsOnlyPositionalArg(t *testing.T) {
	var spec cligen.APIGenCommandSpec
	for _, candidate := range cligen.APIGeneratedCommandSpecs {
		if candidate.OperationID == "search" {
			spec = candidate
			break
		}
	}
	if spec.OperationID == "" {
		t.Fatal("search CLI metadata missing")
	}
	if len(spec.Args) != 1 || spec.Args[0].Name != "q" || spec.Args[0].Source != "query" {
		t.Fatalf("search CLI args = %#v, want one query arg q", spec.Args)
	}
}

func TestDashboardDataCommandsUseGeneratedURLsAndBodies(t *testing.T) {
	visualization := tableVisualizationFixture(t)
	for _, tc := range []struct {
		name     string
		args     []string
		method   string
		path     string
		wantBody []string
		response any
	}{
		{
			name:     "page",
			args:     []string{"page", "executive-sales", "overview"},
			method:   http.MethodGet,
			path:     "/api/v1/workspaces/test/dashboards/executive-sales/pages/overview",
			response: map[string]any{"id": "overview", "title": "Overview", "components": []map[string]any{}},
		},
		{
			name:   "visual describe",
			args:   []string{"visual", "executive-sales", "overview", "orders"},
			method: http.MethodGet,
			path:   "/api/v1/workspaces/test/dashboards/executive-sales/pages/overview/visuals/orders",
			response: map[string]any{
				"id": "orders", "rendererID": visualization["rendererID"],
				"specRevision": visualization["specRevision"], "spec": visualization["spec"],
			},
		},
		{
			name:   "filter describe",
			args:   []string{"filter", "executive-sales", "overview", "state"},
			method: http.MethodGet,
			path:   "/api/v1/workspaces/test/dashboards/executive-sales/pages/overview/filters/state",
			response: map[string]any{
				"definition": map[string]any{
					"id": "state", "field": "orders.state", "label": "State", "calendar": "",
					"options":    map[string]any{"kind": "static", "limit": 0, "values": []map[string]any{}},
					"predicates": []map[string]any{}, "timezone": "", "valueKind": "string", "weekStart": "",
				},
				"binding": map[string]any{
					"id": "fb_state", "key": "fb_state", "filter": "state", "default": map[string]any{"kind": "unfiltered"},
					"maxSelectedValues": 0, "optionDependencies": []map[string]any{}, "paneOrder": 0,
					"paneVisible": true, "readerEditable": true, "scope": "page", "selectionMode": "multiple",
					"targets": []string{},
				},
			},
		},
		{
			name:     "visual data",
			args:     []string{"visual-data", "executive-sales", "overview", "orders", "--count", "7", "--filter-state-json", `{"version":"typed_v1","controls":{"fb_state":{"kind":"set","operator":"in","values":[{"kind":"string","value":"SP"}]}}}`},
			method:   http.MethodPost,
			path:     "/api/v1/workspaces/test/dashboards/executive-sales/pages/overview/visuals/orders/query",
			wantBody: []string{`"filterState"`, `"typed_v1"`, `"fb_state"`, `"limit":7`},
			response: visualization,
		},
		{
			name:     "filter options",
			args:     []string{"filter-options", "executive-sales", "overview", "state", "--limit", "7", "--page-token", "cursor"},
			method:   http.MethodPost,
			path:     "/api/v1/workspaces/test/dashboards/executive-sales/pages/overview/filters/state/values",
			wantBody: []string{},
			response: map[string]any{"items": []map[string]any{}, "page": map[string]any{"nextCursor": ""}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method {
					t.Fatalf("method=%s want=%s", r.Method, tc.method)
				}
				if r.URL.Path != tc.path {
					t.Fatalf("path=%s want=%s", r.URL.Path, tc.path)
				}
				if tc.name == "filter options" {
					if got := r.URL.Query().Get("limit"); got != "7" {
						t.Fatalf("limit=%q", got)
					}
					if got := r.URL.Query().Get("pageToken"); got != "cursor" {
						t.Fatalf("pageToken=%q", got)
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				for _, want := range tc.wantBody {
					if !strings.Contains(string(body), want) {
						t.Fatalf("body missing %q: %s", want, body)
					}
				}
				writeCLIJSON(t, w, tc.response)
			}))
			defer server.Close()

			opts := &rootOptions{workspaceID: "test"}
			cmd := dashboardsCommand(context.Background(), opts)
			args := append([]string{}, tc.args...)
			args = append(args, "--workspace", "test", "--target", server.URL, "--token", "token")
			cmd.SetArgs(args)
			captureStdout(t, func() {
				if err := cmd.Execute(); err != nil {
					t.Fatalf("run command: %v", err)
				}
			})
		})
	}
}

func TestSemanticModelDatasetCommandsUseGeneratedURLsAndBodies(t *testing.T) {
	for _, tc := range []struct {
		name     string
		args     []string
		method   string
		path     string
		wantBody []string
		response any
	}{
		{
			name:     "datasets",
			args:     []string{"datasets", "test", "--limit", "7", "--page-token", "cursor"},
			method:   http.MethodGet,
			path:     "/api/v1/workspaces/test/semantic-models/test/datasets",
			response: map[string]any{"items": []map[string]any{}, "page": map[string]any{"nextCursor": ""}},
		},
		{
			name:     "dataset",
			args:     []string{"dataset", "test", "orders"},
			method:   http.MethodGet,
			path:     "/api/v1/workspaces/test/semantic-models/test/datasets/orders",
			response: map[string]any{"id": "orders"},
		},
		{
			name:     "fields",
			args:     []string{"fields", "test", "orders", "--limit", "7", "--page-token", "cursor"},
			method:   http.MethodGet,
			path:     "/api/v1/workspaces/test/semantic-models/test/datasets/orders/fields",
			response: map[string]any{"items": []map[string]any{}, "page": map[string]any{"nextCursor": ""}},
		},
		{
			name:     "query",
			args:     []string{"query", "test", "--body-json", `{"dimensions":[{"field":"state"}],"measures":[{"field":"order_count"}]}`},
			method:   http.MethodPost,
			path:     "/api/v1/workspaces/test/semantic-models/test/query",
			wantBody: []string{`"state"`, `"order_count"`},
			response: map[string]any{
				"queryId": "query-1", "columns": []map[string]any{}, "rows": []map[string]any{},
				"page": map[string]any{}, "completeness": map[string]any{"hasMore": false, "returnedRows": 0},
				"servingSnapshot": "",
			},
		},
		{
			name:     "explain query",
			args:     []string{"explain-query", "test", "--body-json", `{"dimensions":[{"field":"state"}],"measures":[{"field":"order_count"}]}`},
			method:   http.MethodPost,
			path:     "/api/v1/workspaces/test/semantic-models/test/query/explain",
			wantBody: []string{`"state"`, `"order_count"`},
			response: map[string]any{"mode": "semantic", "sql": "SELECT 1", "args": []map[string]any{}, "columns": []string{"state", "order_count"}, "warnings": []string{}},
		},
		{
			name:     "preview",
			args:     []string{"preview", "test", "orders", "--body-json", `{"dimensions":[{"field":"orders.order_id"}]}`},
			method:   http.MethodPost,
			path:     "/api/v1/workspaces/test/semantic-models/test/datasets/orders/preview",
			wantBody: []string{`"orders.order_id"`},
			response: map[string]any{
				"queryId": "query-1", "columns": []map[string]any{}, "rows": []map[string]any{},
				"page": map[string]any{"nextCursor": ""}, "completeness": map[string]any{"hasMore": false, "returnedRows": 0},
				"servingSnapshot": "",
			},
		},
		{
			name:     "explain preview",
			args:     []string{"explain-preview", "test", "orders", "--body-json", `{"dimensions":[{"field":"orders.order_id"}]}`},
			method:   http.MethodPost,
			path:     "/api/v1/workspaces/test/semantic-models/test/datasets/orders/preview/explain",
			wantBody: []string{`"orders.order_id"`},
			response: map[string]any{"mode": "preview", "sql": "SELECT 1", "args": []map[string]any{}, "columns": []string{"order_id"}, "warnings": []string{}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != tc.method {
					t.Fatalf("method=%s want=%s", r.Method, tc.method)
				}
				if r.URL.Path != tc.path {
					t.Fatalf("path=%s want=%s", r.URL.Path, tc.path)
				}
				if tc.name == "datasets" || tc.name == "fields" {
					if got := r.URL.Query().Get("limit"); got != "7" {
						t.Fatalf("limit=%q", got)
					}
					if got := r.URL.Query().Get("pageToken"); got != "cursor" {
						t.Fatalf("pageToken=%q", got)
					}
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				for _, want := range tc.wantBody {
					if !strings.Contains(string(body), want) {
						t.Fatalf("body missing %q: %s", want, body)
					}
				}
				writeCLIJSON(t, w, tc.response)
			}))
			defer server.Close()

			opts := &rootOptions{workspaceID: "test"}
			cmd := semanticModelsCommand(context.Background(), opts)
			args := append([]string{}, tc.args...)
			args = append(args, "--workspace", "test", "--target", server.URL, "--token", "token")
			cmd.SetArgs(args)
			captureStdout(t, func() {
				if err := cmd.Execute(); err != nil {
					t.Fatalf("run command: %v", err)
				}
			})
		})
	}
}

func tableVisualizationFixture(t *testing.T) map[string]any {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("..", "..", "..", "api", "visualization", "conformance", "table-windowed.json"))
	if err != nil {
		t.Fatalf("read visualization fixture: %v", err)
	}
	var fixture map[string]any
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatalf("decode visualization fixture: %v", err)
	}
	return fixture
}

func TestAgentToolsCommandListsCanonicalTools(t *testing.T) {
	output := captureStdout(t, func() {
		cmd := agentCommand(context.Background(), &rootOptions{})
		cmd.SetArgs([]string{"tools"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("agent tools: %v", err)
		}
	})
	for _, want := range []string{"NAME", "PRIVILEGE", "catalog_search", "catalog_list", "catalog_get", "docs_search", "docs_read", "query_dashboard_visual", "query_semantic_model", "query_visual"} {
		if !strings.Contains(output, want) {
			t.Fatalf("agent tools output missing %q:\n%s", want, output)
		}
	}
	for _, legacy := range []string{"list_dashboards", "list_assets", "describe_asset", "asset_lineage", "explain_semantic_model_query"} {
		if strings.Contains(output, legacy) {
			t.Fatalf("agent tools output contains legacy tool %q:\n%s", legacy, output)
		}
	}
	names := []string{}
	for index, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if index == 0 {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			names = append(names, fields[0])
		}
	}
	wantNames := []string{"catalog_get", "catalog_list", "catalog_search", "docs_read", "docs_search", "query_dashboard_visual", "query_semantic_model", "query_visual"}
	if !slices.Equal(names, wantNames) {
		t.Fatalf("agent tools names = %#v, want %#v\n%s", names, wantNames, output)
	}
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	original := os.Stdout
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe stdout: %v", err)
	}
	os.Stdout = write
	defer func() {
		os.Stdout = original
	}()
	type readResult struct {
		bytes []byte
		err   error
	}
	readDone := make(chan readResult, 1)
	go func() {
		bytes, err := io.ReadAll(read)
		readDone <- readResult{bytes: bytes, err: err}
	}()
	fn()
	if err := write.Close(); err != nil {
		t.Fatalf("close stdout pipe: %v", err)
	}
	result := <-readDone
	if result.err != nil {
		t.Fatalf("read stdout: %v", result.err)
	}
	return string(result.bytes)
}

func writeCLIJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Fatalf("encode JSON: %v", err)
	}
}
