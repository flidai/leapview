//go:build duckdb_arrow

package app

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	analyticsduckdb "github.com/flidai/leapview/internal/analytics/duckdb"
	analyticsducklake "github.com/flidai/leapview/internal/analytics/ducklake"
	analyticsgates "github.com/flidai/leapview/internal/analytics/gates"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/runtimehost"
	"github.com/flidai/leapview/internal/servingstate"
	"github.com/flidai/leapview/internal/workload"
)

var warehouseBoundaryNow = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

type warehouseBoundarySpec struct {
	root        string
	coordinated bool
	freshness   bool
}

type warehouseBoundaryRuntime struct {
	*analyticsduckdb.ProjectRuntime
	database      *analyticsducklake.Environment
	controller    *workload.Controller
	authorization accesssnapshot.AuthorizationSnapshot
	closeOnce     sync.Once
	closeErr      error
}

func (r *warehouseBoundaryRuntime) AuthorizationSnapshot() accesssnapshot.AuthorizationSnapshot {
	return r.authorization
}

func (r *warehouseBoundaryRuntime) Verify(ctx context.Context) error {
	return r.VerifySemantic(ctx, "warehouse")
}

func (r *warehouseBoundaryRuntime) Close() error {
	if r == nil {
		return nil
	}
	r.closeOnce.Do(func() {
		r.closeErr = errors.Join(r.ProjectRuntime.Close(), r.database.Close())
		r.controller.Close()
	})
	return r.closeErr
}

type warehouseBoundaryFactory struct {
	admission testExactExtensionAdmission
	specs     map[servingstate.ID]warehouseBoundarySpec
	mu        sync.Mutex
	runtimes  map[servingstate.ID]*warehouseBoundaryRuntime
}

