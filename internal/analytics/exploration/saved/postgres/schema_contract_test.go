package postgres

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/exploration/saved"
	"github.com/jackc/pgx/v5/pgconn"
)

// The repository tests exercise the typed API. These tests deliberately go
// around it: the database must remain the final authority when an operator,
// an old binary, or a damaged migration writes SQL directly.
func TestSavedExplorationSchemaLifecycleTamperMatrix(t *testing.T) {
	f := newRepositoryFixture(t)
	var valid23, valid20, invalid24, invalidSecond, invalidDate bool
	if err := f.db.QueryRow(t.Context(), `
		SELECT saved_exploration.is_utc_timestamp($1),
		       saved_exploration.is_utc_timestamp($2),
		       saved_exploration.is_utc_timestamp($3),
		       saved_exploration.is_utc_timestamp($4),
		       saved_exploration.is_utc_timestamp($5)`,
		"2026-09-04T23:59:59.999999999Z", "2026-09-04T20:00:00Z",
		"2026-09-04T24:00:00Z", "2026-09-04T23:59:60Z", "2026-02-29T12:00:00Z").
		Scan(&valid23, &valid20, &invalid24, &invalidSecond, &invalidDate); err != nil {
		t.Fatal(err)
	}
	if !valid23 || !valid20 || invalid24 || invalidSecond || invalidDate {
		t.Fatalf("timestamp boundary validation = valid23:%t valid20:%t invalid24:%t invalidSecond:%t invalidDate:%t", valid23, valid20, invalid24, invalidSecond, invalidDate)
	}
	assertSchemaRejected(t, "updated before created", func() error {
		_, err := f.db.Exec(t.Context(), `
			INSERT INTO saved_exploration.saved_explorations
				(project_id, exploration_id, owner_principal_id, title, slug, visibility,
				 status, semantic_model_id, created_at, updated_at,
				 current_revision_id, current_revision_number, current_content_hash)
			VALUES ('project:sales', 'timestamp-order', $1, 'Timestamp order', 'timestamp-order',
				'private', 'active', 'semantic:sales', $2, $3, 'revision-1', 1, $4)`,
			testActorID, formatTime(f.now), formatTime(f.now.Add(-time.Minute)), f.payload.ContentHash())
		return err
	})

	assertSchemaRejected(t, "initial archived lifecycle", func() error {
		_, err := f.db.Exec(t.Context(), `
			INSERT INTO saved_exploration.saved_explorations
				(project_id, exploration_id, owner_principal_id, title, slug, visibility,
				 status, semantic_model_id, created_at, updated_at, archived_at,
				 current_revision_id, current_revision_number, current_content_hash)
			VALUES ($1, $2, $3, 'Archived', 'archived', 'private', 'archived', $4,
				$5, $5, $5, 'revision-1', 1, $6)`,
			"project:sales", "initial-archived", testActorID, "semantic:sales",
			formatTime(f.now), f.payload.ContentHash())
		return err
	})
	assertSchemaRejected(t, "initial revision two", func() error {
		_, err := f.db.Exec(t.Context(), `
			INSERT INTO saved_exploration.saved_explorations
				(project_id, exploration_id, owner_principal_id, title, slug, visibility,
				 status, semantic_model_id, created_at, updated_at,
				 current_revision_id, current_revision_number, current_content_hash)
			VALUES ($1, $2, $3, 'Wrong', 'wrong', 'private', 'active', $4,
				$5, $5, 'revision-2', 2, $6)`,
			"project:sales", "initial-revision-two", testActorID, "semantic:sales",
			formatTime(f.now), f.payload.ContentHash())
		return err
	})

	_, created := f.create(t, "lifecycle-matrix", "lifecycle-matrix", "lifecycle-matrix-create")
	assertSchemaRejected(t, "noncontiguous revision", func() error {
		return insertSchemaRevision(f, "lifecycle-matrix", "revision-3", 3,
			f.payload.Canonical(), f.payload.ContentHash(), f.now.Add(time.Minute),
			string(f.identity.ProjectID), f.identity.Environment, f.identity.GenerationID)
	})
	assertSchemaRejected(t, "revision model id JSON type", func() error {
		return insertSchemaRevision(f, "lifecycle-matrix", "revision-json", 2,
			[]byte(`{"version":1,"spec":{"modelId":123}}`), f.payload.ContentHash(),
			f.now.Add(time.Minute), string(f.identity.ProjectID), f.identity.Environment,
			f.identity.GenerationID)
	})
	assertSchemaRejected(t, "revision serving project", func() error {
		return insertSchemaRevision(f, "lifecycle-matrix", "revision-serving", 2,
			f.payload.Canonical(), f.payload.ContentHash(), f.now.Add(time.Minute),
			"project:other", f.identity.Environment, f.identity.GenerationID)
	})

	// A valid revision transition is required before checking current-pointer
	// consistency; otherwise a missing-reference error would mask the guard.
	if err := insertSchemaRevision(f, created.Lifecycle.ID.String(), "revision-2", 2,
		f.payload.Canonical(), f.payload.ContentHash(), f.now.Add(time.Minute),
		string(f.identity.ProjectID), f.identity.Environment, f.identity.GenerationID); err != nil {
		t.Fatalf("insert valid revision two: %v", err)
	}
	assertSchemaRejected(t, "current revision timestamp", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations
			   SET title = 'Orders v2', slug = 'lifecycle-matrix-v2',
			       updated_at = $1, current_revision_id = 'revision-2',
			       current_revision_number = 2, current_content_hash = $2
			 WHERE project_id = $3 AND exploration_id = $4`,
			formatTime(f.now.Add(2*time.Minute)), f.payload.ContentHash(),
			"project:sales", created.Lifecycle.ID.String())
		return err
	})
	if _, err := f.db.Exec(t.Context(), `
		UPDATE saved_exploration.saved_explorations
		   SET title = 'Orders v2', slug = 'lifecycle-matrix-v2',
		       updated_at = $1, current_revision_id = 'revision-2',
		       current_revision_number = 2, current_content_hash = $2
		 WHERE project_id = $3 AND exploration_id = $4`,
		formatTime(f.now.Add(time.Minute)), f.payload.ContentHash(),
		"project:sales", created.Lifecycle.ID.String()); err != nil {
		t.Fatalf("advance lifecycle to revision two: %v", err)
	}

	assertSchemaRejected(t, "timestamp regression", func() error {
		return updateLifecyclePointer(f, created.Lifecycle.ID.String(), "revision-2", 2,
			f.now, "Orders v2", "lifecycle-matrix-v2")
	})
	assertSchemaRejected(t, "metadata-only update", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations SET title = 'Metadata only'
			 WHERE project_id = 'project:sales' AND exploration_id = $1`, created.Lifecycle.ID.String())
		return err
	})
	assertSchemaRejected(t, "timestamp-only update", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations SET updated_at = $1
			 WHERE project_id = 'project:sales' AND exploration_id = $2`,
			formatTime(f.now.Add(2*time.Minute)), created.Lifecycle.ID.String())
		return err
	})

	// A valid third revision makes the monotonicity check independent of the
	// current-pointer foreign key and sequence checks.
	if err := insertSchemaRevision(f, created.Lifecycle.ID.String(), "revision-3", 3,
		f.payload.Canonical(), f.payload.ContentHash(), f.now.Add(2*time.Minute),
		string(f.identity.ProjectID), f.identity.Environment, f.identity.GenerationID); err != nil {
		t.Fatalf("insert valid revision three: %v", err)
	}
	assertSchemaRejected(t, "regressing revision transition", func() error {
		return updateLifecyclePointer(f, created.Lifecycle.ID.String(), "revision-3", 3,
			f.now.Add(90*time.Second), "Orders v3", "lifecycle-matrix-v3")
	})

	// Archive the already-valid revision two and then prove all subsequent
	// lifecycle/revision mutations are refused.
	if _, err := f.db.Exec(t.Context(), `
		UPDATE saved_exploration.saved_explorations
		   SET status = 'archived', archived_at = $1
		 WHERE project_id = 'project:sales' AND exploration_id = $2`,
		formatTime(f.now.Add(3*time.Minute)), created.Lifecycle.ID.String()); err != nil {
		t.Fatalf("archive valid lifecycle: %v", err)
	}
	assertSchemaRejected(t, "archive metadata mutation", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations SET title = 'Rewritten'
			 WHERE project_id = 'project:sales' AND exploration_id = $1`, created.Lifecycle.ID.String())
		return err
	})
	assertSchemaRejected(t, "archive reopen", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations SET status = 'active', archived_at = NULL
			 WHERE project_id = 'project:sales' AND exploration_id = $1`, created.Lifecycle.ID.String())
		return err
	})
	assertSchemaRejected(t, "archive receives revision", func() error {
		return insertSchemaRevision(f, created.Lifecycle.ID.String(), "revision-4", 4,
			f.payload.Canonical(), f.payload.ContentHash(), f.now.Add(4*time.Minute),
			string(f.identity.ProjectID), f.identity.Environment, f.identity.GenerationID)
	})

	// A pointer change while archiving is rejected by the archive transition
	// guard before the deferred FK can make the failure ambiguous.
	fresh := newRepositoryFixture(t)
	_, freshCreated := fresh.create(t, "archive-pointer", "archive-pointer", "archive-pointer-create")
	if err := insertSchemaRevision(fresh, freshCreated.Lifecycle.ID.String(), "revision-2", 2,
		fresh.payload.Canonical(), fresh.payload.ContentHash(), fresh.now.Add(time.Minute),
		string(fresh.identity.ProjectID), fresh.identity.Environment, fresh.identity.GenerationID); err != nil {
		t.Fatalf("insert archive pointer revision: %v", err)
	}
	assertSchemaRejected(t, "archive timestamp before updated", func() error {
		_, err := fresh.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations
			   SET status = 'archived', updated_at = $1, archived_at = $2
			 WHERE project_id = 'project:sales' AND exploration_id = $3`,
			formatTime(fresh.now.Add(2*time.Minute)), formatTime(fresh.now.Add(time.Minute)), freshCreated.Lifecycle.ID.String())
		return err
	})
	assertSchemaRejected(t, "archive pointer change", func() error {
		_, err := fresh.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_explorations
			   SET status = 'archived', title = 'Rewritten', archived_at = $1,
			       current_revision_id = 'revision-2', current_revision_number = 2,
			       current_content_hash = $2
			 WHERE project_id = 'project:sales' AND exploration_id = $3`,
			formatTime(fresh.now.Add(2*time.Minute)), fresh.payload.ContentHash(), freshCreated.Lifecycle.ID.String())
		return err
	})

	assertSchemaRejected(t, "revision update", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_exploration_revisions SET created_at = created_at
			 WHERE project_id = 'project:sales' AND exploration_id = $1`, created.Lifecycle.ID.String())
		return err
	})
	assertSchemaRejected(t, "revision delete", func() error {
		_, err := f.db.Exec(t.Context(), `
			DELETE FROM saved_exploration.saved_exploration_revisions
			 WHERE project_id = 'project:sales' AND exploration_id = $1`, created.Lifecycle.ID.String())
		return err
	})
}

