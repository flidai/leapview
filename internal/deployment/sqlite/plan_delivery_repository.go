package sqlite

// This file contains the SQLite control-plane adapter for plan-driven delivery.
// The adapter intentionally uses short, explicit transactions for every
// transition.  DuckLake catalogs and object-store bytes are outside these
// transactions; SQLite records the durable evidence and CAS fences only.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func deliveryTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func nullableDeliveryTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return deliveryTime(t)
}

func nullableString(t time.Time) sql.NullString {
	if t.IsZero() {
		return sql.NullString{}
	}
	return sql.NullString{String: deliveryTime(t), Valid: true}
}

func presentString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func parseDeliveryTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC(), nil
}

func parseNullableDeliveryTime(value sql.NullString) (time.Time, error) {
	if !value.Valid || strings.TrimSpace(value.String) == "" {
		return time.Time{}, nil
	}
	return parseDeliveryTime(value.String)
}

func deliveryConflict(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, deployment.ErrDeliveryConflict) || errors.Is(err, deployment.ErrDeliveryStale) || errors.Is(err, deployment.ErrDeliveryTransition) || errors.Is(err, deployment.ErrDeliveryInvalid) {
		return err
	}
	return err
}

// CreatePlan persists one canonical target-owned plan.  A retry with the same
// canonical digest returns the original row; an identity or digest collision
// with different canonical inputs is a conflict.
func (r *Repository) CreatePlan(ctx context.Context, input deployment.DeliveryPlan) (deployment.DeliveryPlan, error) {
	if r == nil || r.db == nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("delivery repository is not open")
	}
	plan, err := deployment.NewDeliveryPlan(input)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	executionJSON, err := json.Marshal(plan.Execution)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	provenanceJSON, err := json.Marshal(plan.Provenance)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	governanceJSON, err := json.Marshal(plan.Governance)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	evidenceJSON, err := json.Marshal(plan.Evidence)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	defer tx.Rollback()
	now := deliveryTime(plan.CreatedAt)
	err = deploydb.New(tx).EnsureDeliveryTargetRevision(ctx, deploydb.EnsureDeliveryTargetRevisionParams{TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	target, err := deploydb.New(tx).GetDeliveryTargetRevision(ctx, plan.TargetID)
	var targetProject, targetEnvironment, active string
	var revision int64
	if err == nil {
		targetProject, targetEnvironment, revision, active = target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	}
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if targetProject != plan.ProjectID.String() || targetEnvironment != plan.Environment {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: target scope differs", deployment.ErrDeliveryConflict)
	}
	if revision != plan.BaseTargetRevision || active != plan.BaseGenerationID {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: target base changed", deployment.ErrDeliveryStale)
	}
	actor := plan.ActorID
	if actor == "" {
		actor = plan.Provenance.Builder
	}
	if actor == "" {
		actor = "delivery"
	}
	plan.ActorID = actor
	err = deploydb.New(tx).CreateDeliveryPlan(ctx, deploydb.CreateDeliveryPlanParams{
		ID: plan.ID, TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment, ActorID: actor,
		OperationKind: string(plan.Operation), SourceDigest: plan.SourceDigest, NULLIF: plan.BaseGenerationID,
		BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest, ExecutionInputsJson: string(executionJSON),
		ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, ProvenanceJson: string(provenanceJSON),
		GovernanceJson: string(governanceJSON), EvidenceJson: string(evidenceJSON), EvidenceDigest: plan.EvidenceDigest,
		PlanDigest: plan.Digest, Status: string(plan.Status), ExpiresAt: deliveryTime(plan.Governance.ExpiresAt), CreatedAt: now,
	})
	if err != nil {
		// Resolve both idempotency dimensions while still holding the write lock.
		var existing deployment.DeliveryPlan
		if same, readErr := deliveryPlanByTargetDigestTx(ctx, tx, plan.TargetID, plan.Digest); readErr == nil {
			if same.Digest == plan.Digest {
				if commitErr := tx.Commit(); commitErr != nil {
					return deployment.DeliveryPlan{}, commitErr
				}
				return same, nil
			}
			existing = same
		}
		if byID, readErr := deliveryPlanByIDTx(ctx, tx, plan.ID); readErr == nil {
			if byID.SameCanonicalIntent(plan) {
				if commitErr := tx.Commit(); commitErr != nil {
					return deployment.DeliveryPlan{}, commitErr
				}
				return byID, nil
			}
			existing = byID
		}
		if existing.ID != "" {
			return deployment.DeliveryPlan{}, fmt.Errorf("%w: plan id or idempotency digest is already bound", deployment.ErrDeliveryConflict)
		}
		return deployment.DeliveryPlan{}, err
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, plan.Digest, "plan_created", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: actor, EventKind: "plan_created", ObjectKind: "plan", ObjectID: plan.ID,
		RequestDigest: plan.Digest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"base_revision": plan.BaseTargetRevision}, CreatedAt: plan.CreatedAt,
	}); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if plan.Operation == deployment.DeliveryOperationRestatement {
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(plan.TargetID, plan.Digest, "restatement_requested", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
			ActorID: actor, EventKind: "restatement_requested", ObjectKind: "plan", ObjectID: plan.ID,
			RequestDigest: plan.Digest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"status": string(plan.Status)}, CreatedAt: plan.CreatedAt,
		}); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	return plan, nil
}

func (r *Repository) CreateDeliveryPlan(ctx context.Context, input deployment.DeliveryPlan) (deployment.DeliveryPlan, error) {
	return r.CreatePlan(ctx, input)
}

func (r *Repository) PlanByID(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
	return deliveryPlanByIDTx(ctx, r.db, id)
}

func (r *Repository) GetPlan(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
	return r.PlanByID(ctx, id)
}

func deliveryPlanByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryPlan, error) {
	if strings.TrimSpace(id) == "" {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	row, err := deploydb.New(q).GetDeliveryPlan(ctx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	var plan deployment.DeliveryPlan
	if err := json.Unmarshal([]byte(row.ExecutionInputsJson), &plan.Execution); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if strings.TrimSpace(row.ProvenanceJson) != "" {
		if err := json.Unmarshal([]byte(row.ProvenanceJson), &plan.Provenance); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	if strings.TrimSpace(row.GovernanceJson) != "" {
		if err := json.Unmarshal([]byte(row.GovernanceJson), &plan.Governance); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	if strings.TrimSpace(row.EvidenceJson) != "" {
		if err := json.Unmarshal([]byte(row.EvidenceJson), &plan.Evidence); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	createdAt, err := parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	expiresAt, err := parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	plan.ID, plan.TargetID, plan.ProjectID, plan.Environment, plan.ActorID = row.ID, row.TargetID, projectID, row.Environment, row.ActorID
	plan.Operation, plan.SourceDigest, plan.BaseTargetRevision = deployment.DeliveryOperationKind(row.OperationKind), row.SourceDigest, row.BaseTargetRevision
	if row.BaseGenerationID.Valid {
		plan.BaseGenerationID = row.BaseGenerationID.String
	}
	plan.ExecutionDigest, plan.ProvenanceDigest, plan.GovernanceDigest, plan.Digest = row.ExecutionDigest, row.ProvenanceDigest, row.GovernanceDigest, row.PlanDigest
	plan.EvidenceDigest = row.EvidenceDigest
	plan.Status, plan.CreatedAt = deployment.DeliveryPlanStatus(row.Status), createdAt
	plan.Governance.ExpiresAt = expiresAt
	return plan, plan.Validate()
}

func deliveryPlanByTargetDigestTx(ctx context.Context, q deploydb.DBTX, targetID, digest string) (deployment.DeliveryPlan, error) {
	id, err := deploydb.New(q).GetDeliveryPlanIDByTargetDigest(ctx, deploydb.GetDeliveryPlanIDByTargetDigestParams{TargetID: targetID, PlanDigest: digest})
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	return deliveryPlanByIDTx(ctx, q, id)
}

func (r *Repository) PlanByTargetDigest(ctx context.Context, targetID, digest string) (deployment.DeliveryPlan, error) {
	return deliveryPlanByTargetDigestTx(ctx, r.db, targetID, digest)
}

func (r *Repository) ExpirePlan(ctx context.Context, id string, now time.Time) (deployment.DeliveryPlan, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	defer tx.Rollback()
	plan, err := deliveryPlanByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	updated, err := plan.Expire(now)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	res, err := deploydb.New(tx).ExpireDeliveryPlan(ctx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if plan.Status == deployment.DeliveryPlanExpired {
			if err := tx.Commit(); err != nil {
				return deployment.DeliveryPlan{}, err
			}
			return plan, nil
		}
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: plan expiry CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("plan-expired:" + plan.ID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, requestDigest, "plan_expired", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(plan.ActorID), EventKind: "plan_expired", ObjectKind: "plan", ObjectID: plan.ID, RequestDigest: requestDigest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"status": string(updated.Status)}, CreatedAt: now,
	}); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	return updated, nil
}

// CreateWriterLeaseAndBuildAttempt creates the lease and attempt under one
// transaction.  The composite foreign key makes it impossible to persist an
// attempt under another attempt's lease or physical pool.
func (r *Repository) CreateWriterLeaseAndBuildAttempt(ctx context.Context, lease deployment.DeliveryWriterLease, attempt deployment.DeliveryBuildAttempt) (deployment.DeliveryWriterLease, deployment.DeliveryBuildAttempt, error) {
	var zeroLease deployment.DeliveryWriterLease
	var zeroAttempt deployment.DeliveryBuildAttempt
	if lease.Epoch < 1 {
		lease.Epoch = 1 // the pool fence allocates the authoritative epoch below
	}
	lease, err := deployment.NewDeliveryWriterLease(lease)
	if err != nil {
		return zeroLease, zeroAttempt, err
	}
	attempt, err = deployment.NewDeliveryBuildAttempt(attempt)
	if err != nil {
		return zeroLease, zeroAttempt, err
	}
	if err := attempt.ValidateWriterLeaseBinding(lease); err != nil {
		return zeroLease, zeroAttempt, err
	}
	plan, err := r.PlanByID(ctx, attempt.PlanID)
	if err != nil {
		return zeroLease, zeroAttempt, err
	}
	if attempt.PlanDigest != plan.Digest || attempt.SourceDigest != plan.SourceDigest || attempt.ExecutionDigest != plan.ExecutionDigest || attempt.BaseGenerationID != plan.BaseGenerationID {
		return zeroLease, zeroAttempt, fmt.Errorf("%w: build does not match plan", deployment.ErrDeliveryConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return zeroLease, zeroAttempt, err
	}
	defer tx.Rollback()
	if err := r.ensurePoolFenceTx(ctx, tx, lease.PhysicalPoolID); err != nil {
		return zeroLease, zeroAttempt, err
	}
	// Retries resolve the exact durable lease before allocating a new epoch.
	// A released lease is replayable only once its exact build has reached the
	// sealed terminal state; released nonterminal work must not be revived.
	if existingLease, readErr := deliveryWriterLeaseByIDTx(ctx, tx, lease.ID); readErr == nil {
		existingAttempt, attemptErr := deliveryBuildAttemptByIDTx(ctx, tx, attempt.ID)
		leaseBindingMatches := existingLease.AttemptID == lease.AttemptID && existingLease.PhysicalPoolID == lease.PhysicalPoolID && existingLease.OwnerID == lease.OwnerID
		if attemptErr == nil && leaseBindingMatches && sameBuildAttemptIdentity(existingAttempt, attempt) {
			activeReplay := existingLease.Status == deployment.DeliveryLeaseActive && existingLease.ExpiresAt.After(lease.CreatedAt)
			sealedReplay := existingLease.Status == deployment.DeliveryLeaseReleased && existingAttempt.Status == deployment.DeliveryBuildSealed
			if activeReplay || sealedReplay {
				if err := tx.Commit(); err != nil {
					return zeroLease, zeroAttempt, err
				}
				existingAttempt, bindErr := r.populateBuildArtifactBinding(ctx, existingAttempt)
				return existingLease, existingAttempt, bindErr
			}
		}
		return zeroLease, zeroAttempt, fmt.Errorf("%w: %w: writer lease/build idempotency identity is already bound", deployment.ErrDeliveryIdempotencyDrift, deployment.ErrDeliveryConflict)
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return zeroLease, zeroAttempt, readErr
	}
	if err := deploydb.New(tx).ExpireDeliveryWriterLeasesForPool(ctx, deploydb.ExpireDeliveryWriterLeasesForPoolParams{ReleasedAt: presentString(deliveryTime(lease.CreatedAt)), PhysicalPoolID: lease.PhysicalPoolID, Julianday: deliveryTime(lease.CreatedAt)}); err != nil {
		return zeroLease, zeroAttempt, err
	}
	res, err := deploydb.New(tx).AdvanceDeliveryWriterEpoch(ctx, deploydb.AdvanceDeliveryWriterEpochParams{UpdatedAt: deliveryTime(lease.CreatedAt), PhysicalPoolID: lease.PhysicalPoolID, Julianday: deliveryTime(lease.CreatedAt)})
	if err != nil {
		return zeroLease, zeroAttempt, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return zeroLease, zeroAttempt, fmt.Errorf("%w: writer pool is fenced", deployment.ErrDeliveryConflict)
	}
	epoch, err := deploydb.New(tx).GetDeliveryWriterEpoch(ctx, lease.PhysicalPoolID)
	if err != nil {
		return zeroLease, zeroAttempt, err
	}
	lease.Epoch = epoch
	err = deploydb.New(tx).CreateDeliveryWriterLease(ctx, deploydb.CreateDeliveryWriterLeaseParams{ID: lease.ID, AttemptID: lease.AttemptID, PhysicalPoolID: lease.PhysicalPoolID, OwnerID: lease.OwnerID, Epoch: lease.Epoch, ExpiresAt: deliveryTime(lease.ExpiresAt), CreatedAt: deliveryTime(lease.CreatedAt)})
	if err != nil {
		return zeroLease, zeroAttempt, fmt.Errorf("%w: writer lease identity is already bound", deployment.ErrDeliveryConflict)
	}
	err = deploydb.New(tx).CreateDeliveryBuildAttempt(ctx, deploydb.CreateDeliveryBuildAttemptParams{ID: attempt.ID, PlanID: attempt.PlanID, IdempotencyKey: attempt.IdempotencyKey, PlanDigest: attempt.PlanDigest, SourceDigest: attempt.SourceDigest, ExecutionDigest: attempt.ExecutionDigest, NULLIF: attempt.BaseGenerationID, NULLIF_2: attempt.BaseCatalogDigest, NULLIF_3: attempt.BasePhysicalPoolID, PhysicalPoolID: attempt.PhysicalPoolID, WriterLeaseID: attempt.WriterLeaseID, CreatedAt: deliveryTime(attempt.CreatedAt), UpdatedAt: deliveryTime(attempt.UpdatedAt)})
	if err != nil {
		return zeroLease, zeroAttempt, fmt.Errorf("%w: build attempt identity is already bound", deployment.ErrDeliveryConflict)
	}
	buildRequestDigest := deployment.CanonicalDeliveryDigest([]byte("build-started:" + attempt.ID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, buildRequestDigest, "build_started", "build_attempt", attempt.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(lease.OwnerID), EventKind: "build_started", ObjectKind: "build_attempt", ObjectID: attempt.ID,
		RequestDigest: buildRequestDigest, PlanDigest: attempt.PlanDigest, ResultDigest: attempt.ExecutionDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(attempt.Status)}, CreatedAt: attempt.CreatedAt,
	}); err != nil {
		return zeroLease, zeroAttempt, err
	}
	if err := tx.Commit(); err != nil {
		return zeroLease, zeroAttempt, err
	}
	attempt, err = r.populateBuildArtifactBinding(ctx, attempt)
	return lease, attempt, err
}

func (r *Repository) DeliveryWriterLeaseByID(ctx context.Context, id string) (deployment.DeliveryWriterLease, error) {
	return deliveryWriterLeaseByIDTx(ctx, r.db, id)
}

func deliveryWriterLeaseByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryWriterLease, error) {
	row, err := deploydb.New(q).GetDeliveryWriterLease(ctx, id)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	lease := deployment.DeliveryWriterLease{ID: row.ID, AttemptID: row.AttemptID, PhysicalPoolID: row.PhysicalPoolID, OwnerID: row.OwnerID, Epoch: row.Epoch, Status: deployment.DeliveryLeaseStatus(row.Status)}
	lease.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	lease.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	lease.ReleasedAt, err = parseNullableDeliveryTime(row.ReleasedAt)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	return lease, lease.Validate()
}

func (r *Repository) TransitionWriterLease(ctx context.Context, id string, expectedStatus deployment.DeliveryLeaseStatus, next deployment.DeliveryLeaseStatus, now time.Time) (deployment.DeliveryWriterLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	defer tx.Rollback()
	lease, err := deliveryWriterLeaseByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	if lease.Status != expectedStatus {
		return deployment.DeliveryWriterLease{}, fmt.Errorf("%w: writer lease status changed", deployment.ErrDeliveryConflict)
	}
	var updated deployment.DeliveryWriterLease
	switch next {
	case deployment.DeliveryLeaseReleased:
		updated, err = lease.Release(now)
	case deployment.DeliveryLeaseExpired:
		updated, err = lease.Expire(now)
	default:
		return deployment.DeliveryWriterLease{}, fmt.Errorf("%w: unsupported writer lease transition", deployment.ErrDeliveryTransition)
	}
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	res, err := deploydb.New(tx).UpdateDeliveryWriterLeaseStatus(ctx, deploydb.UpdateDeliveryWriterLeaseStatusParams{Status: string(updated.Status), ReleasedAt: nullableString(updated.ReleasedAt), ID: id, Status_2: string(expectedStatus)})
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryWriterLease{}, fmt.Errorf("%w: writer lease CAS failed", deployment.ErrDeliveryConflict)
	}
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, lease.AttemptID)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, attempt.PlanID)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	eventKind := "lease_released"
	if next == deployment.DeliveryLeaseExpired {
		eventKind = "lease_expired"
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("lease:" + lease.ID + ":" + string(next)))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, requestDigest, eventKind, "writer_lease", lease.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(plan.ActorID), EventKind: eventKind, ObjectKind: "writer_lease", ObjectID: lease.ID, RequestDigest: requestDigest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"status": string(updated.Status)}, CreatedAt: now,
	}); err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	return updated, nil
}

func (r *Repository) HeartbeatWriterLease(ctx context.Context, id string, now, expiresAt time.Time) (deployment.DeliveryWriterLease, error) {
	lease, err := r.DeliveryWriterLeaseByID(ctx, id)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	updated, err := lease.Heartbeat(now, expiresAt)
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	res, err := r.queries.RenewDeliveryWriterLease(ctx, deploydb.RenewDeliveryWriterLeaseParams{ExpiresAt: deliveryTime(updated.ExpiresAt), ID: id, ExpiresAt_2: deliveryTime(lease.ExpiresAt)})
	if err != nil {
		return deployment.DeliveryWriterLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryWriterLease{}, fmt.Errorf("%w: writer heartbeat CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) TransitionBuildAttempt(ctx context.Context, id string, expectedRevision int64, next deployment.DeliveryBuildAttemptStatus, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	attempt, err := r.DeliveryBuildAttemptByID(ctx, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if attempt.Revision != expectedRevision {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build revision changed", deployment.ErrDeliveryConflict)
	}
	updated, err := attempt.Transition(next, now)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return r.saveBuildAttemptCAS(ctx, updated, expectedRevision)
}

