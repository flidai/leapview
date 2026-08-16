package materialize_test

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsmaterialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/platform"
	refreshrun "github.com/flidai/leapview/internal/refresh/run"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
	analyticsmaterializesqlite "github.com/flidai/leapview/internal/refresh/sqlite"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

func TestModelTableExecutesPlannedSQL(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "test",
		Connections: map[string]semanticmodel.Connection{
			"local_files": {Kind: "managed"},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"revenue":  {Label: "Revenue"},
					"status":   {Label: "Status"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "orders", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	sources := &recordingSourceRegistrar{plan: analyticsmaterialize.ModelTablePlan{Mode: analyticsmaterialize.PlanModeDirectSourceRead, SQL: "CREATE OR REPLACE TABLE model.orders AS SELECT 1 AS order_id"}}
	executor := &recordingStatementsExecutor{}
	if _, err := analyticsmaterialize.Refresh(context.Background(), executor, sources, model); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(sources.ops, []string{"plan:orders"}) {
		t.Fatalf("source ops = %#v, want plan", sources.ops)
	}
	if !reflect.DeepEqual(executor.statements, []string{"CREATE SCHEMA IF NOT EXISTS model", "CREATE OR REPLACE TABLE model.orders AS SELECT 1 AS order_id"}) {
		t.Fatalf("statements = %#v, want planned SQL", executor.statements)
	}
}

func TestModelTablePlannerErrorStopsMaterialization(t *testing.T) {
	model := &semanticmodel.Model{
		Name:        "test",
		Connections: map[string]semanticmodel.Connection{"local_files": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"}},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Columns: map[string]semanticmodel.ModelColumn{
					"order_id": {SourceField: "raw_order_id"},
					"status":   {},
					"revenue":  {SourceField: "gross_revenue"},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"revenue":  {Label: "Revenue"},
					"status":   {Label: "Status"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "orders", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	sources := &recordingSourceRegistrar{planErr: errors.New("plan failed")}
	executor := &recordingStatementsExecutor{}
	if _, err := analyticsmaterialize.Refresh(context.Background(), executor, sources, model); err == nil || !strings.Contains(err.Error(), "plan failed") {
		t.Fatalf("Refresh() error = %v, want plan failed", err)
	}
	if !reflect.DeepEqual(executor.statements, []string{"CREATE SCHEMA IF NOT EXISTS model"}) {
		t.Fatalf("statements = %#v, want only schema setup", executor.statements)
	}
}

func TestSQLModelTableUsesPlannedSQL(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "test",
		Connections: map[string]semanticmodel.Connection{
			"local_files": {Kind: "managed"},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {
				Path:       "orders.csv",
				Format:     "csv",
				Connection: "local_files",
				Fields: map[string]semanticmodel.SourceField{
					"unwanted_source_level_field": {},
				},
			},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Sources:    []string{"orders"},
				Transform:  semanticmodel.Transform{SQL: "SELECT order_id, revenue FROM source.orders"},
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Label: "Order ID"}, "revenue": {Label: "Revenue"}},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "orders", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	sources := &recordingSourceRegistrar{plan: analyticsmaterialize.ModelTablePlan{Mode: analyticsmaterialize.PlanModeProjectedSourceInline, SQL: "CREATE OR REPLACE TABLE model.orders AS SELECT order_id, revenue FROM read_csv('orders.csv')"}}
	executor := &recordingStatementsExecutor{}
	if _, err := analyticsmaterialize.Refresh(context.Background(), executor, sources, model); err != nil {
		t.Fatal(err)
	}
	if len(executor.statements) != 2 || !strings.Contains(executor.statements[1], "read_csv") {
		t.Fatalf("statements = %#v, want planned inline SQL", executor.statements)
	}
}

func TestModelTableExecutionErrorReturnsMaterializationError(t *testing.T) {
	model := &semanticmodel.Model{
		Name:        "test",
		Connections: map[string]semanticmodel.Connection{"local_files": {Kind: "managed"}},
		Sources:     map[string]semanticmodel.Source{"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"}},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Label: "Order ID"}},
			},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	sources := &recordingSourceRegistrar{plan: analyticsmaterialize.ModelTablePlan{Mode: analyticsmaterialize.PlanModeDirectSourceRead, SQL: "CREATE OR REPLACE TABLE model.orders AS SELECT 1"}}
	if _, err := analyticsmaterialize.Refresh(context.Background(), failingExecutor{}, sources, model); err == nil {
		t.Fatal("refresh unexpectedly succeeded")
	}
	if len(sources.ops) != 0 {
		t.Fatalf("source ops = %#v, want none", sources.ops)
	}
}

func TestModelTablesMaterializeAfterModelDependencies(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "test",
		Connections: map[string]semanticmodel.Connection{
			"local_files": {Kind: "managed"},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"},
		},
		Tables: map[string]semanticmodel.Table{
			"order_summary": {
				Sources:    []string{},
				PrimaryKey: "status",
				Transform:  semanticmodel.Transform{SQL: "SELECT status, SUM(revenue) AS revenue FROM model.orders GROUP BY status"},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status":  {Label: "Status"},
					"revenue": {Label: "Revenue"},
				},
			},
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"status":   {Label: "Status"},
					"revenue":  {Label: "Revenue"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "order_summary", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "order_summary.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	executor := &recordingStatementsExecutor{}
	if _, err := analyticsmaterialize.Refresh(context.Background(), executor, &recordingSourceRegistrar{}, model); err != nil {
		t.Fatal(err)
	}
	if len(executor.statements) != 3 {
		t.Fatalf("statements = %#v, want schema setup and two materializations", executor.statements)
	}
	if !strings.Contains(executor.statements[1], "model.orders") || !strings.Contains(executor.statements[2], "model.order_summary") {
		t.Fatalf("materialization order = %#v, want orders before order_summary", executor.statements)
	}
}

func TestMovieLensModelTableOrderMaterializesDimensionsBeforeFacts(t *testing.T) {
	model := &semanticmodel.Model{
		Name:        "movielens",
		Connections: map[string]semanticmodel.Connection{"movielens": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"ratings": {Path: "ratings.csv", Format: "csv", Connection: "movielens"},
			"movies":  {Path: "movies.csv", Format: "csv", Connection: "movielens"},
			"tags":    {Path: "tags.csv", Format: "csv", Connection: "movielens"},
			"links":   {Path: "links.csv", Format: "csv", Connection: "movielens"},
		},
		Tables: map[string]semanticmodel.Table{
			"ratings": {
				Source:     "ratings",
				PrimaryKey: "rating_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"rating_id": {Label: "Rating ID"}},
			},
			"movies": {
				Sources:    []string{"movies", "links"},
				Transform:  semanticmodel.Transform{SQL: "SELECT m.movieId AS movie_id FROM source.movies m LEFT JOIN source.links l ON l.movieId = m.movieId"},
				PrimaryKey: "movie_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"movie_id": {Label: "Movie ID"}},
			},
			"users": {
				Sources:    []string{"ratings", "tags"},
				Transform:  semanticmodel.Transform{SQL: "SELECT r.userId AS user_id FROM source.ratings r LEFT JOIN source.tags t ON t.userId = r.userId"},
				PrimaryKey: "user_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"user_id": {Label: "User ID"}},
			},
			"rating_genres": {
				Transform:  semanticmodel.Transform{SQL: "SELECT rating_id, genre FROM model.ratings JOIN model.movies USING (movie_id)"},
				PrimaryKey: "rating_genre_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"rating_genre_id": {Label: "Rating Genre ID"}},
			},
			"tags": {
				Source:     "tags",
				PrimaryKey: "tag_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"tag_id": {Label: "Tag ID"}},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"rating_count": {Fact: "ratings", Aggregation: "count", Empty: "zero", Label: "Ratings"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}

	order, err := analyticsmaterialize.ModelTableOrder(model)
	if err != nil {
		t.Fatal(err)
	}
	ratingsIndex := indexOf(order, "ratings")
	moviesIndex := indexOf(order, "movies")
	ratingGenresIndex := indexOf(order, "rating_genres")
	if ratingsIndex < 0 || moviesIndex < 0 || ratingGenresIndex < 0 {
		t.Fatalf("order = %#v, want ratings, movies, and rating_genres", order)
	}
	if ratingGenresIndex < ratingsIndex || ratingGenresIndex < moviesIndex {
		t.Fatalf("order = %#v, want rating_genres after ratings and movies", order)
	}
}

func TestWorkspaceRuntimeCommitsModelTablesInOneDuckLakeSnapshot(t *testing.T) {
	ctx, releaseAdmission := admittedRefreshTestContext(t)
	defer releaseAdmission()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("order_id,status,revenue\no1,paid,10\no2,paid,15\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name:        "workspace",
		Connections: map[string]semanticmodel.Connection{"local_files": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"status":   {Label: "Status"},
					"revenue":  {Label: "Revenue"},
				},
			},
			"order_summary": {
				ModelDependencies: []string{"orders"},
				Transform:         semanticmodel.Transform{SQL: "SELECT status, SUM(revenue) AS revenue FROM model.orders GROUP BY status"},
				PrimaryKey:        "status",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status":  {Label: "Status"},
					"revenue": {Label: "Revenue"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "order_summary", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "order_summary.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindManagedTestRoot(model, dir)

	node := openDuckLakeTestNode(t, ctx, dir, "", "", 3)
	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models:   map[string]*semanticmodel.Model{"sales": model},
		Database: node,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.DuckLakeSnapshotID() <= 0 {
		t.Fatalf("DuckLakeSnapshotID = %d, want committed snapshot", runtime.DuckLakeSnapshotID())
	}

	var matchingSnapshots int
	if err := scanDuckLakeTestRow(ctx, node, `
SELECT count(*)
FROM lake.snapshots()
WHERE snapshot_id = ?
  AND CAST(changes AS VARCHAR) LIKE '%model.orders%'
	  AND CAST(changes AS VARCHAR) LIKE '%model.order_summary%'`, []any{runtime.DuckLakeSnapshotID()}, &matchingSnapshots); err != nil {
		t.Fatal(err)
	}
	if matchingSnapshots != 1 {
		t.Fatalf("matching committed snapshots = %d, want 1", matchingSnapshots)
	}
}

func TestWorkspaceRuntimeKeepsControlPlaneAndDuckLakeCatalogSeparate(t *testing.T) {
	ctx, releaseAdmission := admittedRefreshTestContext(t)
	defer releaseAdmission()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "orders.csv"), []byte("order_id,status,revenue\no1,paid,10\no2,paid,15\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(dir, "leapview.db")
	store, err := platform.Open(ctx, catalogPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "sales", Title: "Sales"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	model := &semanticmodel.Model{
		Name:        "workspace",
		Connections: map[string]semanticmodel.Connection{"local_files": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"status":   {Label: "Status"},
					"revenue":  {Label: "Revenue"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "orders", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindManagedTestRoot(model, dataDir)
	duckRoot := filepath.Join(dir, "duckdb", "dev")
	duckLakeDataPath := filepath.Join(dir, ".leapview", "data")
	node := openDuckLakeTestNode(t, ctx, duckRoot, "", duckLakeDataPath, 2)
	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models:   map[string]*semanticmodel.Model{"sales": model},
		Database: node,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	if runtime.DuckLakeSnapshotID() <= 0 {
		t.Fatalf("DuckLakeSnapshotID = %d, want committed snapshot", runtime.DuckLakeSnapshotID())
	}
	if _, err := os.Stat(filepath.Join(duckRoot, "catalog.duckdb")); err != nil {
		t.Fatalf("DuckDB-backed analytical catalog missing: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, "SELECT 1 FROM workspaces WHERE id = 'sales'"); err != nil {
		t.Fatalf("platform control-plane table unavailable after materialization: %v", err)
	}

	var physicalTables int
	if err := scanDuckLakeTestRow(ctx, node, "SELECT count(*) FROM ducklake_table_info('lake') WHERE table_name = 'orders'", nil, &physicalTables); err != nil {
		t.Fatal(err)
	}
	if physicalTables != 1 {
		t.Fatalf("DuckLake physical tables = %d, want one orders table in platform catalog", physicalTables)
	}
}

func TestWorkspaceRuntimeQueriesPinnedDuckLakeSnapshots(t *testing.T) {
	ctx, releaseAdmission := admittedRefreshTestContext(t)
	defer releaseAdmission()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(dir, "leapview.db")
	store, err := platform.Open(ctx, catalogPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	model := simpleOrdersModel(t, dataDir)
	duckRoot := filepath.Join(dir, "duckdb", "dev")
	node := openDuckLakeTestNode(t, ctx, duckRoot, "", filepath.Join(dir, ".leapview", "data"), 3)
	config := analyticsduckdb.WorkspaceRuntimeConfig{
		Models:   map[string]*semanticmodel.Model{"sales": model},
		Database: node,
	}

	writeOrdersCSV(t, dataDir, 10, 15)
	writer, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	snapshot1 := writer.DuckLakeSnapshotID()
	writeOrdersCSV(t, dataDir, 100, 150)
	if err := writer.Refresh(ctx); err != nil {
		t.Fatalf("refresh second snapshot: %v", err)
	}
	snapshot2 := writer.DuckLakeSnapshotID()
	if snapshot2 <= snapshot1 {
		t.Fatalf("snapshot2 = %d, want > snapshot1 %d", snapshot2, snapshot1)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	config.SnapshotID = snapshot1
	first, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, config)
	if err != nil {
		t.Fatalf("open first snapshot: %v", err)
	}
	defer first.Close()
	if got := first.ReadConcurrency(); got != 3 {
		t.Fatalf("first snapshot read concurrency = %d, want 3", got)
	}
	if got := queryRevenue(t, ctx, first); got != 25 {
		t.Fatalf("first snapshot revenue = %v, want 25", got)
	}

	config.SnapshotID = snapshot2
	second, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, config)
	if err != nil {
		t.Fatalf("open second snapshot: %v", err)
	}
	defer second.Close()
	if got := queryRevenue(t, ctx, second); got != 250 {
		t.Fatalf("second snapshot revenue = %v, want 250", got)
	}
}

func TestWorkspaceRuntimeCanCommitWhilePinnedSnapshotIsOpen(t *testing.T) {
	ctx, releaseAdmission := admittedRefreshTestContext(t)
	defer releaseAdmission()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(dir, "leapview.db")
	store, err := platform.Open(ctx, catalogPath)
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	node := openDuckLakeTestNode(t, ctx, filepath.Join(dir, "duckdb", "dev"), "", filepath.Join(dir, ".leapview", "data"), 3)
	config := analyticsduckdb.WorkspaceRuntimeConfig{
		Models:   map[string]*semanticmodel.Model{"sales": simpleOrdersModel(t, dataDir)},
		Database: node,
	}

	writeOrdersCSV(t, dataDir, 10, 15)
	writer, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := writer.DuckLakeSnapshotID()
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}

	config.SnapshotID = snapshotID
	reader, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, config)
	if err != nil {
		t.Fatalf("open pinned snapshot: %v", err)
	}
	defer reader.Close()
	// A second active workspace keeps the same pinned snapshot open. The
	// production DuckLake writer lock must permit a refresh without requiring
	// unrelated workspace runtimes to be closed.
	otherConfig := config
	otherConfig.WorkspaceID = "operations"
	otherConfig.ServingStateID = "operations-active"
	otherReader, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, otherConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer otherReader.Close()

	writeOrdersCSV(t, dataDir, 100, 150)
	config.SnapshotID = 0
	nextWriter, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, config)
	if err != nil {
		t.Fatalf("open writer with pinned snapshot active: %v", err)
	}
	defer nextWriter.Close()
	if got := queryRevenue(t, ctx, nextWriter); got != 250 {
		t.Fatalf("next snapshot revenue = %v, want 250", got)
	}
	if got := queryRevenue(t, ctx, reader); got != 25 {
		t.Fatalf("pinned snapshot revenue = %v, want 25", got)
	}
	if got := queryRevenue(t, ctx, otherReader); got != 25 {
		t.Fatalf("unrelated workspace pinned snapshot revenue = %v, want 25", got)
	}
}

func TestWorkspaceRuntimeWritesDuckLakeDataUnderConfiguredInstanceStore(t *testing.T) {
	ctx, releaseAdmission := admittedRefreshTestContext(t)
	defer releaseAdmission()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "source")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrdersCSV(t, dataDir, 10, 15)
	catalogPath := filepath.Join(dir, ".leapview", "leapview.db")
	dataPath := filepath.Join(dir, ".leapview", "data")
	oldEnvironmentDataPath := filepath.Join(dir, ".leapview", "duckdb", "dev", "data")
	node := openDuckLakeTestNode(t, ctx, filepath.Join(dir, ".leapview", "duckdb"), catalogPath, dataPath, 2)

	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models:   map[string]*semanticmodel.Model{"sales": simpleOrdersModel(t, dataDir)},
		Database: node,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Close()
	info, err := os.Stat(dataPath)
	if err != nil {
		t.Fatalf("DuckLake data path was not created: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("DuckLake data path %s is not a directory", dataPath)
	}
	if _, err := os.Stat(oldEnvironmentDataPath); !os.IsNotExist(err) {
		t.Fatalf("old environment-scoped DuckLake data path exists or stat failed: %v", err)
	}
}

func TestWorkspaceRuntimeWritesDuckLakeCommitMetadata(t *testing.T) {
	ctx, releaseAdmission := admittedRefreshTestContext(t)
	defer releaseAdmission()
	dir := t.TempDir()
	dataDir := filepath.Join(dir, "source")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeOrdersCSV(t, dataDir, 10, 15)
	catalogPath := filepath.Join(dir, ".leapview", "leapview.db")
	dataPath := filepath.Join(dir, ".leapview", "data")
	node := openDuckLakeTestNode(t, ctx, filepath.Join(dir, ".leapview", "duckdb"), catalogPath, dataPath, 2)

	runtime, err := analyticsduckdb.OpenWorkspaceMaterializeRuntime(ctx, analyticsduckdb.WorkspaceRuntimeConfig{
		Models:           map[string]*semanticmodel.Model{"sales": simpleOrdersModel(t, dataDir)},
		Database:         node,
		ServingStateID:   "dep_123",
		WorkspaceID:      "sales",
		Environment:      "prod",
		SemanticDigest:   "semantic-digest",
		ArtifactDigest:   "artifact-digest",
		SourceDataDigest: "source-digest",
	})
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := runtime.DuckLakeSnapshotID()
	defer runtime.Close()

	extra := duckLakeSnapshotExtraInfo(t, ctx, node, snapshotID)
	for _, want := range []string{
		`"servingStateId":"dep_123"`,
		`"workspaceId":"sales"`,
		`"environment":"prod"`,
		`"semanticModelDigest":"semantic-digest"`,
		`"artifactDigest":"artifact-digest"`,
		`"sourceDataDigest":"source-digest"`,
	} {
		if !strings.Contains(extra, want) {
			t.Fatalf("DuckLake snapshot metadata missing %s in %s", want, extra)
		}
	}
}

func duckLakeSnapshotExtraInfo(t *testing.T, ctx context.Context, node *analyticsducklake.Environment, snapshotID int64) string {
	t.Helper()
	var extra string
	if err := scanDuckLakeTestRow(ctx, node, "SELECT CAST(commit_extra_info AS VARCHAR) FROM lake.snapshots() WHERE snapshot_id = ?", []any{snapshotID}, &extra); err != nil {
		t.Fatal(err)
	}
	return extra
}

func scanDuckLakeTestRow(ctx context.Context, node *analyticsducklake.Environment, query string, args []any, destinations ...any) error {
	lease, err := node.Acquire(ctx)
	if err != nil {
		return err
	}
	defer lease.Release()
	session, err := node.Session(lease.Context())
	if err != nil {
		return err
	}
	return session.QueryRowContext(lease.Context(), query, args...).Scan(destinations...)
}

func openDuckLakeTestNode(t *testing.T, ctx context.Context, root, catalogPath, dataPath string, maxConnections int) *analyticsducklake.Environment {
	t.Helper()
	node, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir:        root,
		CatalogPath:    catalogPath,
		DataPath:       dataPath,
		MaxConnections: maxConnections,
	})
	if err != nil {
		t.Fatalf("open DuckDB test node: %v", err)
	}
	t.Cleanup(func() {
		if err := node.Close(); err != nil {
			t.Errorf("close DuckDB test node: %v", err)
		}
	})
	return node
}

func admittedRefreshTestContext(t *testing.T) (context.Context, func()) {
	t.Helper()
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		t.Fatal(err)
	}
	lease, err := controller.Acquire(context.Background(), workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "materialize-test", EstimatedMemoryBytes: 1})
	if err != nil {
		controller.Close()
		t.Fatal(err)
	}
	return lease.Context(), func() {
		lease.Release()
		controller.Close()
	}
}

func simpleOrdersModel(t *testing.T, root string) *semanticmodel.Model {
	t.Helper()
	model := &semanticmodel.Model{
		Name:        "workspace",
		Connections: map[string]semanticmodel.Connection{"local_files": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source:     "orders",
				PrimaryKey: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID"},
					"status":   {Label: "Status"},
					"revenue":  {Label: "Revenue"},
				},
			},
		},
		Measures: map[string]semanticmodel.MetricMeasure{
			"revenue": {Fact: "orders", Aggregation: "sum", Input: semanticmodel.MeasureInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindManagedTestRoot(model, root)
	return model
}

func bindManagedTestRoot(model *semanticmodel.Model, root string) {
	for name, connection := range model.Connections {
		if connection.Kind != "managed" {
			continue
		}
		connection.Root = root
		model.Connections[name] = connection
	}
}

func writeOrdersCSV(t *testing.T, dir string, firstRevenue, secondRevenue int) {
	t.Helper()
	content := fmt.Sprintf("order_id,status,revenue\no1,paid,%d\no2,paid,%d\n", firstRevenue, secondRevenue)
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func queryRevenue(t *testing.T, ctx context.Context, runtime *analyticsduckdb.WorkspaceRuntime) float64 {
	t.Helper()
	result, err := runtime.ExecuteDataQuery(ctx, dataquery.Query{
		ModelID: "sales", Kind: dataquery.KindSemanticAggregate,
		Measures: []dataquery.Field{{Field: "revenue", Alias: "revenue"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	rows := result.Rows
	if len(rows) != 1 {
		t.Fatalf("rows = %#v, want one row", rows)
	}
	switch value := rows[0]["revenue"].(type) {
	case int64:
		return float64(value)
	case float64:
		return value
	case *big.Int:
		return float64(value.Int64())
	default:
		t.Fatalf("revenue value = %#v (%T), want numeric", rows[0]["revenue"], rows[0]["revenue"])
		return 0
	}
}

func TestModelTableDependencyOrderIncludesUpstreamBeforeSelected(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "test",
		Tables: map[string]semanticmodel.Table{
			"customers":     {PrimaryKey: "customer_id"},
			"orders":        {PrimaryKey: "order_id"},
			"order_summary": {PrimaryKey: "status", Transform: semanticmodel.Transform{SQL: "SELECT status FROM model.orders"}, ModelDependencies: []string{"orders"}},
			"daily_summary": {PrimaryKey: "day", ModelDependencies: []string{"order_summary", "customers"}},
		},
	}

	order, err := analyticsmaterialize.ModelTableDependencyOrder(model, "daily_summary")
	if err != nil {
		t.Fatalf("dependency order: %v", err)
	}
	want := []string{"orders", "order_summary", "customers", "daily_summary"}
	if !reflect.DeepEqual(order, want) {
		t.Fatalf("dependency order = %#v, want %#v", order, want)
	}
}

func indexOf(values []string, target string) int {
	for index, value := range values {
		if value == target {
			return index
		}
	}
	return -1
}

func TestModelTablesNamedMaterializesOnlyRequestedOrder(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "test",
		Connections: map[string]semanticmodel.Connection{
			"local_files": {Kind: "managed"},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Path: "orders.csv", Format: "csv", Connection: "local_files"},
		},
		Tables: map[string]semanticmodel.Table{
			"orders":        {Source: "orders", PrimaryKey: "order_id"},
			"customers":     {Source: "orders", PrimaryKey: "customer_id"},
			"order_summary": {PrimaryKey: "status", Transform: semanticmodel.Transform{SQL: "SELECT status FROM model.orders"}, ModelDependencies: []string{"orders"}},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	sources := &recordingSourceRegistrar{}
	executor := &recordingStatementsExecutor{}

	if _, err := analyticsmaterialize.RefreshModelTables(context.Background(), executor, sources, model, []string{"orders", "order_summary"}); err != nil {
		t.Fatalf("refresh model tables: %v", err)
	}
	if !reflect.DeepEqual(sources.ops, []string{"plan:orders", "plan:order_summary"}) {
		t.Fatalf("source ops = %#v, want selected tables only", sources.ops)
	}
	if len(executor.statements) != 3 || strings.Contains(strings.Join(executor.statements, "\n"), "customers") {
		t.Fatalf("statements = %#v, want schema plus selected table materializations", executor.statements)
	}
}

type recordingSourceRegistrar struct {
	plan    analyticsmaterialize.ModelTablePlan
	planErr error
	ops     []string
}

func (r *recordingSourceRegistrar) PlanModelTable(_ context.Context, _ *semanticmodel.Model, tableName string, _ semanticmodel.Table) (analyticsmaterialize.ModelTablePlan, error) {
	r.ops = append(r.ops, "plan:"+tableName)
	if r.planErr != nil {
		return analyticsmaterialize.ModelTablePlan{}, r.planErr
	}
	if r.plan.SQL != "" {
		return r.plan, nil
	}
	return analyticsmaterialize.ModelTablePlan{Mode: analyticsmaterialize.PlanModeModelSQL, SQL: "CREATE OR REPLACE TABLE model." + tableName + " AS SELECT 1"}, nil
}

type recordingExecutor struct{}

func (recordingExecutor) Exec(context.Context, string) error {
	return nil
}

type failingExecutor struct{}

func (failingExecutor) Exec(context.Context, string) error {
	return errors.New("exec failed")
}

type recordingStatementsExecutor struct {
	statements []string
}

func (r *recordingStatementsExecutor) Exec(_ context.Context, statement string) error {
	r.statements = append(r.statements, statement)
	return nil
}

func TestValidateFilesIgnoresRemoteSources(t *testing.T) {
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"lake": {Kind: "s3"},
		},
		Sources: map[string]semanticmodel.Source{
			"events": {Format: "parquet", Path: "s3://bucket/events/*.parquet", Connection: "lake"},
		},
	}
	if err := analyticsmaterialize.ValidateFiles(model); err != nil {
		t.Fatalf("validate files = %v, want nil", err)
	}
}

func TestValidateFilesUsesManagedRevisionRoot(t *testing.T) {
	dir := t.TempDir()
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"local_files": {Kind: "managed", Root: filepath.Join(dir, "fixtures")},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Format: "csv", Path: "orders.csv", Connection: "local_files"},
		},
	}
	err := analyticsmaterialize.ValidateFiles(model)
	var missing *analyticsmaterialize.MissingDataError
	if !errors.As(err, &missing) {
		t.Fatalf("validate files error = %v, want MissingDataError", err)
	}
	want := filepath.Join(dir, "fixtures", "orders.csv")
	if len(missing.Missing) != 1 || missing.Missing[0] != want {
		t.Fatalf("missing files = %#v, want %q", missing.Missing, want)
	}
}

func TestRunRepositoryPersistsPrincipalAttribution(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())
	seedMaterializationPrincipal(t, ctx, store, "principal_alice", "alice@example.com", "Alice")

	input := pipelineRunInput("model.orders", "orders")
	input.PrincipalID = "principal_alice"
	queued, err := repo.CreateRun(ctx, input)
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if queued.PrincipalID != "principal_alice" || queued.PrincipalDisplayName != "Alice" {
		t.Fatalf("queued attribution = %#v, want Alice principal", queued)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", queued.ID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}

	stored, err := repo.GetRun(ctx, "test", queued.ID)
	if err != nil {
		t.Fatalf("get run: %v", err)
	}
	if stored.PrincipalID != "principal_alice" || stored.PrincipalDisplayName != "Alice" {
		t.Fatalf("stored attribution = %#v, want Alice principal", stored)
	}
	listed, err := repo.ListTargetRuns(ctx, "test", refreshrun.TargetRefreshPipeline, "test.orders", refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("list model runs: %v", err)
	}
	if len(listed) != 1 || listed[0].PrincipalID != "principal_alice" || listed[0].PrincipalDisplayName != "Alice" {
		t.Fatalf("listed attribution = %#v, want Alice principal", listed)
	}
	latest, ok, err := repo.LatestSuccessfulTargetRun(ctx, "test", "dev", refreshrun.TargetRefreshPipeline, "test.orders")
	if err != nil || !ok || latest.PrincipalDisplayName != "Alice" {
		t.Fatalf("latest attribution = %#v ok=%v err=%v, want Alice", latest, ok, err)
	}

	if _, err := repo.CreateRun(ctx, refreshrun.RunInput{WorkspaceID: "test", ModelID: "model.legacy"}); err == nil {
		t.Fatal("legacy-shaped run was accepted")
	}
}

func TestRunRepositoryAtomicallyAttachesScheduledOccurrence(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())
	scheduledAt := time.Date(2026, 7, 19, 6, 0, 0, 0, time.UTC)
	occurrence := refreshschedule.Occurrence{
		WorkspaceID: "test", Environment: "prod", PipelineID: "orders", ArtifactDigest: "sha256:test", ScheduledAt: scheduledAt,
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
INSERT INTO refresh_pipeline_occurrences (workspace_id, environment, pipeline_id, artifact_digest, scheduled_at)
VALUES (?, ?, ?, ?, ?)`, occurrence.WorkspaceID, occurrence.Environment, occurrence.PipelineID, occurrence.ArtifactDigest, scheduledAt.Format(time.RFC3339Nano)); err != nil {
		t.Fatal(err)
	}
	input := pipelineRunInput("model.orders", "orders")
	input.TriggerType = refreshrun.TriggerSchedule
	run, err := repo.CreateScheduledRun(ctx, input, occurrence)
	if err != nil {
		t.Fatal(err)
	}
	var attached string
	if err := store.SQLDB().QueryRowContext(ctx, `
SELECT COALESCE(run_id, '') FROM refresh_pipeline_occurrences
WHERE workspace_id = ? AND environment = ? AND pipeline_id = ? AND scheduled_at = ?`,
		occurrence.WorkspaceID, occurrence.Environment, occurrence.PipelineID, scheduledAt.Format(time.RFC3339Nano)).Scan(&attached); err != nil {
		t.Fatal(err)
	}
	if attached != run.ID {
		t.Fatalf("attached run = %q, want %q", attached, run.ID)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", run.ID); err != nil {
		t.Fatal(err)
	}

	missing := occurrence
	missing.ScheduledAt = scheduledAt.Add(time.Hour)
	if _, err := repo.CreateScheduledRun(ctx, input, missing); err == nil {
		t.Fatal("CreateScheduledRun() without claimed occurrence succeeded")
	}
	runs, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("runs after rolled-back scheduled create = %#v, want one", runs)
	}
}

func TestRunRepositoryIsolatesActivePipelinesAndHistoryByEnvironment(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())
	devInput := pipelineRunInput("model.orders", "orders")
	dev, err := repo.CreateRun(ctx, devInput)
	if err != nil {
		t.Fatal(err)
	}
	prodInput := pipelineRunInput("model.orders", "orders")
	prodInput.Environment = "prod"
	prod, err := repo.CreateRun(ctx, prodInput)
	if err != nil {
		t.Fatalf("create same active pipeline in another environment: %v", err)
	}
	if dev.Environment != "dev" || prod.Environment != "prod" {
		t.Fatalf("run environments = %q/%q", dev.Environment, prod.Environment)
	}
	devRuns, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 10, Environment: "dev"})
	if err != nil {
		t.Fatal(err)
	}
	prodRuns, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 10, Environment: "prod"})
	if err != nil {
		t.Fatal(err)
	}
	if len(devRuns) != 1 || devRuns[0].ID != dev.ID || len(prodRuns) != 1 || prodRuns[0].ID != prod.ID {
		t.Fatalf("environment histories = dev %#v, prod %#v", devRuns, prodRuns)
	}
}

func TestRunRepositorySupersedesOlderTargetGenerationAtomically(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	first, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "orders"))
	if err != nil {
		t.Fatal(err)
	}
	child, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID: "test", Environment: "dev", ModelID: "model.orders", ServingStateID: first.ServingStateID,
		TargetType: refreshrun.TargetModelTable, TargetID: "test.orders", TargetGeneration: first.TargetGeneration,
		TriggerType: refreshrun.TriggerDependency, ParentRunID: first.ID, JobKind: refreshrun.JobKindChildRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-reused", time.Minute)
	if err != nil || !ok || claimed.RunID != first.ID {
		t.Fatalf("claim = %#v ok=%v err=%v", claimed, ok, err)
	}
	second, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "orders"))
	if err != nil {
		t.Fatal(err)
	}
	if first.TargetGeneration != 1 || second.TargetGeneration != 2 {
		t.Fatalf("generations = %d/%d, want 1/2", first.TargetGeneration, second.TargetGeneration)
	}
	for _, id := range []string{first.ID, child.ID} {
		stored, err := repo.GetRun(ctx, "test", id)
		if err != nil {
			t.Fatal(err)
		}
		if stored.Status != refreshrun.RunStatusSuperseded || stored.FinishedAt == "" {
			t.Fatalf("superseded run %q = %#v", id, stored)
		}
	}
	if err := repo.RenewJobLease(ctx, claimed, time.Minute); !errors.Is(err, refreshrun.ErrLeaseLost) {
		t.Fatalf("stale generation renewal error = %v, want ErrLeaseLost", err)
	}
}

func TestRunRepositoryClaimsOnlyRequestedEnvironment(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())
	prodInput := pipelineRunInput("model.orders", "prod-orders")
	prodInput.Environment = "prod"
	prod, err := repo.CreateRun(ctx, prodInput)
	if err != nil {
		t.Fatal(err)
	}
	dev, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "dev-orders"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE refresh_jobs
		SET queued_at = CASE id
			WHEN (SELECT job_id FROM refresh_job_runs WHERE id = ?) THEN '2026-01-01 00:00:00'
			ELSE '2026-01-02 00:00:00'
		END
	`, prod.ID); err != nil {
		t.Fatal(err)
	}
	job, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-dev", time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim dev job: ok=%v err=%v", ok, err)
	}
	if job.RunID != dev.ID || job.Environment != "dev" {
		t.Fatalf("claimed job = %#v, want dev run %q", job, dev.ID)
	}
}

func TestRunRepositoryCancelsOnlyQueuedRuns(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, workspace_id, status, source, digest, manifest_json, created_by) VALUES ('dep_candidate', 'test', 'validated', 'refresh', 'sha256:test', '{}', 'test')`); err != nil {
		t.Fatal(err)
	}
	input := pipelineRunInput("model.orders", "orders")
	input.ServingStateID = "dep_candidate"
	queued, err := repo.CreateRun(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	child, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID: "test", Environment: "dev", ModelID: "model.orders", ServingStateID: "dep_candidate",
		TargetType: refreshrun.TargetModelTable, TargetID: "test.orders", TriggerType: refreshrun.TriggerDependency,
		ParentRunID: queued.ID, JobKind: refreshrun.JobKindChildRun,
	})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, err := repo.CancelRun(ctx, "test", queued.ID)
	if err != nil || cancelled.Status != refreshrun.RunStatusCancelled || cancelled.FinishedAt == "" {
		t.Fatalf("cancelled = %#v, error = %v", cancelled, err)
	}
	if _, err := repo.CancelRun(ctx, "test", queued.ID); !errors.Is(err, refreshrun.ErrRunNotCancellable) {
		t.Fatalf("second cancellation error = %v", err)
	}
	storedChild, err := repo.GetRun(ctx, "test", child.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedChild.Status != refreshrun.RunStatusCancelled || storedChild.FinishedAt == "" {
		t.Fatalf("child after cancellation = %#v", storedChild)
	}
	var candidateStatus string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT status FROM serving_states WHERE id = 'dep_candidate'`).Scan(&candidateStatus); err != nil {
		t.Fatal(err)
	}
	if candidateStatus != "failed" {
		t.Fatalf("candidate status = %q, want failed", candidateStatus)
	}
}

