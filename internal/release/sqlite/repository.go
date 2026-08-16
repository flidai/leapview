// Package sqlite persists canonical project-generation releases.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/flidai/leapview/internal/platform/digest"
	"github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	"github.com/flidai/leapview/internal/release"
)

type Repository struct {
	db       *sql.DB
	workflow jobs.WorkflowRecorder
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db} }
func NewRepositoryWithWorkflow(db *sql.DB, workflow jobs.WorkflowRecorder) *Repository {
	return &Repository{db: db, workflow: workflow}
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (r *Repository) Create(ctx context.Context, input release.CreateInput) (release.Release, error) {
	if r == nil || r.db == nil {
		return release.Release{}, release.ErrInvalid
	}
	if input.Provenance == nil || input.ID == "" || input.ProjectID == "" || input.Environment == "" || input.GenerationID == "" || digest.ValidateSHA256Identity(input.ProjectDigest) != nil || digest.ValidateSHA256Identity(input.ArtifactDigest) != nil || digest.ValidateSHA256Identity(input.RequestDigest) != nil {
		return release.Release{}, release.ErrInvalid
	}
	manifestBytes, err := json.Marshal(release.Manifest{Connections: append([]release.ConnectionPin(nil), input.Connections...)})
	if err != nil {
		return release.Release{}, err
	}
	provenanceBytes, err := json.Marshal(input.Provenance)
	if err != nil {
		return release.Release{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback()
	_, err = tx.ExecContext(ctx, `INSERT INTO api_releases (id, project_id, environment, generation_id, project_digest, artifact_digest, request_digest, idempotency_key, status, manifest_json, provenance_json, created_by) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?, ?, ?)`, input.ID, input.ProjectID, input.Environment, input.GenerationID, input.ProjectDigest, input.ArtifactDigest, input.RequestDigest, input.IdempotencyKey, string(manifestBytes), string(provenanceBytes), input.CreatedBy)
	if err != nil {
		existing, getErr := r.Get(ctx, input.ProjectID, input.ID)
		if getErr == nil {
			if existing.RequestDigest != input.RequestDigest {
				return release.Release{}, release.ErrConflict
			}
			return existing, nil
		}
		return release.Release{}, mapError(err)
	}
	for _, pin := range input.Connections {
		if _, err := tx.ExecContext(ctx, `INSERT INTO api_release_connections (release_id, connection_id, revision_id) VALUES (?, ?, ?)`, input.ID, pin.ConnectionID, pin.RevisionID); err != nil {
			return release.Release{}, mapError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return release.Release{}, err
	}
	return r.Get(ctx, input.ProjectID, input.ID)
}

func (r *Repository) Get(ctx context.Context, projectID, releaseID string) (release.Release, error) {
	return getRelease(ctx, r.db, strings.TrimSpace(projectID), strings.TrimSpace(releaseID), "")
}
func (r *Repository) List(ctx context.Context, projectID string) ([]release.Release, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT id FROM api_releases WHERE project_id = ? ORDER BY created_at DESC,id DESC`, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []release.Release
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := r.Get(ctx, projectID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func (r *Repository) ProvenanceForServingState(ctx context.Context, generationID string) (release.Provenance, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT provenance_json FROM api_releases WHERE generation_id = ? AND status = 'ready' ORDER BY finalized_at DESC,id DESC LIMIT 1`, strings.TrimSpace(generationID)).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return release.Provenance{}, release.ErrNotFound
	}
	if err != nil {
		return release.Provenance{}, err
	}
	var p release.Provenance
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return release.Provenance{}, err
	}
	if err := p.Validate(); err != nil {
		return release.Provenance{}, err
	}
	return p, nil
}

func (r *Repository) RecordArtifact(ctx context.Context, artifact release.Artifact) error {
	identity, err := artifact.Identity()
	if err != nil || artifact.ReleaseID == "" || artifact.SizeBytes < 0 || digest.ValidateSHA256Identity(artifact.ExpectedDigest) != nil || artifact.ActualDigest != artifact.ExpectedDigest {
		return release.ErrInvalid
	}
	result, err := r.db.ExecContext(ctx, `UPDATE api_releases SET artifact_actual_digest = ?, artifact_size_bytes = ?, artifact_uploaded_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ? AND environment = ? AND generation_id = ? AND artifact_digest = ? AND status = 'draft' AND artifact_uploaded_at IS NULL`, artifact.ActualDigest, artifact.SizeBytes, artifact.ReleaseID, identity.ProjectID.String(), identity.Environment, identity.GenerationID, artifact.ExpectedDigest)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return release.ErrConflict
	}
	return nil
}

