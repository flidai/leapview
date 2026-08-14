package app

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/workspace"
	uisignals "github.com/flidai/leapview/internal/workspace/ui/signals"
)

type dataExplorerFixtureMetrics struct {
	fakeMetrics
	dataDir              string
	duckDBDir            string
	modelID              string
	sourceKey            string
	semanticPreviewError error
	dataQueries          *[]dataquery.Query
}

func dataExplorerTestRequest(method, target string, body io.Reader) *http.Request {
	req := httptest.NewRequest(method, target, body)
	return req.WithContext(withPrincipal(req.Context(), Principal{ID: "test_principal"}))
}

func (m dataExplorerFixtureMetrics) Catalog() catalog.Catalog {
	modelID := firstNonEmpty(m.modelID, "olist")
	return catalog.Catalog{
		Workspace: catalog.Workspace{ID: "test-workspace", Title: "Test Workspace", Description: "Fixture workspace"},
		Models: []catalog.Model{
			{ID: modelID, Title: modelID, Description: "Fixture model"},
		},
		Dashboards: []catalog.Dashboard{
			{ID: "executive-sales", Title: "Executive Sales Dashboard", Description: "Fixture report", SemanticModel: modelID, Tags: []string{"sales"}, PageCount: 2},
		},
	}
}

func (m dataExplorerFixtureMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	expectedModelID := firstNonEmpty(m.modelID, "olist")
	sourceKey := firstNonEmpty(m.sourceKey, "orders")
	if modelID != expectedModelID {
		return m.fakeMetrics.SemanticModel(modelID)
	}
	return &semanticmodel.Model{
		Name:              expectedModelID,
		Title:             expectedModelID,
		DefaultConnection: "managed",
		Connections: map[string]semanticmodel.Connection{
			"managed": {Kind: "managed", Root: m.dataDir},
		},
		Sources: map[string]semanticmodel.Source{
			sourceKey: {
				Path:       "orders.csv",
				Format:     "csv",
				Connection: "managed",
				Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{
					{Name: "order_id", PhysicalType: "VARCHAR", Ordinal: 1},
					{Name: "status", PhysicalType: "VARCHAR", Ordinal: 2},
				}},
			},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Columns: map[string]semanticmodel.ModelColumn{
					"order_id": {Name: "order_id", Type: "VARCHAR"},
					"status":   {Name: "status", Type: "VARCHAR"},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Expr: "order_id", Label: "Order ID", Type: "string"},
					"status":   {Expr: "status", Label: "Status", Type: "string"},
				},
				Schema: semanticmodel.TableSchema{Columns: []semanticmodel.ColumnSchema{
					{Name: "order_id", PhysicalType: "VARCHAR", Ordinal: 1},
					{Name: "status", PhysicalType: "VARCHAR", Ordinal: 2},
				}},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"order_count": {Fact: "orders", Aggregation: "count", Empty: "zero", Label: "Orders"},
		},
	}, true
}

func (m dataExplorerFixtureMetrics) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	if m.dataQueries != nil {
		*m.dataQueries = append(*m.dataQueries, request)
	}
	if m.semanticPreviewError != nil {
		return dataquery.Result{}, m.semanticPreviewError
	}
	if request.ModelID != firstNonEmpty(m.modelID, "olist") {
		return dataquery.Result{}, nil
	}
	switch request.Kind {
	case dataquery.KindSemanticAggregate:
		return dataquery.Result{
			Columns:      dataquery.ColumnsFromNames([]string{"status", "order_count"}),
			Rows:         []dataquery.Row{{"status": "delivered", "order_count": int64(1)}},
			SQL:          "SELECT status, COUNT(*) AS order_count FROM model.orders GROUP BY status",
			PlanText:     "semantic aggregate on orders",
			DurationMS:   7,
			RowsReturned: 1,
		}, nil
	case dataquery.KindSemanticRows:
		if request.Target != "orders" {
			return dataquery.Result{}, nil
		}
		return dataquery.Result{Columns: dataquery.ColumnsFromNames([]string{"order_id", "status"}), Rows: []dataquery.Row{{"order_id": "o1", "status": "delivered"}}, SQL: "semantic rows: " + request.ModelID + "." + request.Target}, nil
	case dataquery.KindModelTableRows:
		if strings.TrimSpace(m.duckDBDir) == "" {
			return dataquery.Result{Columns: dataquery.ColumnsFromNames([]string{"order_id", "status"}), Rows: []dataquery.Row{{"order_id": "o2", "status": "shipped"}}, TotalRows: 1, TotalRowsKnown: request.IncludeTotal, SQL: "model rows: " + request.Target}, nil
		}
		db, err := m.openModelTableDB(ctx, request.ModelID)
		if err != nil {
			return dataquery.Result{}, err
		}
		defer db.Close()
		sqlText := "SELECT order_id, status FROM model." + quoteDuckDBIdentifier(request.Target)
		if request.Limit > 0 {
			sqlText += fmt.Sprintf(" LIMIT %d", request.Limit)
		}
		if request.Offset > 0 {
			sqlText += fmt.Sprintf(" OFFSET %d", request.Offset)
		}
		rows, err := db.QueryContext(ctx, sqlText)
		if err != nil {
			return dataquery.Result{}, err
		}
		defer rows.Close()
		out := []dataquery.Row{}
		for rows.Next() {
			var orderID, status string
			if err := rows.Scan(&orderID, &status); err != nil {
				return dataquery.Result{}, err
			}
			out = append(out, dataquery.Row{"order_id": orderID, "status": status})
		}
		if err := rows.Err(); err != nil {
			return dataquery.Result{}, err
		}
		result := dataquery.Result{Columns: dataquery.ColumnsFromNames([]string{"order_id", "status"}), Rows: out, SQL: sqlText}
		if request.IncludeTotal {
			var total int
			if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM model."+quoteDuckDBIdentifier(request.Target)).Scan(&total); err != nil {
				return dataquery.Result{}, err
			}
			result.TotalRows = total
			result.TotalRowsKnown = true
		}
		return result, nil
	default:
		return dataquery.Result{}, fmt.Errorf("unsupported query kind %q", request.Kind)
	}
}