func TestSavedExplorationOperationSnapshotAndImmutability(t *testing.T) {
	f := newRepositoryFixture(t)
	_, created := f.create(t, "exploration-operations", "operations", "operations-create")
	valid := schemaOperationForResult(f, created, testActorID, "create", "operation-valid")
	if err := insertSchemaOperation(f, valid); err != nil {
		t.Fatalf("insert valid operation snapshot: %v", err)
	}

	var (
		project, actor, kind, key, fingerprint                                                  string
		resultExploration, owner, title, slug, visibility, status, model                        string
		resultCreated, resultUpdated, revisionID, contentHash                                   string
		revisionCreated, revisionCreator, servingProject, servingEnvironment, servingGeneration string
		evidenceRequest, evidenceCorrelation, evidenceReason, evidenceOccurred, createdAt       string
		archivedAt                                                                              *string
		revisionNumber, evidenceVersion                                                         int64
		override                                                                                bool
	)
	err := f.db.QueryRow(t.Context(), `
		SELECT project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
		       result_exploration_id, result_owner_principal_id, result_title, result_slug,
		       result_visibility, result_status, result_semantic_model_id, result_created_at,
		       result_updated_at, result_archived_at, result_revision_id, result_revision_number,
		       result_content_hash, result_revision_created_at, result_revision_created_by,
		       result_serving_project_id, result_serving_environment, result_serving_generation_id,
		       evidence_version, evidence_request_id, evidence_correlation_id,
		       evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at
		  FROM saved_exploration.saved_exploration_operations
		 WHERE project_id = $1 AND actor_id = $2 AND operation_kind = $3 AND idempotency_key = $4`,
		"project:sales", valid.actor, valid.kind, valid.key).Scan(
		&project, &actor, &kind, &key, &fingerprint, &resultExploration, &owner, &title,
		&slug, &visibility, &status, &model, &resultCreated, &resultUpdated, &archivedAt,
		&revisionID, &revisionNumber, &contentHash, &revisionCreated, &revisionCreator,
		&servingProject, &servingEnvironment, &servingGeneration, &evidenceVersion,
		&evidenceRequest, &evidenceCorrelation, &override, &evidenceReason, &evidenceOccurred,
		&createdAt)
	if err != nil {
		t.Fatal(err)
	}
	if project != "project:sales" || actor != valid.actor || kind != valid.kind || key != valid.key || fingerprint != schemaFingerprint() ||
		resultExploration != "exploration-operations" || owner != valid.owner || title != valid.title || slug != valid.slug ||
		visibility != valid.visibility || status != valid.status || model != valid.model || resultCreated != valid.createdAt ||
		resultUpdated != valid.updatedAt || archivedAt != nil || revisionID != valid.revisionID || revisionNumber != 1 ||
		contentHash != valid.contentHash || revisionCreated != valid.revisionCreatedAt || revisionCreator != valid.revisionCreatedBy ||
		servingProject != valid.servingProject || servingEnvironment != valid.servingEnvironment || servingGeneration != valid.servingGeneration ||
		evidenceVersion != 1 || evidenceRequest != valid.evidenceRequest || evidenceCorrelation != valid.evidenceCorrelation ||
		override || evidenceReason != "" || evidenceOccurred != valid.evidenceOccurred || createdAt != valid.evidenceOccurred {
		t.Fatalf("operation snapshot was not preserved: project=%q actor=%q kind=%q key=%q result=%q revision=%q/%d evidence=%d", project, actor, kind, key, resultExploration, revisionID, revisionNumber, evidenceVersion)
	}

	assertSchemaRejected(t, "operation update", func() error {
		_, err := f.db.Exec(t.Context(), `
			UPDATE saved_exploration.saved_exploration_operations SET result_title = 'rewritten'
			 WHERE project_id = $1 AND actor_id = $2 AND operation_kind = $3 AND idempotency_key = $4`,
			"project:sales", valid.actor, valid.kind, valid.key)
		return err
	})
	assertSchemaRejected(t, "operation delete", func() error {
		_, err := f.db.Exec(t.Context(), `
			DELETE FROM saved_exploration.saved_exploration_operations
			 WHERE project_id = $1 AND actor_id = $2 AND operation_kind = $3 AND idempotency_key = $4`,
			"project:sales", valid.actor, valid.kind, valid.key)
		return err
	})

	invalid := []struct {
		name string
		edit func(*schemaOperationInput)
	}{
		{"create owner identity", func(in *schemaOperationInput) { in.owner = "different-owner" }},
		{"snapshot title", func(in *schemaOperationInput) { in.title = "Orders rewritten" }},
		{"snapshot slug", func(in *schemaOperationInput) { in.slug = "orders-rewritten" }},
		{"snapshot visibility", func(in *schemaOperationInput) { in.visibility = "restricted" }},
		{"snapshot semantic model", func(in *schemaOperationInput) { in.model = "semantic:marketing" }},
		{"snapshot created timestamp", func(in *schemaOperationInput) { in.createdAt = formatTime(f.now.Add(time.Second)) }},
		{"snapshot updated timestamp", func(in *schemaOperationInput) { in.updatedAt = formatTime(f.now.Add(time.Minute)) }},
		{"snapshot serving environment", func(in *schemaOperationInput) { in.servingEnvironment = "staging" }},
		{"snapshot serving generation", func(in *schemaOperationInput) { in.servingGeneration = "generation-2" }},
		{"idempotency newline", func(in *schemaOperationInput) { in.key = "operation-\nkey" }},
		{"request newline", func(in *schemaOperationInput) { in.evidenceRequest = "request-\nkey" }},
		{"correlation newline", func(in *schemaOperationInput) { in.evidenceCorrelation = "correlation-\nkey" }},
		{"override reason mismatch", func(in *schemaOperationInput) { in.evidenceReason = "reason without override" }},
		{"archive status mismatch", func(in *schemaOperationInput) { in.status = "archived"; in.archivedAt = in.updatedAt }},
	}
	for index, testCase := range invalid {
		in := valid
		in.key = fmt.Sprintf("operation-invalid-%d", index)
		testCase.edit(&in)
		assertSchemaRejected(t, testCase.name, func() error { return insertSchemaOperation(f, in) })
	}

	// Archive operations intentionally do not require the actor to equal the
	// revision author. The snapshot guard must still bind that copied field to
	// the immutable revision row.
	archiveFixture := newRepositoryFixture(t)
	_, archiveCreated := archiveFixture.create(t, "exploration-operations", "archive-operation", "archive-operation-create")
	archiveAt := archiveFixture.now.Add(time.Minute)
	if _, err := archiveFixture.db.Exec(t.Context(), `
		UPDATE saved_exploration.saved_explorations
		   SET status = 'archived', archived_at = $1
		 WHERE project_id = 'project:sales' AND exploration_id = $2`,
		formatTime(archiveAt), archiveCreated.Lifecycle.ID.String()); err != nil {
		t.Fatalf("archive operation fixture: %v", err)
	}
	archiveValid := schemaOperationForResult(archiveFixture, archiveCreated, testActorID, "archive", "archive-operation-valid")
	archiveValid.status = "archived"
	archiveValid.archivedAt = formatTime(archiveAt)
	if err := insertSchemaOperation(archiveFixture, archiveValid); err != nil {
		t.Fatalf("insert valid archive operation: %v", err)
	}
	archiveInvalid := archiveValid
	archiveInvalid.key = "archive-operation-revision-author"
	archiveInvalid.revisionCreatedBy = "actor-two"
	assertSchemaRejected(t, "archive revision author snapshot", func() error {
		return insertSchemaOperation(archiveFixture, archiveInvalid)
	})
}

