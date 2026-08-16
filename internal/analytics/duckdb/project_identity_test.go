package duckdb

import (
	"context"
	"strings"
	"testing"

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
