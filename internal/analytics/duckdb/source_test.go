package duckdb

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/connectionbinding"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	extensionsupply "github.com/flidai/leapview/internal/deployment/extensionsupply"
	"github.com/flidai/leapview/internal/extension"
	"github.com/flidai/leapview/internal/workload"
	"github.com/stretchr/testify/require"
)

type duckdbTestExtensionAdmission struct {
	admitted map[string]extension.AdmittedExtension
}

func testCSVEffectiveOptions() map[string]any {
	return map[string]any{"header": true, "delimiter": ",", "quote": `"`, "escape": `"`}
}

var _ extension.Admission = duckdbTestExtensionAdmission{}
var _ extension.Preparation = duckdbTestExtensionAdmission{}

func (a duckdbTestExtensionAdmission) AdmitExtension(ctx context.Context, name string) (extension.AdmittedExtension, error) {
	if err := ctx.Err(); err != nil {
		return extension.AdmittedExtension{}, err
	}
	admitted, ok := a.admitted[name]
	if !ok {
		return extension.AdmittedExtension{}, fmt.Errorf("test extension %q was not admitted", name)
	}
	return admitted, nil
}

func (a duckdbTestExtensionAdmission) PrepareExtensions(ctx context.Context, names []string) ([]extension.Evidence, error) {
	evidence := make([]extension.Evidence, 0, len(names))
	for _, name := range names {
		admitted, err := a.AdmitExtension(ctx, name)
		if err != nil {
			return nil, err
		}
		evidence = append(evidence, admitted.Evidence())
	}
	return evidence, nil
}

func newDuckDBTestExtensionAdmission(t *testing.T, names ...string) extension.Admission {
	t.Helper()
	version, platform := duckdbTestRuntimeTarget(t)
	setupRoot := t.TempDir()
	admitted := make(map[string]extension.AdmittedExtension, len(names))
	for _, name := range names {
		sourcePath := findDuckDBTestExtension(name, version, platform)
		if sourcePath == "" {
			installDuckDBTestExtension(t, name, setupRoot)
			sourcePath = findDuckDBTestExtensionInRoot(setupRoot, name, version, platform)
		}
		if sourcePath == "" {
			t.Fatalf("reviewed local test extension %q is unavailable for DuckDB %s/%s", name, version, platform)
		}
		contents, err := os.ReadFile(sourcePath)
		if err != nil {
			t.Fatalf("read test extension %q: %v", name, err)
		}
		digest := sha256.Sum256(contents)
		digestValue := "sha256:" + hex.EncodeToString(digest[:])
		ownedPath := filepath.Join(setupRoot, name+".duckdb_extension")
		if err := os.WriteFile(ownedPath, contents, 0o600); err != nil {
			t.Fatalf("stage test extension %q: %v", name, err)
		}
		identity := extension.Identity{DuckDBVersion: version, ExtensionVersion: "test-fixture", GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, Name: name, Digest: digestValue, SupportProfile: "test-fixture"}
		canonical, err := identity.Canonical()
		if err != nil {
			t.Fatalf("canonicalize test extension %q: %v", name, err)
		}
		admitted[name] = extension.AdmittedExtension{Name: name, Identity: canonical, Version: "test-fixture", ExtensionVersion: "test-fixture", DuckDBVersion: version, GOOS: runtime.GOOS, GOARCH: runtime.GOARCH, Platform: platform, SupportProfile: "test-fixture", Digest: digestValue, Path: ownedPath, Origin: "reviewed-local-test-fixture", Provenance: "attest:duckdb-test", Signature: "sig:duckdb-test"}
	}
	return duckdbTestExtensionAdmission{admitted: admitted}
}

func duckdbTestRuntimeTarget(t *testing.T) (string, string) {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open DuckDB runtime probe: %v", err)
	}
	defer db.Close()
	var version, platform string
	if err := db.QueryRowContext(t.Context(), "SELECT version()").Scan(&version); err != nil {
		t.Fatalf("read DuckDB runtime version: %v", err)
	}
	if err := db.QueryRowContext(t.Context(), "PRAGMA platform").Scan(&platform); err != nil {
		t.Fatalf("read DuckDB runtime platform: %v", err)
	}
	if version != extensionsupply.CurrentDuckDBVersion {
		t.Fatalf("DuckDB runtime = %q, want pinned %q", version, extensionsupply.CurrentDuckDBVersion)
	}
	return strings.TrimSpace(version), strings.TrimSpace(platform)
}

func findDuckDBTestExtension(name, version, platform string) string {
	if configured := strings.TrimSpace(os.Getenv("DUCKDB_EXTENSION_DIRECTORY")); configured != "" {
		return findDuckDBTestExtensionInRoot(configured, name, version, platform)
	}
	if home, err := os.UserHomeDir(); err == nil {
		return findDuckDBTestExtensionInRoot(filepath.Join(home, ".duckdb", "extensions"), name, version, platform)
	}
	return ""
}

func findDuckDBTestExtensionInRoot(root, name, version, platform string) string {
	filename := name + ".duckdb_extension"
	platformDir := strings.ReplaceAll(platform, "-", "_")
	for _, path := range []string{filepath.Join(root, version, platformDir, filename), filepath.Join(root, version, platform, filename)} {
		if info, err := os.Lstat(path); err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			return filepath.Clean(path)
		}
	}
	return ""
}

