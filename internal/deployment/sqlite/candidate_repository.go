package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	platformdb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (r *Repository) StartCandidate(ctx context.Context, candidate deployment.Candidate, maxActivePerOwner int) (deployment.Candidate, bool, error) {
	return r.startCandidate(ctx, candidate, maxActivePerOwner, nil)
}

// StartCandidateWithClaim atomically establishes the instance project claim
// and creates/resumes the candidate. A claim conflict rolls the whole
// transaction back, so no candidate row can survive a failed binding.
func (r *Repository) StartCandidateWithClaim(ctx context.Context, candidate deployment.Candidate, maxActivePerOwner int, claim deployment.ProjectClaimInput) (deployment.Candidate, bool, error) {
	return r.startCandidate(ctx, candidate, maxActivePerOwner, &claim)
}

func (r *Repository) startCandidate(ctx context.Context, candidate deployment.Candidate, maxActivePerOwner int, claim *deployment.ProjectClaimInput) (deployment.Candidate, bool, error) {
	if r == nil || r.db == nil || maxActivePerOwner <= 0 {
		return deployment.Candidate{}, false, fmt.Errorf("candidate repository and positive quota are required")
	}
	if claim != nil {
		if err := claim.Validate(); err != nil {
			return deployment.Candidate{}, false, err
		}
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.Candidate{}, false, err
	}
	defer tx.Rollback()
	queries := r.queries.WithTx(tx)
	if claim != nil {
		if _, err := r.claimProjectTx(ctx, tx, *claim); err != nil {
			return deployment.Candidate{}, false, err
		}
	}
	now := formatCandidateTime(candidate.CreatedAt)
	if _, err := queries.ExpireProjectCandidates(ctx, platformdb.ExpireProjectCandidatesParams{
		ExpiredAt: nullableCandidateTime(candidate.CreatedAt), UpdatedAt: now,
		TargetID: candidate.TargetID, ExpiresAt: now,
	}); err != nil {
		return deployment.Candidate{}, false, err
	}
	// Expiry is an UPDATE and therefore acquires SQLite's write lock. Read the
	// active generation only after acquiring that lock: activation can either
	// commit before this transaction and be observed here, or wait until this
	// candidate commits. This explicit serialization fence prevents a cutover
	// from committing between the base read and candidate commit.
	baseScope, baseErr := r.activeCandidateBaseScopeTx(ctx, queries, candidate.Scope.ProjectID, candidate.Scope.Environment)
	if baseErr != nil {
		return deployment.Candidate{}, false, baseErr
	}
	candidate.Scope.ProjectID = baseScope.ProjectID
	candidate.Scope.Environment = baseScope.Environment
	candidate.Scope.BaseGenerationID = baseScope.BaseGenerationID
	if r.candidateBaseReadHook != nil {
		r.candidateBaseReadHook()
	}
	existing, err := queries.GetActiveProjectCandidateSession(ctx, platformdb.GetActiveProjectCandidateSessionParams{
		TargetID: candidate.TargetID, ProjectID: candidate.Scope.ProjectID.String(),
		OwnerPrincipalID: candidate.OwnerID, CandidateKey: candidate.Key,
	})
	if err == nil {
		mapped, mapErr := mapCandidate(existing)
		if mapErr != nil {
			return deployment.Candidate{}, false, mapErr
		}
		if sameCandidateStart(mapped, candidate) {
			return mapped, true, nil
		}
		// A candidate is scoped to the serving generation it was based on. If
		// activation advanced while the client was offline, retire the stale
		// row in this transaction before creating its replacement. This keeps
		// the active-session uniqueness fence intact for concurrent starts.
		if mapped.Scope.BaseGenerationID != candidate.Scope.BaseGenerationID {
			changed, cancelErr := queries.CancelSupersededProjectCandidate(ctx, platformdb.CancelSupersededProjectCandidateParams{
				CancelledAt: sql.NullString{String: now, Valid: true}, UpdatedAt: now, ID: mapped.ID,
			})
			if cancelErr != nil {
				return deployment.Candidate{}, false, cancelErr
			}
			if changed != 1 {
				return deployment.Candidate{}, false, deployment.ErrCandidateConflict
			}
			err = sql.ErrNoRows
		} else {
			return deployment.Candidate{}, false, fmt.Errorf("%w: active candidate must be updated explicitly", deployment.ErrCandidateConflict)
		}
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return deployment.Candidate{}, false, err
	}
	count, err := queries.CountActiveProjectCandidatesForOwner(ctx, candidate.OwnerID)
	if err != nil {
		return deployment.Candidate{}, false, err
	}
	if count >= int64(maxActivePerOwner) {
		return deployment.Candidate{}, false, deployment.ErrCandidateQuota
	}
	if err := queries.CreateProjectCandidate(ctx, candidateCreateParams(candidate)); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "constraint") {
			return deployment.Candidate{}, false, fmt.Errorf("%w: active candidate already exists", deployment.ErrCandidateConflict)
		}
		return deployment.Candidate{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.Candidate{}, false, err
	}
	return candidate, false, nil
}

