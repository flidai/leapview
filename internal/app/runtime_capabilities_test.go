package app

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestCapabilityBuildersValidateRequiredDependencies(t *testing.T) {
	t.Run("analytics database", func(t *testing.T) {
		_, err := buildAnalyticsCapability(context.Background(), analyticsCapabilityConfig{})
		if err == nil || err.Error() != "analytics database is required" {
			t.Fatalf("error = %v, want analytics database validation", err)
		}
	})
	t.Run("access database", func(t *testing.T) {
		_, err := buildAccessCapability(context.Background(), accessCapabilityConfig{})
		if err == nil || err.Error() != "access database is required" {
			t.Fatalf("error = %v, want access database validation", err)
		}
	})
	t.Run("production access rejects SQLite", func(t *testing.T) {
		_, err := buildAccessCapability(context.Background(), accessCapabilityConfig{
			Database: new(sql.DB), Production: true,
			CurrentProject: func(context.Context) (projectgraph.ResourceID, error) {
				return "project_internal", nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "production access build rejects SQLite") {
			t.Fatalf("error = %v, want production SQLite rejection", err)
		}
	})
	t.Run("production access requires shared identity pool", func(t *testing.T) {
		_, err := buildAccessCapability(context.Background(), accessCapabilityConfig{
			Production: true,
			CurrentProject: func(context.Context) (projectgraph.ResourceID, error) {
				return "project_internal", nil
			},
		})
		if err == nil || !strings.Contains(err.Error(), "identity PostgreSQL pool") {
			t.Fatalf("error = %v, want shared identity pool validation", err)
		}
	})
	t.Run("production access reuses shared pool", func(t *testing.T) {
		pool := &identityAuthorityPoolStub{}
		module, err := buildAccessCapability(context.Background(), accessCapabilityConfig{
			PostgresDB: pool, Production: true, CSRFKey: strings.Repeat("c", 32),
			CurrentProject: func(context.Context) (projectgraph.ResourceID, error) {
				return "project_internal", nil
			},
		})
		if err != nil {
			t.Fatalf("production access build error = %v", err)
		}
		repository, ok := module.Repository.(*accesspostgres.Repository)
		if !ok {
			t.Fatalf("production access repository = %T, want PostgreSQL repository", module.Repository)
		}
		if repository.DB() != pool {
			t.Fatal("production access repository did not retain the identity pool handle")
		}
	})
	t.Run("jobs database", func(t *testing.T) {
		_, err := buildWorkloadCapability(context.Background(), workloadCapabilityConfig{})
		if err == nil || err.Error() != "jobs database is required" {
			t.Fatalf("error = %v, want jobs database validation", err)
		}
	})
}