func (r *Repository) MarkBuildFailed(ctx context.Context, id string, expectedRevision int64, code string, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	attempt, err := r.DeliveryBuildAttemptByID(ctx, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if attempt.Revision != expectedRevision {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build revision changed", deployment.ErrDeliveryConflict)
	}
	updated, err := attempt.MarkFailed(code, now)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return r.saveBuildAttemptCAS(ctx, updated, expectedRevision)
}

// MarkBuildFailedAndReleaseLease records a deterministic pre-seal failure and
// releases its exact writer lease in one transaction. Remote catalog roots
// are intentionally untouched; GC can retain/reconcile them independently.
func (r *Repository) MarkBuildFailedAndReleaseLease(ctx context.Context, id string, expectedRevision int64, lease deployment.DeliveryWriterLease, code string, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	if r == nil || r.db == nil {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("delivery repository is not open")
	}
	if now.IsZero() {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: failure time is required", deployment.ErrDeliveryInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	defer tx.Rollback()
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if attempt.Revision != expectedRevision {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build revision changed", deployment.ErrDeliveryConflict)
	}
	if attempt.WriterLeaseID != lease.ID || attempt.PhysicalPoolID != lease.PhysicalPoolID {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: writer lease does not match build attempt", deployment.ErrDeliveryConflict)
	}
	updated, err := attempt.MarkFailed(code, now)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if err := updated.Validate(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if _, err := deploydb.New(tx).UpdateDeliveryBuildAttempt(ctx, deploydb.UpdateDeliveryBuildAttemptParams{Status: string(updated.Status), NULLIF: updated.SealID, NULLIF_2: updated.CandidateID, FailureCode: updated.FailureCode, Revision: updated.Revision, UpdatedAt: deliveryTime(updated.UpdatedAt), TerminalAt: nullableString(updated.TerminalAt), ID: updated.ID, Revision_2: expectedRevision}); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	result, err := deploydb.New(tx).ReleaseDeliveryWriterLeaseExact(ctx, deploydb.ReleaseDeliveryWriterLeaseExactParams{ReleasedAt: presentString(deliveryTime(now)), ID: lease.ID, AttemptID: attempt.ID, PhysicalPoolID: attempt.PhysicalPoolID, OwnerID: lease.OwnerID, Epoch: lease.Epoch, Julianday: deliveryTime(now)})
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: exact writer lease release CAS failed", deployment.ErrDeliveryConflict)
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return updated, nil
}

func (r *Repository) AbandonBuild(ctx context.Context, id string, expectedRevision int64, code string, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	attempt, err := r.DeliveryBuildAttemptByID(ctx, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if attempt.Revision != expectedRevision {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build revision changed", deployment.ErrDeliveryConflict)
	}
	updated, err := attempt.Abandon(code, now)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return r.saveBuildAttemptCAS(ctx, updated, expectedRevision)
}

func (r *Repository) saveBuildAttemptCAS(ctx context.Context, attempt deployment.DeliveryBuildAttempt, expectedRevision int64) (deployment.DeliveryBuildAttempt, error) {
	if r == nil || r.db == nil {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("delivery repository is not open")
	}
	if err := attempt.Validate(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	defer tx.Rollback()
	res, err := deploydb.New(tx).UpdateDeliveryBuildAttempt(ctx, deploydb.UpdateDeliveryBuildAttemptParams{Status: string(attempt.Status), NULLIF: attempt.SealID, NULLIF_2: attempt.CandidateID, FailureCode: attempt.FailureCode, Revision: attempt.Revision, UpdatedAt: deliveryTime(attempt.UpdatedAt), TerminalAt: nullableString(attempt.TerminalAt), ID: attempt.ID, Revision_2: expectedRevision})
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build attempt CAS failed", deployment.ErrDeliveryConflict)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, attempt.PlanID)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	buildActor := plan.ActorID
	if lease, leaseErr := deliveryWriterLeaseByIDTx(ctx, tx, attempt.WriterLeaseID); leaseErr == nil && lease.OwnerID != "" {
		buildActor = lease.OwnerID
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte(fmt.Sprintf("build:%s:%d:%s", attempt.ID, expectedRevision, attempt.Status)))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, requestDigest, "build_transitioned", "build_attempt", attempt.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(buildActor), EventKind: "build_transitioned", ObjectKind: "build_attempt", ObjectID: attempt.ID,
		RequestDigest: requestDigest, PlanDigest: attempt.PlanDigest, ResultDigest: attempt.ExecutionDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(attempt.Status)}, CreatedAt: attempt.UpdatedAt,
	}); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return attempt, nil
}

func (r *Repository) DeliveryBuildAttemptByID(ctx context.Context, id string) (deployment.DeliveryBuildAttempt, error) {
	attempt, err := deliveryBuildAttemptByIDTx(ctx, r.db, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return r.populateBuildArtifactBinding(ctx, attempt)
}

func (r *Repository) populateBuildArtifactBinding(ctx context.Context, attempt deployment.DeliveryBuildAttempt) (deployment.DeliveryBuildAttempt, error) {
	binding, err := deploydb.New(r.db).GetDeliveryBuildArtifactBinding(ctx, attempt.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return attempt, nil
	}
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	attempt.ServingArtifactID, attempt.ServingArtifactDigest, attempt.ServingStateID = binding.ServingArtifactID, binding.ServingArtifactDigest, binding.ServingStateID
	if err := attempt.Validate(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return attempt, nil
}

// BindDeliveryBuildArtifacts durably records the serving identity after the
// build attempt exists. Repeated identical binds converge; a foreign bind is
// a typed conflict and can never replace the original identity.
func (r *Repository) BindDeliveryBuildArtifacts(ctx context.Context, attemptID string, expectedRevision int64, identity deployment.DeliveryArtifactIdentity, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	if now.IsZero() || now.Location() != time.UTC {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: binding time must be UTC", deployment.ErrDeliveryInvalid)
	}
	if identity.ServingArtifactID == "" || identity.ServingArtifactDigest == "" || identity.ServingStateID == "" {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: serving artifact identity is incomplete", deployment.ErrDeliveryInvalid)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	defer tx.Rollback()
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, attemptID)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, attempt.PlanID)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if attempt.Revision != expectedRevision {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build artifact binding revision changed", deployment.ErrDeliveryConflict)
	}
	binding, readErr := deploydb.New(tx).GetDeliveryBuildArtifactBinding(ctx, attemptID)
	if readErr == nil {
		attempt.ServingArtifactID, attempt.ServingArtifactDigest, attempt.ServingStateID = binding.ServingArtifactID, binding.ServingArtifactDigest, binding.ServingStateID
		if binding.ServingArtifactID != identity.ServingArtifactID || binding.ServingArtifactDigest != identity.ServingArtifactDigest || binding.ServingStateID != identity.ServingStateID {
			return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: serving artifact identity changed", deployment.ErrDeliveryConflict)
		}
		if err := tx.Commit(); err != nil {
			return deployment.DeliveryBuildAttempt{}, err
		}
		return attempt, attempt.Validate()
	}
	if !errors.Is(readErr, sql.ErrNoRows) {
		return deployment.DeliveryBuildAttempt{}, readErr
	}
	if err := deploydb.New(tx).CreateDeliveryBuildArtifactBinding(ctx, deploydb.CreateDeliveryBuildArtifactBindingParams{AttemptID: attempt.ID, ServingArtifactID: identity.ServingArtifactID, ServingArtifactDigest: identity.ServingArtifactDigest, ServingStateID: identity.ServingStateID, CreatedAt: deliveryTime(now)}); err != nil {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: bind serving artifact identity: %v", deployment.ErrDeliveryConflict, err)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("build-artifact-bound:" + attempt.ID + ":" + identity.ServingArtifactID + ":" + identity.ServingArtifactDigest))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{ID: deployment.DeliveryEventID(plan.TargetID, requestDigest, "build_artifact_bound", "build_attempt", attempt.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment, ActorID: eventActor(plan.ActorID), EventKind: "build_artifact_bound", ObjectKind: "build_attempt", ObjectID: attempt.ID, RequestDigest: requestDigest, PlanDigest: attempt.PlanDigest, ResultDigest: identity.ServingArtifactDigest, Outcome: "accepted", Details: map[string]any{"status": "bound"}, CreatedAt: now}); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	attempt.ServingArtifactID, attempt.ServingArtifactDigest, attempt.ServingStateID = identity.ServingArtifactID, identity.ServingArtifactDigest, identity.ServingStateID
	return attempt, attempt.Validate()
}

func deliveryBuildAttemptByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryBuildAttempt, error) {
	row, err := deploydb.New(q).GetDeliveryBuildAttempt(ctx, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	a := deployment.DeliveryBuildAttempt{ID: row.ID, PlanID: row.PlanID, IdempotencyKey: row.IdempotencyKey, PlanDigest: row.PlanDigest, SourceDigest: row.SourceDigest, ExecutionDigest: row.ExecutionDigest, PhysicalPoolID: row.PhysicalPoolID, WriterLeaseID: row.WriterLeaseID, Status: deployment.DeliveryBuildAttemptStatus(row.Status), Revision: row.Revision, FailureCode: row.FailureCode}
	if row.BaseGenerationID.Valid {
		a.BaseGenerationID = row.BaseGenerationID.String
	}
	if row.BaseCatalogDigest.Valid {
		a.BaseCatalogDigest = row.BaseCatalogDigest.String
	}
	if row.BasePhysicalPoolID.Valid {
		a.BasePhysicalPoolID = row.BasePhysicalPoolID.String
	}
	if row.SealID.Valid {
		a.SealID = row.SealID.String
	}
	if row.CandidateID.Valid {
		a.CandidateID = row.CandidateID.String
	}
	a.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	a.UpdatedAt, err = parseDeliveryTime(row.UpdatedAt)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	a.TerminalAt, err = parseNullableDeliveryTime(row.TerminalAt)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return a, a.Validate()
}

// PrepareCatalogSeal records the durable immutable seal identity before any
// remote upload is attempted.
func (r *Repository) PrepareCatalogSeal(ctx context.Context, seal deployment.CatalogSeal) (deployment.CatalogSeal, error) {
	attempt, err := r.DeliveryBuildAttemptByID(ctx, seal.AttemptID)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if seal.PlanID == "" {
		seal.PlanID = attempt.PlanID
	}
	seal, err = deployment.NewCatalogSeal(seal)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if err := seal.ValidateBuildBinding(attempt); err != nil {
		return deployment.CatalogSeal{}, err
	}
	err = r.queries.CreateDeliveryCatalogSeal(ctx, deploydb.CreateDeliveryCatalogSealParams{ID: seal.ID, AttemptID: seal.AttemptID, PlanID: seal.PlanID, PlanDigest: seal.PlanDigest, ExecutionDigest: seal.ExecutionDigest, PhysicalPoolID: seal.PhysicalPoolID, NULLIF: seal.CatalogDigest, NULLIF_2: seal.BaseCatalogDigest, NULLIF_3: seal.BasePhysicalPoolID, CompatibilityDigest: seal.CompatibilityDigest, ObjectKey: seal.ObjectKey, ObjectSize: seal.ObjectSize, ServingArtifactID: seal.ServingArtifactID, ServingArtifactDigest: seal.ServingArtifactDigest, ServingStateID: seal.ServingStateID, CreatedAt: deliveryTime(seal.CreatedAt)})
	if err != nil {
		if existing, readErr := r.DeliveryCatalogSealByID(ctx, seal.ID); readErr == nil && sameSealIdentity(existing, seal) {
			return existing, nil
		}
		return deployment.CatalogSeal{}, fmt.Errorf("%w: catalog seal identity conflict", deployment.ErrDeliveryConflict)
	}
	return seal, nil
}

func sameSealIdentity(a, b deployment.CatalogSeal) bool {
	return a.ID == b.ID && a.AttemptID == b.AttemptID && a.PlanDigest == b.PlanDigest && a.ExecutionDigest == b.ExecutionDigest && a.PhysicalPoolID == b.PhysicalPoolID && a.CatalogDigest == b.CatalogDigest && a.BaseCatalogDigest == b.BaseCatalogDigest && a.BasePhysicalPoolID == b.BasePhysicalPoolID && a.CompatibilityDigest == b.CompatibilityDigest && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.ServingStateID == b.ServingStateID && a.ObjectKey == b.ObjectKey && a.ObjectSize == b.ObjectSize
}

func (r *Repository) DeliveryCatalogSealByID(ctx context.Context, id string) (deployment.CatalogSeal, error) {
	return deliveryCatalogSealByIDTx(ctx, r.db, id)
}

func deliveryCatalogSealByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.CatalogSeal, error) {
	row, err := deploydb.New(q).GetDeliveryCatalogSeal(ctx, id)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	s := deployment.CatalogSeal{ID: row.ID, AttemptID: row.AttemptID, PlanID: row.PlanID, PlanDigest: row.PlanDigest, ExecutionDigest: row.ExecutionDigest, PhysicalPoolID: row.PhysicalPoolID, CatalogDigest: row.CatalogDigest, CompatibilityDigest: row.CompatibilityDigest, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest, ServingStateID: row.ServingStateID, ObjectKey: row.ObjectKey, ObjectSize: row.ObjectSize, Status: deployment.CatalogSealStatus(row.Status), FailureCode: row.FailureCode}
	if row.BaseCatalogDigest.Valid {
		s.BaseCatalogDigest = row.BaseCatalogDigest.String
	}
	if row.BasePhysicalPoolID.Valid {
		s.BasePhysicalPoolID = row.BasePhysicalPoolID.String
	}
	if row.ClosureDigest.Valid {
		s.ClosureDigest = row.ClosureDigest.String
	}
	if row.QualificationDigest.Valid {
		s.QualificationDigest = row.QualificationDigest.String
	}
	s.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	s.VerifiedAt, err = parseNullableDeliveryTime(row.VerifiedAt)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	return s, s.Validate()
}

func (r *Repository) MarkCatalogSealUploaded(ctx context.Context, id string) (deployment.CatalogSeal, error) {
	seal, err := r.DeliveryCatalogSealByID(ctx, id)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if seal.Status == deployment.CatalogSealUploaded {
		return seal, nil
	}
	updated, err := seal.MarkUploaded()
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	res, err := r.queries.MarkDeliveryCatalogSealUploaded(ctx, id)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.CatalogSeal{}, fmt.Errorf("%w: seal upload CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) VerifyCatalogSeal(ctx context.Context, id, closureDigest, qualificationDigest string, now time.Time) (deployment.CatalogSeal, error) {
	seal, err := r.DeliveryCatalogSealByID(ctx, id)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	updated, err := seal.MarkVerified(closureDigest, qualificationDigest, now)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if seal.Status == deployment.CatalogSealVerified {
		return seal, nil
	}
	res, err := r.queries.MarkDeliveryCatalogSealVerified(ctx, deploydb.MarkDeliveryCatalogSealVerifiedParams{ClosureDigest: presentString(updated.ClosureDigest), QualificationDigest: presentString(updated.QualificationDigest), VerifiedAt: presentString(deliveryTime(updated.VerifiedAt)), ID: id})
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.CatalogSeal{}, fmt.Errorf("%w: seal verify CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) FailCatalogSeal(ctx context.Context, id, code string) (deployment.CatalogSeal, error) {
	seal, err := r.DeliveryCatalogSealByID(ctx, id)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	updated, err := seal.MarkFailed(code)
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if seal.Status == deployment.CatalogSealFailed {
		return seal, nil
	}
	res, err := r.queries.FailDeliveryCatalogSeal(ctx, deploydb.FailDeliveryCatalogSealParams{FailureCode: updated.FailureCode, ID: id})
	if err != nil {
		return deployment.CatalogSeal{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.CatalogSeal{}, fmt.Errorf("%w: seal failure CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) CreateCandidate(ctx context.Context, candidate deployment.DeliveryCandidate) (deployment.DeliveryCandidate, error) {
	// A candidate is a queryable root only after remote verification.  The
	// composite seal FK intentionally prevents a preparatory row from being
	// left behind while the seal's qualification digest changes.
	seal, sealErr := r.DeliveryCatalogSealByID(ctx, candidate.SealID)
	if sealErr != nil {
		return deployment.DeliveryCandidate{}, sealErr
	}
	if seal.Status != deployment.CatalogSealVerified {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: candidate requires a verified catalog seal", deployment.ErrDeliveryTransition)
	}
	return r.CreateCandidateReady(ctx, candidate, seal, candidate.CreatedAt)
}

func sameCandidateIdentity(a, b deployment.DeliveryCandidate) bool {
	return a.ID == b.ID && a.PlanID == b.PlanID && a.PlanDigest == b.PlanDigest && a.SealID == b.SealID && a.CatalogDigest == b.CatalogDigest && a.CatalogObjectKey == b.CatalogObjectKey && a.PhysicalPoolID == b.PhysicalPoolID && a.CompatibilityDigest == b.CompatibilityDigest && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.ServingStateID == b.ServingStateID
}

func (r *Repository) DeliveryCandidateByID(ctx context.Context, id string) (deployment.DeliveryCandidate, error) {
	return deliveryCandidateByIDTx(ctx, r.db, id)
}
func deliveryCandidateByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryCandidate, error) {
	row, err := deploydb.New(q).GetDeliveryCandidate(ctx, id)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	c := deployment.DeliveryCandidate{ID: row.ID, PlanID: row.PlanID, PlanDigest: row.PlanDigest, TargetID: row.TargetID, Environment: row.Environment, SourceDigest: row.SourceDigest, ExecutionDigest: row.ExecutionDigest, BaseTargetRevision: row.BaseTargetRevision, SealID: row.SealID, CatalogDigest: row.CatalogDigest, CompatibilityDigest: row.CompatibilityDigest, CatalogObjectKey: row.CatalogObjectKey, PhysicalPoolID: row.PhysicalPoolID, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest, ServingStateID: row.ServingStateID, Status: deployment.DeliveryCandidateStatus(row.Status), FailureCode: row.FailureCode}
	c.ProjectID, err = projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if row.BaseGenerationID.Valid {
		c.BaseGenerationID = row.BaseGenerationID.String
	}
	if row.BaseCatalogDigest.Valid {
		c.BaseCatalogDigest = row.BaseCatalogDigest.String
	}
	if row.BasePhysicalPoolID.Valid {
		c.BasePhysicalPoolID = row.BasePhysicalPoolID.String
	}
	if row.QualificationDigest.Valid {
		c.QualificationDigest = row.QualificationDigest.String
	}
	if strings.TrimSpace(row.ResolvedInputsJson) != "" {
		if err := json.Unmarshal([]byte(row.ResolvedInputsJson), &c.ResolvedInputs); err != nil {
			return deployment.DeliveryCandidate{}, err
		}
	}
	c.ResolvedInputs.EvidenceDigest = row.ResolvedInputsDigest
	c.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	c.ReadyAt, err = parseNullableDeliveryTime(row.ReadyAt)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	c.RetiredAt, err = parseNullableDeliveryTime(row.RetiredAt)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	return c, c.Validate()
}

// MarkCandidateReady is one control transaction: verified seal, ready
// candidate, and sealed build attempt become visible together.
func (r *Repository) MarkCandidateReady(ctx context.Context, candidateID, sealID string, now time.Time) (deployment.DeliveryCandidate, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	defer tx.Rollback()
	candidate, err := deliveryCandidateByIDTx(ctx, tx, candidateID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	seal, err := deliveryCatalogSealByIDTx(ctx, tx, sealID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	updated, err := candidate.MarkReady(seal, now)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, seal.AttemptID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	sealedAttempt, err := attempt.SealCandidate(seal.ID, candidate.ID, now)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	res, err := deploydb.New(tx).MarkDeliveryCandidateReady(ctx, deploydb.MarkDeliveryCandidateReadyParams{QualificationDigest: presentString(updated.QualificationDigest), ReadyAt: presentString(deliveryTime(updated.ReadyAt)), ID: candidate.ID})
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: candidate ready CAS failed", deployment.ErrDeliveryConflict)
	}
	res, err = deploydb.New(tx).SealDeliveryBuildAttempt(ctx, deploydb.SealDeliveryBuildAttemptParams{SealID: sql.NullString{String: sealedAttempt.SealID, Valid: true}, CandidateID: sql.NullString{String: sealedAttempt.CandidateID, Valid: true}, Revision: sealedAttempt.Revision, UpdatedAt: deliveryTime(sealedAttempt.UpdatedAt), TerminalAt: nullableString(sealedAttempt.UpdatedAt), ID: attempt.ID, Revision_2: attempt.Revision})
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: build seal CAS failed", deployment.ErrDeliveryConflict)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, candidate.PlanID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	buildActor := plan.ActorID
	if lease, leaseErr := deliveryWriterLeaseByIDTx(ctx, tx, attempt.WriterLeaseID); leaseErr == nil && lease.OwnerID != "" {
		buildActor = lease.OwnerID
	}
	qualificationRequest := deployment.CanonicalDeliveryDigest([]byte("qualification:" + updated.ID + ":" + updated.QualificationDigest))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, qualificationRequest, "candidate_qualified", "candidate", updated.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(buildActor), EventKind: "candidate_qualified", ObjectKind: "candidate", ObjectID: updated.ID,
		RequestDigest: qualificationRequest, PlanDigest: updated.PlanDigest, ResultDigest: updated.QualificationDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(updated.Status)}, CreatedAt: updated.ReadyAt,
	}); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	sealRequest := deployment.CanonicalDeliveryDigest([]byte("seal:" + updated.ID + ":" + updated.SealID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, sealRequest, "candidate_sealed", "candidate", updated.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(buildActor), EventKind: "candidate_sealed", ObjectKind: "candidate", ObjectID: updated.ID,
		RequestDigest: sealRequest, PlanDigest: updated.PlanDigest, ResultDigest: updated.CatalogDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(updated.Status)}, CreatedAt: updated.ReadyAt,
	}); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	return updated, nil
}

func (r *Repository) CreateCandidateReady(ctx context.Context, candidate deployment.DeliveryCandidate, seal deployment.CatalogSeal, now time.Time) (deployment.DeliveryCandidate, error) {
	candidate, err := deployment.NewDeliveryCandidate(candidate)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	plan, err := r.PlanByID(ctx, candidate.PlanID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if candidate.PlanDigest != plan.Digest || candidate.TargetID != plan.TargetID || candidate.ProjectID != plan.ProjectID || candidate.Environment != plan.Environment {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: candidate does not match plan", deployment.ErrDeliveryConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	defer tx.Rollback()
	storedSeal, err := deliveryCatalogSealByIDTx(ctx, tx, seal.ID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	ready, err := candidate.MarkReady(storedSeal, now)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if strings.TrimSpace(ready.ServingStateID) == "" {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: serving state identity is required", deployment.ErrDeliveryInvalid)
	}
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, storedSeal.AttemptID)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	buildActor := plan.ActorID
	if lease, leaseErr := deliveryWriterLeaseByIDTx(ctx, tx, attempt.WriterLeaseID); leaseErr == nil && lease.OwnerID != "" {
		buildActor = lease.OwnerID
	}
	sealedAttempt, err := attempt.SealCandidate(storedSeal.ID, ready.ID, now)
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	resolvedJSON, marshalErr := json.Marshal(ready.ResolvedInputs)
	if marshalErr != nil {
		return deployment.DeliveryCandidate{}, marshalErr
	}
	err = deploydb.New(tx).CreateDeliveryCandidateReady(ctx, deploydb.CreateDeliveryCandidateReadyParams{ID: ready.ID, PlanID: ready.PlanID, PlanDigest: ready.PlanDigest, TargetID: ready.TargetID, ProjectID: ready.ProjectID.String(), Environment: ready.Environment, SourceDigest: ready.SourceDigest, ExecutionDigest: ready.ExecutionDigest, NULLIF: ready.BaseGenerationID, BaseTargetRevision: ready.BaseTargetRevision, SealID: ready.SealID, CatalogDigest: ready.CatalogDigest, NULLIF_2: ready.BaseCatalogDigest, NULLIF_3: ready.BasePhysicalPoolID, CompatibilityDigest: ready.CompatibilityDigest, CatalogObjectKey: ready.CatalogObjectKey, PhysicalPoolID: ready.PhysicalPoolID, ServingArtifactID: ready.ServingArtifactID, ServingArtifactDigest: ready.ServingArtifactDigest, ServingStateID: ready.ServingStateID, QualificationDigest: sql.NullString{String: ready.QualificationDigest, Valid: true}, ResolvedInputsJson: string(resolvedJSON), ResolvedInputsDigest: ready.ResolvedInputs.EvidenceDigest, CreatedAt: deliveryTime(ready.CreatedAt), ReadyAt: sql.NullString{String: deliveryTime(ready.ReadyAt), Valid: true}})
	if err != nil {
		if existing, readErr := deliveryCandidateByIDTx(ctx, tx, ready.ID); readErr == nil && existing.Status == deployment.DeliveryCandidateReady && sameCandidateIdentity(existing, ready) {
			if commitErr := tx.Commit(); commitErr != nil {
				return deployment.DeliveryCandidate{}, commitErr
			}
			return existing, nil
		}
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: candidate identity conflict: %v", deployment.ErrDeliveryConflict, err)
	}
	res, err := deploydb.New(tx).SealDeliveryBuildAttempt(ctx, deploydb.SealDeliveryBuildAttemptParams{SealID: sql.NullString{String: sealedAttempt.SealID, Valid: true}, CandidateID: sql.NullString{String: sealedAttempt.CandidateID, Valid: true}, Revision: sealedAttempt.Revision, UpdatedAt: deliveryTime(sealedAttempt.UpdatedAt), TerminalAt: nullableString(sealedAttempt.TerminalAt), ID: attempt.ID, Revision_2: attempt.Revision})
	if err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryCandidate{}, fmt.Errorf("%w: build seal CAS failed", deployment.ErrDeliveryConflict)
	}
	qualificationRequest := deployment.CanonicalDeliveryDigest([]byte("qualification:" + ready.ID + ":" + ready.QualificationDigest))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, qualificationRequest, "candidate_qualified", "candidate", ready.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(buildActor), EventKind: "candidate_qualified", ObjectKind: "candidate", ObjectID: ready.ID,
		RequestDigest: qualificationRequest, PlanDigest: ready.PlanDigest, ResultDigest: ready.QualificationDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(ready.Status)}, CreatedAt: ready.ReadyAt,
	}); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	sealRequest := deployment.CanonicalDeliveryDigest([]byte("seal:" + ready.ID + ":" + ready.SealID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, sealRequest, "candidate_sealed", "candidate", ready.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(buildActor), EventKind: "candidate_sealed", ObjectKind: "candidate", ObjectID: ready.ID,
		RequestDigest: sealRequest, PlanDigest: ready.PlanDigest, ResultDigest: ready.CatalogDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(ready.Status)}, CreatedAt: ready.ReadyAt,
	}); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryCandidate{}, err
	}
	return ready, nil
}

