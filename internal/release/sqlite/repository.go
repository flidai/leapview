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
	jobplatform "github.com/flidai/leapview/internal/platform/jobs"
	"github.com/flidai/leapview/internal/platform/transaction"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/release"
	releasedb "github.com/flidai/leapview/internal/release/internal/db"
	"github.com/flidai/leapview/pkg/jobs"
)

type Repository struct {
	db       *sql.DB
	queries  *releasedb.Queries
	workflow jobplatform.WorkflowRecorder
}

func NewRepository(db *sql.DB) *Repository { return &Repository{db: db, queries: releasedb.New(db)} }
func NewRepositoryWithWorkflow(db *sql.DB, workflow jobplatform.WorkflowRecorder) *Repository {
	return &Repository{db: db, queries: releasedb.New(db), workflow: workflow}
}

func (r *Repository) Create(ctx context.Context, input release.CreateInput) (release.Release, error) {
	if r == nil || r.db == nil {
		return release.Release{}, release.ErrInvalid
	}
	identity, identityErr := input.Identity()
	if identityErr != nil || input.Provenance == nil || input.ID == "" || digest.ValidateSHA256Identity(input.ProjectDigest) != nil || digest.ValidateSHA256Identity(input.ArtifactDigest) != nil || digest.ValidateSHA256Identity(input.RequestDigest) != nil {
		return release.Release{}, release.ErrInvalid
	}
	if err := input.Provenance.Validate(); err != nil {
		return release.Release{}, release.ErrInvalid
	}
	if input.Provenance.Plan.Identity != identity || input.Provenance.Artifact.ProjectDigest != input.ProjectDigest || input.Provenance.Artifact.ContentDigest != input.ArtifactDigest {
		return release.Release{}, release.ErrInvalid
	}
	seenConnections := make(map[string]struct{}, len(input.Connections))
	for _, pin := range input.Connections {
		if pin.ConnectionID == "" || pin.RevisionID == "" {
			return release.Release{}, release.ErrInvalid
		}
		if _, exists := seenConnections[pin.ConnectionID]; exists {
			return release.Release{}, release.ErrInvalid
		}
		seenConnections[pin.ConnectionID] = struct{}{}
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
	qtx := releasedb.New(tx)
	err = qtx.CreateAPIRelease(ctx, releasedb.CreateAPIReleaseParams{ID: input.ID, ProjectID: input.ServingIdentity.ProjectID.String(), Environment: input.ServingIdentity.Environment, GenerationID: input.ServingIdentity.GenerationID, ProjectDigest: input.ProjectDigest, ArtifactDigest: input.ArtifactDigest, RequestDigest: input.RequestDigest, IdempotencyKey: input.IdempotencyKey, ProvenanceJson: string(provenanceBytes), CreatedBy: input.CreatedBy})
	if err != nil {
		// Resolve idempotent replays through the transaction. Querying r.db here
		// can deadlock when SQLite is configured with one connection because the
		// open transaction owns that connection until this function returns.
		existing, getErr := getRelease(ctx, qtx, input.ServingIdentity.ProjectID.String(), input.ID)
		if getErr == nil {
			if existing.RequestDigest != input.RequestDigest {
				return release.Release{}, release.ErrConflict
			}
			return existing, nil
		}
		return release.Release{}, mapError(err)
	}
	for _, pin := range input.Connections {
		if err := qtx.CreateAPIReleaseConnection(ctx, releasedb.CreateAPIReleaseConnectionParams{ReleaseID: input.ID, ConnectionID: pin.ConnectionID, RevisionID: pin.RevisionID}); err != nil {
			return release.Release{}, mapError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return release.Release{}, err
	}
	return r.Get(ctx, input.ServingIdentity.ProjectID, input.ID)
}

func (r *Repository) Get(ctx context.Context, projectID projectgraph.ResourceID, releaseID string) (release.Release, error) {
	if projectID.Validate() != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return release.Release{}, release.ErrInvalid
	}
	return getRelease(ctx, r.queries, projectID.String(), releaseID)
}
func (r *Repository) List(ctx context.Context, projectID projectgraph.ResourceID) ([]release.Release, error) {
	if projectID.Validate() != nil {
		return nil, release.ErrInvalid
	}
	ids, err := r.queries.ListAPIReleaseIDs(ctx, projectID.String())
	if err != nil {
		return nil, err
	}
	out := make([]release.Release, 0, len(ids))
	for _, id := range ids {
		item, err := r.Get(ctx, projectID, id)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, nil
}

func (r *Repository) ProvenanceForServingState(ctx context.Context, identity projectgraph.ServingIdentity) (release.Provenance, error) {
	if err := identity.Validate(); err != nil {
		return release.Provenance{}, release.ErrInvalid
	}
	raw, err := r.queries.GetReadyReleaseProvenanceByGeneration(ctx, releasedb.GetReadyReleaseProvenanceByGenerationParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID})
	if errors.Is(err, sql.ErrNoRows) {
		// Canonical sealed delivery retains immutable provenance with the
		// candidate before publication; it does not create a second legacy API
		// release row. Resolve that exact serving identity after the legacy read
		// misses. More than one row is an identity conflict, never a reason to
		// pick the newest provenance silently.
		matches, candidateErr := r.queries.GetCandidateProvenanceByGeneration(ctx, releasedb.GetCandidateProvenanceByGenerationParams{ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID})
		if candidateErr != nil {
			return release.Provenance{}, candidateErr
		}
		if len(matches) == 0 {
			return release.Provenance{}, release.ErrNotFound
		}
		if len(matches) != 1 {
			return release.Provenance{}, release.ErrConflict
		}
		raw = matches[0]
	}
	if err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return release.Provenance{}, err
		}
	}
	var p release.Provenance
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		return release.Provenance{}, err
	}
	if err := p.Validate(); err != nil {
		return release.Provenance{}, err
	}
	if p.Plan.Identity != identity {
		return release.Provenance{}, release.ErrConflict
	}
	return p, nil
}

