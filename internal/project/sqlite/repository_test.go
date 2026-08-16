package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/platform"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsqlite "github.com/flidai/leapview/internal/project/sqlite"
)

func TestRepositoryProjectIdentityRoundTrip(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	repository := projectsqlite.NewRepository(store.SQLDB())
	id := projectgraph.ResourceID("sales")
	if err := repository.Ensure(ctx, projectsqlite.EnsureInput{ID: id, Title: "Sales", Description: "Sales project"}); err != nil {
		t.Fatalf("ensure project: %v", err)
	}
	record, err := repository.ByID(ctx, id)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	if record.ID != id || record.Title != "Sales" || record.Description != "Sales project" {
		t.Fatalf("project record = %#v", record)
	}
	projects, err := repository.List(ctx)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	if len(projects) != 1 || projects[0].ID != id {
		t.Fatalf("projects = %#v", projects)
	}
}

func TestRepositoryRejectsInvalidAndMissingProjects(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	repository := projectsqlite.NewRepository(store.SQLDB())
	if err := repository.Ensure(ctx, projectsqlite.EnsureInput{}); err == nil {
		t.Fatal("Ensure(empty) error = nil")
	}
	if _, err := repository.ByID(ctx, projectgraph.ResourceID("missing")); !errors.Is(err, projectsqlite.ErrNotFound) {
		t.Fatalf("ByID error = %v, want ErrNotFound", err)
	}
}