func (r *Repository) ActiveCandidate(
	ctx context.Context,
	targetID string,
	projectID projectgraph.ResourceID,
	ownerID,
	key string,
) (deployment.Candidate, error) {
	if r == nil || r.queries == nil || targetID == "" || targetID != strings.TrimSpace(targetID) || projectID.Validate() != nil || projectID.String() != strings.TrimSpace(projectID.String()) || ownerID == "" || ownerID != strings.TrimSpace(ownerID) || key != strings.TrimSpace(key) {
		return deployment.Candidate{}, deployment.ErrCandidateNotFound
	}
	row, err := r.queries.GetActiveProjectCandidateByKey(
		ctx,
		platformdb.GetActiveProjectCandidateByKeyParams{
			TargetID: targetID, ProjectID: projectID.String(),
			OwnerPrincipalID: ownerID, CandidateKey: key,
		},
	)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Candidate{}, deployment.ErrCandidateNotFound
	}
	if err != nil {
		return deployment.Candidate{}, err
	}
	return mapCandidate(row)
}

func (r *Repository) CandidateByID(ctx context.Context, id string) (deployment.Candidate, error) {
	if r == nil || r.queries == nil || id == "" || id != strings.TrimSpace(id) {
		return deployment.Candidate{}, deployment.ErrCandidateNotFound
	}
	row, err := r.queries.GetProjectCandidate(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.Candidate{}, deployment.ErrCandidateNotFound
	}
	if err != nil {
		return deployment.Candidate{}, err
	}
	return mapCandidate(row)
}

func (r *Repository) ActiveCandidateBaseScope(ctx context.Context, projectID projectgraph.ResourceID, environment string) (deployment.CandidateScope, error) {
	if r == nil || r.queries == nil || projectID.Validate() != nil || environment == "" || environment != strings.TrimSpace(environment) {
		return deployment.CandidateScope{}, fmt.Errorf("candidate project and environment are required")
	}
	return r.activeCandidateBaseScopeTx(ctx, r.queries, projectID, environment)
}

type candidateGenerationQuerier interface {
	GetActiveProjectCandidateBaseGeneration(context.Context, platformdb.GetActiveProjectCandidateBaseGenerationParams) (string, error)
}

func (r *Repository) activeCandidateBaseScopeTx(ctx context.Context, q candidateGenerationQuerier, projectID projectgraph.ResourceID, environment string) (deployment.CandidateScope, error) {
	generation, err := q.GetActiveProjectCandidateBaseGeneration(ctx, platformdb.GetActiveProjectCandidateBaseGenerationParams{ProjectID: projectID.String(), Environment: environment})
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.CandidateScope{ProjectID: projectID, Environment: environment}, nil
	}
	if err != nil {
		return deployment.CandidateScope{}, err
	}
	return deployment.CandidateScope{ProjectID: projectID, Environment: environment, BaseGenerationID: generation}, nil
}

func (r *Repository) SaveCandidate(ctx context.Context, candidate deployment.Candidate, expectedRevision int64) (deployment.Candidate, error) {
	if r == nil || r.queries == nil || candidate.ID == "" || expectedRevision <= 0 || candidate.Revision != expectedRevision+1 {
		return deployment.Candidate{}, fmt.Errorf("%w: invalid candidate revision", deployment.ErrCandidateConflict)
	}
	count, err := r.queries.UpdateProjectCandidate(ctx, platformdb.UpdateProjectCandidateParams{
		ArtifactDigest:   candidate.ArtifactDigest,
		ProvenanceDigest: candidate.ProvenanceDigest,
		Status:           string(candidate.Status), FailureReason: candidate.FailureReason,
		ExpiresAt: formatCandidateTime(candidate.ExpiresAt), UpdatedAt: formatCandidateTime(candidate.UpdatedAt),
		ReadyAt: nullableCandidateTime(candidate.ReadyAt), CancelledAt: nullableCandidateTime(candidate.CancelledAt),
		ExpiredAt: nullableCandidateTime(candidate.ExpiredAt), Revision: candidate.Revision,
		ID: candidate.ID, Revision_2: expectedRevision,
	})
	if err != nil {
		return deployment.Candidate{}, err
	}
	if count != 1 {
		return deployment.Candidate{}, deployment.ErrCandidateConflict
	}
	return candidate, nil
}