func (r *Repository) RecordArtifact(ctx context.Context, artifact release.Artifact) error {
	identity, err := artifact.Identity()
	if err != nil || artifact.ReleaseID == "" || artifact.SizeBytes < 0 || digest.ValidateSHA256Identity(artifact.ExpectedDigest) != nil || artifact.ActualDigest != artifact.ExpectedDigest {
		return release.ErrInvalid
	}
	n, err := r.queries.RecordAPIReleaseArtifact(ctx, releasedb.RecordAPIReleaseArtifactParams{ArtifactActualDigest: artifact.ActualDigest, ArtifactSizeBytes: artifact.SizeBytes, ID: artifact.ReleaseID, ProjectID: identity.ProjectID.String(), Environment: identity.Environment, GenerationID: identity.GenerationID, ArtifactDigest: artifact.ExpectedDigest})
	if err != nil {
		return err
	}
	if n != 1 {
		return release.ErrConflict
	}
	return nil
}

func (r *Repository) RetainCandidateProvenance(ctx context.Context, projectID projectgraph.ResourceID, p release.Provenance) (release.Provenance, error) {
	if projectID.Validate() != nil {
		return release.Provenance{}, release.ErrInvalid
	}
	if err := p.Validate(); err != nil {
		return release.Provenance{}, err
	}
	if p.Plan.Identity.ProjectID != projectID {
		return release.Provenance{}, release.ErrConflict
	}
	encoded, err := json.Marshal(p)
	if err != nil {
		return release.Provenance{}, err
	}
	_, err = r.queries.RetainCandidateProvenance(ctx, releasedb.RetainCandidateProvenanceParams{ProjectID: projectID.String(), CandidateID: p.Candidate.ID, CandidateRevision: p.Candidate.Revision, ProvenanceDigest: p.Digest, ProvenanceJson: string(encoded)})
	if err != nil {
		return release.Provenance{}, err
	}
	retained, err := r.CandidateProvenance(ctx, projectID, p.Candidate.ID, p.Candidate.Revision)
	if err != nil {
		return release.Provenance{}, err
	}
	if retained.Digest != p.Digest {
		return release.Provenance{}, release.ErrConflict
	}
	return retained, nil
}
func (r *Repository) CandidateProvenance(ctx context.Context, projectID projectgraph.ResourceID, candidateID string, revision int64) (release.Provenance, error) {
	if projectID.Validate() != nil || candidateID == "" || candidateID != strings.TrimSpace(candidateID) {
		return release.Provenance{}, release.ErrInvalid
	}
	raw, err := r.queries.GetCandidateProvenance(ctx, releasedb.GetCandidateProvenanceParams{ProjectID: projectID.String(), CandidateID: candidateID, CandidateRevision: revision})
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
	qtx := releasedb.New(tx)
	current, err := getRelease(ctx, qtx, projectID, releaseID)
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
		if n, err := qtx.MarkAPIReleaseValidating(ctx, releasedb.MarkAPIReleaseValidatingParams{ID: releaseID, ProjectID: projectID}); err != nil {
			return release.Release{}, err
		} else if n != 1 {
			return release.Release{}, release.ErrConflict
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
	project, err := projectgraph.NewResourceID(projectID)
	if err != nil || releaseID == "" || releaseID != strings.TrimSpace(releaseID) {
		return release.Release{}, release.ErrInvalid
	}
	return r.Get(ctx, project, releaseID)
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
	qtx := releasedb.New(tx)
	current, err := getRelease(ctx, qtx, projectID, releaseID)
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
	n, err := qtx.MarkAPIReleaseReady(ctx, releasedb.MarkAPIReleaseReadyParams{ID: releaseID, ProjectID: projectID})
	if err != nil {
		return release.Release{}, err
	}
	if n != 1 {
		return release.Release{}, release.ErrConflict
	}
	ready, err := getRelease(ctx, qtx, projectID, releaseID)
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
	qtx := releasedb.New(tx)
	current, err := getRelease(ctx, qtx, projectID, releaseID)
	if err != nil {
		return release.Release{}, err
	}
	if current.Status == release.StatusFailed {
		return current, nil
	}
	if current.Status != release.StatusValidating {
		return release.Release{}, release.ErrImmutable
	}
	n, err := qtx.MarkAPIReleaseFailed(ctx, releasedb.MarkAPIReleaseFailedParams{Error: message, ID: releaseID, ProjectID: projectID})
	if err != nil {
		return release.Release{}, err
	}
	if n != 1 {
		return release.Release{}, release.ErrConflict
	}
	failed, err := getRelease(ctx, qtx, projectID, releaseID)
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
	return linkDeployment(ctx, r.db, projectID, deploymentID, releaseID, rollbackOf)
}
func (r *Repository) LinkDeploymentTx(ctx context.Context, tx transaction.Transaction, projectID, deploymentID, releaseID, rollbackOf string) error {
	if tx == nil {
		return fmt.Errorf("release linkage transaction is required")
	}
	return linkDeployment(ctx, tx, projectID, deploymentID, releaseID, rollbackOf)
}

func linkDeployment(ctx context.Context, q releasedb.DBTX, projectID, deploymentID, releaseID, rollbackOf string) error {
	if projectID == "" || deploymentID == "" || releaseID == "" ||
		projectID != strings.TrimSpace(projectID) || deploymentID != strings.TrimSpace(deploymentID) || releaseID != strings.TrimSpace(releaseID) || rollbackOf != strings.TrimSpace(rollbackOf) {
		return release.ErrInvalid
	}
	queries := releasedb.New(q)
	if err := queries.CreateAPIReleaseDeployment(ctx, releasedb.CreateAPIReleaseDeploymentParams{DeploymentID: deploymentID, ProjectID: projectID, ReleaseID: releaseID, RollbackOf: sql.NullString{String: rollbackOf, Valid: rollbackOf != ""}}); err != nil {
		return mapError(err)
	}
	existing, err := queries.GetAPIReleaseDeploymentByDeployment(ctx, deploymentID)
	if err != nil {
		return mapError(err)
	}
	if existing.ProjectID != projectID || existing.ReleaseID != releaseID || existing.RollbackOf != rollbackOf {
		return release.ErrConflict
	}
	return nil
}
func (r *Repository) DeploymentRelease(ctx context.Context, projectID, deploymentID string) (string, string, error) {
	row, err := r.queries.GetAPIReleaseDeployment(ctx, releasedb.GetAPIReleaseDeploymentParams{ProjectID: projectID, DeploymentID: deploymentID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", release.ErrNotFound
	}
	return row.ReleaseID, row.RollbackOf, err
}
func (r *Repository) ListDeploymentIDs(ctx context.Context, projectID string) ([]string, error) {
	return r.queries.ListAPIReleaseDeploymentIDs(ctx, projectID)
}
func (r *Repository) PriorDeploymentRelease(ctx context.Context, projectID, deploymentID string) (string, error) {
	rid, err := r.queries.GetPriorAPIReleaseDeployment(ctx, releasedb.GetPriorAPIReleaseDeploymentParams{ProjectID: projectID, DeploymentID: deploymentID})
	if errors.Is(err, sql.ErrNoRows) {
		return "", release.ErrNotFound
	}
	return rid, err
}

func getRelease(ctx context.Context, q *releasedb.Queries, projectID, releaseID string) (release.Release, error) {
	dbrow, err := q.GetAPIReleaseByID(ctx, releasedb.GetAPIReleaseByIDParams{ProjectID: projectID, ID: releaseID})
	if errors.Is(err, sql.ErrNoRows) {
		return release.Release{}, release.ErrNotFound
	}
	if err != nil {
		return release.Release{}, err
	}
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(dbrow.ProjectID), dbrow.Environment, dbrow.GenerationID)
	if err != nil {
		return release.Release{}, fmt.Errorf("release %q has invalid serving identity: %w", dbrow.ID, err)
	}
	row := release.Release{ID: dbrow.ID, ServingIdentity: identity, ProjectDigest: dbrow.ProjectDigest, ArtifactDigest: dbrow.ArtifactDigest, ActualDigest: dbrow.ArtifactActualDigest, ArtifactSizeBytes: dbrow.ArtifactSizeBytes, ArtifactUploadedAt: dbrow.ArtifactUploadedAt, RequestDigest: dbrow.RequestDigest, IdempotencyKey: dbrow.IdempotencyKey, Status: release.Status(dbrow.Status), CreatedBy: dbrow.CreatedBy, CreatedAt: dbrow.CreatedAt, FinalizedAt: dbrow.FinalizedAt, Error: dbrow.Error}
	if dbrow.ProvenanceJson != "" && dbrow.ProvenanceJson != "{}" {
		var p release.Provenance
		if err := json.Unmarshal([]byte(dbrow.ProvenanceJson), &p); err != nil {
			return release.Release{}, err
		}
		if err := p.Validate(); err != nil {
			return release.Release{}, err
		}
		row.Provenance = &p
	}
	pins, err := q.GetAPIReleaseConnections(ctx, row.ID)
	if err != nil {
		return release.Release{}, err
	}
	for _, pin := range pins {
		row.Manifest.Connections = append(row.Manifest.Connections, release.ConnectionPin{ConnectionID: pin.ConnectionID, RevisionID: pin.RevisionID})
	}
	return row, nil
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