func installDuckDBTestExtension(t *testing.T, name, root string) {
	t.Helper()
	db, err := sql.Open("duckdb", ":memory:")
	if err != nil {
		t.Fatalf("open test extension installer: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("SET extension_directory = '" + strings.ReplaceAll(root, "'", "''") + "'"); err != nil {
		t.Fatalf("set test extension directory: %v", err)
	}
	if _, err := db.Exec("INSTALL " + name + " FROM core"); err != nil {
		t.Fatalf("install test extension %q: %v", name, err)
	}
}

func TestDiscoverSchemasCapturesSourceAndModelColumns(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("order_id,revenue\n1,10.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name:              "olist",
		DefaultConnection: "local",
		Connections:       map[string]semanticmodel.Connection{"local": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{"orders": {
			Connection:       "local",
			Path:             "orders.csv",
			Format:           "csv",
			EffectiveOptions: testCSVEffectiveOptions(),
			SchemaMode:       "compatible",
			Fields: map[string]semanticmodel.SourceField{
				"order_id": {Description: "Raw order identifier."},
			},
		}},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source: "orders", ModelName: "sales_orders",
				Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeInteger},
					"revenue":  {Label: "Revenue", Datatype: semanticmodel.DataTypeFloat},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "sales_orders"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindTestManagedRoot(model, "local", dir)
	_, _, _ = openSchemaTestRuntime(t, ctx, dir, model)
	if got := model.Sources["orders"].Schema.Columns; len(got) != 2 || got[0].Name != "order_id" || got[0].Ordinal != 1 {
		t.Fatalf("source schema = %#v, want ordered source columns", got)
	}
	if got := model.Sources["orders"].Fields["order_id"].Description; got != "Raw order identifier." {
		t.Fatalf("source field description = %q, want docs preserved", got)
	}
	columns := model.Tables["orders"].Schema.Columns
	if len(columns) != 2 {
		t.Fatalf("model schema column count = %d, want 2: %#v", len(columns), columns)
	}
	if columns[0].Name != "order_id" || columns[0].PhysicalType == "" || columns[0].Nullable == nil {
		t.Fatalf("model order_id column = %#v, want physical type", columns[0])
	}
	if columns[1].Name != "revenue" || columns[1].PhysicalType == "" || columns[1].Nullable == nil {
		t.Fatalf("model revenue column = %#v, want physical type", columns[1])
	}
}

func TestSnapshotRuntimeDiscoversDistinctSemanticDatasetAliasSchemas(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "customers.csv"), []byte("customer_id,customer_state\nc1,SP\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	newModel := func() *semanticmodel.Model {
		model := &semanticmodel.Model{
			Name:              "operations",
			DefaultConnection: "olist",
			Connections:       map[string]semanticmodel.Connection{"olist": {Kind: "managed"}},
			Sources: map[string]semanticmodel.Source{"olist_customers": {
				Connection: "olist", Path: "customers.csv", Format: "csv",
				EffectiveOptions: testCSVEffectiveOptions(),
				SchemaMode:       "compatible",
				Fields: map[string]semanticmodel.SourceField{
					"customer_id":    {Description: "Customer identifier."},
					"customer_state": {Description: "Customer state."},
				},
			}},
			Tables: map[string]semanticmodel.Table{"operations_customers": {
				ModelName: "olist_customers", Sources: []string{"olist_customers"},
				Transform:           semanticmodel.Transform{SQL: "SELECT customer_id, customer_state AS state FROM source.olist_customers"},
				SQLAnalysisEvidence: &semanticmodel.SQLAnalysisEvidence{Validated: true, SourceRefs: []string{"olist_customers"}},
				Entities:            map[string]semanticmodel.ModelEntitySpec{"customer": {Type: "primary", Fields: []string{"customer_id"}}},
				GrainEntity:         "customer",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Datatype: semanticmodel.DataTypeString},
					"state":       {Datatype: semanticmodel.DataTypeString},
				},
			}},
			Datasets: map[string]semanticmodel.SemanticDatasetSpec{"operations_customers": {Model: "olist_customers"}},
		}
		require.NoError(t, model.ValidateAuthored())
		bindTestManagedRoot(model, "olist", dir)
		return model
	}
	firstModel := newModel()
	admission := newDuckDBTestExtensionAdmission(t, "ducklake")
	environment, err := analyticsducklake.Open(ctx, analyticsducklake.Config{RootDir: filepath.Join(dir, "ducklake"), MaxConnections: 2, ExtensionAdmission: admission})
	require.NoError(t, err)
	t.Cleanup(func() { _ = environment.Close() })
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	t.Cleanup(func() { controller.Close() })
	lease, err := controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "alias-schema-test", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	t.Cleanup(func() { lease.Release() })
	first, err := OpenProjectMaterializeRuntime(lease.Context(), ProjectRuntimeConfig{
		ProjectID: "test", Models: map[string]*semanticmodel.Model{"semantic-model:operations": firstModel}, Database: environment,
		ExtensionAdmission: admission,
	})
	require.NoError(t, err)
	snapshotID := first.DuckLakeSnapshotID()
	require.Positive(t, snapshotID)
	require.NoError(t, first.Close())

	configureSnapshotSource := func(model *semanticmodel.Model) {
		// A reopened serving generation may retain no source credentials or local
		// source files. Keep the authored source external and make any attempted
		// connection resolution fail; snapshot activation must only inspect model.*.
		model.Connections["olist"] = semanticmodel.Connection{Kind: "postgres"}
		external := model.Sources["olist_customers"]
		external.Path = "customers.csv"
		external.Object = "customers"
		model.Sources["olist_customers"] = external
		model.Connections["unavailable"] = semanticmodel.Connection{Kind: "managed", Root: filepath.Join(dir, "unavailable-after-refresh")}
		model.Sources["unavailable_local"] = semanticmodel.Source{Connection: "unavailable", Path: "missing.csv", Format: "csv"}
	}
	secondModel := newModel()
	authoredSecondModel := newModel()
	configureSnapshotSource(secondModel)
	configureSnapshotSource(authoredSecondModel)
	resolver := &snapshotRejectingConnectionResolver{}
	// No database lease is held here: the snapshot opener must acquire its own
	// admitted lease from the workload context before DESCRIBE.
	second, err := OpenProjectMaterializeRuntime(lease.Context(), ProjectRuntimeConfig{
		ProjectID: "test", Models: map[string]*semanticmodel.Model{"semantic-model:operations": secondModel}, Database: environment,
		SnapshotID: snapshotID, SkipInitialRefresh: true, ConnectionResolver: resolver, ExtensionAdmission: admission,
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = second.Close() })
	if err := second.VerifySemantic(lease.Context(), "semantic-model:operations"); err != nil {
		t.Fatalf("snapshot verification with distinct dataset/model/source aliases: %v", err)
	}
	planner, ok := second.Planner("semantic-model:operations")
	require.True(t, ok)
	require.True(t, planner.CompiledModel().MatchesModel(authoredSecondModel), "snapshot discovery changed authored planner fingerprint")
	require.Zero(t, resolver.calls.Load(), "snapshot activation reacquired external source")

	badModel := newModel()
	badTable := badModel.Tables["operations_customers"]
	badTable.ModelName = "missing_snapshot_table"
	badModel.Tables["operations_customers"] = badTable
	badModel.Datasets["operations_customers"] = semanticmodel.SemanticDatasetSpec{Model: "missing_snapshot_table"}
	cachePool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 2, RuntimeBytes: 1024, NodeEntries: 2, NodeBytes: 1024})
	require.NoError(t, err)
	t.Cleanup(func() { _ = cachePool.Close() })
	cacheScope, err := cachePool.OpenScope(resultcache.ScopeID{RuntimeID: "snapshot-discovery-failure"})
	require.NoError(t, err)
	_, err = OpenProjectMaterializeRuntime(lease.Context(), ProjectRuntimeConfig{
		ProjectID: "test", Models: map[string]*semanticmodel.Model{"semantic-model:operations": badModel}, Database: environment,
		SnapshotID: snapshotID, SkipInitialRefresh: true, QueryCache: cacheScope, ExtensionAdmission: admission,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "snapshot schema discovery")
	// Discovery runs after planner/view construction. A failure must close the
	// partially built ProjectRuntime, including its generation cache scope.
	reopened, reopenErr := cachePool.OpenScope(resultcache.ScopeID{RuntimeID: "snapshot-discovery-failure"})
	require.NoError(t, reopenErr, "post-construction discovery failure leaked cache scope")
	reopened.Close()
}

