package duckdb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	materialize "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/resultcache"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestOpenProjectMaterializeRuntimeRejectsInvalidProjectID(t *testing.T) {
	_, err := OpenProjectMaterializeRuntime(context.Background(), ProjectRuntimeConfig{
		ProjectID: projectgraph.ResourceID("not a project id"),
		Models:    map[string]*semanticmodel.Model{"orders": {}},
	})
	if err == nil || !strings.Contains(err.Error(), "project id") {
		t.Fatalf("error = %v, want invalid project id", err)
	}
}

func TestOpenProjectMaterializeRuntimeRequiresBoundedCandidateNamespace(t *testing.T) {
	base := ProjectRuntimeConfig{
		ProjectID:   "sales",
		CandidateID: "candidate-1",
		Models:      map[string]*semanticmodel.Model{"orders": {}},
	}
	for name, namespace := range map[string]string{
		"missing":      "",
		"shared model": "model",
		"invalid":      "candidate;drop",
		"oversized":    strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			config := base
			config.RelationNamespace = namespace
			if _, err := OpenProjectMaterializeRuntime(context.Background(), config); err == nil || !strings.Contains(err.Error(), "candidate relation namespace") && !strings.Contains(err.Error(), "relation namespace") {
				t.Fatalf("namespace %q error = %v, want namespace validation", namespace, err)
			}
		})
	}
}

func TestProjectRuntimeBindsEveryQueryToItsProject(t *testing.T) {
	runtime := &ProjectRuntime{projectID: projectgraph.ResourceID("sales")}
	valid := dataquery.Query{ProjectID: projectgraph.ResourceID("sales")}
	if _, err := runtime.ExecuteDataQuery(context.Background(), valid); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("valid query error = %v, want runtime initialization failure after identity validation", err)
	}
	if _, err := runtime.ExecuteDataQueryArrow(context.Background(), valid, nil); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("valid arrow query error = %v, want runtime initialization failure", err)
	}
	if _, err := runtime.ExecuteDataQueryBundle(context.Background(), []dataquery.BundleRequest{{ID: "valid", Query: valid}}); err == nil || !strings.Contains(err.Error(), "not compiled") {
		t.Fatalf("valid bundle error = %v, want execution failure after identity validation", err)
	}
}

func TestProjectRuntimeRejectsEmptyOrMismatchedProjectQueries(t *testing.T) {
	runtime := &ProjectRuntime{projectID: projectgraph.ResourceID("sales")}
	for _, query := range []dataquery.Query{
		{},
		{ProjectID: projectgraph.ResourceID("operations")},
	} {
		if _, err := runtime.ExecuteDataQuery(context.Background(), query); err == nil || !strings.Contains(err.Error(), "project id") {
			t.Fatalf("query %#v error = %v, want project identity rejection", query, err)
		}
		if _, err := runtime.ExecuteDataQueryArrow(context.Background(), query, nil); err == nil || !strings.Contains(err.Error(), "project id") {
			t.Fatalf("arrow query %#v error = %v, want project identity rejection", query, err)
		}
		if _, err := runtime.ExecuteDataQueryBundle(context.Background(), []dataquery.BundleRequest{{ID: "invalid", Query: query}}); err == nil || !strings.Contains(err.Error(), "project id") {
			t.Fatalf("bundle query %#v error = %v, want project identity rejection", query, err)
		}
	}
}

func TestProjectRuntimeCloseDrainsExecutionBeforeCacheScopes(t *testing.T) {
	pool, err := resultcache.New(resultcache.Limits{RuntimeEntries: 4, RuntimeBytes: 1 << 20, NodeEntries: 8, NodeBytes: 2 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	stable, err := pool.OpenSharedScope(resultcache.ScopeID{RuntimeID: "partition-production"})
	if err != nil {
		t.Fatal(err)
	}
	bytes, err := pool.OpenScope(resultcache.ScopeID{RuntimeID: "generation-one"})
	if err != nil {
		t.Fatal(err)
	}
	execution := resultcache.NewExecutionScope()
	runtime := &ProjectRuntime{
		views: map[string]*materialize.Runtime{}, executionScope: execution,
		queryResultCacheScope: stable, immutableByteCacheScope: bytes,
	}
	started := make(chan struct{})
	scopeCheck := make(chan error, 1)
	flightDone := make(chan error, 1)
	go func() {
		_, _, flightErr := execution.Coalesce(context.Background(), "query", func(ctx context.Context) (any, error) {
			close(started)
			<-ctx.Done()
			if _, _, _, lookupErr := stable.LookupArrow("missing"); lookupErr != nil {
				scopeCheck <- lookupErr
				return nil, ctx.Err()
			}
			if _, _, _, lookupErr := bytes.LookupBytes("missing"); lookupErr != nil {
				scopeCheck <- lookupErr
				return nil, ctx.Err()
			}
			scopeCheck <- nil
			return nil, ctx.Err()
		})
		flightDone <- flightErr
	}()
	<-started
	if err := runtime.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-scopeCheck; err != nil {
		t.Fatalf("cache scope closed before execution drained: %v", err)
	}
	if err := <-flightDone; !errors.Is(err, resultcache.ErrExecutionScopeClosed) {
		t.Fatalf("flight error = %v, want execution scope close", err)
	}
	if _, _, _, err := stable.LookupArrow("missing"); err == nil {
		t.Fatal("stable cache handle remained open after project runtime close")
	}
	if _, _, _, err := bytes.LookupBytes("missing"); err == nil {
		t.Fatal("generation byte cache remained open after project runtime close")
	}
}