func (r *Repository) RetainCandidateProvenance(ctx context.Context, projectID string, p release.Provenance) (release.Provenance, error) {
	if strings.TrimSpace(projectID) == "" {
		return release.Provenance{}, release.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return release.Provenance{}, err
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return release.Provenance{}, err
	}
	_, err = r.db.ExecContext(ctx, `INSERT OR IGNORE INTO release_candidate_provenance (project_id,candidate_id,candidate_revision,provenance_digest,provenance_json) VALUES (?,?,?,?,?)`, strings.TrimSpace(projectID), p.Candidate.ID, p.Candidate.Revision, p.Digest, string(encoded))
	if err != nil {
		return release.Provenance{}, err
	}
	return r.CandidateProvenance(ctx, projectID, p.Candidate.ID, p.Candidate.Revision)
}
func (r *Repository) CandidateProvenance(ctx context.Context, projectID, candidateID string, revision int64) (release.Provenance, error) {
	var raw string
	err := r.db.QueryRowContext(ctx, `SELECT provenance_json FROM release_candidate_provenance WHERE project_id = ? AND candidate_id = ? AND candidate_revision = ?`, strings.TrimSpace(projectID), strings.TrimSpace(candidateID), revision).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return release.Provenance{}, release.ErrNotFound
	}
	if err != nil {
		return release.Provenance{}, err
	}
	var p release.Provenance
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return release.Provenance{}, err
	}
	if err := p.Validate(); err != nil {
		return release.Provenance{}, err
	}
	return p, nil
}