type snapshotRejectingConnectionResolver struct {
	calls atomic.Int32
}

func (r *snapshotRejectingConnectionResolver) Resolve(context.Context, string, semanticmodel.Connection) (semanticmodel.Connection, error) {
	r.calls.Add(1)
	return semanticmodel.Connection{}, fmt.Errorf("external source must not be reacquired during snapshot activation")
}

func TestPhysicalProjectModelDeduplicatesDatasetAliases(t *testing.T) {
	table := semanticmodel.Table{
		Source: "orders",
		Entities: map[string]semanticmodel.ModelEntitySpec{
			"order_id": {Type: "primary", Fields: []string{"order_id"}},
		},
		GrainEntity: "order_id",
	}
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    func() semanticmodel.Table { table.ModelName = "sales_orders"; return table }(),
			"purchases": func() semanticmodel.Table { table.ModelName = "sales_orders"; return table }(),
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":    {Model: "sales_orders"},
			"purchases": {Model: "sales_orders"},
		},
	}
	physical, err := physicalProjectModel(map[string]*semanticmodel.Model{"sales": model})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(physical.Tables); got != 1 {
		t.Fatalf("physical table count = %d, want one shared Model table", got)
	}
	if _, ok := physical.Tables["sales_orders"]; !ok {
		t.Fatalf("physical tables = %#v, want sales_orders", physical.Tables)
	}
	order, err := ProjectModelTableDependencyOrder(map[string]*semanticmodel.Model{"sales": model}, "orders")
	if err != nil {
		t.Fatalf("dependency order by semantic alias: %v", err)
	}
	if len(order) != 1 || order[0] != "sales_orders" {
		t.Fatalf("dependency order = %#v, want sales_orders", order)
	}
}

func TestPhysicalProjectModelPreservesCanonicalPhysicalDependencies(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":  {ModelName: "sales_orders", Source: "orders", ModelDependencies: nil},
			"summary": {ModelName: "sales_summary", Transform: semanticmodel.Transform{SQL: "SELECT * FROM model.sales_orders"}, ModelDependencies: []string{"sales_orders"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders":  {Model: "sales_orders"},
			"summary": {Model: "sales_summary"},
		},
	}
	physical, err := physicalProjectModel(map[string]*semanticmodel.Model{"sales": model})
	if err != nil {
		t.Fatal(err)
	}
	if got := physical.Tables["sales_summary"].ModelDependencies; len(got) != 1 || got[0] != "sales_orders" {
		t.Fatalf("physical dependencies = %#v, want sales_orders", got)
	}
}

func TestPhysicalProjectModelResolvesDistinctAliasPhysicalDependencies(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders_alias":  {ModelName: "sales_orders", Source: "orders"},
			"summary_alias": {ModelName: "sales_summary", Transform: semanticmodel.Transform{SQL: "SELECT * FROM model.sales_orders"}, ModelDependencies: []string{"sales_orders"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders_alias":  {Model: "sales_orders"},
			"summary_alias": {Model: "sales_summary"},
		},
	}
	physical, err := physicalProjectModel(map[string]*semanticmodel.Model{"sales": model})
	if err != nil {
		t.Fatalf("physicalProjectModel() error = %v", err)
	}
	if got := physical.Tables["sales_summary"].ModelDependencies; len(got) != 1 || got[0] != "sales_orders" {
		t.Fatalf("physical dependencies = %#v, want sales_orders", got)
	}
	order, err := ProjectModelTableDependencyOrder(map[string]*semanticmodel.Model{"sales": model}, "summary_alias")
	if err != nil {
		t.Fatalf("ProjectModelTableDependencyOrder() error = %v", err)
	}
	if want := []string{"sales_orders", "sales_summary"}; !reflect.DeepEqual(order, want) {
		t.Fatalf("dependency order = %#v, want %#v", order, want)
	}
}

func TestPhysicalProjectModelRejectsDatasetAliasAsPhysicalDependency(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"sales_orders":  {ModelName: "sales_summary"},
			"orders_alias":  {ModelName: "orders_physical"},
			"derived_alias": {ModelName: "sales_derived", Transform: semanticmodel.Transform{SQL: "SELECT * FROM model.sales_orders"}, ModelDependencies: []string{"sales_orders"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"sales_orders":  {Model: "sales_summary"},
			"orders_alias":  {Model: "orders_physical"},
			"derived_alias": {Model: "sales_derived"},
		},
	}
	if _, err := physicalProjectModel(map[string]*semanticmodel.Model{"sales": model}); err == nil || !strings.Contains(err.Error(), "unknown model dependency") {
		t.Fatalf("physicalProjectModel() error = %v, want unknown physical dependency", err)
	}
}

func TestDiscoverSchemasIgnoresAttachedDatabaseSchemas(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("order_id,revenue\n1,10.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name:              "olist",
		DefaultConnection: "local",
		Connections:       map[string]semanticmodel.Connection{"local": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{"orders": {
			Connection:       "local",
			Path:             "orders.csv",
			Format:           "csv",
			EffectiveOptions: testCSVEffectiveOptions(),
			SchemaMode:       "inferred",
		}},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source: "orders", ModelName: "orders",
				Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeInteger},
					"revenue":  {Label: "Revenue", Datatype: semanticmodel.DataTypeFloat},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics:  map[string]semanticmodel.Metric{},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindTestManagedRoot(model, "local", dir)
	leaseCtx, db, _ := openSchemaTestRuntime(t, ctx, dir, model)
	session, err := db.Session(leaseCtx)
	require.NoError(t, err)
	if _, err := session.ExecContext(leaseCtx, `
ATTACH ':memory:' AS attached_catalog;
CREATE SCHEMA attached_catalog.source;
CREATE TABLE attached_catalog.source.orders (attached_only INTEGER);
CREATE SCHEMA attached_catalog.model;
CREATE TABLE attached_catalog.model.orders (attached_only INTEGER);`); err != nil {
		t.Fatal(err)
	}
	if err := discoverSchemas(leaseCtx, db, model); err != nil {
		t.Fatal(err)
	}
	sourceColumns := model.Sources["orders"].Schema.Columns
	for _, column := range sourceColumns {
		if column.Name == "attached_only" {
			t.Fatalf("source schema included attached database column: %#v", sourceColumns)
		}
	}
	tableColumns := model.Tables["orders"].Schema.Columns
	for _, column := range tableColumns {
		if column.Name == "attached_only" {
			t.Fatalf("model schema included attached database column: %#v", tableColumns)
		}
	}
}

func TestDiscoverSchemasRejectsMissingDocumentedSourceField(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "orders.csv"), []byte("order_id,revenue\n1,10.5\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name:              "olist",
		DefaultConnection: "local",
		Connections:       map[string]semanticmodel.Connection{"local": {Kind: "managed"}},
		Sources: map[string]semanticmodel.Source{"orders": {
			Connection:       "local",
			Path:             "orders.csv",
			Format:           "csv",
			EffectiveOptions: testCSVEffectiveOptions(),
			SchemaMode:       "compatible",
			Fields: map[string]semanticmodel.SourceField{
				"missing": {Description: "Missing source field."},
			},
		}},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source: "orders", ModelName: "orders",
				Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Label: "Order ID", Datatype: semanticmodel.DataTypeInteger},
					"revenue":  {Label: "Revenue", Datatype: semanticmodel.DataTypeFloat},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindTestManagedRoot(model, "local", dir)
	_, err := openSchemaTestRuntimeExpectError(t, ctx, dir, model)
	if err == nil || !strings.Contains(err.Error(), `source "orders" field "missing" is not in discovered schema`) {
		t.Fatalf("DiscoverSchemas() error = %v, want missing source field validation", err)
	}
}