func TestRunRepositoryPersistsRetryLineageSeparatelyFromChildRuns(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	prior, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "orders"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.MarkRunFailed(ctx, "test", prior.ID, "boom"); err != nil {
		t.Fatal(err)
	}
	retryInput := pipelineRunInput("model.orders", "orders")
	retryInput.TriggerType = refreshrun.TriggerRetry
	retryInput.RetryOf = prior.ID
	retry, err := repo.CreateRun(ctx, retryInput)
	if err != nil {
		t.Fatal(err)
	}
	if retry.RetryOf != prior.ID || retry.ParentRunID != "" {
		t.Fatalf("retry lineage = %#v", retry)
	}
	stored, err := repo.GetRun(ctx, "test", retry.ID)
	if err != nil || stored.RetryOf != prior.ID {
		t.Fatalf("stored retry = %#v err=%v", stored, err)
	}
}

func TestRunRepositoryListsAndFindsLatestByModel(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	ordersSucceeded, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "orders"))
	if err != nil {
		t.Fatalf("create succeeded run: %v", err)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", ordersSucceeded.ID); err != nil {
		t.Fatalf("mark succeeded: %v", err)
	}
	other, err := repo.CreateRun(ctx, pipelineRunInput("model.customers", "customers"))
	if err != nil {
		t.Fatalf("create other run: %v", err)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", other.ID); err != nil {
		t.Fatalf("mark other succeeded: %v", err)
	}
	ordersFailed, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "orders"))
	if err != nil {
		t.Fatalf("create failed run: %v", err)
	}
	if _, err := repo.MarkRunFailed(ctx, "test", ordersFailed.ID, "boom"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}

	runs, err := repo.ListTargetRuns(ctx, "test", refreshrun.TargetRefreshPipeline, "test.orders", refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("list model runs: %v", err)
	}
	if len(runs) != 2 || runs[0].ID != ordersFailed.ID || runs[1].ID != ordersSucceeded.ID {
		t.Fatalf("model runs = %#v, want failed then succeeded orders runs", runs)
	}
	for _, run := range runs {
		if run.ModelID != "model.orders" {
			t.Fatalf("list included wrong model run: %#v", run)
		}
	}

	latest, ok, err := repo.LatestTargetRun(ctx, "test", "dev", refreshrun.TargetRefreshPipeline, "test.orders")
	if err != nil || !ok || latest.ID != ordersFailed.ID {
		t.Fatalf("latest = %#v ok=%v err=%v, want failed latest", latest, ok, err)
	}
	latestSucceeded, ok, err := repo.LatestSuccessfulTargetRun(ctx, "test", "dev", refreshrun.TargetRefreshPipeline, "test.orders")
	if err != nil || !ok || latestSucceeded.ID != ordersSucceeded.ID {
		t.Fatalf("latest succeeded = %#v ok=%v err=%v, want older succeeded", latestSucceeded, ok, err)
	}
}