func TestDataExplorerExploreBuildsGovernedSemanticTable(t *testing.T) {
	t.Parallel()
	requests := []dataquery.Query{}
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), dataQueries: &requests}
	store := testStore(t)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))

	command := dataExplorerCommandFromQuery("test", "")
	command.Mode = uisignals.Pointer("explore")
	command.Explore = &uisignals.DataExploreCommand{
		WorkspaceID: uisignals.Pointer("test"), ModelID: uisignals.Pointer("olist"), DatasetID: uisignals.Pointer("orders"),
		Dimensions: []string{"orders.status"}, Measures: []string{"order_count"}, Filters: []uisignals.DataExploreFilterSignal{},
		Sort: []uisignals.DataExploreSortSignal{{Field: "order_count", Direction: "desc"}}, Limit: 100,
	}
	req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=test&mode=explore", nil)
	_, explorer, err := server.globalDataExplorerState(req, command)
	if err != nil {
		t.Fatalf("globalDataExplorerState() error = %v", err)
	}
	if got := uisignals.ValueOrZero(explorer.Command.Mode); got != "explore" {
		t.Fatalf("mode = %q, want explore", got)
	}
	if explorer.Explore.SelectedModel == nil || explorer.Explore.SelectedModel.ID != "olist" {
		t.Fatalf("selected model = %#v", explorer.Explore.SelectedModel)
	}
	if explorer.Explore.SelectedDataset == nil || explorer.Explore.SelectedDataset.ID != "orders" {
		t.Fatalf("selected dataset = %#v", explorer.Explore.SelectedDataset)
	}
	if len(explorer.Explore.Fields) != 3 {
		t.Fatalf("field catalog = %#v, want two dimensions and one measure", explorer.Explore.Fields)
	}
	if len(explorer.Explore.Result.Rows) != 1 || explorer.Explore.Result.Rows[0]["status"] != "delivered" {
		t.Fatalf("result = %#v", explorer.Explore.Result)
	}
	if explorer.Explore.Result.SQL == nil || !strings.Contains(*explorer.Explore.Result.SQL, "COUNT") {
		t.Fatalf("sql = %#v", explorer.Explore.Result.SQL)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(requests))
	}
	query := requests[0]
	if query.Kind != dataquery.KindSemanticAggregate || query.Target != "orders" {
		t.Fatalf("query = %#v", query)
	}
	if query.Surface != dataquery.SurfaceDataExplorer || query.Operation != "semantic_explore" {
		t.Fatalf("metadata = surface %q operation %q", query.Surface, query.Operation)
	}
}