func openSchemaTestRuntime(t *testing.T, ctx context.Context, dir string, model *semanticmodel.Model) (context.Context, *analyticsducklake.Environment, *ProjectRuntime) {
	t.Helper()
	admission := newDuckDBTestExtensionAdmission(t, "ducklake")
	environment, err := analyticsducklake.Open(ctx, analyticsducklake.Config{RootDir: filepath.Join(dir, "ducklake"), MaxConnections: 2, ExtensionAdmission: admission})
	require.NoError(t, err)
	controller, err := workload.New(workload.DefaultConfig())
	require.NoError(t, err)
	lease, err := controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "schema-test", EstimatedMemoryBytes: 1})
	require.NoError(t, err)
	runtime, err := OpenProjectMaterializeRuntime(lease.Context(), ProjectRuntimeConfig{ProjectID: "test", Models: map[string]*semanticmodel.Model{"test": model}, Database: environment, ExtensionAdmission: admission})
	if err != nil {
		lease.Release()
		controller.Close()
		_ = environment.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = runtime.Close()
		lease.Release()
		controller.Close()
		_ = environment.Close()
	})
	analyticalLease, err := environment.Acquire(lease.Context())
	require.NoError(t, err)
	t.Cleanup(analyticalLease.Release)
	return analyticalLease.Context(), environment, runtime
}

func openSchemaTestRuntimeExpectError(t *testing.T, ctx context.Context, dir string, model *semanticmodel.Model) (*ProjectRuntime, error) {
	t.Helper()
	admission := newDuckDBTestExtensionAdmission(t, "ducklake")
	environment, err := analyticsducklake.Open(ctx, analyticsducklake.Config{RootDir: filepath.Join(dir, "ducklake"), MaxConnections: 2, ExtensionAdmission: admission})
	if err != nil {
		return nil, err
	}
	defer environment.Close()
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		return nil, err
	}
	defer controller.Close()
	lease, err := controller.Acquire(ctx, workload.Request{Class: workload.Refresh, PrincipalID: "test", Operation: "schema-test", EstimatedMemoryBytes: 1})
	if err != nil {
		return nil, err
	}
	defer lease.Release()
	return OpenProjectMaterializeRuntime(lease.Context(), ProjectRuntimeConfig{ProjectID: "test", Models: map[string]*semanticmodel.Model{"test": model}, Database: environment, ExtensionAdmission: admission})
}

func TestCompileSourceRelation(t *testing.T) {
	cases := map[string]struct {
		plan sourcePlan
		want string
	}{
		"csv": {
			plan: sourcePlan{kind: "path", format: "csv", path: "/data/orders.csv", options: map[string]any{"header": true, "sample_size": 1000}},
			want: "SELECT * FROM read_csv('/data/orders.csv', header = true, sample_size = 1000)",
		},
		"json": {
			plan: sourcePlan{kind: "path", format: "json", path: "/data/orders.json", options: map[string]any{"format": "array"}},
			want: "SELECT * FROM read_json('/data/orders.json', format = 'array')",
		},
		"parquet": {
			plan: sourcePlan{kind: "path", format: "parquet", path: "s3://bucket/orders/*.parquet", options: map[string]any{"union_by_name": true}},
			want: "SELECT * FROM read_parquet('s3://bucket/orders/*.parquet', union_by_name = true)",
		},
		"excel": {
			plan: sourcePlan{kind: "path", format: "excel", path: "/data/budget.xlsx", options: map[string]any{"sheet": "FY2026"}},
			want: "SELECT * FROM read_xlsx('/data/budget.xlsx', sheet = 'FY2026')",
		},
		"text": {
			plan: sourcePlan{kind: "path", format: "text", path: "/data/readme.txt"},
			want: "SELECT * FROM read_text('/data/readme.txt')",
		},
		"blob": {
			plan: sourcePlan{kind: "path", format: "blob", path: "/data/archive.blob"},
			want: "SELECT * FROM read_blob('/data/archive.blob')",
		},
		"vortex": {
			plan: sourcePlan{kind: "path", format: "vortex", path: "/data/orders.vortex"},
			want: "SELECT * FROM read_vortex('/data/orders.vortex')",
		},
		"lance": {
			plan: sourcePlan{kind: "path", format: "lance", path: "s3://bucket/vectors/products.lance"},
			want: "SELECT * FROM 's3://bucket/vectors/products.lance'",
		},
		"delta": {
			plan: sourcePlan{kind: "path", format: "delta", path: "az://warehouse/orders"},
			want: "SELECT * FROM delta_scan('az://warehouse/orders')",
		},
		"iceberg": {
			plan: sourcePlan{kind: "path", format: "iceberg", path: "s3://warehouse/orders/metadata/v1.metadata.json", options: map[string]any{"allow_moved_paths": true}},
			want: "SELECT * FROM iceberg_scan('s3://warehouse/orders/metadata/v1.metadata.json', allow_moved_paths = true)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			relation, err := compileSourceRelation(tc.plan)
			require.NoError(t, err)
			if relation != tc.want {
				t.Fatalf("relation = %q, want %q", relation, tc.want)
			}
		})
	}

	relation, err := compileSourceRelation(sourcePlan{
		kind:           "object",
		connection:     "crm",
		connectionSpec: semanticmodel.ConnectionSpec{ObjectRelation: semanticmodel.ObjectRelationAttach},
		object:         "public.accounts",
	})
	require.NoError(t, err)
	if want := "SELECT * FROM conn_crm.public.accounts"; relation != want {
		t.Fatalf("object relation = %q, want %q", relation, want)
	}

	relation, err = compileSourceRelation(sourcePlan{
		kind:   "path",
		format: "csv",
		path:   "/data/orders.csv",
		columns: []sourceReadColumn{
			{SourceField: "raw_order_id", OutputField: "order_id"},
			{SourceField: "gross_revenue", OutputField: "revenue"},
		},
	})
	require.NoError(t, err)
	want := "SELECT raw_order_id AS order_id, gross_revenue AS revenue FROM read_csv('/data/orders.csv')"
	if relation != want {
		t.Fatalf("projected column relation = %q, want %q", relation, want)
	}

	_, err = compileSourceRelation(sourcePlan{kind: "path", format: "csv", path: "/data/orders.csv", options: map[string]any{"bad-key": true}})
	if err == nil || !strings.Contains(err.Error(), "invalid source option") {
		t.Fatalf("invalid option error = %v, want invalid source option", err)
	}

	_, err = compileSourceRelation(sourcePlan{kind: "path", format: "lance", path: "/data/products.lance", options: map[string]any{"sample_size": 1000}})
	if err == nil || !strings.Contains(err.Error(), "lance source cannot set options") {
		t.Fatalf("lance option error = %v, want lance option rejection", err)
	}
}