func TestRunRepositoryPagesRunsInSQLOrder(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	first, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "first"))
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := repo.CreateRun(ctx, pipelineRunInput("model.customers", "second"))
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}
	third, err := repo.CreateRun(ctx, pipelineRunInput("model.orders", "third"))
	if err != nil {
		t.Fatalf("create third run: %v", err)
	}

	pageOne, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if got, want := runIDs(pageOne), []string{third.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first page ids = %#v, want %#v", got, want)
	}
	pageTwo, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 2, After: second.ID})
	if err != nil {
		t.Fatalf("list second page: %v", err)
	}
	if got, want := runIDs(pageTwo), []string{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second page ids = %#v, want %#v", got, want)
	}
	unknown, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 2, After: "matrun_missing"})
	if err != nil {
		t.Fatalf("list unknown cursor: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown cursor page = %#v, want empty", unknown)
	}
}

func TestRunRepositoryPagesTargetRunsInSQLOrder(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	first, err := repo.CreateRun(ctx, pipelineRunInput("olist", "orders"))
	if err != nil {
		t.Fatalf("create first target run: %v", err)
	}
	if _, err := repo.CreateRun(ctx, pipelineRunInput("olist", "customers")); err != nil {
		t.Fatalf("create other target run: %v", err)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", first.ID); err != nil {
		t.Fatal(err)
	}
	second, err := repo.CreateRun(ctx, pipelineRunInput("olist", "orders"))
	if err != nil {
		t.Fatalf("create second target run: %v", err)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", second.ID); err != nil {
		t.Fatal(err)
	}
	third, err := repo.CreateRun(ctx, pipelineRunInput("olist", "orders"))
	if err != nil {
		t.Fatalf("create third target run: %v", err)
	}

	pageOne, err := repo.ListTargetRuns(ctx, "test", refreshrun.TargetRefreshPipeline, "test.orders", refreshrun.RunPage{Limit: 2})
	if err != nil {
		t.Fatalf("list first target page: %v", err)
	}
	if got, want := runIDs(pageOne), []string{third.ID, second.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("first target page ids = %#v, want %#v", got, want)
	}
	pageTwo, err := repo.ListTargetRuns(ctx, "test", refreshrun.TargetRefreshPipeline, "test.orders", refreshrun.RunPage{Limit: 2, After: second.ID})
	if err != nil {
		t.Fatalf("list second target page: %v", err)
	}
	if got, want := runIDs(pageTwo), []string{first.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("second target page ids = %#v, want %#v", got, want)
	}
	unknown, err := repo.ListTargetRuns(ctx, "test", refreshrun.TargetRefreshPipeline, "test.orders", refreshrun.RunPage{Limit: 2, After: "matrun_missing"})
	if err != nil {
		t.Fatalf("list unknown target cursor: %v", err)
	}
	if len(unknown) != 0 {
		t.Fatalf("unknown target cursor page = %#v, want empty", unknown)
	}
}