func (m dataExplorerFixtureMetrics) WorkspaceAssets(workspaceID, servingStateID string) ([]workspace.Asset, []workspace.AssetEdge, bool) {
	catalog, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeCatalog, workspaceID, "", "Catalog", "", "catalog.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	connection, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeConnection, "olist.local", catalog.ID, "local", "", "connection.v1", map[string]any{"Kind": "local"})
	if err != nil {
		return nil, nil, false
	}
	modelID := firstNonEmpty(m.modelID, "olist")
	sourceKey := firstNonEmpty(m.sourceKey, "orders")
	sourceAssetKey := firstNonEmpty(m.sourceKey, "olist.orders")
	source, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeSource, sourceAssetKey, catalog.ID, "orders source", "", "source.v1", map[string]any{"Connection": "local", "Format": "csv", "Path": "orders.csv"})
	if err != nil {
		return nil, nil, false
	}
	model, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeSemanticModel, modelID, catalog.ID, modelID, "", "semantic_model.v1", map[string]any{})
	if err != nil {
		return nil, nil, false
	}
	table, err := testWorkspaceAsset(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), workspace.AssetTypeModelTable, modelID+".orders", model.ID, "orders", "", "model_table.v1", map[string]any{"Source": sourceKey})
	if err != nil {
		return nil, nil, false
	}
	return []workspace.Asset{catalog, connection, source, model, table}, []workspace.AssetEdge{
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), catalog.ID, connection.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), catalog.ID, source.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), catalog.ID, model.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), model.ID, table.ID, workspace.AssetEdgeContains),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), source.ID, connection.ID, workspace.AssetEdgeUsesConnection),
		workspace.NewAssetEdge(workspace.WorkspaceID(workspaceID), workspace.ServingStateID(servingStateID), table.ID, source.ID, workspace.AssetEdgeReadsSource),
	}, true
}

func (m dataExplorerFixtureMetrics) openModelTableDB(ctx context.Context, modelID string) (*sql.DB, error) {
	if strings.TrimSpace(m.duckDBDir) == "" {
		return nil, fmt.Errorf("fixture DuckDB directory is not configured")
	}
	return openTestDuckDBForInspection(ctx, dataExplorerTestDatabasePath(m.duckDBDir, modelID))
}

func openTestDuckDBForInspection(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", path+"?access_mode=READ_ONLY")
	if err == nil {
		db.SetMaxOpenConns(1)
		db.SetMaxIdleConns(1)
		pingErr := db.PingContext(ctx)
		if pingErr == nil {
			return db, nil
		}
		_ = db.Close()
		err = pingErr
	}
	fallbackDB, fallbackErr := sql.Open("duckdb", path)
	if fallbackErr != nil {
		return nil, errors.Join(err, fallbackErr)
	}
	fallbackDB.SetMaxOpenConns(1)
	fallbackDB.SetMaxIdleConns(1)
	if pingErr := fallbackDB.PingContext(ctx); pingErr != nil {
		_ = fallbackDB.Close()
		return nil, errors.Join(err, pingErr)
	}
	return fallbackDB, nil
}

func quoteDuckDBIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

func TestDataExplorerRouteRendersSignalsAndWiring(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	duckDBDir := seedDataExplorerDuckDB(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), duckDBDir: duckDBDir}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test", DuckDBDir: duckDBDir}))

	req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=test&object=model_table:model_table:olist.orders", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{
		"<lv-data-explorer",
		"/static/data-explorer.js",
		`<meta name="csrf-token" content="`,
		"/static/command.js",
		`data-init="@get('/updates?`,
		"route=data",
		"workspace=test",
		"/data/command",
		"window.LeapViewCommand.headers()",
		"object=model_table%3Amodel_table%3Aolist.orders",
	} {
		body = html.UnescapeString(body)
		if !strings.Contains(body, want) {
			t.Fatalf("data route missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "data-signals=") || strings.Contains(body, `"csrfToken"`) {
		t.Fatalf("data route should not embed initial signals or csrfToken signal:\n%s", body)
	}
}

func TestWorkspaceDataExplorerRouteRedirectsToGlobalRoute(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test"})

	req := dataExplorerTestRequest(http.MethodGet, "/workspaces/test/data?object=source:olist.orders", nil)
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	location := rec.Header().Get("Location")
	if location != "/data?object=source%3Aolist.orders&workspace=test" {
		t.Fatalf("location = %q", location)
	}
}

func TestGlobalDataExplorerSelectsDuplicateKeysByWorkspace(t *testing.T) {
	store := testStore(t)
	duckDBDir := seedDataExplorerDuckDB(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), duckDBDir: duckDBDir}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	seedActiveDeploymentFromWorkspaceAssets(t, store, "ops", metrics)
	server := assembleRuntime(NewMultiWorkspaceMetrics(map[string]QueryMetrics{
		"test": metrics,
		"ops":  metrics,
	}), testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: duckDBDir}))

	req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=ops&object=model_table:model_table:olist.orders", nil)
	_, explorer, err := server.globalDataExplorerState(req, dataExplorerCommandFromQuery("ops", "model_table:model_table:olist.orders"))
	if err != nil {
		t.Fatalf("globalDataExplorerState() error = %v", err)
	}
	if uisignals.ValueOrZero(explorer.SelectedWorkspaceID) != "ops" || uisignals.ValueOrZero(explorer.Command.WorkspaceID) != "ops" {
		t.Fatalf("selected workspace = %#v command=%#v", explorer.SelectedWorkspaceID, explorer.Command)
	}
	if uisignals.ValueOrZero(explorer.SelectedKey) != "model_table:model_table:olist.orders" || explorer.SelectedObject == nil || explorer.SelectedObject.WorkspaceID != "ops" {
		t.Fatalf("selected object = %#v key=%q", explorer.SelectedObject, uisignals.ValueOrZero(explorer.SelectedKey))
	}
	if len(explorer.Objects) != 2 {
		t.Fatalf("object count = %d, want both workspaces' model table", len(explorer.Objects))
	}
}