func (f *warehouseBoundaryFactory) Prepare(ctx context.Context, input runtimehost.RuntimeInput) (runtimehost.PreparedRuntime, error) {
	spec, ok := f.specs[input.State.ID]
	if !ok {
		return nil, fmt.Errorf("warehouse-boundary specification is missing for %s", input.State.ID)
	}
	database, err := analyticsducklake.Open(ctx, analyticsducklake.Config{
		RootDir: filepath.Join(spec.root, ".ducklake"), MaxConnections: 2, ExtensionAdmission: f.admission,
	})
	if err != nil {
		return nil, err
	}
	controller, err := workload.New(workload.DefaultConfig())
	if err != nil {
		_ = database.Close()
		return nil, err
	}
	lease, err := controller.Acquire(ctx, workload.Request{
		Class: workload.Refresh, PrincipalID: "warehouse-boundary", Operation: "candidate.build", EstimatedMemoryBytes: 1,
	})
	if err != nil {
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	model := warehouseBoundaryModel(spec)
	projectRuntime, err := analyticsduckdb.OpenProjectMaterializeRuntime(lease.Context(), analyticsduckdb.ProjectRuntimeConfig{
		Models: map[string]*semanticmodel.Model{"warehouse": model}, Database: database,
		ExtensionAdmission: f.admission, ProjectID: input.State.ProjectID, Environment: string(input.State.Environment),
	})
	lease.Release()
	if err != nil {
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	identity, err := projectgraph.NewServingIdentity(input.State.ProjectID, string(input.State.Environment), string(input.State.ID))
	if err != nil {
		_ = projectRuntime.Close()
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{{ID: input.State.ProjectID, Kind: projectgraph.KindProject, Name: "warehouse-boundary"}}, nil)
	if err != nil {
		_ = projectRuntime.Close()
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	authorization, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, nil, nil)
	if err != nil {
		_ = projectRuntime.Close()
		controller.Close()
		_ = database.Close()
		return nil, err
	}
	runtime := &warehouseBoundaryRuntime{ProjectRuntime: projectRuntime, database: database, controller: controller, authorization: authorization}
	f.mu.Lock()
	if f.runtimes == nil {
		f.runtimes = map[servingstate.ID]*warehouseBoundaryRuntime{}
	}
	f.runtimes[input.State.ID] = runtime
	f.mu.Unlock()
	return runtime, nil
}

func (f *warehouseBoundaryFactory) runtime(id servingstate.ID) *warehouseBoundaryRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.runtimes[id]
}

type warehouseBoundaryRepo struct {
	active    servingstate.ID
	states    map[servingstate.ID]servingstate.State
	artifacts map[servingstate.ID]servingstate.Artifact
}

func (r *warehouseBoundaryRepo) ActiveArtifact(context.Context, projectgraph.ResourceID, servingstate.Environment) (servingstate.State, servingstate.Artifact, error) {
	state, ok := r.states[r.active]
	if !ok {
		return servingstate.State{}, servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return state, r.artifacts[state.ID], nil
}

func (r *warehouseBoundaryRepo) ByID(_ context.Context, id servingstate.ID) (servingstate.State, error) {
	state, ok := r.states[id]
	if !ok {
		return servingstate.State{}, servingstate.ErrNotFound
	}
	return state, nil
}

func (r *warehouseBoundaryRepo) ArtifactByServingState(_ context.Context, id servingstate.ID) (servingstate.Artifact, error) {
	artifact, ok := r.artifacts[id]
	if !ok {
		return servingstate.Artifact{}, servingstate.ErrNotFound
	}
	return artifact, nil
}

func (*warehouseBoundaryRepo) RecordDuckLakeSnapshot(context.Context, servingstate.ID, int64) error {
	return nil
}

type warehouseBoundaryAuthorization struct{}

func (warehouseBoundaryAuthorization) InstallAuthorizationSnapshot(context.Context, accesssnapshot.AuthorizationSnapshot) error {
	return nil
}

func TestProducerNeutralWarehouseBoundaryQualification(t *testing.T) {
	admission := newTestExactExtensionAdmission(t, "ducklake")
	tests := []struct {
		name               string
		publication        string
		coordinated        bool
		freshness          bool
		prepareFails       bool
		qualificationFails bool
		activationFails    bool
	}{
		{name: "compatible physical Parquet activates atomically", publication: "compatible"},
		{name: "complete immutable multi-mart prefix activates atomically", publication: "compatible", coordinated: true},
		{name: "incompatible physical type is blocked", publication: "incompatible_schema", prepareFails: true},
		{name: "removed physical field is blocked", publication: "missing_column", prepareFails: true},
		{name: "stale freshness is blocked", publication: "stale", freshness: true, qualificationFails: true},
		{name: "invalid grain is blocked", publication: "duplicate_grain", qualificationFails: true},
		{name: "failed Model check is blocked", publication: "failed_check", qualificationFails: true},
		{name: "malformed Parquet is blocked", publication: "malformed", prepareFails: true},
		{name: "partial coordinated publication is blocked", publication: "partial", coordinated: true, prepareFails: true},
		{name: "activation failure preserves the old generation", publication: "compatible", activationFails: true},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			baseRoot := filepath.Join(t.TempDir(), "ordinary-current")
			writeWarehouseBoundaryPublication(t, baseRoot, "compatible", false)
			candidatePrefix := "ordinary-next"
			if test.coordinated {
				candidatePrefix = "version-2026-09-04T120000Z"
			}
			candidateRoot := filepath.Join(t.TempDir(), candidatePrefix)
			writeWarehouseBoundaryPublication(t, candidateRoot, test.publication, test.coordinated)

			baseID := servingstate.ID(fmt.Sprintf("generation_base_%d", index))
			candidateID := servingstate.ID(fmt.Sprintf("generation_candidate_%d", index))
			baseDigest := "sha256:" + strings.Repeat("a", 64)
			candidateDigest := fmt.Sprintf("sha256:%064x", index+11)
			states := map[servingstate.ID]servingstate.State{
				baseID:      {ID: baseID, ProjectID: "project:warehouse-boundary", Environment: "test", Status: servingstate.StatusValidated, Digest: baseDigest},
				candidateID: {ID: candidateID, ProjectID: "project:warehouse-boundary", Environment: "test", Status: servingstate.StatusValidated, Digest: candidateDigest},
			}
			artifacts := map[servingstate.ID]servingstate.Artifact{
				baseID:      {ID: "artifact_" + string(baseID), ServingStateID: baseID, Digest: baseDigest},
				candidateID: {ID: "artifact_" + string(candidateID), ServingStateID: candidateID, Digest: candidateDigest},
			}
			repo := &warehouseBoundaryRepo{active: baseID, states: states, artifacts: artifacts}
			factory := &warehouseBoundaryFactory{admission: admission, specs: map[servingstate.ID]warehouseBoundarySpec{
				baseID: {root: baseRoot}, candidateID: {root: candidateRoot, coordinated: test.coordinated, freshness: test.freshness},
			}}
			registry := runtimehost.NewRegistryWithFactory(runtimehost.RegistryOptions{
				Repo: repo, ProjectID: "project:warehouse-boundary", Environment: "test", Factory: factory, Authorization: warehouseBoundaryAuthorization{},
			})
			defer registry.Close()

			base, err := registry.PrepareServingState(t.Context(), string(baseID))
			if err != nil {
				t.Fatalf("prepare base generation: %v", err)
			}
			if err := registry.ActivatePrepared(base, func() error {
				return qualifyWarehouseBoundary(t.Context(), factory.runtime(baseID), warehouseBoundaryModel(factory.specs[baseID]), baseID)
			}); err != nil {
				t.Fatalf("activate base generation: %v", err)
			}

			prepared, err := registry.PrepareServingState(t.Context(), string(candidateID))
			if test.prepareFails {
				if err == nil {
					_ = prepared.Close()
					t.Fatal("candidate preparation unexpectedly succeeded")
				}
				assertWarehouseBoundaryGeneration(t, registry, baseID)
				return
			}
			if err != nil {
				t.Fatalf("prepare candidate: %v", err)
			}
			if test.coordinated {
				observations := factory.runtime(candidateID).SourceObservations()
				if len(observations) != 2 || !strings.HasPrefix(filepath.Base(candidateRoot), "version-") {
					t.Fatalf("coordinated candidate observations=%d prefix=%q", len(observations), filepath.Base(candidateRoot))
				}
			}
			activationErr := registry.ActivatePrepared(prepared, func() error {
				if err := qualifyWarehouseBoundary(t.Context(), factory.runtime(candidateID), warehouseBoundaryModel(factory.specs[candidateID]), candidateID); err != nil {
					return err
				}
				if test.activationFails {
					return errors.New("injected durable activation failure")
				}
				return nil
			})
			if test.qualificationFails || test.activationFails {
				if activationErr == nil {
					t.Fatal("blocking candidate unexpectedly activated")
				}
				assertWarehouseBoundaryGeneration(t, registry, baseID)
				return
			}
			if activationErr != nil {
				t.Fatalf("activate compatible candidate: %v", activationErr)
			}
			assertWarehouseBoundaryGeneration(t, registry, candidateID)
		})
	}
}