func TestRefreshCredentialResolutionUsesUniqueEphemeralConnectionNames(t *testing.T) {
	model := &semanticmodel.Model{
		DefaultConnection: "crm",
		Connections:       map[string]semanticmodel.Connection{"crm": {Kind: "postgres", Credentials: semanticmodel.ConnectionCredentials{Provider: "env", Secret: "CRM"}}},
		Sources:           map[string]semanticmodel.Source{"accounts": {Connection: "crm", Object: "accounts"}},
	}
	runtime := NewSourceRuntimeWithCredentials(nil, staticCredentialResolver{auth: semanticmodel.ConnectionAuth{"password": "secret"}})
	first, err := runtime.resolveCredentials(context.Background(), model)
	require.NoError(t, err)
	second, err := runtime.resolveCredentials(context.Background(), model)
	require.NoError(t, err)
	if first.DefaultConnection == second.DefaultConnection || first.Sources["accounts"].Connection == second.Sources["accounts"].Connection {
		t.Fatalf("refresh connection names were reused: %q %q", first.DefaultConnection, second.DefaultConnection)
	}
	if len(model.Connections["crm"].Auth) != 0 {
		t.Fatal("resolved credentials leaked into the compiled model")
	}
}

func TestRefreshConnectionResolutionUsesTargetOwnedEndpointAndCredentials(t *testing.T) {
	model := &semanticmodel.Model{
		DefaultConnection: "crm",
		Connections: map[string]semanticmodel.Connection{
			"crm": {Kind: "postgres", Host: "artifact-host"},
		},
		Sources: map[string]semanticmodel.Source{
			"accounts": {Connection: "crm", Object: "accounts"},
		},
	}
	runtime := NewSourceRuntimeWithConnectionResolver(nil, staticConnectionResolver{
		connection: semanticmodel.Connection{
			Kind: "postgres", Host: "target-host", Database: "analytics",
			Auth: semanticmodel.ConnectionAuth{"password": "target-secret"},
		},
	})
	resolved, err := runtime.resolveCredentials(t.Context(), model)
	require.NoError(t, err)
	connection := resolved.Connections[resolved.DefaultConnection]
	if connection.Host != "target-host" || connection.Database != "analytics" ||
		connection.Auth["password"] != "target-secret" {
		t.Fatalf("resolved connection = %#v", connection)
	}
	if model.Connections["crm"].Host != "artifact-host" ||
		len(model.Connections["crm"].Auth) != 0 {
		t.Fatalf("target connection leaked into compiled model: %#v", model.Connections["crm"])
	}
}

func TestRefreshConnectionResolutionUsesTargetOwnedScopeForPublicConnection(t *testing.T) {
	model := &semanticmodel.Model{
		DefaultConnection: "files",
		Connections: map[string]semanticmodel.Connection{
			"files": {Kind: "s3", Access: semanticmodel.ConnectionAccessPublic},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Connection: "files", Path: "orders.csv", Format: "csv", EffectiveOptions: map[string]any{}},
		},
	}
	runtime := NewSourceRuntimeWithConnectionResolver(nil, staticConnectionResolver{
		connection: semanticmodel.Connection{Kind: "s3", Access: semanticmodel.ConnectionAccessPublic, Scope: "s3://public-target/"},
	})
	resolved, err := runtime.resolveCredentials(t.Context(), model)
	require.NoError(t, err)
	connection := resolved.Connections[resolved.DefaultConnection]
	if connection.Scope != "s3://public-target/" || connection.Access != semanticmodel.ConnectionAccessPublic || len(connection.Auth) != 0 {
		t.Fatalf("resolved public target connection = %#v, want target scope and no auth", connection)
	}
}

func TestRefreshConnectionResolutionKeepsTrustedManagedDataRoot(t *testing.T) {
	model := &semanticmodel.Model{
		DefaultConnection: "olist",
		Connections: map[string]semanticmodel.Connection{
			"olist": {Kind: "managed", Root: "/managed/olist"},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {
				Connection: "olist", Path: "orders.parquet", Format: "parquet",
			},
		},
	}
	runtime := NewSourceRuntimeWithConnectionResolver(
		nil,
		rejectingConnectionResolver{},
	)
	resolved, err := runtime.resolveCredentials(t.Context(), model)
	require.NoError(t, err)
	connection := resolved.Connections[resolved.DefaultConnection]
	if connection.Kind != "managed" ||
		connection.Root != "/managed/olist" ||
		len(connection.Auth) != 0 {
		t.Fatalf("resolved managed connection = %#v", connection)
	}
}

func TestSecretScopeLockReportsOnlySameScopeContention(t *testing.T) {
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{"source": {Kind: "s3", Scope: "s3://bucket/prefix/"}},
		Sources:     map[string]semanticmodel.Source{"orders": {Connection: "source"}},
	}
	observer := &recordingRefreshTelemetry{}
	releaseFirst := lockSourceScopes(model, observer)
	acquired := make(chan func(), 1)
	go func() { acquired <- lockSourceScopes(model, observer) }()
	deadline := time.After(time.Second)
	for observer.contentions.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("same-scope waiter did not report contention")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	releaseFirst()
	releaseSecond := <-acquired
	releaseSecond()
	if observer.contentions.Load() != 1 {
		t.Fatalf("contention observations = %d, want 1", observer.contentions.Load())
	}
}

type staticCredentialResolver struct{ auth semanticmodel.ConnectionAuth }

func (r staticCredentialResolver) Resolve(context.Context, string, semanticmodel.Connection) (semanticmodel.ConnectionAuth, error) {
	return r.auth, nil
}

type staticConnectionResolver struct {
	connection semanticmodel.Connection
}

func (resolver staticConnectionResolver) Resolve(
	context.Context,
	string,
	semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	return resolver.connection, nil
}

type rejectingConnectionResolver struct{}

func (rejectingConnectionResolver) Resolve(
	context.Context,
	string,
	semanticmodel.Connection,
) (semanticmodel.Connection, error) {
	return semanticmodel.Connection{}, connectionbinding.ErrBindingNotFound
}