func (r *Repository) ExpireCandidates(ctx context.Context, targetID string, now time.Time) (int64, error) {
	if r == nil || r.queries == nil || targetID == "" || targetID != strings.TrimSpace(targetID) || now.IsZero() {
		return 0, fmt.Errorf("candidate target and reconciliation time are required")
	}
	value := formatCandidateTime(now)
	return r.queries.ExpireProjectCandidates(ctx, platformdb.ExpireProjectCandidatesParams{
		ExpiredAt: nullableCandidateTime(now), UpdatedAt: value, TargetID: targetID, ExpiresAt: value,
	})
}

func candidateCreateParams(candidate deployment.Candidate) platformdb.CreateProjectCandidateParams {
	return platformdb.CreateProjectCandidateParams{
		ID: candidate.ID, ProjectID: candidate.Scope.ProjectID.String(), TargetID: candidate.TargetID,
		Environment: candidate.Scope.Environment, OwnerPrincipalID: candidate.OwnerID,
		CandidateKey:   candidate.Key,
		BaseGeneration: candidate.Scope.BaseGenerationID, ArtifactDigest: candidate.ArtifactDigest,
		ProvenanceDigest: candidate.ProvenanceDigest,
		Status:           string(candidate.Status), FailureReason: candidate.FailureReason,
		ExpiresAt: formatCandidateTime(candidate.ExpiresAt), CreatedAt: formatCandidateTime(candidate.CreatedAt),
		UpdatedAt: formatCandidateTime(candidate.UpdatedAt), ReadyAt: nullableCandidateTime(candidate.ReadyAt),
		CancelledAt: nullableCandidateTime(candidate.CancelledAt), ExpiredAt: nullableCandidateTime(candidate.ExpiredAt),
		Revision: candidate.Revision,
	}
}

func mapCandidate(row platformdb.ProjectCandidate) (deployment.Candidate, error) {
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate project: %w", err)
	}
	scope := deployment.CandidateScope{ProjectID: projectID, Environment: row.Environment, BaseGenerationID: row.BaseGeneration}
	if err := scope.Validate(); err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate scope: %w", err)
	}
	expiresAt, err := parseCandidateTime(row.ExpiresAt)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate expiry: %w", err)
	}
	createdAt, err := parseCandidateTime(row.CreatedAt)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate creation: %w", err)
	}
	updatedAt, err := parseCandidateTime(row.UpdatedAt)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate update: %w", err)
	}
	readyAt, err := parseNullableCandidateTime(row.ReadyAt)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate readiness: %w", err)
	}
	cancelledAt, err := parseNullableCandidateTime(row.CancelledAt)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate cancellation: %w", err)
	}
	expiredAt, err := parseNullableCandidateTime(row.ExpiredAt)
	if err != nil {
		return deployment.Candidate{}, fmt.Errorf("parse candidate expiration: %w", err)
	}
	candidate := deployment.Candidate{
		ID: row.ID, Key: row.CandidateKey, TargetID: row.TargetID,
		OwnerID: row.OwnerPrincipalID, Scope: scope, ArtifactDigest: row.ArtifactDigest,
		ProvenanceDigest: row.ProvenanceDigest,
		Status:           deployment.CandidateStatus(row.Status), FailureReason: row.FailureReason,
		ExpiresAt: expiresAt, CreatedAt: createdAt, UpdatedAt: updatedAt, ReadyAt: readyAt,
		CancelledAt: cancelledAt, ExpiredAt: expiredAt, Revision: row.Revision,
	}
	if err := candidate.Validate(); err != nil {
		return deployment.Candidate{}, err
	}
	return candidate, nil
}

func sameCandidateStart(existing, candidate deployment.Candidate) bool {
	return existing.Scope.ProjectID == candidate.Scope.ProjectID && existing.TargetID == candidate.TargetID &&
		existing.Scope.Environment == candidate.Scope.Environment && existing.Scope.BaseGenerationID == candidate.Scope.BaseGenerationID && existing.OwnerID == candidate.OwnerID &&
		existing.Key == candidate.Key &&
		existing.ArtifactDigest == candidate.ArtifactDigest
}

func formatCandidateTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

func nullableCandidateTime(value time.Time) sql.NullString {
	if value.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: formatCandidateTime(value), Valid: true}
}

func parseCandidateTime(value string) (time.Time, error) {
	return time.Parse(time.RFC3339Nano, value)
}

func parseNullableCandidateTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return parseCandidateTime(value.String)
}
