package platform

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

func TestBindInstanceEnvironmentPersistsAndRejectsChanges(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.BindInstanceEnvironment(ctx, "prod"); err != nil {
		t.Fatal(err)
	}
	if got, err := store.InstanceEnvironment(ctx); err != nil || got != "prod" {
		t.Fatalf("environment = %q, %v", got, err)
	}
	if err := store.BindInstanceEnvironment(ctx, "staging"); err == nil || !strings.Contains(err.Error(), "bound to environment \"prod\"") {
		t.Fatalf("mismatch error = %v", err)
	}
}

func TestBindInstanceEnvironmentRejectsConflictingActiveState(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO projects (id, title) VALUES ('sales', 'Sales')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO serving_states (id, project_id, environment, status, source) VALUES ('state_prod', 'sales', 'prod', 'active', 'publish')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO project_active_serving_states (project_id, environment, generation_id) VALUES ('sales', 'prod', 'state_prod')`); err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, "staging"); err == nil || !strings.Contains(err.Error(), "existing active state") {
		t.Fatalf("conflict error = %v", err)
	}
}

func TestBindInstanceEnvironmentRejectsConflictingManagedDataPointer(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `
INSERT INTO managed_data_collections (id, project_id, connection_id, name) VALUES ('orders', 'commerce', 'connection:lake', 'Orders');
INSERT INTO managed_data_revisions (id, collection_id, sequence, digest, status, manifest_json, file_count, size_bytes, ready_at)
VALUES ('revision_1', 'orders', 1, 'sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', 'ready', '{}', 0, 0, CURRENT_TIMESTAMP);
INSERT INTO serving_states (id, project_id, environment, status, source)
VALUES ('generation_1', 'commerce', 'prod', 'validated', 'publish');
INSERT INTO project_deployments (id, project_id, environment, generation_id, artifact_digest, request_digest, status, activated_at)
VALUES ('deployment_1', 'commerce', 'prod', 'generation_1',
  'sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb',
  'sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc',
  'active', CURRENT_TIMESTAMP);
INSERT INTO managed_data_environment_pointers (collection_id, environment, revision_id, deployment_id, generation)
VALUES ('orders', 'prod', 'revision_1', 'deployment_1', 1);`); err != nil {
		t.Fatal(err)
	}
	if err := store.BindInstanceEnvironment(ctx, "staging"); err == nil || !strings.Contains(err.Error(), "existing active state") {
		t.Fatalf("conflict error = %v", err)
	}
}