func TestGlobalDataExplorerFallsBackToRuntimeCatalogWithoutActiveAssetDeployment(t *testing.T) {
	server := assembleRuntime(fakeMetrics{}, assemblyConfig{WorkspaceID: "test-workspace"})
	req := dataExplorerTestRequest(http.MethodGet, "/data", nil)

	page, explorer, err := server.globalDataExplorerState(req, dataExplorerCommandFromQuery("", ""))
	if err != nil {
		t.Fatalf("globalDataExplorerState() error = %v", err)
	}
	if uisignals.ValueOrZero(page.SelectedWorkspaceID) != "test-workspace" || uisignals.ValueOrZero(explorer.SelectedWorkspaceID) != "test-workspace" {
		t.Fatalf("selected workspace page=%q explorer=%q", uisignals.ValueOrZero(page.SelectedWorkspaceID), uisignals.ValueOrZero(explorer.SelectedWorkspaceID))
	}
	rendered := fmtSprint(explorer)
	for _, want := range []string{"model_table:model_table:test.orders"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("fallback explorer missing %q:\n%#v", want, explorer)
		}
	}
	if strings.Contains(rendered, "assets were not found") {
		t.Fatalf("runtime catalog fallback should not expose missing asset deployment warning:\n%#v", explorer.Warnings)
	}
}

func TestDataExplorerPreviewsModelTable(t *testing.T) {
	store := testStore(t)
	dataDir := seedDataExplorerCSV(t)
	duckDBDir := seedDataExplorerDuckDB(t)
	metrics := dataExplorerFixtureMetrics{dataDir: dataDir, duckDBDir: duckDBDir}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: duckDBDir}))

	cases := []struct {
		name     string
		object   string
		want     string
		wantRows bool
	}{
		{name: "model table", object: "model_table:model_table:olist.orders", want: "shipped", wantRows: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=test&object="+tc.object, nil)
			_, explorer, err := server.globalDataExplorerState(req, dataExplorerCommandFromQuery("test", tc.object))
			if err != nil {
				t.Fatalf("globalDataExplorerState() error = %v", err)
			}
			if uisignals.ValueOrZero(explorer.Preview.Error) != "" {
				t.Fatalf("preview error = %q", uisignals.ValueOrZero(explorer.Preview.Error))
			}
			if explorer.Preview.ChunkSize != dataExplorerDefaultLimit || explorer.Preview.RowHeight != dataExplorerRowHeight {
				t.Fatalf("preview window defaults = chunk %d rowHeight %d", explorer.Preview.ChunkSize, explorer.Preview.RowHeight)
			}
			if len(explorer.Preview.Blocks) == 0 || tc.wantRows && len(explorer.Preview.Blocks["a"].Rows) == 0 {
				t.Fatalf("preview row state is incomplete:\n%#v", explorer.Preview)
			}
			rendered := fmtSprint(explorer.Preview)
			if !strings.Contains(rendered, tc.want) {
				t.Fatalf("preview missing %q:\n%#v", tc.want, explorer.Preview)
			}
			if strings.Contains(rendered, "order_count") {
				t.Fatalf("semantic preview included aggregate measure:\n%#v", explorer.Preview)
			}
		})
	}
}