type recordingRefreshTelemetry struct{ contentions atomic.Uint64 }

func (*recordingRefreshTelemetry) ObserveSourceAcquisition(string, string) {}
func (r *recordingRefreshTelemetry) ObserveSecretScopeContention(string) {
	r.contentions.Add(1)
}
func (*recordingRefreshTelemetry) ObserveRefreshCleanup(bool) {}

func TestCompileConnectionSecret(t *testing.T) {
	stmt, ok, err := compileConnectionSecret("prod_lake", semanticmodel.Connection{
		Kind:  "s3",
		Scope: "s3://analytics-prod/",
		Auth: semanticmodel.ConnectionAuth{
			"access_key_id":     "key",
			"secret_access_key": "secret",
			"region":            "us-east-1",
		},
	})
	require.NoError(t, err)
	if !ok {
		t.Fatal("secret ok = false, want true")
	}
	want := "CREATE OR REPLACE TEMPORARY SECRET leapview_prod_lake (TYPE s3, PROVIDER config, KEY_ID 'key', REGION 'us-east-1', SECRET 'secret', SCOPE 's3://analytics-prod/')"
	if stmt != want {
		t.Fatalf("s3 secret = %q, want %q", stmt, want)
	}

	stmt, ok, err = compileConnectionSecret("azure_lake", semanticmodel.Connection{
		Kind: "azure_blob",
		Auth: semanticmodel.ConnectionAuth{"connection_string": "DefaultEndpointsProtocol=https;AccountName=mystorageaccount"},
	})
	require.NoError(t, err)
	want = "CREATE OR REPLACE TEMPORARY SECRET leapview_azure_lake (TYPE azure, PROVIDER config, CONNECTION_STRING 'DefaultEndpointsProtocol=https;AccountName=mystorageaccount')"
	if !ok || stmt != want {
		t.Fatalf("azure secret = %q ok=%v, want %q ok=true", stmt, ok, want)
	}

	t.Setenv("LEAPVIEW_TEST_AZURE_CREDENTIALS", `{"connection_string":"DefaultEndpointsProtocol=https;AccountName=envstorage"}`)
	azureConnection := semanticmodel.Connection{
		Kind:        "azure_blob",
		Credentials: semanticmodel.ConnectionCredentials{Provider: "env", Secret: "LEAPVIEW_TEST_AZURE_CREDENTIALS"},
	}
	selection, err := connectionbinding.NewResolverSelection(connectionbinding.ResolverSelectionInput{
		TargetID: "test-target", ProjectID: "test", Environment: "test", TargetClass: connectionbinding.TargetDevelopment,
		Kind: connectionbinding.ResolverEnvironment,
	})
	require.NoError(t, err)
	developmentResolver, err := NewDevelopmentEnvironmentCredentialResolver(selection)
	require.NoError(t, err)
	azureConnection.Auth, err = developmentResolver.Resolve(context.Background(), "azure_lake", azureConnection)
	require.NoError(t, err)
	stmt, ok, err = compileConnectionSecret("azure_lake", azureConnection)
	require.NoError(t, err)
	want = "CREATE OR REPLACE TEMPORARY SECRET leapview_azure_lake (TYPE azure, PROVIDER config, CONNECTION_STRING 'DefaultEndpointsProtocol=https;AccountName=envstorage')"
	if !ok || stmt != want {
		t.Fatalf("azure env credential secret = %q ok=%v, want %q ok=true", stmt, ok, want)
	}

	stmt, ok, err = compileConnectionSecret("azure_lake", semanticmodel.Connection{
		Kind: "azure_blob",
		Auth: semanticmodel.ConnectionAuth{
			"account_name":  "mystorageaccount",
			"tenant_id":     "tenant",
			"client_id":     "client",
			"client_secret": "secret",
		},
	})
	require.NoError(t, err)
	want = "CREATE OR REPLACE TEMPORARY SECRET leapview_azure_lake (TYPE azure, PROVIDER service_principal, ACCOUNT_NAME 'mystorageaccount', CLIENT_ID 'client', CLIENT_SECRET 'secret', TENANT_ID 'tenant')"
	if !ok || stmt != want {
		t.Fatalf("azure service principal secret = %q ok=%v, want %q ok=true", stmt, ok, want)
	}

	stmt, ok, err = compileConnectionSecret("crm", semanticmodel.Connection{
		Kind: "postgres",
		Auth: semanticmodel.ConnectionAuth{"connection_string": "postgres://crm"},
	})
	require.NoError(t, err)
	if ok || stmt != "" {
		t.Fatalf("postgres secret = %q ok=%v, want empty ok=false", stmt, ok)
	}

	stmt, ok, err = compileConnectionSecret("lakehouse", semanticmodel.Connection{
		Kind:  "ducklake",
		Path:  "metadata.ducklake",
		Scope: "s3://analytics-prod/ducklake/",
		Auth: semanticmodel.ConnectionAuth{
			"access_key_id":     "key",
			"secret_access_key": "secret",
			"region":            "us-east-1",
		},
	})
	require.NoError(t, err)
	want = "CREATE OR REPLACE TEMPORARY SECRET leapview_lakehouse (TYPE ducklake, PROVIDER config, KEY_ID 'key', REGION 'us-east-1', SECRET 'secret', SCOPE 's3://analytics-prod/ducklake/')"
	if !ok || stmt != want {
		t.Fatalf("ducklake secret = %q ok=%v, want %q ok=true", stmt, ok, want)
	}

}

func TestCompileAmbientConnectionSecrets(t *testing.T) {
	stmt, ok, err := compileConnectionSecret("lake", semanticmodel.Connection{
		Kind: "s3", Scope: "s3://analytics/", Credentials: semanticmodel.ConnectionCredentials{Provider: "ambient", Region: "eu-west-1", Endpoint: "s3.eu-west-1.amazonaws.com"},
	})
	require.NoError(t, err)
	want := "CREATE OR REPLACE TEMPORARY SECRET leapview_lake (TYPE s3, PROVIDER credential_chain, ENDPOINT 's3.eu-west-1.amazonaws.com', REGION 'eu-west-1', SCOPE 's3://analytics/')"
	if !ok || stmt != want {
		t.Fatalf("ambient s3 secret = %q ok=%v, want %q", stmt, ok, want)
	}
	stmt, ok, err = compileConnectionSecret("azure", semanticmodel.Connection{
		Kind: "azure_blob", Scope: "az://container/", Credentials: semanticmodel.ConnectionCredentials{Provider: "ambient", AccountName: "analytics"},
	})
	require.NoError(t, err)
	want = "CREATE OR REPLACE TEMPORARY SECRET leapview_azure (TYPE azure, PROVIDER credential_chain, ACCOUNT_NAME 'analytics', SCOPE 'az://container/')"
	if !ok || stmt != want {
		t.Fatalf("ambient azure secret = %q ok=%v, want %q", stmt, ok, want)
	}
	stmt, ok, err = compileConnectionSecret("public", semanticmodel.Connection{Kind: "s3", Credentials: semanticmodel.ConnectionCredentials{Provider: "none"}})
	if err != nil || ok || stmt != "" {
		t.Fatalf("public s3 secret = %q ok=%v err=%v", stmt, ok, err)
	}
}