func assertSchemaRejected(t *testing.T, name string, operation func() error) {
	t.Helper()
	err := operation()
	if err == nil {
		t.Fatalf("%s was accepted", name)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s returned non-PostgreSQL error %T: %v", name, err, err)
	}
	if pgErr.Code != "P0001" && pgErr.Code != "23514" {
		t.Fatalf("%s failed with SQLSTATE %s (%s), want trigger P0001 or check 23514; a missing FK would be 23503", name, pgErr.Code, pgErr.Message)
	}
}

func insertSchemaRevision(f *repositoryFixture, explorationID, revisionID string, number int, payload []byte, hash string, createdAt time.Time, servingProject, environment, generation string) error {
	_, err := f.db.Exec(context.Background(), `
		INSERT INTO saved_exploration.saved_exploration_revisions
			(project_id, exploration_id, revision_id, revision_number,
			 spec_envelope_version, spec_canonical_json, content_hash, created_by,
			 created_at, serving_project_id, serving_environment, serving_generation_id)
		VALUES ('project:sales', $1, $2, $3, 1, $4, $5, $6, $7, $8, $9, $10)`,
		explorationID, revisionID, number, payload, hash, testActorID, formatTime(createdAt),
		servingProject, environment, generation)
	return err
}

func updateLifecyclePointer(f *repositoryFixture, explorationID, revisionID string, number int, updatedAt time.Time, title, slug string) error {
	_, err := f.db.Exec(context.Background(), `
		UPDATE saved_exploration.saved_explorations
		   SET title = $1, slug = $2, updated_at = $3,
		       current_revision_id = $4, current_revision_number = $5,
		       current_content_hash = $6
		 WHERE project_id = 'project:sales' AND exploration_id = $7`,
		title, slug, formatTime(updatedAt), revisionID, number, f.payload.ContentHash(), explorationID)
	return err
}