func TestDataExplorerModelTablePreviewUsesRuntimeBackedModelTable(t *testing.T) {
	store := testStore(t)
	dataDir := seedDataExplorerCSV(t)
	runtimeDuckDBDir := seedDataExplorerDuckDBForModel(t, "sales")
	appDuckDBDir := t.TempDir()
	metrics := dataExplorerFixtureMetrics{dataDir: dataDir, duckDBDir: runtimeDuckDBDir, modelID: "sales", sourceKey: "olist.payments"}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "sales", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "sales", DuckDBDir: appDuckDBDir}))

	req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=sales&object=model_table:model_table:sales.orders", nil)
	_, explorer, err := server.globalDataExplorerState(req, dataExplorerCommandFromQuery("sales", "model_table:model_table:sales.orders"))
	if err != nil {
		t.Fatalf("globalDataExplorerState() error = %v", err)
	}
	if explorer.SelectedObject == nil || uisignals.ValueOrZero(explorer.SelectedObject.ModelID) != "sales" || uisignals.ValueOrZero(explorer.SelectedObject.Table) != "orders" {
		t.Fatalf("selected model table = %#v", explorer.SelectedObject)
	}
	if uisignals.ValueOrZero(explorer.Preview.Error) != "" {
		t.Fatalf("preview error = %q", uisignals.ValueOrZero(explorer.Preview.Error))
	}
	if len(explorer.Preview.Blocks["a"].Rows) == 0 || fmt.Sprint(explorer.Preview.Blocks["a"].Rows[0]["status"]) != "delivered" {
		t.Fatalf("model table preview rows missing delivered row: %#v", explorer.Preview.Blocks)
	}
	if _, err := os.Stat(dataExplorerTestDatabasePath(appDuckDBDir, "sales")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("data explorer should not create/open app-level model DB, stat err = %v", err)
	}
	if _, err := os.Stat(dataExplorerTestDatabasePath(runtimeDuckDBDir, "sales")); err != nil {
		t.Fatalf("runtime fixture DB missing: %v", err)
	}
}

func TestDataExplorerCommandPublishesPatch(t *testing.T) {
	store := testStore(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t)}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))
	updates, unsubscribe := server.runtime.broker.Subscribe("data-explorer:test-client")
	defer unsubscribe()

	body := strings.NewReader(`{"dataExplorerCommand":{"workspaceId":"test","objectKey":"model_table:model_table:olist.orders","block":"b","start":100,"count":100,"requestSeq":7,"resetVersion":2,"sort":{"column":"status","direction":"asc"}}}`)
	req := dataExplorerTestRequest(http.MethodPost, "/data/command", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		explorer, ok := patch["dataExplorer"].(uisignals.DataExplorerSignal)
		if !ok {
			t.Fatalf("patch missing dataExplorer: %#v", patch)
		}
		if uisignals.ValueOrZero(explorer.SelectedWorkspaceID) != "test" || uisignals.ValueOrZero(explorer.SelectedKey) != "model_table:model_table:olist.orders" || uisignals.ValueOrZero(explorer.Preview.Error) != "" {
			t.Fatalf("unexpected explorer patch: %#v", explorer)
		}
		block := explorer.Preview.Blocks["b"]
		if block.Start != 100 || block.RequestSeq != 7 || block.ResetVersion != 2 || uisignals.ValueOrZero(block.Sort.Column) != "status" || uisignals.ValueOrZero(block.Sort.Direction) != "asc" {
			t.Fatalf("unexpected preview block: %#v", block)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data explorer patch")
	}
}

func TestDataExplorerModelTablePreviewIgnoresInvalidSortColumn(t *testing.T) {
	requests := []dataquery.Query{}
	store := testStore(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), dataQueries: &requests}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))

	req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=test&object=model_table:model_table:olist.orders", nil)
	command := dataExplorerCommandFromQuery("test", "model_table:model_table:olist.orders")
	command.Sort = uisignals.DataPreviewSortSignal{Column: uisignals.Pointer("order_count"), Direction: uisignals.Pointer("desc")}
	_, explorer, err := server.globalDataExplorerState(req, command)
	if err != nil {
		t.Fatalf("globalDataExplorerState() error = %v", err)
	}
	if uisignals.ValueOrZero(explorer.Preview.Error) != "" {
		t.Fatalf("preview error = %q", uisignals.ValueOrZero(explorer.Preview.Error))
	}
	if len(requests) == 0 {
		t.Fatal("model table preview was not requested")
	}
	for _, sort := range requests[len(requests)-1].Sort {
		if sort.Field == "order_count" {
			t.Fatalf("invalid model table sort was forwarded to planner: %#v", requests[len(requests)-1].Sort)
		}
	}
}

