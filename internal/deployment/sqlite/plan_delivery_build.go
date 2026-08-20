package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	"github.com/flidai/leapview/internal/release"
)

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
	freshBuild := attempt.BaseGenerationID == "" && attempt.BaseCatalogDigest == "" && attempt.BasePhysicalPoolID == ""
	baseGenerationMatches := attempt.BaseGenerationID == plan.BaseGenerationID
	if attempt.PlanDigest != plan.Digest || attempt.SourceDigest != plan.SourceDigest || attempt.ExecutionDigest != plan.ExecutionDigest || (!baseGenerationMatches && !freshBuild) {
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

// RecordFailedBuildGateEvidence durably records the exact normalized,
// non-secret evidence produced before a candidate was rejected. Repeated
// retries for an attempt are idempotent and cannot rewrite a different digest.
func (r *Repository) RecordFailedBuildGateEvidence(ctx context.Context, attemptID string, evidence *release.GateEvidence) error {
	if r == nil || r.db == nil || strings.TrimSpace(attemptID) == "" || evidence == nil {
		return fmt.Errorf("failed gate evidence recording requires attempt and evidence")
	}
	canonical, err := evidence.Canonical()
	if err != nil {
		return err
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if r.deliveryNow != nil {
		now = r.deliveryNow().UTC()
	}
	existing, lookupErr := r.queries.GetFailedGateEvidenceDigest(ctx, attemptID)
	if lookupErr == nil {
		if existing != canonical.Digest {
			return fmt.Errorf("%w: failed gate evidence for attempt %q is immutable", deployment.ErrDeliveryConflict, attemptID)
		}
		return nil
	}
	if !errors.Is(lookupErr, sql.ErrNoRows) {
		return lookupErr
	}
	_, err = r.queries.InsertFailedGateEvidence(ctx, deploydb.InsertFailedGateEvidenceParams{
		AttemptID:      attemptID,
		EvidenceJson:   string(payload),
		EvidenceDigest: canonical.Digest,
		CreatedAt:      deliveryTime(now),
	})
	if err != nil {
		return err
	}
	if existing, err = r.queries.GetFailedGateEvidenceDigest(ctx, attemptID); err != nil {
		return err
	}
	if existing != canonical.Digest {
		return fmt.Errorf("%w: failed gate evidence for attempt %q changed concurrently", deployment.ErrDeliveryConflict, attemptID)
	}
	return nil
}

func (r *Repository) FailedBuildGateEvidence(ctx context.Context, attemptID string) (*release.GateEvidence, error) {
	if r == nil || r.db == nil {
		return nil, fmt.Errorf("delivery repository is not open")
	}
	record, err := r.queries.GetFailedGateEvidence(ctx, attemptID)
	if err != nil {
		return nil, err
	}
	payload, digest := record.EvidenceJson, record.EvidenceDigest
	var evidence release.GateEvidence
	if err := json.Unmarshal([]byte(payload), &evidence); err != nil {
		return nil, fmt.Errorf("decode failed gate evidence: %w", err)
	}
	if evidence.Digest != digest {
		return nil, fmt.Errorf("failed gate evidence digest mismatch")
	}
	canonical, err := evidence.Canonical()
	if err != nil || canonical.Digest != digest {
		if err == nil {
			err = fmt.Errorf("failed gate evidence is not canonical")
		}
		return nil, err
	}
	return &canonical, nil
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

// BindDeliveryBuildSnapshot records qualified data-version evidence on the
// build attempt without pinning the sealed serving state to a DuckLake
// snapshot. Replays converge only on the same immutable snapshot.
func (r *Repository) BindDeliveryBuildSnapshot(ctx context.Context, attemptID string, expectedRevision, snapshotID int64, now time.Time) (deployment.DeliveryBuildAttempt, error) {
	if snapshotID <= 0 || now.IsZero() || now.Location() != time.UTC {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: qualified snapshot binding is invalid", deployment.ErrDeliveryInvalid)
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
	if attempt.Revision != expectedRevision {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build snapshot binding revision changed", deployment.ErrDeliveryConflict)
	}
	if attempt.QualifiedSnapshotID != 0 {
		if attempt.QualifiedSnapshotID != snapshotID {
			return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: qualified snapshot identity changed", deployment.ErrDeliveryConflict)
		}
		if err := tx.Commit(); err != nil {
			return deployment.DeliveryBuildAttempt{}, err
		}
		return attempt, nil
	}
	result, err := deploydb.New(tx).BindDeliveryBuildSnapshot(ctx, deploydb.BindDeliveryBuildSnapshotParams{
		QualifiedSnapshotID: snapshotID,
		UpdatedAt:           deliveryTime(now),
		ID:                  attemptID,
		Revision:            expectedRevision,
	})
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if affected, _ := result.RowsAffected(); affected != 1 {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: build snapshot binding changed", deployment.ErrDeliveryConflict)
	}
	attempt, err = deliveryBuildAttemptByIDTx(ctx, tx, attemptID)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return attempt, nil
}

func deliveryBuildAttemptByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryBuildAttempt, error) {
	row, err := deploydb.New(q).GetDeliveryBuildAttempt(ctx, id)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	a := deployment.DeliveryBuildAttempt{ID: row.ID, PlanID: row.PlanID, IdempotencyKey: row.IdempotencyKey, PlanDigest: row.PlanDigest, SourceDigest: row.SourceDigest, ExecutionDigest: row.ExecutionDigest, PhysicalPoolID: row.PhysicalPoolID, WriterLeaseID: row.WriterLeaseID, Status: deployment.DeliveryBuildAttemptStatus(row.Status), Revision: row.Revision, QualifiedSnapshotID: row.QualifiedSnapshotID, FailureCode: row.FailureCode}
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