func qualifyWarehouseBoundary(ctx context.Context, runtime *warehouseBoundaryRuntime, model *semanticmodel.Model, candidateID servingstate.ID) error {
	if runtime == nil {
		return errors.New("candidate runtime is unavailable")
	}
	input := analyticsgates.Input{
		CandidateID: string(candidateID), SourceDigest: "sha256:" + strings.Repeat("c", 64),
		BindingGeneration: "sha256:" + strings.Repeat("d", 64), RuntimeVersion: "test-runtime", DuckDBVersion: "test-duckdb",
		Now: warehouseBoundaryNow, Bounds: analyticsgates.Bounds{MaxRows: 1000, MaxQueries: 64, MaxMillis: 5000}, Query: runtime.database.Query,
	}
	for _, observation := range runtime.SourceObservations() {
		source, ok := model.Sources[observation.ID]
		if !ok {
			return fmt.Errorf("unknown source observation %q", observation.ID)
		}
		input.Sources = append(input.Sources, analyticsgates.SourceInput{
			ID: observation.ID, Source: source, Relation: observation.ID, Observed: observation.Schema,
			Revision: observation.Revision, RevisionObserved: observation.RevisionObserved,
			FreshnessObserved: observation.FreshnessObserved, FreshnessEmpty: observation.FreshnessEmpty,
			SchemaFailure: observation.SchemaFailure, FreshnessFailure: observation.FreshnessFailure,
			ObservationQueries: observation.ObservationQueries, ObservationRows: observation.ObservationRows, ObservationMillis: observation.ObservationMillis,
		})
		input.PreflightQueries += observation.ObservationQueries
		input.PreflightRows += observation.ObservationRows
		input.PreflightMillis += observation.ObservationMillis
	}
	for id, table := range model.Tables {
		input.Models = append(input.Models, analyticsgates.ModelInput{ID: id, Model: table})
	}
	_, err := analyticsgates.Evaluate(ctx, input)
	return err
}

func assertWarehouseBoundaryGeneration(t *testing.T, registry *runtimehost.Registry, want servingstate.ID) {
	t.Helper()
	lease, err := registry.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	if got := servingstate.ID(lease.Identity().GenerationID); got != want {
		t.Fatalf("active generation = %s, want %s", got, want)
	}
}