type schemaOperationInput struct {
	actor, kind, key, owner, title, slug, visibility, status, model        string
	contentHash                                                            string
	createdAt, updatedAt, archivedAt                                       string
	revisionID, revisionCreatedAt, revisionCreatedBy                       string
	servingProject, servingEnvironment, servingGeneration                  string
	evidenceRequest, evidenceCorrelation, evidenceReason, evidenceOccurred string
	override                                                               bool
}

func validSchemaOperation(f *repositoryFixture, actor, kind, key string) schemaOperationInput {
	at := formatTime(f.now)
	return schemaOperationInput{
		actor: actor, kind: kind, key: key, owner: testActorID,
		title: "Orders", slug: "orders", visibility: "private", status: "active",
		model: "semantic:sales", contentHash: f.payload.ContentHash(), createdAt: at, updatedAt: at,
		revisionID: "revision-operation", revisionCreatedAt: at, revisionCreatedBy: actor,
		servingProject: string(f.identity.ProjectID), servingEnvironment: f.identity.Environment,
		servingGeneration: f.identity.GenerationID, evidenceRequest: "request-" + key,
		evidenceCorrelation: "correlation-" + key, evidenceOccurred: at,
	}
}

func schemaOperationForResult(f *repositoryFixture, result saved.MutationResult, actor, kind, key string) schemaOperationInput {
	in := validSchemaOperation(f, actor, kind, key)
	in.owner = result.Lifecycle.OwnerPrincipalID
	in.title = result.Lifecycle.Title
	in.slug = result.Lifecycle.Slug
	in.visibility = string(result.Lifecycle.Visibility)
	in.status = string(result.Lifecycle.Status)
	in.model = string(result.Lifecycle.SemanticModelID)
	in.createdAt = formatTime(result.Lifecycle.CreatedAt)
	in.updatedAt = formatTime(result.Lifecycle.UpdatedAt)
	in.revisionID = result.Lifecycle.CurrentRevision.ID.String()
	in.contentHash = result.Lifecycle.CurrentRevision.ContentHash
	in.revisionCreatedAt = formatTime(result.Lifecycle.CurrentRevision.CreatedAt)
	in.revisionCreatedBy = result.Lifecycle.CurrentRevision.CreatedBy
	in.servingProject = string(result.Lifecycle.CurrentRevision.ServingIdentity.ProjectID)
	in.servingEnvironment = result.Lifecycle.CurrentRevision.ServingIdentity.Environment
	in.servingGeneration = result.Lifecycle.CurrentRevision.ServingIdentity.GenerationID
	if result.Lifecycle.ArchivedAt != nil {
		in.archivedAt = formatTime(*result.Lifecycle.ArchivedAt)
	}
	return in
}