func TestRunRepositoryPersistsTargetTriggerAndParentRun(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	parent, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "test.olist",
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
		ServingStateID: "dep_1",
	})
	if err != nil {
		t.Fatalf("create parent run: %v", err)
	}
	child, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		TargetType:     refreshrun.TargetModelTable,
		TargetID:       "olist.orders",
		TriggerType:    refreshrun.TriggerDependency,
		ParentRunID:    parent.ID,
		ServingStateID: "dep_1",
		JobKind:        refreshrun.JobKindChildRun,
	})
	if err != nil {
		t.Fatalf("create child run: %v", err)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", child.ID); err != nil {
		t.Fatalf("mark child succeeded: %v", err)
	}

	stored, err := repo.GetRun(ctx, "test", child.ID)
	if err != nil {
		t.Fatalf("get child run: %v", err)
	}
	if stored.TargetType != refreshrun.TargetModelTable || stored.TargetID != "olist.orders" || stored.TriggerType != refreshrun.TriggerDependency || stored.ParentRunID != parent.ID {
		t.Fatalf("child run metadata = %#v", stored)
	}
	tableRuns, err := repo.ListTargetRuns(ctx, "test", refreshrun.TargetModelTable, "olist.orders", refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("list table runs: %v", err)
	}
	if len(tableRuns) != 1 || tableRuns[0].ID != child.ID {
		t.Fatalf("table runs = %#v, want child only", tableRuns)
	}
	modelRuns, err := repo.ListRuns(ctx, "test", refreshrun.RunPage{Limit: 10})
	if err != nil {
		t.Fatalf("list model runs: %v", err)
	}
	if len(modelRuns) != 1 || modelRuns[0].ID != parent.ID {
		t.Fatalf("semantic model runs = %#v, want parent only", modelRuns)
	}
	latest, ok, err := repo.LatestSuccessfulTargetRun(ctx, "test", "dev", refreshrun.TargetModelTable, "olist.orders")
	if err != nil || !ok || latest.ID != child.ID {
		t.Fatalf("latest successful table run = %#v ok=%v err=%v, want child", latest, ok, err)
	}

	if _, err := repo.CreateRun(ctx, refreshrun.RunInput{WorkspaceID: "test", ModelID: "legacy"}); err == nil {
		t.Fatal("legacy-shaped run was accepted")
	}
}