func warehouseBoundaryModel(spec warehouseBoundarySpec) *semanticmodel.Model {
	ordersFields := map[string]semanticmodel.SourceField{
		"order_id": {Datatype: semanticmodel.DataTypeString}, "customer_id": {Datatype: semanticmodel.DataTypeString},
		"revenue": {Datatype: semanticmodel.DataTypeFloat}, "updated_at": {Datatype: semanticmodel.DataTypeDateTime},
	}
	orders := semanticmodel.Source{
		Connection: "warehouse", Path: "orders.parquet", Format: "parquet", SchemaMode: "strict", Fields: ordersFields,
		EffectivePathLocation: warehouseBoundaryParquetLocation("orders.parquet"),
	}
	if spec.freshness {
		orders.Freshness = &semanticmodel.SourceFreshnessSpec{Basis: "field", Field: "updated_at", ErrorAfter: &semanticmodel.FreshnessDurationSpec{Amount: 1, Unit: "hour"}}
	}
	model := &semanticmodel.Model{
		Name: "warehouse", DefaultConnection: "warehouse",
		Connections: map[string]semanticmodel.Connection{"warehouse": {Kind: "managed", Root: spec.root}},
		Sources:     map[string]semanticmodel.Source{"orders": orders},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, ModelName: "orders",
				Entities: map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}}, GrainEntity: "order",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Datatype: semanticmodel.DataTypeString}, "customer_id": {Datatype: semanticmodel.DataTypeString},
					"revenue": {Type: "number", Datatype: semanticmodel.DataTypeFloat}, "updated_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTime},
				},
				Checks: []semanticmodel.ModelCheck{{Type: "non_null", Field: "revenue", Severity: "error"}},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Type: "aggregate", Dataset: "orders", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}, Empty: "zero", Label: "Revenue"},
		},
	}
	if spec.coordinated {
		model.Sources["customers"] = semanticmodel.Source{
			Connection: "warehouse", Path: "customers.parquet", Format: "parquet", SchemaMode: "strict",
			Fields:                map[string]semanticmodel.SourceField{"customer_id": {Datatype: semanticmodel.DataTypeString}, "region": {Datatype: semanticmodel.DataTypeString}},
			EffectivePathLocation: warehouseBoundaryParquetLocation("customers.parquet"),
		}
		model.Tables["customers"] = semanticmodel.Table{
			Execution: semanticmodel.ExecutionDefinition{Source: "customers"}, ModelName: "customers",
			Entities: map[string]semanticmodel.EntityDefinition{"customer": {Type: "primary", Fields: []string{"customer_id"}}}, GrainEntity: "customer",
			Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {Datatype: semanticmodel.DataTypeString}, "region": {Datatype: semanticmodel.DataTypeString}},
		}
		model.Datasets["customers"] = semanticmodel.SemanticDatasetSpec{Model: "customers"}
	}
	return model
}

func warehouseBoundaryParquetLocation(path string) *projectcontracts.PathSourceLocation {
	base := projectcontracts.PathSourceLocationBase{Type: "path", Path: path, Format: "parquet"}
	return &projectcontracts.PathSourceLocation{Value: &projectcontracts.ParquetPathSourceLocation{PathSourceLocationBase: base, Format: "parquet", Options: projectcontracts.DefaultParquetReaderOptions()}}
}

func writeWarehouseBoundaryPublication(t *testing.T, root, kind string, coordinated bool) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if kind == "malformed" {
		if err := os.WriteFile(filepath.Join(root, "orders.parquet"), []byte("not parquet"), 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	updatedAt := "TIMESTAMP '2026-09-04 11:59:00'"
	if kind == "stale" {
		updatedAt = "TIMESTAMP '2026-09-01 00:00:00'"
	}
	revenue := "CAST(10 AS DOUBLE)"
	if kind == "incompatible_schema" {
		revenue = "CAST('ten' AS VARCHAR)"
	}
	rows := fmt.Sprintf("('o1', 'c1', %s, %s), ('o2', 'c2', CAST(20 AS %s), %s)", revenue, updatedAt, map[bool]string{true: "VARCHAR", false: "DOUBLE"}[kind == "incompatible_schema"], updatedAt)
	if kind == "duplicate_grain" {
		rows = fmt.Sprintf("('o1', 'c1', CAST(10 AS DOUBLE), %s), ('o1', 'c2', CAST(20 AS DOUBLE), %s)", updatedAt, updatedAt)
	}
	if kind == "failed_check" {
		rows = fmt.Sprintf("('o1', 'c1', CAST(NULL AS DOUBLE), %s), ('o2', 'c2', CAST(20 AS DOUBLE), %s)", updatedAt, updatedAt)
	}
	columns := "order_id, customer_id, revenue, updated_at"
	if kind == "missing_column" {
		rows = fmt.Sprintf("('o1', 'c1', %s), ('o2', 'c2', %s)", updatedAt, updatedAt)
		columns = "order_id, customer_id, updated_at"
	}
	statement := fmt.Sprintf("COPY (SELECT * FROM (VALUES %s) AS orders(%s)) TO '%s' (FORMAT PARQUET)", rows, columns, analyticsduckdb.SQLString(filepath.Join(root, "orders.parquet")))
	if _, err := db.Exec(statement); err != nil {
		t.Fatal(err)
	}
	if coordinated && kind != "partial" {
		customers := fmt.Sprintf("COPY (SELECT * FROM (VALUES ('c1', 'north'), ('c2', 'south')) AS customers(customer_id, region)) TO '%s' (FORMAT PARQUET)", analyticsduckdb.SQLString(filepath.Join(root, "customers.parquet")))
		if _, err := db.Exec(customers); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var files []string
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, entry.Name())
		}
	}
	sort.Strings(files)
	if coordinated && kind != "partial" && strings.Join(files, ",") != "customers.parquet,orders.parquet" {
		t.Fatalf("coordinated publication contains %v", files)
	}
}
