package materialize

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
)

func TestRuntimeQueryPlannerFailsClosedWhenActivationPlannerIsAbsent(t *testing.T) {
	runtime := &Runtime{}
	if _, err := runtime.queryPlanner(); err == nil {
		t.Fatal("queryPlanner() accepted a runtime without an activation planner")
	}
}

type ownershipDatabase struct {
	cacheRuntimeDatabase
	closes   atomic.Int32
	closeErr error
}

func (d *ownershipDatabase) Close() error {
	d.closes.Add(1)
	return d.closeErr
}

type ownershipSources struct{ err error }

func (s ownershipSources) Prepare(context.Context, *semanticmodel.Model) (PreparedSources, error) {
	if s.err != nil {
		return nil, s.err
	}
	return ownershipPreparedSources{}, nil
}

type ownershipPreparedSources struct{}

func ownershipPartition(t *testing.T) resultidentity.Partition {
	t.Helper()
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{Kind: resultidentity.PartitionProduction, ProjectID: "project:ownership", Environment: "test"})
	if err != nil {
		t.Fatal(err)
	}
	return partition
}

func (ownershipPreparedSources) Close() error { return nil }
func (ownershipPreparedSources) PlanModelTable(context.Context, *semanticmodel.Model, string, semanticmodel.Table) (ModelTablePlan, error) {
	return ModelTablePlan{Mode: PlanModeModelSQL, SQL: "SELECT 1 AS id"}, nil
}

func ownershipModel() *semanticmodel.Model {
	return &semanticmodel.Model{Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {
		ModelName: "orders", Execution: semanticmodel.ExecutionDefinition{SQL: "SELECT 1 AS id"},
		Dimensions: map[string]semanticmodel.MetricDimension{"id": {Name: "id", Type: "integer", Datatype: semanticmodel.DataTypeInteger}},
		Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"id"}}}, GrainEntity: "order",
	}}, Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}}}
}

func TestOpenRuntimeOwnedFailureClosesDatabaseExactlyOnceAndJoinsErrors(t *testing.T) {
	primary, cleanup := errors.New("refresh failed"), errors.New("database close failed")
	db := &ownershipDatabase{closeErr: cleanup}
	_, err := OpenRuntime(context.Background(), RuntimeConfig{Model: ownershipModel(), Database: db, Sources: ownershipSources{err: primary}, QueryCachePartition: ownershipPartition(t), OwnDatabase: true})
	if !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("OpenRuntime error = %v, want primary and cleanup", err)
	}
	if got := db.closes.Load(); got != 1 {
		t.Fatalf("database close count = %d, want 1", got)
	}
}

func TestOpenRuntimeBorrowedDatabaseRemainsOpenOnFailure(t *testing.T) {
	db := &ownershipDatabase{}
	_, err := OpenRuntime(context.Background(), RuntimeConfig{Model: ownershipModel(), Database: db, Sources: ownershipSources{err: errors.New("refresh failed")}, QueryCachePartition: ownershipPartition(t)})
	if err == nil {
		t.Fatal("OpenRuntime unexpectedly succeeded")
	}
	if got := db.closes.Load(); got != 0 {
		t.Fatalf("borrowed database close count = %d, want 0", got)
	}
}

func TestOpenRuntimeCloseIsExactlyOnceAndSharedCacheSurvives(t *testing.T) {
	db := &ownershipDatabase{}
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: 2, PartitionBytes: 1024, NodeEntries: 2, NodeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "r", PartitionID: "r"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(context.Background(), RuntimeConfig{Model: ownershipModel(), Database: db, Sources: ownershipSources{}, QueryCache: scope, QueryCachePartition: ownershipPartition(t)})
	if err != nil {
		t.Fatal(err)
	}
	var wait sync.WaitGroup
	for range 8 {
		wait.Add(1)
		go func() { defer wait.Done(); _ = runtime.Close() }()
	}
	wait.Wait()
	if got := db.closes.Load(); got != 0 {
		t.Fatalf("borrowed database close count = %d, want 0", got)
	}
	other, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "other", PartitionID: "other"})
	if err != nil || other == nil {
		t.Fatalf("shared cache pool was closed: scope=%v err=%v", other, err)
	}
	_ = other.Close()
	_ = pool.Close()
}

func TestOpenRuntimeOwnedCacheScopeClosesOnRuntimeClose(t *testing.T) {
	db := &ownershipDatabase{}
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: 2, PartitionBytes: 1024, NodeEntries: 2, NodeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "owned", PartitionID: "owned"})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := OpenRuntime(context.Background(), RuntimeConfig{Model: ownershipModel(), Database: db, Sources: ownershipSources{}, QueryCache: scope, QueryCachePartition: ownershipPartition(t), OwnQueryCache: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "owned", PartitionID: "owned"}); err != nil {
		t.Fatalf("owned cache scope remained open: %v", err)
	}
	_ = pool.Close()
}

func TestNewRuntimeViewOwnedCacheClosesOnModelCompilationFailure(t *testing.T) {
	cleanup := errors.New("database close failed")
	db := &ownershipDatabase{closeErr: cleanup}
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: 2, PartitionBytes: 1024, NodeEntries: 2, NodeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "owned-input", PartitionID: "owned-input"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRuntimeView(context.Background(), RuntimeConfig{Model: nil, Database: db, Sources: ownershipSources{}, QueryCache: scope, OwnQueryCache: true, OwnDatabase: true})
	if err == nil {
		t.Fatal("model compilation unexpectedly succeeded")
	}
	if !errors.Is(err, cleanup) {
		t.Fatalf("construction error = %v, want joined database cleanup error", err)
	}
	if got := db.closes.Load(); got != 1 {
		t.Fatalf("database close count = %d, want 1", got)
	}
	if _, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "owned-input", PartitionID: "owned-input"}); err != nil {
		t.Fatalf("owned cache scope leaked after model failure: %v", err)
	}
	_ = pool.Close()
}

func TestNewRuntimeViewBorrowedCacheRemainsOpenOnModelFailure(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{PartitionEntries: 2, PartitionBytes: 1024, NodeEntries: 2, NodeBytes: 1024})
	if err != nil {
		t.Fatal(err)
	}
	scope, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "borrowed-failure", PartitionID: "borrowed-failure"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewRuntimeView(context.Background(), RuntimeConfig{Model: nil, Database: &ownershipDatabase{}, Sources: ownershipSources{}, QueryCache: scope})
	if err == nil {
		t.Fatal("model compilation unexpectedly succeeded")
	}
	if _, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "borrowed-failure", PartitionID: "borrowed-failure"}); err == nil {
		t.Fatal("borrowed cache scope was closed")
	}
	_ = pool.Close()
}