func TestRunRepositoryFailsRunsForTerminalDeployments(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO serving_states (id, workspace_id, environment, status, digest, manifest_json, created_by)
		VALUES ('dep_failed', 'test', 'dev', 'failed', 'sha256:failed', '{}', 'test'),
		       ('dep_prod_failed', 'test', 'prod', 'failed', 'sha256:prod-failed', '{}', 'test')
	`); err != nil {
		t.Fatalf("seed failed deployment: %v", err)
	}

	failedDeploymentRun, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		ServingStateID: "dep_failed",
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "test.orders",
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
	})
	if err != nil {
		t.Fatalf("create terminal deployment run: %v", err)
	}
	if _, err := repo.MarkRunRunning(ctx, "test", failedDeploymentRun.ID); err != nil {
		t.Fatalf("mark terminal deployment run running: %v", err)
	}
	otherEnvironmentRun, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		ServingStateID: "dep_prod_failed",
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "test.prod-orders",
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
	})
	if err != nil {
		t.Fatalf("create other environment run: %v", err)
	}
	if _, err := repo.MarkRunRunning(ctx, "test", otherEnvironmentRun.ID); err != nil {
		t.Fatalf("mark other environment run running: %v", err)
	}
	activeDeploymentRun, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		ServingStateID: "dep_1",
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "test.customers",
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
	})
	if err != nil {
		t.Fatalf("create active deployment run: %v", err)
	}
	if _, err := repo.MarkRunRunning(ctx, "test", activeDeploymentRun.ID); err != nil {
		t.Fatalf("mark active deployment run running: %v", err)
	}

	if err := repo.FailRunsForTerminalServingStates(ctx, "dev", "refresh did not complete"); err != nil {
		t.Fatalf("fail terminal deployment runs: %v", err)
	}

	storedFailed, err := repo.GetRun(ctx, "test", failedDeploymentRun.ID)
	if err != nil {
		t.Fatalf("get failed deployment run: %v", err)
	}
	if storedFailed.Status != refreshrun.RunStatusFailed || storedFailed.Error != "refresh did not complete" || storedFailed.FinishedAt == "" {
		t.Fatalf("failed deployment run = %#v, want failed with message and finish time", storedFailed)
	}
	storedActive, err := repo.GetRun(ctx, "test", activeDeploymentRun.ID)
	if err != nil {
		t.Fatalf("get active deployment run: %v", err)
	}
	if storedActive.Status != refreshrun.RunStatusRunning || storedActive.Error != "" || storedActive.FinishedAt != "" {
		t.Fatalf("active deployment run = %#v, want still running", storedActive)
	}
	storedOther, err := repo.GetRun(ctx, "test", otherEnvironmentRun.ID)
	if err != nil {
		t.Fatalf("get other environment run: %v", err)
	}
	if storedOther.Status != refreshrun.RunStatusRunning {
		t.Fatalf("other environment run status = %q, want running", storedOther.Status)
	}
}

func TestRunRepositoryClaimsExecutableRootJobs(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	parent, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		ServingStateID: "dep_1",
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "test.olist",
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
		PayloadJSON:    `{"pipelineId":"olist"}`,
	})
	if err != nil {
		t.Fatalf("create parent run: %v", err)
	}
	if _, err := repo.CreateRun(ctx, refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        "olist",
		TargetType:     refreshrun.TargetModelTable,
		TargetID:       "olist.orders",
		TriggerType:    refreshrun.TriggerDependency,
		ParentRunID:    parent.ID,
		ServingStateID: "dep_1",
		JobKind:        refreshrun.JobKindChildRun,
	}); err != nil {
		t.Fatalf("create child run: %v", err)
	}

	job, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("claim job: %v", err)
	}
	if !ok {
		t.Fatal("expected queued root job")
	}
	if job.RunID != parent.ID || job.Kind != refreshrun.JobKindRefreshPipeline || job.AttemptCount != 1 {
		t.Fatalf("claimed job = %#v, want parent workspace refresh attempt 1", job)
	}
	stored, err := repo.GetRun(ctx, "test", parent.ID)
	if err != nil {
		t.Fatalf("get parent run: %v", err)
	}
	if stored.Status != refreshrun.RunStatusRunning || stored.StartedAt == "" {
		t.Fatalf("parent run = %#v, want running with start time", stored)
	}
	if _, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-1", time.Minute); err != nil || ok {
		t.Fatalf("second claim ok=%v err=%v, want no child job claimed", ok, err)
	}
}

func TestRunRepositoryReclaimsExpiredJobLease(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	run, err := repo.CreateRun(ctx, pipelineRunInput("olist", "reclaim"))
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	job, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-1", time.Minute)
	if err != nil || !ok {
		t.Fatalf("first claim ok=%v err=%v", ok, err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE refresh_jobs
		SET lease_expires_at = datetime('now', '-1 second')
		WHERE id = ?
	`, job.ID); err != nil {
		t.Fatalf("expire lease: %v", err)
	}

	reclaimed, ok, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-2", time.Minute)
	if err != nil {
		t.Fatalf("reclaim job: %v", err)
	}
	if !ok || reclaimed.ID != job.ID || reclaimed.RunID != run.ID || reclaimed.AttemptCount != 2 {
		t.Fatalf("reclaimed job = %#v ok=%v, want same job attempt 2", reclaimed, ok)
	}
	if err := repo.RenewJobLease(ctx, reclaimed, time.Minute); err != nil {
		t.Fatalf("renew lease: %v", err)
	}
	var owner string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT lease_owner FROM refresh_jobs WHERE id = ?`, reclaimed.ID).Scan(&owner); err != nil {
		t.Fatalf("read lease owner: %v", err)
	}
	if owner != "worker-2" {
		t.Fatalf("lease owner = %q, want worker-2", owner)
	}
}

func TestRunRepositoryReportsDurableQueueStats(t *testing.T) {
	ctx := context.Background()
	store := openMaterializationStore(t, ctx)
	defer store.Close()
	repo := analyticsmaterializesqlite.NewSQLRunRepository(store.SQLDB())

	if _, err := repo.CreateRun(ctx, pipelineRunInput("queued", "queued")); err != nil {
		t.Fatalf("create queued run: %v", err)
	}
	running, err := repo.CreateRun(ctx, pipelineRunInput("running", "running"))
	if err != nil {
		t.Fatalf("create running run: %v", err)
	}
	stale, err := repo.CreateRun(ctx, pipelineRunInput("stale", "stale"))
	if err != nil {
		t.Fatalf("create stale run: %v", err)
	}
	if _, _, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-1", time.Minute); err != nil {
		t.Fatalf("claim queued job: %v", err)
	}
	if _, _, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-1", time.Minute); err != nil {
		t.Fatalf("claim running job: %v", err)
	}
	if _, _, err := repo.ClaimNextExecutableJob(ctx, "dev", "worker-1", time.Minute); err != nil {
		t.Fatalf("claim stale job: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		UPDATE refresh_jobs
		SET lease_expires_at = datetime('now', '-1 second')
		WHERE id = (SELECT job_id FROM refresh_job_runs WHERE id = ?)
	`, stale.ID); err != nil {
		t.Fatalf("expire stale lease: %v", err)
	}
	if _, err := repo.MarkRunSucceeded(ctx, "test", running.ID); err != nil {
		t.Fatalf("finish one running run: %v", err)
	}

	stats, err := repo.JobQueueStats(ctx, "dev")
	if err != nil {
		t.Fatalf("queue stats: %v", err)
	}
	if stats.QueuedJobs != 0 || stats.RunningJobs != 1 || stats.StaleLeasedJobs != 1 {
		t.Fatalf("queue stats = %#v, want 0 queued, 1 running, 1 stale", stats)
	}
}

