package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

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