func insertSchemaOperation(f *repositoryFixture, input schemaOperationInput) error {
	_, err := f.db.Exec(context.Background(), `
		INSERT INTO saved_exploration.saved_exploration_operations
			(project_id, actor_id, operation_kind, idempotency_key, request_fingerprint,
			 result_exploration_id, result_owner_principal_id, result_title, result_slug,
			 result_visibility, result_status, result_semantic_model_id, result_created_at,
			 result_updated_at, result_archived_at, result_revision_id, result_revision_number,
			 result_content_hash, result_revision_created_at, result_revision_created_by,
			 result_serving_project_id, result_serving_environment, result_serving_generation_id,
			 evidence_version, evidence_request_id, evidence_correlation_id,
			 evidence_admin_override, evidence_admin_reason, evidence_occurred_at, created_at)
		VALUES ($1, $2, $3, $4, $5, 'exploration-operations', $6, $7, $8, $9, $10,
			$11, $12, $13, $14, $15, 1, $16, $17, $18, $19, $20, $21, 1, $22, $23,
			$24, $25, $26, $26)`,
		"project:sales", input.actor, input.kind, input.key, schemaFingerprint(), input.owner,
		input.title, input.slug, input.visibility, input.status, input.model, input.createdAt,
		input.updatedAt, schemaNullableString(input.archivedAt), input.revisionID, input.contentHash,
		input.revisionCreatedAt, input.revisionCreatedBy, input.servingProject,
		input.servingEnvironment, input.servingGeneration, input.evidenceRequest,
		input.evidenceCorrelation, input.override, input.evidenceReason, input.evidenceOccurred)
	return err
}

func schemaFingerprint() string { return "sha256:" + strings.Repeat("0", 64) }

func schemaNullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