func TestCompileSourceSecretStatements(t *testing.T) {
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"prod_lake": {
				Kind:  "s3",
				Scope: "s3://analytics-prod/",
				Auth: semanticmodel.ConnectionAuth{
					"access_key_id":     "key",
					"secret_access_key": "secret",
				},
			},
			"public": {
				Kind: "http",
			},
		},
		Sources: map[string]semanticmodel.Source{
			"embeddings": {Connection: "prod_lake", Path: "vectors/products.lance", Format: "lance"},
			"orders":     {Connection: "prod_lake", Path: "orders.parquet", Format: "parquet"},
			"public":     {Connection: "public", Path: "https://example.com/products.lance", Format: "lance"},
		},
	}
	statements, err := compileSourceSecretStatements(model)
	require.NoError(t, err)
	want := []string{"CREATE OR REPLACE TEMPORARY SECRET leapview_prod_lake_lance (TYPE lance, PROVIDER config, KEY_ID 'key', SECRET 'secret', SCOPE 's3://analytics-prod/')"}
	if fmt.Sprint(statements) != fmt.Sprint(want) {
		t.Fatalf("lance secrets = %#v, want %#v", statements, want)
	}
}

func TestCompileDatabaseAttach(t *testing.T) {
	cases := map[string]struct {
		connection semanticmodel.Connection
		want       string
	}{
		"postgres_auth": {
			connection: semanticmodel.Connection{Kind: "postgres", Auth: semanticmodel.ConnectionAuth{"connection_string": "postgres://crm"}},
			want:       "ATTACH 'postgres://crm' AS conn_crm (TYPE postgres, READ_ONLY)",
		},
		"mysql_auth": {
			connection: semanticmodel.Connection{Kind: "mysql", Auth: semanticmodel.ConnectionAuth{"connection_string": "mysql://sales"}},
			want:       "ATTACH 'mysql://sales' AS conn_crm (TYPE mysql, READ_ONLY)",
		},
		"sqlite_option_path": {
			connection: semanticmodel.Connection{Kind: "sqlite", Options: map[string]any{"path": "/tmp/source.sqlite"}},
			want:       "ATTACH '/tmp/source.sqlite' AS conn_crm (TYPE sqlite, READ_ONLY)",
		},
		"sqlite_auth_path": {
			connection: semanticmodel.Connection{Kind: "sqlite", Auth: semanticmodel.ConnectionAuth{"path": "/tmp/source.sqlite"}},
			want:       "ATTACH '/tmp/source.sqlite' AS conn_crm (TYPE sqlite, READ_ONLY)",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			stmt, err := compileDatabaseAttach("crm", tc.connection)
			require.NoError(t, err)
			if stmt != tc.want {
				t.Fatalf("attach = %q, want %q", stmt, tc.want)
			}
		})
	}
}

func TestCompileDatabaseAttachUsesTargetEndpointAndTemporaryNamedSecret(t *testing.T) {
	connection := semanticmodel.Connection{
		Kind: "postgres", Host: "warehouse.internal", Port: 5432,
		Database: "analytics", Username: "leapview_runtime", SSLMode: "verify-full",
		Auth: semanticmodel.ConnectionAuth{"password": "source-secret"},
	}
	secret, ok, err := compileConnectionSecret("warehouse", connection)
	require.NoError(t, err)
	if !ok {
		t.Fatal("structured database credentials did not produce a temporary secret")
	}
	for _, required := range []string{
		"CREATE OR REPLACE TEMPORARY SECRET leapview_warehouse",
		"TYPE postgres",
		"HOST 'warehouse.internal'",
		"PORT 5432",
		"DATABASE 'analytics'",
		"USER 'leapview_runtime'",
		"PASSWORD 'source-secret'",
		"SSLMODE 'verify-full'",
	} {
		if !strings.Contains(secret, required) {
			t.Fatalf("database secret missing %q: %s", required, secret)
		}
	}
	if strings.Contains(secret, "PERSISTENT") {
		t.Fatalf("database secret is persistent: %s", secret)
	}

	attach, err := compileDatabaseAttach("warehouse", connection)
	require.NoError(t, err)
	if attach != "ATTACH '' AS conn_warehouse (TYPE postgres, READ_ONLY, SECRET leapview_warehouse)" {
		t.Fatalf("database attach = %q", attach)
	}
}

func TestCompileDuckLakeAttach(t *testing.T) {
	stmt, err := compileObjectAttach(&semanticmodel.Model{}, "lakehouse", semanticmodel.Connection{
		Kind: "ducklake",
		Path: "metadata.ducklake",
		Options: map[string]any{
			"data_path": "data_files",
		},
	})
	require.NoError(t, err)
	want := "ATTACH 'ducklake:metadata.ducklake' AS conn_lakehouse (DATA_PATH 'data_files')"
	if stmt != want {
		t.Fatalf("local ducklake attach = %q, want %q", stmt, want)
	}

	stmt, err = compileObjectAttach(&semanticmodel.Model{}, "lakehouse", semanticmodel.Connection{
		Kind:  "ducklake",
		Scope: "s3://analytics-prod/ducklake/",
		Path:  "metadata.ducklake",
		Options: map[string]any{
			"data_path": "data",
		},
	})
	require.NoError(t, err)
	want = "ATTACH 'ducklake:s3://analytics-prod/ducklake/metadata.ducklake' AS conn_lakehouse (DATA_PATH 's3://analytics-prod/ducklake/data')"
	if stmt != want {
		t.Fatalf("remote ducklake attach = %q, want %q", stmt, want)
	}
}

func TestCompileQuackSecretIsTemporaryTokenOnlyAndEndpointScoped(t *testing.T) {
	connection := semanticmodel.Connection{
		Kind: "quack", Host: "quack.example.com", Port: 443, SSLMode: "require",
		Auth: semanticmodel.ConnectionAuth{"token": "source-secret"},
	}
	secret, ok, err := compileConnectionSecret("lakehouse", connection)
	require.NoError(t, err)
	if !ok {
		t.Fatal("Quack credentials did not produce a temporary secret")
	}
	want := "CREATE OR REPLACE TEMPORARY SECRET leapview_lakehouse (TYPE quack, TOKEN 'source-secret', SCOPE 'quack:quack.example.com:443')"
	if secret != want {
		t.Fatalf("Quack secret = %q, want %q", secret, want)
	}
	for _, forbidden := range []string{"PERSISTENT", "HOST ", "PORT ", "PROVIDER"} {
		if strings.Contains(secret, forbidden) {
			t.Fatalf("Quack secret contains forbidden %q: %s", forbidden, secret)
		}
	}
}