func TestDataExplorerModelTablePreviewAcceptsExposedSortColumn(t *testing.T) {
	requests := []dataquery.Query{}
	store := testStore(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), dataQueries: &requests}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))

	req := dataExplorerTestRequest(http.MethodGet, "/data?workspace=test&object=model_table:model_table:olist.orders", nil)
	command := dataExplorerCommandFromQuery("test", "model_table:model_table:olist.orders")
	command.Sort = uisignals.DataPreviewSortSignal{Column: uisignals.Pointer("status"), Direction: uisignals.Pointer("asc")}
	_, explorer, err := server.globalDataExplorerState(req, command)
	if err != nil {
		t.Fatalf("globalDataExplorerState() error = %v", err)
	}
	if uisignals.ValueOrZero(explorer.Preview.Error) != "" {
		t.Fatalf("preview error = %q", uisignals.ValueOrZero(explorer.Preview.Error))
	}
	if len(requests) == 0 || len(requests[len(requests)-1].Sort) != 1 {
		t.Fatalf("model table preview did not receive valid sort: %#v", requests)
	}
	if got := requests[len(requests)-1].Sort[0]; got.Field != "status" || got.Direction != "asc" {
		t.Fatalf("model table sort = %#v", got)
	}
}

func TestDataExplorerCommandReusesPostedPreviewTotalsForScroll(t *testing.T) {
	store := testStore(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t)}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))
	updates, unsubscribe := server.runtime.broker.Subscribe("data-explorer:test-client")
	defer unsubscribe()

	object := uisignals.DataExplorerObjectSignal{
		Key:         "model_table:model_table:olist.orders",
		WorkspaceID: "test",
		Layer:       "model_table",
		ModelID:     uisignals.Pointer("olist"),
		Table:       uisignals.Pointer("orders"),
		Title:       "orders",
		Columns: uisignals.OptionalSlice([]uisignals.DataPreviewColumnSignal{
			{Key: "order_id", Label: "Order ID", Type: uisignals.Pointer("string")},
			{Key: "status", Label: "Status", Type: uisignals.Pointer("string")},
		}),
	}
	currentCommand := uisignals.DataExplorerCommand{
		WorkspaceID:  uisignals.Pointer("test"),
		ObjectKey:    uisignals.Pointer(object.Key),
		Block:        uisignals.Pointer("a"),
		Start:        0,
		Limit:        100,
		Count:        100,
		RequestSeq:   1,
		ResetVersion: 3,
		Sort:         uisignals.DataPreviewSortSignal{Column: uisignals.Pointer("status"), Direction: uisignals.Pointer("asc")},
	}
	currentExplorer := uisignals.DataExplorerSignal{
		Objects:             []uisignals.DataExplorerObjectSignal{object},
		SelectedWorkspaceID: uisignals.Pointer("test"),
		SelectedKey:         uisignals.Pointer(object.Key),
		SelectedObject:      &object,
		Preview: uisignals.DataPreviewSignal{
			Columns:       uisignals.ValueOrZero(object.Columns),
			TotalRows:     250,
			AvailableRows: 250,
			ChunkSize:     100,
			RowHeight:     dataExplorerRowHeight,
			ResetVersion:  3,
			Blocks:        emptyDataPreviewBlocks(100, currentCommand.Sort, 3),
			TotalRowLabel: uisignals.Pointer("250"),
			Sort:          currentCommand.Sort,
		},
		Command: currentCommand,
	}
	nextCommand := currentCommand
	nextCommand.Block = uisignals.Pointer("b")
	nextCommand.Start = 100
	nextCommand.Offset = 100
	nextCommand.RequestSeq = 2
	bodyBytes, err := json.Marshal(map[string]any{
		"dataExplorer":        currentExplorer,
		"dataExplorerCommand": nextCommand,
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	req := dataExplorerTestRequest(http.MethodPost, "/data/command", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		explorer, ok := patch["dataExplorer"].(uisignals.DataExplorerSignal)
		if !ok {
			t.Fatalf("patch missing dataExplorer: %#v", patch)
		}
		if uisignals.ValueOrZero(explorer.Preview.Error) != "" {
			t.Fatalf("scroll command unexpectedly counted preview: %#v", explorer.Preview)
		}
		if explorer.Preview.TotalRows != 250 || explorer.Preview.AvailableRows != 250 || uisignals.ValueOrZero(explorer.Preview.TotalRowLabel) != "250" {
			t.Fatalf("preview totals were not reused: %#v", explorer.Preview)
		}
		if block := explorer.Preview.Blocks["b"]; block.Start != 100 || block.RequestSeq != 2 {
			t.Fatalf("requested scroll block missing: %#v", block)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data explorer patch")
	}
}

func TestDataExplorerCommandDoesNotPublishCanceledPreview(t *testing.T) {
	store := testStore(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), semanticPreviewError: context.Canceled}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))
	updates, unsubscribe := server.runtime.broker.Subscribe("data-explorer:test-client")
	defer unsubscribe()

	body := strings.NewReader(`{"dataExplorerCommand":{"workspaceId":"test","objectKey":"model_table:model_table:olist.orders","block":"b","start":100,"count":100,"requestSeq":7,"resetVersion":2}}`)
	req := dataExplorerTestRequest(http.MethodPost, "/data/command", body)
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		t.Fatalf("received canceled data explorer patch: %#v", patch)
	default:
	}
}

