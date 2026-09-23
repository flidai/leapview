package platform

import (
	"context"
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	canonical "github.com/flidai/leapview/internal/analytics/exploration"
	saved "github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/pressly/goose/v3"
)

func TestSavedExplorationMigrationUpDownAndConstraints(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "saved-exploration.db")
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	defer database.Close()
	database.SetMaxOpenConns(1)
	goose.SetBaseFS(migrationsFS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("sqlite3"); err != nil {
		t.Fatalf("set migration dialect: %v", err)
	}
	if err := goose.UpToContext(ctx, database, "migrations", 95); err != nil {
		t.Fatalf("seed migration 095: %v", err)
	}
	if err := goose.UpToContext(ctx, database, "migrations", 96); err != nil {
		t.Fatalf("apply saved-exploration migration: %v", err)
	}
	for _, table := range []string{
		"saved_explorations", "saved_exploration_revisions", "saved_exploration_operations",
	} {
		assertSQLTableCount(t, ctx, database, table, 1)
	}

	if _, err := database.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('actor:one', 'owner@example.test', 'Owner')`); err != nil {
		t.Fatalf("insert owner: %v", err)
	}
	salesPayload, err := saved.NewExplorationSpecPayload(testSavedExplorationSpec("semantic_model:sales"))
	if err != nil {
		t.Fatalf("build canonical sales payload: %v", err)
	}
	hash := salesPayload.ContentHash()
	spec := string(salesPayload.Canonical())
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin mutation transaction: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO saved_explorations (
		  project_id, exploration_id, owner_principal_id, title, slug, visibility,
		  status, semantic_model_id, created_at, updated_at,
		  current_revision_id, current_revision_number, current_content_hash
		) VALUES ('project:sales', 'exploration:sales', 'actor:one', 'Sales',
		          'sales', 'private', 'active', 'semantic_model:sales',
		          '2026-08-25T00:00:00Z', '2026-08-25T00:00:00Z',
		          'revision:one', 1, ?)`, hash); err != nil {
		t.Fatalf("insert lifecycle: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO saved_exploration_revisions (
		  project_id, exploration_id, revision_id, revision_number,
		  spec_envelope_version, spec_canonical_json, content_hash, created_by,
		  created_at, serving_project_id, serving_environment, serving_generation_id
		) VALUES ('project:sales', 'exploration:sales', 'revision:one', 1, 1, ?, ?,
		          'actor:one', '2026-08-25T00:00:00Z', 'project:sales', 'prod',
		          'generation:one')`, spec, hash); err != nil {
		t.Fatalf("insert revision: %v", err)
	}
	// Operation references are immediate: the repository validates and inserts
	// the lifecycle and exact revision before recording the replay snapshot.
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO saved_exploration_operations (
		  project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
		  result_exploration_id, result_owner_principal_id, result_title, result_slug,
		  result_visibility, result_status, result_semantic_model_id,
		  result_created_at, result_updated_at, result_archived_at,
		  result_revision_id, result_revision_number, result_content_hash,
		  result_revision_created_at, result_revision_created_by,
		  result_serving_project_id, result_serving_environment, result_serving_generation_id,
		  evidence_version, evidence_request_id, evidence_correlation_id,
		  evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at
		) VALUES ('project:sales', 'actor:one', 'create', 'request-1', ?,
		          'exploration:sales', 'actor:one', 'Sales', 'sales', 'private', 'active',
		          'semantic_model:sales', '2026-08-25T00:00:00Z', '2026-08-25T00:00:00Z', NULL,
		          'revision:one', 1, ?, '2026-08-25T00:00:00Z', 'actor:one',
		          'project:sales', 'prod', 'generation:one', 1, 'request-1',
		          'correlation-1', 0, '', '2026-08-25T00:00:00Z', '2026-08-25T00:00:00Z')`, hash, hash); err != nil {
		t.Fatalf("insert operation: %v", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO audit_outbox (
		  event_id, source, operation, action, aggregate_key, aggregate_sequence,
		  outcome, payload_digest
		) VALUES ('saved-exploration:event-1', 'saved_exploration', 'create',
		          'saved_exploration.created', 'saved_exploration:project:sales:exploration:sales',
		          0, 'success', ?)`, hash); err != nil {
		t.Fatalf("insert same-transaction audit intent: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit mutation, operation, and audit transaction: %v", err)
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_exploration_operations (
		  project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
		  result_exploration_id, result_owner_principal_id, result_title, result_slug,
		  result_visibility, result_status, result_semantic_model_id,
		  result_created_at, result_updated_at, result_archived_at,
		  result_revision_id, result_revision_number, result_content_hash,
		  result_revision_created_at, result_revision_created_by,
		  result_serving_project_id, result_serving_environment, result_serving_generation_id,
		  evidence_version, evidence_request_id, evidence_correlation_id,
		  evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at
		) VALUES ('project:sales', 'actor:one', 'create', 'request-mismatch', ?,
		          'exploration:sales', 'actor:one', 'Sales changed', 'sales', 'private', 'active',
		          'semantic_model:sales', '2026-08-25T00:00:00Z', '2026-08-25T00:00:00Z', NULL,
		          'revision:one', 1, ?, '2026-08-25T00:00:00Z', 'actor:one',
		          'project:sales', 'prod', 'generation:one', 1, 'request-mismatch',
		          'correlation-mismatch', 0, '', '2026-08-25T00:00:00Z', '2026-08-25T00:00:01Z')`, hash, hash); err == nil {
		t.Fatal("mismatched replay snapshot unexpectedly succeeded")
	}
	if err := insertSavedExplorationOperation(ctx, database, savedOperationInput{
		actor: "actor:two", kind: "create", key: "actor-mismatch-create", fingerprint: hash,
		owner: "actor:one", title: "Sales", model: "semantic_model:sales", status: "active",
		revisionID: "revision:one", revisionNumber: 1, contentHash: hash,
		revisionCreatedAt: "2026-08-25T00:00:00Z", revisionCreatedBy: "actor:one",
		servingProject: "project:sales", servingEnvironment: "prod", servingGeneration: "generation:one",
	}); err == nil {
		t.Fatal("create operation with actor-mismatched owner unexpectedly succeeded")
	}
	if err := insertSavedExplorationOperation(ctx, database, savedOperationInput{
		actor: "actor:one", kind: "create", key: "snapshot-invalid-serving", fingerprint: hash,
		owner: "actor:one", title: "Sales", model: "semantic_model:sales", status: "active",
		revisionID: "revision:one", revisionNumber: 1, contentHash: hash,
		revisionCreatedAt: "2026-08-25T00:00:00Z", revisionCreatedBy: "actor:one",
		servingProject: "project:sales", servingEnvironment: ":invalid-environment", servingGeneration: "generation:one",
	}); err == nil {
		t.Fatal("operation snapshot with non-alphanumeric serving ID unexpectedly succeeded")
	}
	if err := insertSavedExplorationOperation(ctx, database, savedOperationInput{
		actor: "actor:one", kind: "create", key: "snapshot-invalid-model", fingerprint: hash,
		owner: "actor:one", title: "Sales", model: ":invalid-model", status: "active",
		revisionID: "revision:one", revisionNumber: 1, contentHash: hash,
		revisionCreatedAt: "2026-08-25T00:00:00Z", revisionCreatedBy: "actor:one",
		servingProject: "project:sales", servingEnvironment: "prod", servingGeneration: "generation:one",
	}); err == nil {
		t.Fatal("operation snapshot with non-alphanumeric semantic model ID unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_explorations (
		  project_id, exploration_id, owner_principal_id, title, slug, visibility,
		  status, semantic_model_id, created_at, updated_at,
		  current_revision_id, current_revision_number, current_content_hash
		) VALUES ('project:bad-sequence', 'exploration:bad-sequence', 'actor:one',
		          'Bad sequence', 'bad-sequence', 'private', 'active',
		          'semantic_model:sales', '2026-08-25T00:00:00Z',
		          '2026-08-25T00:00:00Z', 'revision:bad', 2, ?)`, hash); err == nil {
		t.Fatal("initial revision number 2 unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_explorations (
		  project_id, exploration_id, owner_principal_id, title, slug, visibility,
		  status, semantic_model_id, created_at, updated_at, archived_at,
		  current_revision_id, current_revision_number, current_content_hash
		) VALUES ('project:initial-archived', 'exploration:initial-archived', 'actor:one',
		          'Archived at create', 'archived-at-create', 'private', 'archived',
		          'semantic_model:sales', '2026-08-25T00:00:00Z',
		          '2026-08-25T00:00:00Z', '2026-08-25T00:00:00Z',
		          'revision:missing', 1, ?)`, hash); err == nil {
		t.Fatal("initial archived lifecycle unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_explorations (
		  project_id, exploration_id, owner_principal_id, title, slug, visibility,
		  status, semantic_model_id, created_at, updated_at,
		  current_revision_id, current_revision_number, current_content_hash
		) VALUES (':invalid-project', 'exploration:invalid-project', 'actor:one',
		          'Invalid project', 'invalid-project', 'private', 'active',
		          'semantic_model:sales', '2026-08-25T00:00:00Z',
		          '2026-08-25T00:00:00Z', 'revision:missing', 1, ?)`, hash); err == nil {
		t.Fatal("non-alphanumeric project ID unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations SET semantic_model_id = '_invalid-model' WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err == nil {
		t.Fatal("non-alphanumeric semantic model ID unexpectedly succeeded")
	}

	var storedFingerprint, resultID, resultRevision, resultHash string
	var resultNumber int
	if err := database.QueryRowContext(ctx, `
		SELECT request_fingerprint, result_exploration_id, result_revision_id,
		       result_revision_number, result_content_hash
		FROM saved_exploration_operations
		WHERE project_id = 'project:sales' AND actor_id = 'actor:one'
		  AND operation_kind = 'create' AND idempotency_key = 'request-1'`).Scan(
		&storedFingerprint, &resultID, &resultRevision, &resultNumber, &resultHash); err != nil {
		t.Fatalf("read durable operation result: %v", err)
	}
	if storedFingerprint != hash || resultID != "exploration:sales" || resultRevision != "revision:one" || resultNumber != 1 || resultHash != hash {
		t.Fatalf("durable operation result = %q/%q/%q/%d/%q", storedFingerprint, resultID, resultRevision, resultNumber, resultHash)
	}
	differentHash := "sha256:" + strings.Repeat("b", 64)

	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_exploration_operations (
		  project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
		  result_exploration_id, result_owner_principal_id, result_title, result_slug,
		  result_visibility, result_status, result_semantic_model_id,
		  result_created_at, result_updated_at, result_archived_at,
		  result_revision_id, result_revision_number, result_content_hash,
		  result_revision_created_at, result_revision_created_by,
		  result_serving_project_id, result_serving_environment, result_serving_generation_id,
		  evidence_version, evidence_request_id, evidence_correlation_id,
		  evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at
		) VALUES ('project:sales', 'actor:one', 'create', 'request-1', ?,
		          'exploration:sales', 'actor:one', 'Sales', 'sales', 'private', 'active',
		          'semantic_model:sales', '2026-08-25T00:00:00Z', '2026-08-25T00:00:00Z', NULL,
		          'revision:one', 1, ?, '2026-08-25T00:00:00Z', 'actor:one',
		          'project:sales', 'prod', 'generation:one', 1, 'request-1',
		          'correlation-1', 0, '', '2026-08-25T00:00:00Z', '2026-08-25T00:00:01Z')`,
		differentHash, hash); err == nil {
		t.Fatal("same actor/project/kind/key with a different fingerprint unexpectedly succeeded")
	}

	if _, err := database.ExecContext(ctx, `UPDATE saved_exploration_revisions SET created_by = 'actor:two' WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales' AND revision_id = 'revision:one'`); err == nil {
		t.Fatal("revision update unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `DELETE FROM saved_exploration_revisions WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales' AND revision_id = 'revision:one'`); err == nil {
		t.Fatal("revision delete unexpectedly succeeded")
	}
	marketingPayload, err := saved.NewExplorationSpecPayload(testSavedExplorationSpec("semantic_model:marketing"))
	if err != nil {
		t.Fatalf("build canonical marketing payload: %v", err)
	}
	nextHash := marketingPayload.ContentHash()
	nextSpec := string(marketingPayload.Canonical())
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_exploration_revisions (
		  project_id, exploration_id, revision_id, revision_number,
			  spec_envelope_version, spec_canonical_json, content_hash, created_by,
			  created_at, serving_project_id, serving_environment, serving_generation_id
			) VALUES ('project:sales', 'exploration:sales', 'revision:two', 2, 1, ?, ?,
			          'actor:one', '2026-08-25T00:01:00Z', 'project:sales', 'prod',
		          'generation:two')`, nextSpec, nextHash); err != nil {
		t.Fatalf("insert second revision: %v", err)
	}
	for _, item := range []struct {
		name        string
		project     string
		environment string
		generation  string
	}{
		{name: "project", project: ":invalid-serving-project", environment: "prod", generation: "generation:bad-serving-project"},
		{name: "environment", project: "project:sales", environment: ":invalid-environment", generation: "generation:bad-serving-environment"},
		{name: "generation", project: "project:sales", environment: "prod", generation: ":invalid-generation"},
	} {
		_, err := database.ExecContext(ctx, `
			INSERT INTO saved_exploration_revisions (
			  project_id, exploration_id, revision_id, revision_number,
			  spec_envelope_version, spec_canonical_json, content_hash, created_by,
			  created_at, serving_project_id, serving_environment, serving_generation_id
			) VALUES ('project:sales', 'exploration:sales', ?, 3, 1, ?, ?,
			          'actor:two', '2026-08-25T00:02:00Z', ?, ?, ?)`,
			"revision:bad-serving-"+item.name, nextSpec, nextHash, item.project, item.environment, item.generation)
		if err == nil {
			t.Fatalf("revision with non-alphanumeric serving %s ID unexpectedly succeeded", item.name)
		}
	}
	invalidModelSpec := strings.Replace(spec, "semantic_model:sales", ":invalid-model", 1)
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_exploration_revisions (
		  project_id, exploration_id, revision_id, revision_number,
		  spec_envelope_version, spec_canonical_json, content_hash, created_by,
		  created_at, serving_project_id, serving_environment, serving_generation_id
		) VALUES ('project:sales', 'exploration:sales', 'revision:bad-model', 3, 1, ?, ?,
		          'actor:two', '2026-08-25T00:02:00Z', 'project:sales', 'prod', 'generation:bad-model')`, invalidModelSpec, hash); err == nil {
		t.Fatal("revision with non-alphanumeric semantic model ID unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET current_revision_id = 'revision:two', current_revision_number = 2,
		    current_content_hash = ?, updated_at = '2026-08-25T00:01:00Z'
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`, nextHash); err == nil {
		t.Fatal("pointer to revision with a different semantic model unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET semantic_model_id = 'semantic_model:marketing', current_revision_id = 'revision:two',
		    current_revision_number = 2, current_content_hash = ?, updated_at = '2026-08-25T00:01:00Z'
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`, nextHash); err != nil {
		t.Fatalf("align lifecycle with second revision: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET title = 'Sales metadata without revision'
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err == nil {
		t.Fatal("active metadata-only update unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET updated_at = '2026-08-25T00:02:00Z'
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err == nil {
		t.Fatal("active timestamp-only update unexpectedly succeeded")
	}
	for _, item := range []struct {
		id, generation, createdAt string
		number                    int
	}{
		{id: "revision:three", generation: "generation:three", createdAt: "2026-08-25T00:02:00Z", number: 3},
		{id: "revision:four", generation: "generation:four", createdAt: "2026-08-25T00:03:00Z", number: 4},
	} {
		if _, err := database.ExecContext(ctx, `
			INSERT INTO saved_exploration_revisions (
			  project_id, exploration_id, revision_id, revision_number,
			  spec_envelope_version, spec_canonical_json, content_hash, created_by,
			  created_at, serving_project_id, serving_environment, serving_generation_id
			) VALUES ('project:sales', 'exploration:sales', ?, ?, 1, ?, ?,
			          'actor:two', ?, 'project:sales', 'prod', ?)`,
			item.id, item.number, nextSpec, nextHash, item.createdAt, item.generation); err != nil {
			t.Fatalf("insert revision %d: %v", item.number, err)
		}
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET current_revision_id = 'revision:three', current_revision_number = 3,
		    current_content_hash = ?, updated_at = '2026-08-25T00:03:00Z'
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`, nextHash); err == nil {
		t.Fatal("active pointer update with mismatched revision timestamp unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET current_revision_id = 'revision:four', current_revision_number = 4,
		    current_content_hash = ?, updated_at = '2026-08-25T00:04:00Z'
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`, nextHash); err == nil {
		t.Fatal("active pointer update that skipped a revision unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations
		SET status = 'archived', archived_at = '2026-08-25T00:04:00Z',
		    updated_at = '2026-08-25T00:04:00Z', current_revision_id = 'revision:three',
		    current_revision_number = 3, current_content_hash = ?
		WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`, nextHash); err == nil {
		t.Fatal("archive that changed the current revision unexpectedly succeeded")
	}
	if err := insertSavedExplorationOperation(ctx, database, savedOperationInput{
		actor: "actor:two", kind: "update", key: "actor-mismatch-update", fingerprint: nextHash,
		owner: "actor:one", title: "Sales", model: "semantic_model:marketing", status: "active",
		updatedAt: "2026-08-25T00:01:00Z", revisionID: "revision:two", revisionNumber: 2, contentHash: nextHash,
		revisionCreatedAt: "2026-08-25T00:01:00Z", revisionCreatedBy: "actor:one",
		servingProject: "project:sales", servingEnvironment: "prod", servingGeneration: "generation:two",
	}); err == nil {
		t.Fatal("update operation with actor-mismatched revision creator unexpectedly succeeded")
	}
	longProject := strings.Repeat("p", 129)
	longModel := "semantic_model:" + strings.Repeat("m", 129)
	longPayload, err := saved.NewExplorationSpecPayload(testSavedExplorationSpec(longModel))
	if err != nil {
		t.Fatalf("build canonical long payload: %v", err)
	}
	longSpec := string(longPayload.Canonical())
	longHash := longPayload.ContentHash()
	longTx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin long project transaction: %v", err)
	}
	if _, err := longTx.ExecContext(ctx, `
		INSERT INTO saved_explorations (
		  project_id, exploration_id, owner_principal_id, title, slug, visibility,
		  status, semantic_model_id, created_at, updated_at,
		  current_revision_id, current_revision_number, current_content_hash
		) VALUES (?, 'exploration:long', 'actor:one', 'Long project', 'long-project',
		          'organization', 'active', ?, '2026-08-25T00:00:00Z',
		          '2026-08-25T00:00:00Z', 'revision:long', 1, ?)`, longProject, longModel, longHash); err != nil {
		longTx.Rollback()
		t.Fatalf("insert long project lifecycle: %v", err)
	}
	if _, err := longTx.ExecContext(ctx, `
		INSERT INTO saved_exploration_revisions (
		  project_id, exploration_id, revision_id, revision_number,
		  spec_envelope_version, spec_canonical_json, content_hash, created_by,
		  created_at, serving_project_id, serving_environment, serving_generation_id
		) VALUES (?, 'exploration:long', 'revision:long', 1, 1, ?, ?, 'actor:one',
		          '2026-08-25T00:00:00Z', ?, 'production', 'generation:long')`, longProject, longSpec, longHash, longProject); err != nil {
		longTx.Rollback()
		t.Fatalf("insert long project revision: %v", err)
	}
	if err := longTx.Commit(); err != nil {
		t.Fatalf("commit long project transaction: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations SET updated_at = '2026-08-24T23:59:59Z' WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err == nil {
		t.Fatal("lifecycle timestamp regression unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations SET status = 'archived', archived_at = '2026-08-25T00:01:00Z', updated_at = '2026-08-25T00:01:00Z' WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err != nil {
		t.Fatalf("archive lifecycle: %v", err)
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations SET title = 'Renamed' WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err == nil {
		t.Fatal("archived lifecycle modification unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_explorations SET status = 'active', archived_at = NULL, updated_at = '2026-08-25T00:02:00Z' WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`); err == nil {
		t.Fatal("archived lifecycle reopen unexpectedly succeeded")
	}
	if _, err := database.ExecContext(ctx, `
		INSERT INTO saved_exploration_revisions (
		  project_id, exploration_id, revision_id, revision_number,
		  spec_envelope_version, spec_canonical_json, content_hash, created_by,
		  created_at, serving_project_id, serving_environment, serving_generation_id
		) VALUES ('project:sales', 'exploration:sales', 'revision:five', 5, 1, ?, ?,
		          'actor:two', '2026-08-25T00:05:00Z', 'project:sales', 'prod', 'generation:five')`, nextSpec, nextHash); err == nil {
		t.Fatal("archived lifecycle append unexpectedly succeeded")
	}
	var revisionCount, outboxCount int
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM saved_exploration_revisions WHERE project_id = 'project:sales' AND exploration_id = 'exploration:sales'`).Scan(&revisionCount); err != nil {
		t.Fatalf("count archived history: %v", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT count(*) FROM audit_outbox WHERE event_id = 'saved-exploration:event-1'`).Scan(&outboxCount); err != nil {
		t.Fatalf("count audit intent: %v", err)
	}
	if revisionCount != 4 || outboxCount != 1 {
		t.Fatalf("archive retained revisions/audit intent = %d/%d, want 4/1", revisionCount, outboxCount)
	}
	var resultTitle, resultStatus, resultModel string
	var storedActor, storedAction, storedKey string
	var evidenceRequest, evidenceCorrelation, evidenceReason, evidenceOccurred string
	var evidenceVersion, evidenceOverride int
	if err := database.QueryRowContext(ctx, `
		SELECT result_title, result_status, result_semantic_model_id,
		       actor_id, operation_kind, idempotency_key, request_fingerprint,
		       evidence_version, evidence_request_id, evidence_correlation_id,
		       evidence_admin_override, evidence_admin_reason, evidence_occurred_at
		FROM saved_exploration_operations
		WHERE project_id = 'project:sales' AND actor_id = 'actor:one'
		  AND operation_kind = 'create' AND idempotency_key = 'request-1'`).Scan(
		&resultTitle, &resultStatus, &resultModel, &storedActor, &storedAction, &storedKey,
		&storedFingerprint, &evidenceVersion, &evidenceRequest, &evidenceCorrelation,
		&evidenceOverride, &evidenceReason, &evidenceOccurred); err != nil {
		t.Fatalf("read original mutation snapshot: %v", err)
	}
	if resultTitle != "Sales" || resultStatus != "active" || resultModel != "semantic_model:sales" || storedActor != "actor:one" || storedAction != "create" || storedKey != "request-1" || storedFingerprint != hash || evidenceVersion != 1 || evidenceRequest != "request-1" || evidenceCorrelation != "correlation-1" || evidenceOverride != 0 || evidenceReason != "" || evidenceOccurred != "2026-08-25T00:00:00Z" {
		t.Fatalf("original mutation snapshot changed = %q/%q/%q/%q/%q/%q/%q/%d/%q/%q/%d/%q/%q", resultTitle, resultStatus, resultModel, storedActor, storedAction, storedKey, storedFingerprint, evidenceVersion, evidenceRequest, evidenceCorrelation, evidenceOverride, evidenceReason, evidenceOccurred)
	}
	if _, err := database.ExecContext(ctx, `UPDATE saved_exploration_operations SET result_title = 'changed' WHERE project_id = 'project:sales' AND actor_id = 'actor:one' AND operation_kind = 'create' AND idempotency_key = 'request-1'`); err == nil {
		t.Fatal("operation replay snapshot update unexpectedly succeeded")
	}
	for _, item := range []struct {
		key, evidenceRequest, evidenceCorrelation, evidenceReason string
		evidenceOverride                                          int
		wantErr                                                   bool
	}{
		{key: strings.Repeat("k", 200), evidenceRequest: "request-long", evidenceCorrelation: "correlation-long"},
		{key: strings.Repeat("l", 201), evidenceRequest: "request-long", evidenceCorrelation: "correlation-long", wantErr: true},
		{key: "bad\x00key", evidenceRequest: "request-long", evidenceCorrelation: "correlation-long", wantErr: true},
		{key: "bad\tkey", evidenceRequest: "request-long", evidenceCorrelation: "correlation-long", wantErr: true},
		{key: "bad\nkey", evidenceRequest: "request-long", evidenceCorrelation: "correlation-long", wantErr: true},
		{key: "bad\rkey", evidenceRequest: "request-long", evidenceCorrelation: "correlation-long", wantErr: true},
		{key: "bad-request-evidence", evidenceRequest: "bad\nrequest", evidenceCorrelation: "correlation-long", wantErr: true},
		{key: "bad-correlation-evidence", evidenceRequest: "request-long", evidenceCorrelation: "bad\rcorrelation", wantErr: true},
		{key: "bad-admin-reason", evidenceRequest: "request-long", evidenceCorrelation: "correlation-long", evidenceOverride: 1, evidenceReason: "bad\treason", wantErr: true},
	} {
		_, err := database.ExecContext(ctx, `
			INSERT INTO saved_exploration_operations (
			  project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
			  result_exploration_id, result_owner_principal_id, result_title, result_slug,
			  result_visibility, result_status, result_semantic_model_id,
			  result_created_at, result_updated_at, result_archived_at,
			  result_revision_id, result_revision_number, result_content_hash,
			  result_revision_created_at, result_revision_created_by,
			  result_serving_project_id, result_serving_environment, result_serving_generation_id,
				  evidence_version, evidence_request_id, evidence_correlation_id,
				  evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at
				) VALUES ('project:sales', 'actor:two', 'archive', ?, ?,
			          'exploration:sales', 'actor:one', 'Sales', 'sales', 'private', 'archived',
			          'semantic_model:marketing', '2026-08-25T00:00:00Z', '2026-08-25T00:01:00Z', '2026-08-25T00:01:00Z',
				          'revision:two', 2, ?, '2026-08-25T00:01:00Z', 'actor:one',
				          'project:sales', 'prod', 'generation:two', 1, ?,
				          ?, ?, ?, '2026-08-25T00:01:00Z', '2026-08-25T00:02:00Z')`,
			item.key, nextHash, nextHash, item.evidenceRequest, item.evidenceCorrelation,
			item.evidenceOverride, item.evidenceReason)
		if item.wantErr && err == nil {
			t.Fatalf("idempotency key length %d unexpectedly succeeded", len(item.key))
		}
		if !item.wantErr && err != nil {
			t.Fatalf("idempotency key length %d rejected: %v", len(item.key), err)
		}
	}

	if err := goose.DownToContext(ctx, database, "migrations", 95); err != nil {
		t.Fatalf("down migration 096: %v", err)
	}
	for _, table := range []string{
		"saved_explorations", "saved_exploration_revisions", "saved_exploration_operations",
	} {
		assertSQLTableCount(t, ctx, database, table, 0)
	}
	if err := goose.UpToContext(ctx, database, "migrations", 96); err != nil {
		t.Fatalf("reapply migration 096: %v", err)
	}
}

func testSavedExplorationSpec(modelID string) canonical.ExplorationSpec {
	return canonical.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       modelID,
		Dimensions:    []canonical.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:       []canonical.ExplorationMetricRef{{Field: "order_count"}},
		Filters:       []canonical.ExplorationFilter{},
		Sort:          []canonical.ExplorationSort{},
		Limit:         100,
	}
}

type savedOperationInput struct {
	actor, kind, key, fingerprint string
	owner, title, model, status   string
	updatedAt, archivedAt         string
	revisionID                    string
	revisionNumber                int
	contentHash                   string
	revisionCreatedAt             string
	revisionCreatedBy             string
	servingProject                string
	servingEnvironment            string
	servingGeneration             string
	evidenceRequest               string
	evidenceCorrelation           string
	evidenceOverride              int
	evidenceReason                string
}

func insertSavedExplorationOperation(ctx context.Context, database *sql.DB, input savedOperationInput) error {
	if input.title == "" {
		input.title = "Sales"
	}
	if input.updatedAt == "" {
		input.updatedAt = "2026-08-25T00:00:00Z"
	}
	if input.evidenceRequest == "" {
		input.evidenceRequest = "request-test"
	}
	if input.evidenceCorrelation == "" {
		input.evidenceCorrelation = "correlation-test"
	}
	return func() error {
		_, err := database.ExecContext(ctx, `
			INSERT INTO saved_exploration_operations (
			  project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
			  result_exploration_id, result_owner_principal_id, result_title, result_slug,
			  result_visibility, result_status, result_semantic_model_id,
			  result_created_at, result_updated_at, result_archived_at,
			  result_revision_id, result_revision_number, result_content_hash,
			  result_revision_created_at, result_revision_created_by,
			  result_serving_project_id, result_serving_environment, result_serving_generation_id,
			  evidence_version, evidence_request_id, evidence_correlation_id,
			  evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at
			) VALUES ('project:sales', ?, ?, ?, ?,
			          'exploration:sales', ?, ?, 'sales', 'private', ?, ?,
			          '2026-08-25T00:00:00Z', ?, ?,
			          ?, ?, ?, ?, ?,
			          ?, ?, ?, 1, ?, ?, ?, ?, '2026-08-25T00:05:00Z', '2026-08-25T00:05:00Z')`,
			input.actor, input.kind, input.key, input.fingerprint,
			input.owner, input.title, input.status, input.model,
			input.updatedAt, input.archivedAt,
			input.revisionID, input.revisionNumber, input.contentHash,
			input.revisionCreatedAt, input.revisionCreatedBy,
			input.servingProject, input.servingEnvironment, input.servingGeneration,
			input.evidenceRequest, input.evidenceCorrelation,
			input.evidenceOverride, input.evidenceReason)
		return err
	}()
}