func (r *Repository) BeginFinalization(ctx context.Context, projectID, releaseID string, workflow jobs.WorkflowIntent) (release.Release, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback()
	current, err := getRelease(ctx, tx, projectID, releaseID, "")
	if err != nil {
		return release.Release{}, err
	}
	if current.Status != release.StatusDraft && current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	if current.Status == release.StatusDraft {
		if current.ArtifactUploadedAt == "" || current.ActualDigest != current.ArtifactDigest {
			return release.Release{}, release.ErrIncomplete
		}
		if _, err := tx.ExecContext(ctx, `UPDATE api_releases SET status='validating' WHERE id=? AND project_id=? AND status='draft'`, releaseID, projectID); err != nil {
			return release.Release{}, err
		}
	}
	if workflow.Job.ID != "" || workflow.Event.Key != "" {
		if r.workflow == nil {
			return release.Release{}, fmt.Errorf("release workflow recorder is required")
		}
		if err := r.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return release.Release{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return release.Release{}, err
	}
	return r.Get(ctx, projectID, releaseID)
}
func (r *Repository) CompleteFinalization(ctx context.Context, projectID, releaseID, actualDigest string) (release.Release, error) {
	if digest.ValidateSHA256Identity(actualDigest) != nil {
		return release.Release{}, release.ErrInvalid
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback()
	current, err := getRelease(ctx, tx, projectID, releaseID, "")
	if err != nil {
		return release.Release{}, err
	}
	if current.Status == release.StatusReady {
		return current, nil
	}
	if current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	if actualDigest != current.ArtifactDigest || current.ArtifactUploadedAt == "" {
		return release.Release{}, release.ErrConflict
	}
	result, err := tx.ExecContext(ctx, `UPDATE api_releases SET artifact_actual_digest=?,status='ready',finalized_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND status='validating'`, actualDigest, releaseID, projectID)
	if err != nil {
		return release.Release{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return release.Release{}, release.ErrConflict
	}
	ready, err := getRelease(ctx, tx, projectID, releaseID, "")
	if err != nil {
		return release.Release{}, err
	}
	if err := r.recordFinalizationEvent(ctx, tx, ready, "release.ready"); err != nil {
		return release.Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return release.Release{}, err
	}
	return ready, nil
}
func (r *Repository) FailFinalization(ctx context.Context, projectID, releaseID string, cause error) (release.Release, error) {
	message := "release validation failed"
	if cause != nil && strings.TrimSpace(cause.Error()) != "" {
		message = cause.Error()
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return release.Release{}, err
	}
	defer tx.Rollback()
	current, err := getRelease(ctx, tx, projectID, releaseID, "")
	if err != nil {
		return release.Release{}, err
	}
	if current.Status == release.StatusFailed {
		return current, nil
	}
	if current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	result, err := tx.ExecContext(ctx, `UPDATE api_releases SET status='failed',error=?,finalized_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND status='validating'`, message, releaseID, projectID)
	if err != nil {
		return release.Release{}, err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return release.Release{}, release.ErrConflict
	}
	failed, err := getRelease(ctx, tx, projectID, releaseID, "")
	if err != nil {
		return release.Release{}, err
	}
	if err := r.recordFinalizationEvent(ctx, tx, failed, "release.failed"); err != nil {
		return release.Release{}, err
	}
	if err := tx.Commit(); err != nil {
		return release.Release{}, err
	}
	return failed, nil
}

func (r *Repository) recordFinalizationEvent(ctx context.Context, tx *sql.Tx, row release.Release, eventType string) error {
	if r.workflow == nil {
		return fmt.Errorf("release finalization workflow recorder is required")
	}
	data, err := json.Marshal(release.FinalizationEventData(row))
	if err != nil {
		return err
	}
	return r.workflow.RecordWorkflow(ctx, tx, jobs.WorkflowIntent{Event: jobs.EventInput{Key: eventType, ResourceKind: "release", ResourceID: row.ID, EventType: eventType, Data: data}})
}

func (r *Repository) LinkDeployment(ctx context.Context, projectID, deploymentID, releaseID, rollbackOf string) error {
	_, err := r.db.ExecContext(ctx, `INSERT INTO api_deployment_releases (deployment_id,project_id,release_id,rollback_of) VALUES (?,?,?,?) ON CONFLICT(deployment_id) DO UPDATE SET release_id=excluded.release_id,rollback_of=excluded.rollback_of`, strings.TrimSpace(deploymentID), strings.TrimSpace(projectID), strings.TrimSpace(releaseID), nullableString(rollbackOf))
	return mapError(err)
}
func (r *Repository) LinkDeploymentTx(ctx context.Context, tx transaction.Transaction, projectID, deploymentID, releaseID, rollbackOf string) error {
	if tx == nil {
		return fmt.Errorf("release linkage transaction is required")
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO api_deployment_releases (deployment_id,project_id,release_id,rollback_of) VALUES (?,?,?,?) ON CONFLICT(deployment_id) DO UPDATE SET release_id=excluded.release_id,rollback_of=excluded.rollback_of`, strings.TrimSpace(deploymentID), strings.TrimSpace(projectID), strings.TrimSpace(releaseID), nullableString(rollbackOf))
	return mapError(err)
}
func (r *Repository) DeploymentRelease(ctx context.Context, projectID, deploymentID string) (string, string, error) {
	var rid, rollback string
	err := r.db.QueryRowContext(ctx, `SELECT release_id,COALESCE(rollback_of,'') FROM api_deployment_releases WHERE project_id=? AND deployment_id=?`, projectID, deploymentID).Scan(&rid, &rollback)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", release.ErrNotFound
	}
	return rid, rollback, err
}
func (r *Repository) ListDeploymentIDs(ctx context.Context, projectID string) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT deployment_id FROM api_deployment_releases WHERE project_id=? ORDER BY created_at DESC,deployment_id DESC`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
func (r *Repository) PriorDeploymentRelease(ctx context.Context, projectID, deploymentID string) (string, error) {
	var rid string
	err := r.db.QueryRowContext(ctx, `SELECT prior.release_id FROM api_deployment_releases current JOIN project_deployments current_deployment ON current_deployment.id=current.deployment_id JOIN api_deployment_releases prior ON prior.project_id=current.project_id JOIN project_deployments prior_deployment ON prior_deployment.id=prior.deployment_id WHERE current.project_id=? AND current.deployment_id=? AND current_deployment.status IN ('active','superseded') AND prior_deployment.status IN ('active','superseded') AND prior_deployment.activated_at<current_deployment.activated_at ORDER BY prior_deployment.activated_at DESC,prior.deployment_id DESC LIMIT 1`, projectID, deploymentID).Scan(&rid)
	if errors.Is(err, sql.ErrNoRows) {
		return "", release.ErrNotFound
	}
	return rid, err
}

func getRelease(ctx context.Context, q queryer, projectID, releaseID, idempotency string) (release.Release, error) {
	var row release.Release
	var status, manifest, provenance, createdAt, finalized, errorText string
	var uploaded sql.NullString
	var query string
	var args []any
	if idempotency != "" {
		query = `SELECT id,project_id,environment,generation_id,project_digest,artifact_digest,artifact_actual_digest,artifact_size_bytes,artifact_uploaded_at,request_digest,idempotency_key,status,manifest_json,provenance_json,created_by,created_at,COALESCE(finalized_at,''),error FROM api_releases WHERE project_id=? AND idempotency_key=?`
		args = []any{projectID, idempotency}
	} else {
		query = `SELECT id,project_id,environment,generation_id,project_digest,artifact_digest,artifact_actual_digest,artifact_size_bytes,artifact_uploaded_at,request_digest,idempotency_key,status,manifest_json,provenance_json,created_by,created_at,COALESCE(finalized_at,''),error FROM api_releases WHERE project_id=? AND id=?`
		args = []any{projectID, releaseID}
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&row.ID, &row.ProjectID, &row.Environment, &row.GenerationID, &row.ProjectDigest, &row.ArtifactDigest, &row.ActualDigest, &row.ArtifactSizeBytes, &uploaded, &row.RequestDigest, &row.IdempotencyKey, &status, &manifest, &provenance, &row.CreatedBy, &createdAt, &finalized, &errorText)
	if errors.Is(err, sql.ErrNoRows) {
		return release.Release{}, release.ErrNotFound
	}
	if err != nil {
		return release.Release{}, err
	}
	row.Status = release.Status(status)
	row.CreatedAt, row.FinalizedAt, row.Error = createdAt, finalized, errorText
	if uploaded.Valid {
		row.ArtifactUploadedAt = uploaded.String
	}
	if err := json.Unmarshal([]byte(manifest), &row.Manifest); err != nil {
		return release.Release{}, err
	}
	if provenance != "" && provenance != "{}" {
		var p release.Provenance
		if err := json.Unmarshal([]byte(provenance), &p); err != nil {
			return release.Release{}, err
		}
		if err := p.Validate(); err != nil {
			return release.Release{}, err
		}
		row.Provenance = &p
	}
	pins, err := q.QueryContext(ctx, `SELECT connection_id,revision_id FROM api_release_connections WHERE release_id=? ORDER BY connection_id`, row.ID)
	if err != nil {
		return release.Release{}, err
	}
	defer pins.Close()
	for pins.Next() {
		var p release.ConnectionPin
		if err := pins.Scan(&p.ConnectionID, &p.RevisionID); err != nil {
			return release.Release{}, err
		}
		row.Manifest.Connections = append(row.Manifest.Connections, p)
	}
	return row, pins.Err()
}

func nullableString(value string) any {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	return value
}
func mapError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return release.ErrNotFound
	}
	if strings.Contains(strings.ToLower(err.Error()), "constraint") || strings.Contains(strings.ToLower(err.Error()), "unique") {
		return release.ErrConflict
	}
	return err
}

var _ release.Repository = (*Repository)(nil)