func (r *Repository) RetireDeliveryCandidate(ctx context.Context, id string, now time.Time) (deployment.DeliveryCandidate, error) {
	return r.retireDeliveryCandidateFenced(ctx, id, now)
}

// CreatePublication creates the prepared generation and publication intent in
// one transaction.  The variadic generation argument keeps the adapter useful
// to callers that only have a candidate (the generation is then derived from
// it) while allowing operators to provide explicit rollback evidence.
func (r *Repository) CreatePublication(ctx context.Context, input deployment.DeliveryPublication, supplied ...deployment.DeliveryGeneration) (deployment.DeliveryPublication, error) {
	publication, err := deployment.NewDeliveryPublication(input)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	plan, err := r.PlanByID(ctx, publication.PlanID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if publication.ActorID == "" {
		publication.ActorID = plan.ActorID
	}
	candidate, err := r.DeliveryCandidateByID(ctx, publication.CandidateID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if candidate.Status != deployment.DeliveryCandidateReady || candidate.PlanID != plan.ID || candidate.PlanDigest != publication.PlanDigest || candidate.TargetID != publication.TargetID || candidate.ProjectID != publication.ProjectID || candidate.Environment != publication.Environment {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication candidate is not the exact ready candidate", deployment.ErrDeliveryConflict)
	}
	var generation deployment.DeliveryGeneration
	if len(supplied) > 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: one generation may be supplied", deployment.ErrDeliveryInvalid)
	}
	if len(supplied) == 1 {
		generation = supplied[0]
	} else {
		rollbackClass, rollbackUntil, rollbackEffects, evidenceErr := rollbackEvidenceForPlan(plan, publication.CreatedAt)
		if evidenceErr != nil {
			return deployment.DeliveryPublication{}, evidenceErr
		}
		generation = deployment.DeliveryGeneration{
			ID: publication.GenerationID, CandidateID: candidate.ID, PlanID: plan.ID, PlanDigest: plan.Digest,
			TargetID: candidate.TargetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment,
			CatalogDigest: candidate.CatalogDigest, CatalogObjectKey: candidate.CatalogObjectKey,
			PhysicalPoolID: candidate.PhysicalPoolID, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest, ServingStateID: candidate.ServingStateID, CompatibilityDigest: candidate.CompatibilityDigest, RollbackClass: rollbackClass, RollbackExternalEffects: rollbackEffects, RollbackUntil: rollbackUntil,
			CreatedAt: publication.CreatedAt,
		}
	}
	generation, err = deployment.NewDeliveryGeneration(generation)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	expectedClass, expectedUntil, expectedEffects, evidenceErr := rollbackEvidenceForPlan(plan, generation.CreatedAt)
	if evidenceErr != nil {
		return deployment.DeliveryPublication{}, evidenceErr
	}
	if generation.RollbackClass != expectedClass || !generation.RollbackUntil.Equal(expectedUntil) || !sameStringSlice(generation.RollbackExternalEffects, expectedEffects) {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation rollback evidence differs from reviewed plan", deployment.ErrDeliveryConflict)
	}
	if generation.ID != publication.GenerationID || generation.CandidateID != candidate.ID || generation.PlanID != plan.ID || generation.PlanDigest != plan.Digest || generation.TargetID != candidate.TargetID || generation.ProjectID != candidate.ProjectID || generation.Environment != candidate.Environment || generation.CatalogDigest != candidate.CatalogDigest || generation.CatalogObjectKey != candidate.CatalogObjectKey || generation.PhysicalPoolID != candidate.PhysicalPoolID || generation.ServingArtifactID != candidate.ServingArtifactID || generation.ServingArtifactDigest != candidate.ServingArtifactDigest || generation.ServingStateID != candidate.ServingStateID || generation.CompatibilityDigest != candidate.CompatibilityDigest {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation does not match candidate", deployment.ErrDeliveryConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	// Resolve a committed request before evaluating mutable plan expiry. A
	// retry after the governance window has elapsed must return the original
	// durable result, while a new request is checked against authoritative now
	// below. Identity drift never converges silently.
	if existing, readErr := deliveryPublicationByIDTx(ctx, tx, publication.ID); readErr == nil {
		if !samePublicationIdentity(existing, publication) {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication request identity conflict", deployment.ErrDeliveryConflict)
		}
		if existing.Status == deployment.DeliveryPublicationCommitted {
			if err := tx.Commit(); err != nil {
				return deployment.DeliveryPublication{}, err
			}
			return existing, nil
		}
		if existing.Status != deployment.DeliveryPublicationPending && existing.Status != deployment.DeliveryPublicationIndeterminate {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication request is %s", deployment.ErrDeliveryTransition, existing.Status)
		}
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return deployment.DeliveryPublication{}, readErr
	}
	var active string
	var revision int64
	var targetProject, targetEnvironment string
	target, targetErr := deploydb.New(tx).GetDeliveryTargetRevision(ctx, publication.TargetID)
	err = targetErr
	if err == nil {
		targetProject, targetEnvironment, revision, active = target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	}
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	now := time.Now().UTC()
	if r != nil && r.deliveryNow != nil {
		now = r.deliveryNow().UTC()
	}
	if err := candidate.PublicationEligible(plan, active, revision, now); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if targetProject != publication.ProjectID.String() || targetEnvironment != publication.Environment || active != publication.ExpectedBaseGenerationID || revision != publication.ExpectedTargetRevision {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication target fence changed", deployment.ErrDeliveryStale)
	}
	externalEffectsJSON, marshalEffectsErr := json.Marshal(generation.RollbackExternalEffects)
	if marshalEffectsErr != nil {
		return deployment.DeliveryPublication{}, marshalEffectsErr
	}
	err = deploydb.New(tx).CreateDeliveryGeneration(ctx, deploydb.CreateDeliveryGenerationParams{ID: generation.ID, CandidateID: generation.CandidateID, PlanID: generation.PlanID, PlanDigest: generation.PlanDigest, TargetID: generation.TargetID, ProjectID: generation.ProjectID.String(), Environment: generation.Environment, CatalogDigest: generation.CatalogDigest, CatalogObjectKey: generation.CatalogObjectKey, PhysicalPoolID: generation.PhysicalPoolID, ServingArtifactID: generation.ServingArtifactID, ServingArtifactDigest: generation.ServingArtifactDigest, ServingStateID: generation.ServingStateID, CompatibilityDigest: generation.CompatibilityDigest, RollbackClass: string(generation.RollbackClass), RollbackExternalEffectsJson: string(externalEffectsJSON), CreatedAt: deliveryTime(generation.CreatedAt), ActivatedAt: sql.NullString{}, RollbackUntil: nullableString(generation.RollbackUntil)})
	if err != nil {
		if existing, readErr := deliveryGenerationByIDTx(ctx, tx, generation.ID); readErr == nil && sameGenerationIdentity(existing, generation) {
			// A retry may have created the prepared root before its response was
			// lost. Continue to the publication idempotency lookup.
		} else {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation identity conflict", deployment.ErrDeliveryConflict)
		}
	}
	err = deploydb.New(tx).CreateDeliveryPublication(ctx, deploydb.CreateDeliveryPublicationParams{ID: publication.ID, RequestDigest: publication.RequestDigest, TargetID: publication.TargetID, ProjectID: publication.ProjectID.String(), Environment: publication.Environment, PlanID: publication.PlanID, PlanDigest: publication.PlanDigest, CandidateID: publication.CandidateID, GenerationID: publication.GenerationID, NULLIF: publication.ExpectedBaseGenerationID, ExpectedTargetRevision: publication.ExpectedTargetRevision, CreatedAt: deliveryTime(publication.CreatedAt)})
	if err != nil {
		if existing, readErr := deliveryPublicationByRequestTx(ctx, tx, publication.TargetID, publication.RequestDigest); readErr == nil {
			if samePublicationIdentity(existing, publication) {
				if commitErr := tx.Commit(); commitErr != nil {
					return deployment.DeliveryPublication{}, commitErr
				}
				return existing, nil
			}
		}
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication idempotency conflict", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(publication.TargetID, publication.RequestDigest, "publish_requested", "publication", publication.ID), TargetID: publication.TargetID, ProjectID: publication.ProjectID.String(), Environment: publication.Environment,
		ActorID: eventActor(publication.ActorID), EventKind: "publish_requested", ObjectKind: "publication", ObjectID: publication.ID,
		RequestDigest: publication.RequestDigest, PlanDigest: publication.PlanDigest, Outcome: "accepted",
		Details: map[string]any{"candidate_id": publication.CandidateID, "generation_id": publication.GenerationID}, CreatedAt: publication.CreatedAt,
	}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return publication, nil
}

func rollbackEvidenceForPlan(plan deployment.DeliveryPlan, createdAt time.Time) (deployment.DeliveryRollbackClass, time.Time, []string, error) {
	class := plan.Evidence.Rollback.Class
	if class == "" {
		return "", time.Time{}, nil, fmt.Errorf("%w: delivery plan rollback class is missing", deployment.ErrDeliveryInvalid)
	}
	var until time.Time
	if window := strings.TrimSpace(plan.Evidence.Rollback.RetentionWindow); window != "" {
		duration, err := time.ParseDuration(window)
		if err != nil || duration <= 0 {
			return "", time.Time{}, nil, fmt.Errorf("%w: invalid rollback retention window %q", deployment.ErrDeliveryInvalid, window)
		}
		until = createdAt.Add(duration)
	}
	effects := append([]string(nil), plan.Evidence.Rollback.ExternalEffects...)
	if effects == nil {
		effects = []string{}
	}
	sort.Strings(effects)
	return class, until, effects, nil
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *Repository) CreatePublicationIntent(ctx context.Context, input deployment.DeliveryPublication, supplied ...deployment.DeliveryGeneration) (deployment.DeliveryPublication, error) {
	return r.CreatePublication(ctx, input, supplied...)
}

func samePublicationIdentity(a, b deployment.DeliveryPublication) bool {
	return a.ID == b.ID && a.RequestDigest == b.RequestDigest && a.TargetID == b.TargetID && a.ProjectID == b.ProjectID && a.Environment == b.Environment && a.PlanID == b.PlanID && a.PlanDigest == b.PlanDigest && a.CandidateID == b.CandidateID && a.GenerationID == b.GenerationID && a.ExpectedBaseGenerationID == b.ExpectedBaseGenerationID && a.ExpectedTargetRevision == b.ExpectedTargetRevision
}

func (r *Repository) DeliveryPublicationByID(ctx context.Context, id string) (deployment.DeliveryPublication, error) {
	return deliveryPublicationByIDTx(ctx, r.db, id)
}

func (r *Repository) DeliveryPublicationByRequest(ctx context.Context, targetID, requestDigest string) (deployment.DeliveryPublication, error) {
	return deliveryPublicationByRequestTx(ctx, r.db, targetID, requestDigest)
}

func deliveryPublicationByRequestTx(ctx context.Context, q deploydb.DBTX, targetID, requestDigest string) (deployment.DeliveryPublication, error) {
	id, err := deploydb.New(q).GetDeliveryPublicationIDByTargetDigest(ctx, deploydb.GetDeliveryPublicationIDByTargetDigestParams{TargetID: targetID, RequestDigest: requestDigest})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return deliveryPublicationByIDTx(ctx, q, id)
}

func deliveryPublicationByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryPublication, error) {
	row, err := deploydb.New(q).GetDeliveryPublication(ctx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	p := deployment.DeliveryPublication{ID: row.ID, RequestDigest: row.RequestDigest, TargetID: row.TargetID, Environment: row.Environment, PlanID: row.PlanID, PlanDigest: row.PlanDigest, CandidateID: row.CandidateID, GenerationID: row.GenerationID, ExpectedTargetRevision: row.ExpectedTargetRevision, ResultTargetRevision: row.ResultTargetRevision, Status: deployment.DeliveryPublicationStatus(row.Status), Reason: row.Reason}
	p.ProjectID, err = projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if row.ExpectedBaseGenerationID.Valid {
		p.ExpectedBaseGenerationID = row.ExpectedBaseGenerationID.String
	}
	p.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	p.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return p, p.Validate()
}

func sameGenerationIdentity(a, b deployment.DeliveryGeneration) bool {
	return a.ID == b.ID && a.CandidateID == b.CandidateID && a.PlanID == b.PlanID && a.PlanDigest == b.PlanDigest && a.TargetID == b.TargetID && a.ProjectID == b.ProjectID && a.Environment == b.Environment && a.CatalogDigest == b.CatalogDigest && a.CatalogObjectKey == b.CatalogObjectKey && a.PhysicalPoolID == b.PhysicalPoolID && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.ServingStateID == b.ServingStateID && a.CompatibilityDigest == b.CompatibilityDigest && a.RollbackClass == b.RollbackClass && sameStringSlice(a.RollbackExternalEffects, b.RollbackExternalEffects) && a.RollbackUntil.Equal(b.RollbackUntil)
}

func (r *Repository) DeliveryGenerationByID(ctx context.Context, id string) (deployment.DeliveryGeneration, error) {
	return deliveryGenerationByIDTx(ctx, r.db, id)
}

func deliveryGenerationByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryGeneration, error) {
	row, err := deploydb.New(q).GetDeliveryGeneration(ctx, id)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	var rollbackEffects []string
	if strings.TrimSpace(row.RollbackExternalEffectsJson) != "" {
		if err := json.Unmarshal([]byte(row.RollbackExternalEffectsJson), &rollbackEffects); err != nil {
			return deployment.DeliveryGeneration{}, err
		}
	}
	g := deployment.DeliveryGeneration{ID: row.ID, CandidateID: row.CandidateID, PlanID: row.PlanID, PlanDigest: row.PlanDigest, TargetID: row.TargetID, Environment: row.Environment, CatalogDigest: row.CatalogDigest, CatalogObjectKey: row.CatalogObjectKey, PhysicalPoolID: row.PhysicalPoolID, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest, ServingStateID: row.ServingStateID, CompatibilityDigest: row.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackClass(row.RollbackClass), RollbackExternalEffects: rollbackEffects, Status: deployment.DeliveryGenerationStatus(row.Status)}
	g.ProjectID, err = projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.ActivatedAt, err = parseNullableDeliveryTime(row.ActivatedAt)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.RetiredAt, err = parseNullableDeliveryTime(row.RetiredAt)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.RollbackUntil, err = parseNullableDeliveryTime(row.RollbackUntil)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	return g, g.Validate()
}

// CommitPublication performs the target CAS, generation activation, previous
// generation retirement, and publication completion as one SQLite commit.
func (r *Repository) CommitPublication(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	p, err := deliveryPublicationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if p.Status == deployment.DeliveryPublicationCommitted {
		return p, nil
	}
	if p.Status != deployment.DeliveryPublicationPending && p.Status != deployment.DeliveryPublicationIndeterminate {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication is %s", deployment.ErrDeliveryTransition, p.Status)
	}
	actor := "delivery"
	if persistedActor, actorErr := deploydb.New(tx).GetDeliveryPublicationRequestActor(ctx, p.ID); actorErr == nil {
		actor = persistedActor
	}
	actor = eventActor(actor)
	var active string
	var revision int64
	var targetProject, targetEnvironment string
	target, targetErr := deploydb.New(tx).GetDeliveryTargetRevision(ctx, p.TargetID)
	err = targetErr
	if err == nil {
		targetProject, targetEnvironment, revision, active = target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	}
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if targetProject != p.ProjectID.String() || targetEnvironment != p.Environment {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication scope changed", deployment.ErrDeliveryConflict)
	}
	if p.Status == deployment.DeliveryPublicationIndeterminate && active == p.GenerationID && revision == p.ExpectedTargetRevision+1 {
		committed, commitErr := p.Commit(p.ExpectedBaseGenerationID, p.ExpectedTargetRevision, now)
		if commitErr != nil {
			return deployment.DeliveryPublication{}, commitErr
		}
		res, execErr := deploydb.New(tx).CommitIndeterminateDeliveryPublication(ctx, deploydb.CommitIndeterminateDeliveryPublicationParams{ResultTargetRevision: committed.ResultTargetRevision, CompletedAt: presentString(deliveryTime(committed.CompletedAt)), ID: id})
		if execErr != nil {
			return deployment.DeliveryPublication{}, execErr
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: indeterminate publication CAS failed", deployment.ErrDeliveryConflict)
		}
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "activation_committed", "generation", p.GenerationID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
			ActorID: actor, EventKind: "activation_committed", ObjectKind: "generation", ObjectID: p.GenerationID,
			RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, Outcome: "accepted",
			Details: map[string]any{"publication_id": p.ID, "target_revision": committed.ResultTargetRevision}, CreatedAt: committed.CompletedAt,
		}); err != nil {
			return deployment.DeliveryPublication{}, err
		}
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_committed", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
			ActorID: actor, EventKind: "publish_committed", ObjectKind: "publication", ObjectID: p.ID,
			RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ResultDigest: deployment.CanonicalDeliveryDigest([]byte(fmt.Sprintf("%d", committed.ResultTargetRevision))), Outcome: "accepted",
			Details: map[string]any{"generation_id": p.GenerationID, "target_revision": committed.ResultTargetRevision}, CreatedAt: committed.CompletedAt,
		}); err != nil {
			return deployment.DeliveryPublication{}, err
		}
		if err := r.commitPublicationTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return r.reconcilePublicationCommitError(ctx, id, now.UTC(), err)
		}
		return committed, nil
	}
	if active != p.ExpectedBaseGenerationID || revision != p.ExpectedTargetRevision {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication CAS fence changed", deployment.ErrDeliveryStale)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, p.PlanID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	candidate, err := deliveryCandidateByIDTx(ctx, tx, p.CandidateID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if candidate.PlanID != p.PlanID || candidate.PlanDigest != p.PlanDigest {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: candidate is not bound to publication plan", deployment.ErrDeliveryConflict)
	}
	if err := candidate.PublicationEligible(plan, active, revision, now.UTC()); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	g, err := deliveryGenerationByIDTx(ctx, tx, p.GenerationID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if g.Status != deployment.DeliveryGenerationPrepared {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation is %s", deployment.ErrDeliveryTransition, g.Status)
	}
	if err := validateDeliveryTimeForCommit(now, p.CreatedAt); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	committed, err := p.Commit(active, revision, now)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	res, err := deploydb.New(tx).ActivateDeliveryGeneration(ctx, deploydb.ActivateDeliveryGenerationParams{ActivatedAt: presentString(deliveryTime(now)), ID: g.ID})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation activation CAS failed", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "activation_committed", "generation", p.GenerationID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
		ActorID: actor, EventKind: "activation_committed", ObjectKind: "generation", ObjectID: g.ID,
		RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ResultDigest: g.CatalogDigest, Outcome: "accepted",
		Details: map[string]any{"publication_id": p.ID, "target_revision": p.ExpectedTargetRevision + 1}, CreatedAt: now,
	}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if active != "" {
		res, execErr := deploydb.New(tx).RetireDeliveryGeneration(ctx, deploydb.RetireDeliveryGenerationParams{RetiredAt: presentString(deliveryTime(now)), ID: active})
		if execErr != nil {
			return deployment.DeliveryPublication{}, execErr
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: prior generation retirement CAS failed", deployment.ErrDeliveryConflict)
		}
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "retirement_committed", "generation", active), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
			ActorID: actor, EventKind: "retirement_committed", ObjectKind: "generation", ObjectID: active,
			RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, Outcome: "accepted",
			Details: map[string]any{"replaced_by_generation_id": g.ID, "publication_id": p.ID}, CreatedAt: now,
		}); err != nil {
			return deployment.DeliveryPublication{}, err
		}
	}
	res, err = deploydb.New(tx).AdvanceDeliveryTargetRevision(ctx, deploydb.AdvanceDeliveryTargetRevisionParams{ActiveGenerationID: presentString(p.GenerationID), UpdatedAt: deliveryTime(now), TargetID: p.TargetID, TargetRevision: p.ExpectedTargetRevision, ActiveGenerationID_2: sql.NullString{}, ActiveGenerationID_3: presentString(p.ExpectedBaseGenerationID)})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: target revision CAS failed", deployment.ErrDeliveryStale)
	}
	res, err = deploydb.New(tx).CommitDeliveryPublication(ctx, deploydb.CommitDeliveryPublicationParams{ResultTargetRevision: committed.ResultTargetRevision, CompletedAt: presentString(deliveryTime(committed.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication completion CAS failed", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_committed", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
		ActorID: actor, EventKind: "publish_committed", ObjectKind: "publication", ObjectID: p.ID,
		RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ResultDigest: deployment.CanonicalDeliveryDigest([]byte(fmt.Sprintf("%d", committed.ResultTargetRevision))), Outcome: "accepted",
		Details: map[string]any{"generation_id": p.GenerationID, "target_revision": committed.ResultTargetRevision}, CreatedAt: committed.CompletedAt,
	}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := r.commitPublicationTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return r.reconcilePublicationCommitError(ctx, id, now.UTC(), err)
	}
	return committed, nil
}

func (r *Repository) reconcilePublicationCommitError(ctx context.Context, id string, now time.Time, commitErr error) (deployment.DeliveryPublication, error) {
	// A provider/database timeout often cancels the request context. Recovery
	// is deliberately detached from cancellation, but bounded, so the durable
	// indeterminate marker can still be written without allowing an operator
	// request to hang indefinitely.
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	observed, observeErr := r.DeliveryPublicationByID(reconcileCtx, id)
	if observeErr != nil {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: activation commit acknowledgement was lost: %w", deployment.ErrDeliveryOutcomeUnknown, errors.Join(commitErr, observeErr))
	}
	if observed.Status == deployment.DeliveryPublicationCommitted {
		return observed, nil
	}
	if observed.Status == deployment.DeliveryPublicationPending {
		marked, markErr := r.MarkPublicationIndeterminate(reconcileCtx, id, now)
		if markErr != nil {
			return observed, fmt.Errorf("%w: activation commit acknowledgement was lost: %w", deployment.ErrDeliveryOutcomeUnknown, errors.Join(commitErr, markErr))
		}
		observed = marked
	}
	return observed, fmt.Errorf("%w: activation commit acknowledgement was lost: %w", deployment.ErrDeliveryOutcomeUnknown, commitErr)
}

// commitPublicationTx exists solely to make the final acknowledgement
// boundary testable. Production uses sql.Tx.Commit directly; a controlled
// hook may commit and then return an error to model a crash/timeout after the
// durable transaction completed.
func (r *Repository) commitPublicationTx(ctx context.Context, tx *sql.Tx) error {
	if r != nil && r.hooks.CommitPublication != nil {
		return r.hooks.CommitPublication(ctx, tx)
	}
	return tx.Commit()
}

// ReconcilePublication resolves one durable indeterminate activation without
// touching DuckLake or object storage. It commits only when the exact target
// pointer/revision prove that this request committed. It rejects only when
// the exact expected base/revision prove that no activation committed. Any
// other state remains indeterminate and is never used to activate or clean
// the intended candidate.
func (r *Repository) ReconcilePublication(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	publication, err := r.DeliveryPublicationByID(ctx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if publication.Status == deployment.DeliveryPublicationCommitted || publication.Status == deployment.DeliveryPublicationRejected {
		return publication, nil
	}
	if publication.Status == deployment.DeliveryPublicationPending {
		return publication, fmt.Errorf("%w: publication has not been marked indeterminate", deployment.ErrDeliveryOutcomeUnknown)
	}
	if publication.Status != deployment.DeliveryPublicationIndeterminate {
		return publication, fmt.Errorf("%w: unsupported publication state %s", deployment.ErrDeliveryOutcomeUnknown, publication.Status)
	}
	target, err := r.DeliveryTargetRevision(ctx, publication.TargetID)
	if err != nil {
		return publication, err
	}
	switch {
	case target.ActiveGenerationID == publication.GenerationID && target.TargetRevision == publication.ExpectedTargetRevision+1:
		return r.CommitPublication(ctx, id, now.UTC())
	case target.ActiveGenerationID == publication.ExpectedBaseGenerationID && target.TargetRevision == publication.ExpectedTargetRevision:
		// Durable target state proves the activation transaction did not commit.
		// Terminal rejection is control-plane evidence only; the candidate and
		// all physical objects remain untouched for a later explicit build.
		return r.RejectPublication(ctx, id, "activation_not_committed", now.UTC())
	default:
		return publication, fmt.Errorf("%w: target pointer/revision do not prove commit or non-commit", deployment.ErrDeliveryOutcomeUnknown)
	}
}

func validateDeliveryTimeForCommit(now, created time.Time) error {
	if now.IsZero() || now.UTC().Before(created) {
		return fmt.Errorf("%w: publication completion time is invalid", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func eventActor(actor string) string {
	if strings.TrimSpace(actor) == "" {
		// Legacy callers predating authenticated command evidence remain
		// replayable; new coordinators set ActorID before persistence.
		return "delivery"
	}
	return actor
}

func (r *Repository) RejectPublication(ctx context.Context, id, reason string, now time.Time) (deployment.DeliveryPublication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	p, err := deliveryPublicationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	updated, err := p.Reject(reason, now)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	res, err := deploydb.New(tx).RejectDeliveryPublication(ctx, deploydb.RejectDeliveryPublicationParams{Reason: updated.Reason, CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication rejection CAS failed", deployment.ErrDeliveryConflict)
	}
	actor := "delivery"
	if persistedActor, actorErr := deploydb.New(tx).GetDeliveryPublicationRequestActor(ctx, p.ID); actorErr == nil {
		actor = persistedActor
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_rejected", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment, ActorID: eventActor(actor), EventKind: "publish_rejected", ObjectKind: "publication", ObjectID: p.ID, RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, Outcome: "rejected", Details: map[string]any{"reason_code": "publication_rejected"}, CreatedAt: updated.CompletedAt}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return updated, nil
}
func (r *Repository) MarkPublicationIndeterminate(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	p, err := deliveryPublicationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	updated, err := p.MarkIndeterminate(now)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	res, err := deploydb.New(tx).MarkDeliveryPublicationIndeterminate(ctx, deploydb.MarkDeliveryPublicationIndeterminateParams{CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication indeterminate CAS failed", deployment.ErrDeliveryConflict)
	}
	actor := "delivery"
	if persistedActor, actorErr := deploydb.New(tx).GetDeliveryPublicationRequestActor(ctx, p.ID); actorErr == nil {
		actor = persistedActor
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_indeterminate", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment, ActorID: eventActor(actor), EventKind: "publish_indeterminate", ObjectKind: "publication", ObjectID: p.ID, RequestDigest: p.RequestDigest, Outcome: "indeterminate", Details: map[string]any{}, CreatedAt: updated.CompletedAt}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return updated, nil
}

// CreateQueryLease is the root/GC fence for long-running reads.  It accepts
// exactly one complete candidate or generation catalog and validates the
// catalog/pool binding before inserting the root.
func (r *Repository) CreateQueryLease(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, error) {
	lease, err := deployment.NewDeliveryQueryLease(input)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	defer tx.Rollback()
	if lease.CandidateID != "" {
		candidate, readErr := deliveryCandidateByIDTx(ctx, tx, lease.CandidateID)
		if readErr != nil {
			return deployment.DeliveryQueryLease{}, readErr
		}
		if candidate.Status != deployment.DeliveryCandidateReady || candidate.CatalogDigest != lease.CatalogDigest || candidate.PhysicalPoolID != lease.PhysicalPoolID {
			return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: candidate root is not queryable", deployment.ErrDeliveryConflict)
		}
	} else {
		generation, readErr := deliveryGenerationByIDTx(ctx, tx, lease.GenerationID)
		if readErr != nil {
			return deployment.DeliveryQueryLease{}, readErr
		}
		if (generation.Status != deployment.DeliveryGenerationPrepared && generation.Status != deployment.DeliveryGenerationActive) || generation.CatalogDigest != lease.CatalogDigest || generation.PhysicalPoolID != lease.PhysicalPoolID {
			return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: generation root is not queryable", deployment.ErrDeliveryConflict)
		}
	}
	err = deploydb.New(tx).CreateDeliveryQueryLease(ctx, deploydb.CreateDeliveryQueryLeaseParams{ID: lease.ID, HolderID: lease.HolderID, NULLIF: lease.CandidateID, NULLIF_2: lease.GenerationID, CatalogDigest: lease.CatalogDigest, PhysicalPoolID: lease.PhysicalPoolID, ExpiresAt: deliveryTime(lease.ExpiresAt), CreatedAt: deliveryTime(lease.CreatedAt)})
	if err != nil {
		if existing, readErr := deliveryQueryLeaseByIDTx(ctx, tx, lease.ID); readErr == nil && sameQueryLeaseIdentity(existing, lease) {
			if eventErr := appendQueryLeaseEventTx(ctx, tx, existing, "lease_acquired", deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:"+existing.ID)), "accepted", existing.CreatedAt); eventErr != nil {
				return deployment.DeliveryQueryLease{}, eventErr
			}
			if commitErr := tx.Commit(); commitErr != nil {
				return deployment.DeliveryQueryLease{}, commitErr
			}
			return existing, nil
		}
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease identity conflict", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, lease, "lease_acquired", deployment.CanonicalDeliveryDigest([]byte("query-lease-acquired:"+lease.ID)), "accepted", lease.CreatedAt); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return lease, nil
}

func (r *Repository) AcquireQueryLease(ctx context.Context, input deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, error) {
	return r.CreateQueryLease(ctx, input)
}

func sameQueryLeaseIdentity(a, b deployment.DeliveryQueryLease) bool {
	return a.ID == b.ID && a.HolderID == b.HolderID && a.CandidateID == b.CandidateID && a.GenerationID == b.GenerationID && a.CatalogDigest == b.CatalogDigest && a.PhysicalPoolID == b.PhysicalPoolID && a.ExpiresAt.Equal(b.ExpiresAt) && a.CreatedAt.Equal(b.CreatedAt)
}

func (r *Repository) DeliveryQueryLeaseByID(ctx context.Context, id string) (deployment.DeliveryQueryLease, error) {
	return deliveryQueryLeaseByIDTx(ctx, r.db, id)
}

func deliveryQueryLeaseByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryQueryLease, error) {
	row, err := deploydb.New(q).GetDeliveryQueryLease(ctx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	l := deployment.DeliveryQueryLease{ID: row.ID, HolderID: row.HolderID, CatalogDigest: row.CatalogDigest, PhysicalPoolID: row.PhysicalPoolID, Status: deployment.DeliveryLeaseStatus(row.Status)}
	if row.CandidateID.Valid {
		l.CandidateID = row.CandidateID.String
	}
	if row.GenerationID.Valid {
		l.GenerationID = row.GenerationID.String
	}
	l.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	l.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	l.ReleasedAt, err = parseNullableDeliveryTime(row.ReleasedAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return l, l.Validate()
}

func (r *Repository) HeartbeatQueryLease(ctx context.Context, id string, now, expiresAt time.Time) (deployment.DeliveryQueryLease, error) {
	l, err := r.DeliveryQueryLeaseByID(ctx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	updated, err := l.Heartbeat(now, expiresAt)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	res, err := r.queries.RenewDeliveryQueryLease(ctx, deploydb.RenewDeliveryQueryLeaseParams{ExpiresAt: deliveryTime(updated.ExpiresAt), ID: id, ExpiresAt_2: deliveryTime(l.ExpiresAt)})
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease heartbeat CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) ReleaseQueryLease(ctx context.Context, id string, now time.Time) (deployment.DeliveryQueryLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	defer tx.Rollback()
	l, err := deliveryQueryLeaseByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if l.Status == deployment.DeliveryLeaseReleased {
		return l, nil
	}
	updated, err := l.Release(now)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	res, err := deploydb.New(tx).ReleaseDeliveryQueryLease(ctx, deploydb.ReleaseDeliveryQueryLeaseParams{ReleasedAt: presentString(deliveryTime(updated.ReleasedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease release CAS failed", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, updated, "lease_released", deployment.CanonicalDeliveryDigest([]byte("query-lease-released:"+updated.ID)), "accepted", updated.ReleasedAt); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return updated, nil
}
func (r *Repository) ExpireQueryLease(ctx context.Context, id string, now time.Time) (deployment.DeliveryQueryLease, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	defer tx.Rollback()
	l, err := deliveryQueryLeaseByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if l.Status == deployment.DeliveryLeaseExpired {
		return l, nil
	}
	updated, err := l.Expire(now)
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	res, err := deploydb.New(tx).ExpireDeliveryQueryLease(ctx, deploydb.ExpireDeliveryQueryLeaseParams{ReleasedAt: presentString(deliveryTime(updated.ReleasedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryQueryLease{}, fmt.Errorf("%w: query lease expiry CAS failed", deployment.ErrDeliveryConflict)
	}
	if err := appendQueryLeaseEventTx(ctx, tx, updated, "lease_expired", deployment.CanonicalDeliveryDigest([]byte("query-lease-expired:"+updated.ID)), "accepted", updated.ReleasedAt); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryQueryLease{}, err
	}
	return updated, nil
}

func (r *Repository) CreateRetentionException(ctx context.Context, input deployment.DeliveryRetentionException) (deployment.DeliveryRetentionException, error) {
	root, err := deployment.NewDeliveryRetentionException(input)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	if root.CandidateID != "" {
		c, readErr := r.DeliveryCandidateByID(ctx, root.CandidateID)
		if readErr != nil {
			return deployment.DeliveryRetentionException{}, readErr
		}
		if c.CatalogDigest != root.CatalogDigest || c.PhysicalPoolID != root.PhysicalPoolID {
			return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention root differs from candidate", deployment.ErrDeliveryConflict)
		}
	} else {
		g, readErr := r.DeliveryGenerationByID(ctx, root.GenerationID)
		if readErr != nil {
			return deployment.DeliveryRetentionException{}, readErr
		}
		if g.CatalogDigest != root.CatalogDigest || g.PhysicalPoolID != root.PhysicalPoolID {
			return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention root differs from generation", deployment.ErrDeliveryConflict)
		}
	}
	err = r.queries.CreateDeliveryRetentionException(ctx, deploydb.CreateDeliveryRetentionExceptionParams{ID: root.ID, PhysicalPoolID: root.PhysicalPoolID, NULLIF: root.CandidateID, NULLIF_2: root.GenerationID, CatalogDigest: root.CatalogDigest, Reason: root.Reason, ExpiresAt: deliveryTime(root.ExpiresAt), CreatedAt: deliveryTime(root.CreatedAt)})
	if err != nil {
		if existing, readErr := r.DeliveryRetentionExceptionByID(ctx, root.ID); readErr == nil && sameRetentionIdentity(existing, root) {
			return existing, nil
		}
		return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention identity conflict", deployment.ErrDeliveryConflict)
	}
	return root, nil
}
func (r *Repository) CreateRetentionRoot(ctx context.Context, input deployment.DeliveryRetentionException) (deployment.DeliveryRetentionException, error) {
	return r.CreateRetentionException(ctx, input)
}
func sameRetentionIdentity(a, b deployment.DeliveryRetentionException) bool {
	return a.ID == b.ID && a.PhysicalPoolID == b.PhysicalPoolID && a.CandidateID == b.CandidateID && a.GenerationID == b.GenerationID && a.CatalogDigest == b.CatalogDigest && a.Reason == b.Reason && a.ExpiresAt.Equal(b.ExpiresAt) && a.CreatedAt.Equal(b.CreatedAt)
}
func (r *Repository) DeliveryRetentionExceptionByID(ctx context.Context, id string) (deployment.DeliveryRetentionException, error) {
	row, err := r.queries.GetDeliveryRetentionException(ctx, id)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	root := deployment.DeliveryRetentionException{ID: row.ID, PhysicalPoolID: row.PhysicalPoolID, CatalogDigest: row.CatalogDigest, Reason: row.Reason, Status: deployment.DeliveryRetentionExceptionStatus(row.Status)}
	if row.CandidateID.Valid {
		root.CandidateID = row.CandidateID.String
	}
	if row.GenerationID.Valid {
		root.GenerationID = row.GenerationID.String
	}
	root.ExpiresAt, err = parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	root.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	root.ReleasedAt, err = parseNullableDeliveryTime(row.ReleasedAt)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	return root, root.Validate()
}
func (r *Repository) ReleaseRetentionException(ctx context.Context, id string, now time.Time) (deployment.DeliveryRetentionException, error) {
	root, err := r.DeliveryRetentionExceptionByID(ctx, id)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	if root.Status == deployment.DeliveryRetentionReleased {
		return root, nil
	}
	updated, err := root.Release(now)
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	res, err := r.queries.ReleaseDeliveryRetentionException(ctx, deploydb.ReleaseDeliveryRetentionExceptionParams{ReleasedAt: presentString(deliveryTime(updated.ReleasedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryRetentionException{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryRetentionException{}, fmt.Errorf("%w: retention release CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}

func (r *Repository) CreateGCCycle(ctx context.Context, input deployment.DeliveryGCCycle) (deployment.DeliveryGCCycle, error) {
	cycle, err := deployment.NewDeliveryGCCycle(input)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	err = deploydb.New(tx).CreateDeliveryGCCycle(ctx, deploydb.CreateDeliveryGCCycleParams{ID: cycle.ID, ActorID: eventActor(cycle.ActorID), PhysicalPoolID: cycle.PhysicalPoolID, Epoch: cycle.Epoch, RootRevision: cycle.RootRevision, CreatedAt: deliveryTime(cycle.CreatedAt)})
	if err != nil {
		_ = tx.Rollback()
		if existing, readErr := r.DeliveryGCCycleByID(ctx, cycle.ID); readErr == nil && existing.PhysicalPoolID == cycle.PhysicalPoolID && existing.Epoch == cycle.Epoch && existing.RootRevision == cycle.RootRevision {
			return existing, nil
		}
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC cycle identity conflict", deployment.ErrDeliveryConflict)
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return cycle, nil
}
func (r *Repository) DeliveryGCCycleByID(ctx context.Context, id string) (deployment.DeliveryGCCycle, error) {
	return deliveryGCCycleByIDTx(ctx, r.db, id)
}

func deliveryGCCycleByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryGCCycle, error) {
	row, err := deploydb.New(q).GetDeliveryGCCycle(ctx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	c := deployment.DeliveryGCCycle{ID: row.ID, ActorID: row.ActorID, PhysicalPoolID: row.PhysicalPoolID, Epoch: row.Epoch, RootRevision: row.RootRevision, Status: deployment.DeliveryGCStatus(row.Status), AbortReason: row.AbortReason}
	if row.MarkDigest.Valid {
		c.MarkDigest = row.MarkDigest.String
	}
	c.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	c.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return c, c.Validate()
}
func (r *Repository) MarkGCCycle(ctx context.Context, id, markDigest string) (deployment.DeliveryGCCycle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	c, err := deliveryGCCycleByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if (c.Status == deployment.DeliveryGCMarked || c.Status == deployment.DeliveryGCDeleting || c.Status == deployment.DeliveryGCComplete) && c.MarkDigest == markDigest {
		return c, nil
	}
	updated, err := c.Mark(markDigest)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := deploydb.New(tx).MarkDeliveryGCCycle(ctx, deploydb.MarkDeliveryGCCycleParams{MarkDigest: presentString(updated.MarkDigest), ID: id})
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC mark CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-marked:" + updated.ID + ":" + updated.MarkDigest))
	if err := appendGCCycleEventTx(ctx, tx, updated, "gc_marked", updated.ID, requestDigest, "accepted", updated.ActorID, map[string]any{"status": string(updated.Status)}, updated.CreatedAt); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return updated, nil
}
func (r *Repository) BeginGCDelete(ctx context.Context, id string) (deployment.DeliveryGCCycle, error) {
	c, err := r.DeliveryGCCycleByID(ctx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if c.Status == deployment.DeliveryGCDeleting {
		return c, nil
	}
	updated, err := c.BeginDelete()
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := r.queries.BeginDeliveryGCDelete(ctx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC delete CAS failed", deployment.ErrDeliveryConflict)
	}
	return updated, nil
}
func (r *Repository) CompleteGCCycle(ctx context.Context, id string, now time.Time) (deployment.DeliveryGCCycle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	c, err := deliveryGCCycleByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if c.Status == deployment.DeliveryGCComplete {
		return c, nil
	}
	updated, err := c.Complete(now)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := deploydb.New(tx).CompleteDeliveryGCCycle(ctx, deploydb.CompleteDeliveryGCCycleParams{CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC completion CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-complete:" + updated.ID))
	if err := appendGCCycleEventTx(ctx, tx, updated, "cleanup_completed", updated.ID, requestDigest, "accepted", updated.ActorID, map[string]any{"status": string(updated.Status)}, updated.CompletedAt); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return updated, nil
}
func (r *Repository) AbortGCCycle(ctx context.Context, id, reason string, now time.Time) (deployment.DeliveryGCCycle, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	defer tx.Rollback()
	c, err := deliveryGCCycleByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if c.Status == deployment.DeliveryGCAborted && c.AbortReason == reason {
		return c, nil
	}
	updated, err := c.Abort(reason, now)
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	res, err := deploydb.New(tx).AbortDeliveryGCCycle(ctx, deploydb.AbortDeliveryGCCycleParams{AbortReason: updated.AbortReason, CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCCycle{}, fmt.Errorf("%w: GC abort CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-aborted:" + updated.ID + ":" + updated.AbortReason))
	if err := appendGCCycleEventTx(ctx, tx, updated, "gc_aborted", updated.ID, requestDigest, "failed", updated.ActorID, map[string]any{"status": string(updated.Status)}, updated.CompletedAt); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCCycle{}, err
	}
	return updated, nil
}

func (r *Repository) CreateGCDeleteIntent(ctx context.Context, input deployment.DeliveryGCDeleteIntent) (deployment.DeliveryGCDeleteIntent, error) {
	intent, err := deployment.NewDeliveryGCDeleteIntent(input)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	cycle, err := r.DeliveryGCCycleByID(ctx, intent.CycleID)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if cycle.PhysicalPoolID != intent.PhysicalPoolID || cycle.Status != deployment.DeliveryGCDeleting {
		return deployment.DeliveryGCDeleteIntent{}, fmt.Errorf("%w: GC cycle is not deleting", deployment.ErrDeliveryTransition)
	}
	err = r.queries.CreateDeliveryGCDeleteIntent(ctx, deploydb.CreateDeliveryGCDeleteIntentParams{ID: intent.ID, CycleID: intent.CycleID, PhysicalPoolID: intent.PhysicalPoolID, ObjectKey: intent.ObjectKey, ObjectDigest: intent.ObjectDigest, ObjectVersion: sql.NullString{String: intent.ObjectVersion, Valid: intent.ObjectVersion != ""}, CreatedAt: deliveryTime(intent.CreatedAt)})
	if err != nil {
		if existing, readErr := r.DeliveryGCDeleteIntentByID(ctx, intent.ID); readErr == nil && existing.CycleID == intent.CycleID && existing.ObjectKey == intent.ObjectKey && existing.ObjectDigest == intent.ObjectDigest && existing.ObjectVersion == intent.ObjectVersion {
			return existing, nil
		}
		return deployment.DeliveryGCDeleteIntent{}, fmt.Errorf("%w: delete intent identity conflict", deployment.ErrDeliveryConflict)
	}
	return intent, nil
}
func (r *Repository) DeliveryGCDeleteIntentByID(ctx context.Context, id string) (deployment.DeliveryGCDeleteIntent, error) {
	row, err := r.queries.GetDeliveryGCDeleteIntent(ctx, id)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i := deployment.DeliveryGCDeleteIntent{ID: row.ID, CycleID: row.CycleID, PhysicalPoolID: row.PhysicalPoolID, ObjectKey: row.ObjectKey, ObjectDigest: row.ObjectDigest, ObjectVersion: row.ObjectVersion.String, Status: deployment.DeliveryGCDeleteIntentStatus(row.Status)}
	i.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	return i, i.Validate()
}
func (r *Repository) CompleteGCDeleteIntent(ctx context.Context, id string, status deployment.DeliveryGCDeleteIntentStatus, now time.Time) (deployment.DeliveryGCDeleteIntent, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	defer tx.Rollback()
	row, err := deploydb.New(tx).GetDeliveryGCDeleteIntent(ctx, id)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i := deployment.DeliveryGCDeleteIntent{ID: row.ID, CycleID: row.CycleID, PhysicalPoolID: row.PhysicalPoolID, ObjectKey: row.ObjectKey, ObjectDigest: row.ObjectDigest, ObjectVersion: row.ObjectVersion.String, Status: deployment.DeliveryGCDeleteIntentStatus(row.Status)}
	i.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	i.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if err := i.Validate(); err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if i.Status == status && status != deployment.DeliveryGCDeletePending {
		return i, nil
	}
	updated, err := i.Complete(status, now)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	res, err := deploydb.New(tx).CompleteDeliveryGCDeleteIntent(ctx, deploydb.CompleteDeliveryGCDeleteIntentParams{Status: string(updated.Status), CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryGCDeleteIntent{}, fmt.Errorf("%w: delete intent CAS failed", deployment.ErrDeliveryConflict)
	}
	cycle, err := deliveryGCCycleByIDTx(ctx, tx, updated.CycleID)
	if err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("gc-deleted:" + updated.ID + ":" + string(updated.Status)))
	if err := appendGCCycleEventTx(ctx, tx, cycle, "gc_deleted", updated.ID, requestDigest, "accepted", cycle.ActorID, map[string]any{"status": string(updated.Status)}, updated.CompletedAt); err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryGCDeleteIntent{}, err
	}
	return updated, nil
}

// Concise aliases mirror the control-contract vocabulary while retaining the
// Delivery prefix on read methods that would otherwise collide with the
// historical release-candidate repository API.
func (r *Repository) DeliveryPlanByID(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
	return r.PlanByID(ctx, id)
}

func (r *Repository) CreateWriterLeaseAndAttempt(ctx context.Context, lease deployment.DeliveryWriterLease, attempt deployment.DeliveryBuildAttempt) (deployment.DeliveryWriterLease, deployment.DeliveryBuildAttempt, error) {
	return r.CreateWriterLeaseAndBuildAttempt(ctx, lease, attempt)
}

func (r *Repository) CASBuildTransition(ctx context.Context, id string, expectedRevision int64, next deployment.DeliveryBuildAttemptStatus, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	return r.TransitionBuildAttempt(ctx, id, expectedRevision, next, now)
}

func (r *Repository) PrepareSeal(ctx context.Context, seal deployment.CatalogSeal) (deployment.CatalogSeal, error) {
	return r.PrepareCatalogSeal(ctx, seal)
}

func (r *Repository) UploadSeal(ctx context.Context, id string) (deployment.CatalogSeal, error) {
	return r.MarkCatalogSealUploaded(ctx, id)
}

func (r *Repository) VerifySeal(ctx context.Context, id, closureDigest, qualificationDigest string, now time.Time) (deployment.CatalogSeal, error) {
	return r.VerifyCatalogSeal(ctx, id, closureDigest, qualificationDigest, now)
}

func (r *Repository) ReadyCandidate(ctx context.Context, candidate deployment.DeliveryCandidate, seal deployment.CatalogSeal, now time.Time) (deployment.DeliveryCandidate, error) {
	return r.CreateCandidateReady(ctx, candidate, seal, now)
}

func (r *Repository) RequestPublication(ctx context.Context, publication deployment.DeliveryPublication, generation ...deployment.DeliveryGeneration) (deployment.DeliveryPublication, error) {
	return r.CreatePublication(ctx, publication, generation...)
}

func (r *Repository) ActivatePublication(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	return r.CommitPublication(ctx, id, now)
}

func (r *Repository) CreateLease(ctx context.Context, lease deployment.DeliveryQueryLease) (deployment.DeliveryQueryLease, error) {
	return r.CreateQueryLease(ctx, lease)
}

func (r *Repository) ReleaseLease(ctx context.Context, id string, now time.Time) (deployment.DeliveryQueryLease, error) {
	return r.ReleaseQueryLease(ctx, id, now)
}

func (r *Repository) CreateRetentionRootException(ctx context.Context, root deployment.DeliveryRetentionException) (deployment.DeliveryRetentionException, error) {
	return r.CreateRetentionException(ctx, root)
}

func (r *Repository) CreateGCDelete(ctx context.Context, intent deployment.DeliveryGCDeleteIntent) (deployment.DeliveryGCDeleteIntent, error) {
	return r.CreateGCDeleteIntent(ctx, intent)
}