func runIDs(runs []refreshrun.RunRecord) []string {
	ids := make([]string, 0, len(runs))
	for _, run := range runs {
		ids = append(ids, run.ID)
	}
	return ids
}

func pipelineRunInput(modelID, pipelineID string) refreshrun.RunInput {
	return refreshrun.RunInput{
		WorkspaceID:    "test",
		ModelID:        modelID,
		ServingStateID: "dep_1",
		TargetType:     refreshrun.TargetRefreshPipeline,
		TargetID:       "test." + pipelineID,
		TriggerType:    refreshrun.TriggerManual,
		JobKind:        refreshrun.JobKindRefreshPipeline,
	}
}

func writeFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func openMaterializationStore(t *testing.T, ctx context.Context) *platform.Store {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO serving_states (id, workspace_id, status, digest, manifest_json, created_by)
		VALUES ('dep_1', 'test', 'active', 'sha256:test', '{}', 'test')
	`); err != nil {
		t.Fatalf("seed deployment: %v", err)
	}
	return store
}

func seedMaterializationPrincipal(t *testing.T, ctx context.Context, store *platform.Store, id, email, displayName string) {
	t.Helper()
	if _, err := store.SQLDB().ExecContext(ctx, `
		INSERT INTO principals (id, email, display_name)
		VALUES (?, ?, ?)
	`, id, email, displayName); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
}