func TestDataExplorerCommandColumnWidthsReuseCurrentPreview(t *testing.T) {
	store := testStore(t)
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t), semanticPreviewError: errors.New("preview should not run for column widths")}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))
	updates, unsubscribe := server.runtime.broker.Subscribe("data-explorer:test-client")
	defer unsubscribe()

	object := uisignals.DataExplorerObjectSignal{
		Key:         "model_table:model_table:olist.orders",
		WorkspaceID: "test",
		Layer:       "model_table",
		ModelID:     uisignals.Pointer("olist"),
		Table:       uisignals.Pointer("orders"),
		Title:       "orders",
		Columns: uisignals.OptionalSlice([]uisignals.DataPreviewColumnSignal{
			{Key: "order_id", Label: "Order ID", Type: uisignals.Pointer("string")},
			{Key: "status", Label: "Status", Type: uisignals.Pointer("string")},
		}),
	}
	currentCommand := uisignals.DataExplorerCommand{WorkspaceID: uisignals.Pointer("test"), ObjectKey: uisignals.Pointer(object.Key), Block: uisignals.Pointer("all"), Limit: 100, Count: 100, Sort: uisignals.DataPreviewSortSignal{}, VisibleColumns: uisignals.Pointer([]string{})}
	currentExplorer := uisignals.DataExplorerSignal{
		Objects:             []uisignals.DataExplorerObjectSignal{object},
		SelectedWorkspaceID: uisignals.Pointer("test"),
		SelectedKey:         uisignals.Pointer(object.Key),
		SelectedObject:      &object,
		Preview: uisignals.DataPreviewSignal{
			Columns:       uisignals.ValueOrZero(object.Columns),
			TotalRows:     1,
			AvailableRows: 1,
			ChunkSize:     100,
			RowHeight:     dataExplorerRowHeight,
			Blocks: map[string]uisignals.DataPreviewBlockSignal{
				"a": {Start: 0, Rows: []map[string]any{{"order_id": "o1", "status": "delivered"}}},
			},
			TotalRowLabel: uisignals.Pointer("1"),
		},
		Command: currentCommand,
	}
	nextCommand := currentCommand
	nextCommand.ColumnWidths = uisignals.Pointer(map[string]float64{"order_id": 260})
	bodyBytes, err := json.Marshal(map[string]any{
		"dataExplorer":        currentExplorer,
		"dataExplorerCommand": nextCommand,
	})
	if err != nil {
		t.Fatalf("marshal command: %v", err)
	}
	req := dataExplorerTestRequest(http.MethodPost, "/data/command", strings.NewReader(string(bodyBytes)))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	rec := httptest.NewRecorder()
	server.Routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	select {
	case patch := <-updates:
		explorer, ok := patch["dataExplorer"].(uisignals.DataExplorerSignal)
		if !ok {
			t.Fatalf("patch missing dataExplorer: %#v", patch)
		}
		if uisignals.ValueOrZero(explorer.Preview.Error) != "" {
			t.Fatalf("resize-only command re-ran preview: %#v", explorer.Preview)
		}
		if got := uisignals.ValueOrZero(explorer.Command.ColumnWidths)["order_id"]; got != 260 {
			t.Fatalf("column width was not patched: %#v", explorer.Command.ColumnWidths)
		}
		if len(explorer.Preview.Blocks["a"].Rows) != 1 {
			t.Fatalf("current preview rows were not preserved: %#v", explorer.Preview.Blocks)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data explorer patch")
	}
}