func TestSourceRelationCompilesGovernedQuackQuery(t *testing.T) {
	model := &semanticmodel.Model{Connections: map[string]semanticmodel.Connection{
		"lakehouse": {
			Kind: "quack", Host: "quack.example.com", Port: 443, SSLMode: "require",
			Auth: semanticmodel.ConnectionAuth{"token": "source-secret"},
		},
	}}
	relation, err := SourceRelation(model, semanticmodel.Source{
		Connection: "lakehouse", Object: "oeducklake.src_nocodb.jobs",
	})
	require.NoError(t, err)
	want := "SELECT * FROM quack_query('quack:quack.example.com:443', 'SELECT * FROM oeducklake.src_nocodb.jobs')"
	if relation != want {
		t.Fatalf("Quack relation = %q, want %q", relation, want)
	}
	if strings.Contains(relation, "source-secret") || strings.Contains(relation, "ATTACH") {
		t.Fatalf("Quack relation leaks credentials or attachment capability: %s", relation)
	}
}

func TestRequiredExtensions(t *testing.T) {
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"lake":   {Kind: "s3"},
			"azure":  {Kind: "azure_blob"},
			"crm":    {Kind: "postgres"},
			"duck":   {Kind: "ducklake", Path: "metadata.ducklake"},
			"remote": {Kind: "quack"},
		},
		Sources: map[string]semanticmodel.Source{
			"events":   {Format: "parquet", Path: "s3://bucket/events/*.parquet", Connection: "lake"},
			"budget":   {Format: "excel", Path: "budget.xlsx", Connection: "lake"},
			"orders":   {Format: "delta", Path: "az://warehouse/orders", Connection: "azure"},
			"archive":  {Format: "vortex", Path: "orders.vortex", Connection: "lake"},
			"vectors":  {Format: "lance", Path: "vectors/products.lance", Connection: "lake"},
			"accounts": {Connection: "crm", Object: "public.accounts"},
			"lake_tbl": {Connection: "duck", Object: "main.orders"},
			"jobs":     {Connection: "remote", Object: "operations.jobs"},
		},
	}
	if got := strings.Join(RequiredExtensions(model), ","); got != "azure,delta,ducklake,excel,httpfs,lance,postgres,quack,vortex" {
		t.Fatalf("required extensions = %q, want azure,delta,ducklake,excel,httpfs,lance,postgres,quack,vortex", got)
	}
}

func TestSourceRelationResolvesSourcePlans(t *testing.T) {
	dir := t.TempDir()
	model := &semanticmodel.Model{
		Name:              "test",
		DefaultConnection: "local_files",
		Connections: map[string]semanticmodel.Connection{
			"local_files": {
				Kind: "managed",
				Defaults: semanticmodel.ConnectionDefaults{
					Options: map[string]any{"header": true},
				},
			},
			"prod_lake": {Kind: "s3", Scope: "s3://analytics-prod/", Auth: semanticmodel.ConnectionAuth{"access_key_id": "key", "secret_access_key": "secret"}},
			"azure":     {Kind: "azure_blob", Scope: "az://warehouse/", Auth: semanticmodel.ConnectionAuth{"connection_string": "DefaultEndpointsProtocol=https;AccountName=warehouse"}},
			"vectors":   {Kind: "s3", Scope: "s3://analytics-prod/", Auth: semanticmodel.ConnectionAuth{"access_key_id": "key", "secret_access_key": "secret"}},
		},
		Sources: map[string]semanticmodel.Source{
			"orders":     {Path: "orders.csv", Format: "csv", EffectiveOptions: map[string]any{"header": true}},
			"events":     {Connection: "prod_lake", Path: "events/*", Format: "parquet", EffectiveOptions: map[string]any{}},
			"delta":      {Connection: "azure", Path: "tables/orders", Format: "delta", EffectiveOptions: map[string]any{}},
			"embeddings": {Connection: "vectors", Path: "vectors/products.lance", Format: "lance", EffectiveOptions: map[string]any{}},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Source: "orders", Entities: map[string]semanticmodel.ModelEntitySpec{"order_id": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order_id",
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeString}},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics:  map[string]semanticmodel.Metric{"orders": {Type: "aggregate", Dataset: "orders", Label: "Orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"}},
	}
	if err := model.Validate(); err != nil {
		t.Fatal(err)
	}
	bindTestManagedRoot(model, "local_files", filepath.Join(dir, "fixtures"))
	relation, err := SourceRelation(model, model.Sources["orders"])
	require.NoError(t, err)
	wantLocal := "SELECT * FROM read_csv('" + SQLString(filepath.Join(dir, "fixtures", "orders.csv")) + "', header = true)"
	if relation != wantLocal {
		t.Fatalf("local relation = %q, want %q", relation, wantLocal)
	}

	relation, err = SourceRelation(model, model.Sources["events"])
	require.NoError(t, err)
	if want := "SELECT * FROM read_parquet('s3://analytics-prod/events/*')"; relation != want {
		t.Fatalf("remote relation = %q, want %q", relation, want)
	}

	relation, err = SourceRelation(model, model.Sources["delta"])
	require.NoError(t, err)
	if want := "SELECT * FROM delta_scan('az://warehouse/tables/orders')"; relation != want {
		t.Fatalf("delta relation = %q, want %q", relation, want)
	}

	relation, err = SourceRelation(model, model.Sources["embeddings"])
	require.NoError(t, err)
	if want := "SELECT * FROM 's3://analytics-prod/vectors/products.lance'"; relation != want {
		t.Fatalf("lance relation = %q, want %q", relation, want)
	}

	bad := model.Sources["events"]
	bad.Path = "s3://other-bucket/events/*"
	_, err = SourceRelation(model, bad)
	if err == nil || !strings.Contains(err.Error(), "outside connection") {
		t.Fatalf("mismatched remote path error = %v, want outside connection", err)
	}
}

func TestManagedSourceRelationUsesImmutableConnectionRoot(t *testing.T) {
	root := t.TempDir()
	model := &semanticmodel.Model{
		Connections: map[string]semanticmodel.Connection{
			"olist": {Kind: "managed", Root: root},
		},
		Sources: map[string]semanticmodel.Source{
			"orders": {Connection: "olist", Path: "orders.csv", Format: "csv", EffectiveOptions: map[string]any{}},
		},
	}
	relation, err := SourceRelation(model, model.Sources["orders"])
	require.NoError(t, err)
	want := "SELECT * FROM read_csv('" + SQLString(filepath.Join(root, "orders.csv")) + "')"
	if relation != want {
		t.Fatalf("managed relation = %q, want %q", relation, want)
	}
}

func bindTestManagedRoot(model *semanticmodel.Model, connectionName, root string) {
	connection := model.Connections[connectionName]
	connection.Root = root
	model.Connections[connectionName] = connection
}
