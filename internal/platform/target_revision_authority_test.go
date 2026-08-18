package platform

import (
	"context"
	"path/filepath"
	"testing"
)

func TestTargetRevisionAuthorityTracksResultBindingsNotSecretOnlyRotations(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, filepath.Join(t.TempDir(), "target-revision.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO delivery_target_revisions(target_id,project_id,environment,created_at,updated_at) VALUES ('target-authority','project-authority','prod','2026-08-17T00:00:00Z','2026-08-17T00:00:00Z')`); err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO target_connection_bindings
 (id,target_id,connection_id,connector_kind,authentication_mode,project_id,environment,endpoint_json,
  credential_project_id,credential_environment,credential_secret_path,credential_secret_key,enabled,
  validated_version,health,health_reason,created_at,updated_at,revision)
 VALUES ('binding-authority','target-authority','warehouse','duckdb','external_bundle','project-authority','prod','{"host":"warehouse"}',
  'project-authority','prod','secrets/warehouse','password',1,'v1','healthy','',
  '2026-08-17T00:00:00Z','2026-08-17T00:00:00Z',1)`
	if _, err := store.SQLDB().ExecContext(ctx, insert); err != nil {
		t.Fatal(err)
	}
	var revision int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT target_revision FROM delivery_target_revisions WHERE target_id='target-authority'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("binding insert revision=%d, want 1", revision)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE target_connection_bindings SET credential_secret_path='secrets/warehouse-v2',credential_secret_key='password-v2',updated_at='2026-08-17T00:01:00Z' WHERE id='binding-authority'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT target_revision FROM delivery_target_revisions WHERE target_id='target-authority'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 1 {
		t.Fatalf("secret-only rotation revision=%d, want unchanged 1", revision)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE target_connection_bindings SET endpoint_json='{"host":"warehouse-new"}',updated_at='2026-08-17T00:02:00Z' WHERE id='binding-authority'`); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT target_revision FROM delivery_target_revisions WHERE target_id='target-authority'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if revision != 2 {
		t.Fatalf("endpoint mutation revision=%d, want 2", revision)
	}
	var kind, operation string
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT component_kind,operation FROM delivery_target_revision_components WHERE target_id='target-authority' AND target_revision=2`).Scan(&kind, &operation); err != nil {
		t.Fatal(err)
	}
	if kind != "connection_binding" || operation != "update" {
		t.Fatalf("component evidence=%q/%q", kind, operation)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `INSERT INTO delivery_target_revisions(target_id,project_id,environment,created_at,updated_at) VALUES ('target-authority-new','project-authority-new','staging','2026-08-17T00:03:00Z','2026-08-17T00:03:00Z')`); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE target_connection_bindings SET target_id='target-authority-new',project_id='project-authority-new',environment='staging',updated_at='2026-08-17T00:04:00Z' WHERE id='binding-authority'`); err != nil {
		t.Fatal(err)
	}
	var oldRevision, newRevision int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT target_revision FROM delivery_target_revisions WHERE target_id='target-authority'`).Scan(&oldRevision); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT target_revision FROM delivery_target_revisions WHERE target_id='target-authority-new'`).Scan(&newRevision); err != nil {
		t.Fatal(err)
	}
	if oldRevision != 3 || newRevision != 1 {
		t.Fatalf("target move revisions old=%d new=%d, want 3/1", oldRevision, newRevision)
	}
	var oldComponent, newComponent int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM delivery_target_revision_components WHERE target_id='target-authority' AND target_revision=3 AND component_id='binding-authority'`).Scan(&oldComponent); err != nil {
		t.Fatal(err)
	}
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM delivery_target_revision_components WHERE target_id='target-authority-new' AND target_revision=1 AND component_id='binding-authority'`).Scan(&newComponent); err != nil {
		t.Fatal(err)
	}
	if oldComponent != 1 || newComponent != 1 {
		t.Fatalf("target move component evidence old=%d new=%d, want 1/1", oldComponent, newComponent)
	}
}
