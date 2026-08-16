package duckdb

import (
	"context"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
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