func TestDataExplorerBrowserCommandRequiresAndAcceptsCSRF(t *testing.T) {
	store := testStore(t)
	auth := testAuth(store, "test", AuthConfig{DevBypass: true})
	metrics := dataExplorerFixtureMetrics{dataDir: seedDataExplorerCSV(t)}
	seedActiveDeploymentFromWorkspaceAssets(t, store, "test", metrics)
	server := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{Auth: auth, WorkspaceID: "test", DuckDBDir: seedDataExplorerDuckDB(t)}))
	updates, unsubscribe := server.runtime.broker.Subscribe("data-explorer:test-client")
	defer unsubscribe()

	commandBody := `{"dataExplorerCommand":{"workspaceId":"test","objectKey":"model_table:model_table:olist.orders","block":"b","start":100,"count":100,"requestSeq":7,"resetVersion":2,"sort":{"column":"status","direction":"asc"}}}`
	forbiddenReq := dataExplorerTestRequest(http.MethodPost, "http://localhost:8150/data/command", strings.NewReader(commandBody))
	forbiddenReq.Header.Set("Content-Type", "application/json")
	forbiddenReq.Header.Set("Accept", "application/json")
	forbiddenReq.Header.Set("Referer", "http://localhost:8150/data?workspace=test")
	forbiddenReq.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	forbiddenRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(forbiddenRec, forbiddenReq)
	if forbiddenRec.Code != http.StatusForbidden {
		t.Fatalf("POST without CSRF status = %d, want %d body=%s", forbiddenRec.Code, http.StatusForbidden, forbiddenRec.Body.String())
	}

	getReq := dataExplorerTestRequest(http.MethodGet, "http://localhost:8150/data?workspace=test&object=model_table:model_table:olist.orders", nil)
	getRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(getRec, getReq)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", getRec.Code, getRec.Body.String())
	}
	token := dataExplorerCSRFToken(t, getRec.Body.String())

	allowedReq := dataExplorerTestRequest(http.MethodPost, "http://localhost:8150/data/command", strings.NewReader(commandBody))
	allowedReq.Header.Set("Content-Type", "application/json")
	allowedReq.Header.Set("Accept", "application/json")
	allowedReq.Header.Set("X-CSRF-Token", token)
	allowedReq.Header.Set("Referer", "http://localhost:8150/data?workspace=test")
	allowedReq.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "test-client"})
	for _, cookie := range getRec.Result().Cookies() {
		allowedReq.AddCookie(cookie)
	}
	allowedRec := httptest.NewRecorder()
	server.Routes().ServeHTTP(allowedRec, allowedReq)
	if allowedRec.Code != http.StatusNoContent {
		t.Fatalf("POST with CSRF status = %d, want %d body=%s", allowedRec.Code, http.StatusNoContent, allowedRec.Body.String())
	}

	select {
	case patch := <-updates:
		explorer, ok := patch["dataExplorer"].(uisignals.DataExplorerSignal)
		if !ok {
			t.Fatalf("patch missing dataExplorer: %#v", patch)
		}
		block := explorer.Preview.Blocks["b"]
		if block.Start != 100 || block.RequestSeq != 7 || block.ResetVersion != 2 || uisignals.ValueOrZero(block.Sort.Column) != "status" || uisignals.ValueOrZero(block.Sort.Direction) != "asc" {
			t.Fatalf("unexpected preview block: %#v", block)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for data explorer patch")
	}
}

func dataExplorerCSRFToken(t *testing.T, body string) string {
	t.Helper()
	matches := regexp.MustCompile(`<meta name="csrf-token" content="([^"]+)"`).FindStringSubmatch(html.UnescapeString(body))
	if len(matches) != 2 || strings.TrimSpace(matches[1]) == "" {
		t.Fatalf("data route did not render csrf meta token:\n%s", body)
	}
	return matches[1]
}

func seedDataExplorerCSV(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("order_id,status\no1,delivered\no2,shipped\n"), 0o644); err != nil {
		t.Fatalf("write csv: %v", err)
	}
	return dir
}

func seedDataExplorerDuckDB(t *testing.T) string {
	t.Helper()
	return seedDataExplorerDuckDBForModel(t, "olist")
}

func seedDataExplorerDuckDBForModel(t *testing.T, modelID string) string {
	t.Helper()
	dir := t.TempDir()
	db, err := sql.Open("duckdb", dataExplorerTestDatabasePath(dir, modelID))
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE SCHEMA model"); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE model.orders(order_id VARCHAR, status VARCHAR)"); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := db.Exec("INSERT INTO model.orders VALUES ('o1', 'delivered'), ('o2', 'shipped')"); err != nil {
		t.Fatalf("insert rows: %v", err)
	}
	return dir
}

func dataExplorerTestDatabasePath(dir, modelID string) string {
	return filepath.Join(dir, "leapview-"+modelID+".duckdb")
}

func fmtSprint(value any) string {
	return strings.ReplaceAll(strings.ReplaceAll(fmt.Sprintf("%#v", value), "\n", " "), "\t", " ")
}
