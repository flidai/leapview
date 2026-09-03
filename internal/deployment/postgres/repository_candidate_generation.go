package postgres

// Candidate and serving-generation persistence lives in this file to keep the repository surface focused.

import (
	"context"
	"errors"
	"fmt"

	"github.com/flidai/leapview/internal/deployment"
	depdb "github.com/flidai/leapview/internal/deployment/postgres/internal/db"
	"github.com/jackc/pgx/v5"
)

// CreateCandidate creates the mutable admission projection. Qualification is
// a separate operation so a partial/missing seal cannot be published.
func (r *Repository) CreateCandidate(ctx context.Context, in CandidateInput) (DeliveryCandidate, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return createCandidate(contextOrBackground(ctx), db, in)
}

// CreateCandidateTx persists a candidate through a caller-owned control-plane
// transaction. It deliberately never commits or rolls back tx, allowing
// candidate admission to share the project-claim/audit/workflow boundary when
// the composition root has all authorities on the same PostgreSQL database.
func (r *Repository) CreateCandidateTx(ctx context.Context, tx Tx, in CandidateInput) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	return createCandidate(contextOrBackground(ctx), tx, in)
}

// CreateCandidateAllocatedTx admits a candidate with the next target-owned
// revision through a caller-owned transaction. Existing UUIDs are replayed
// before allocation and compare only immutable admission identity.
func (r *Repository) CreateCandidateAllocatedTx(ctx context.Context, tx Tx, in CandidateInput) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	return createCandidateAllocated(contextOrBackground(ctx), tx, in)
}

// RejectCandidateTx moves a non-terminal candidate to the explicit rejected
// state through the caller-owned transaction. Rejection is intentionally
// evidence-light at the candidate layer; detailed failure evidence is stored
// on the attempt and operation ledgers by the native termination authority.
// The operation is idempotent for an already-rejected candidate.
func (r *Repository) RejectCandidateTx(ctx context.Context, tx Tx, candidateID string) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	id, err := uuidID(candidateID, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	ctx = contextOrBackground(ctx)
	candidate, err := loadCandidate(ctx, tx, id, CandidateInput{})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if candidate.Status == "rejected" {
		return candidate, nil
	}
	if candidate.Status != "building" && candidate.Status != "ready" && candidate.Status != "qualified" {
		return DeliveryCandidate{}, ErrConflict
	}
	tag, err := depdb.New(tx).RejectCandidate(ctx, dbUUID(id))
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if tag != 1 {
		candidate, err = loadCandidate(ctx, tx, id, CandidateInput{})
		if err == nil && candidate.Status == "rejected" {
			return candidate, nil
		}
		if err != nil {
			return DeliveryCandidate{}, err
		}
		return DeliveryCandidate{}, ErrConflict
	}
	return loadCandidate(ctx, tx, id, CandidateInput{})
}

// CreateCandidateAllocated owns a short transaction around the Tx API.
func (r *Repository) CreateCandidateAllocated(ctx context.Context, in CandidateInput) (DeliveryCandidate, error) {
	tx, err := r.begin(contextOrBackground(ctx))
	if err != nil {
		return DeliveryCandidate{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	out, err := r.CreateCandidateAllocatedTx(ctx, tx, in)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryCandidate{}, err
	}
	return out, nil
}

// StartCandidateWithClaimTx composes the instance project claim and native
// candidate admission in one caller-owned control-plane transaction. It is
// the supported atomic seam for composition roots that need candidate start
// and claim/audit evidence to commit together.
func (r *Repository) StartCandidateWithClaimTx(ctx context.Context, tx Tx, claim deployment.ProjectClaimInput, in CandidateInput) (deployment.ProjectClaim, DeliveryCandidate, error) {
	if tx == nil {
		return deployment.ProjectClaim{}, DeliveryCandidate{}, ErrInvalid
	}
	projectClaim, err := r.ClaimProjectTx(ctx, tx, claim)
	if err != nil {
		return deployment.ProjectClaim{}, DeliveryCandidate{}, err
	}
	candidate, err := r.CreateCandidateTx(ctx, tx, in)
	if err != nil {
		return deployment.ProjectClaim{}, DeliveryCandidate{}, err
	}
	return projectClaim, candidate, nil
}
func createCandidate(ctx context.Context, db DBTX, in CandidateInput) (DeliveryCandidate, error) {
	id, err := uuidID(in.CandidateID, "candidate id", true)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryCandidate{}, err
	}
	plan, err := uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if in.SnapshotSealID != "" {
		if _, err := uuidID(in.SnapshotSealID, "snapshot seal id", false); err != nil {
			return DeliveryCandidate{}, err
		}
	}
	if in.CandidateRevision <= 0 {
		return DeliveryCandidate{}, ErrInvalid
	}
	if _, err := digest(in.ArtifactDigest, "artifact digest"); err != nil {
		return DeliveryCandidate{}, err
	}
	planTarget, err := depdb.New(db).GetPlanTarget(ctx, dbUUID(plan))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	} else if err != nil {
		return DeliveryCandidate{}, err
	} else if planTarget != target {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate target differs from plan target", ErrConflict)
	}
	status := in.Status
	if status == "" {
		status = "building"
	}
	if status != "building" {
		return DeliveryCandidate{}, ErrInvalid
	}
	var qualificationDigest *string
	if in.QualificationDigest != "" {
		qualificationDigest = &in.QualificationDigest
	}
	err = depdb.New(db).InsertCandidate(ctx, depdb.InsertCandidateParams{CandidateID: dbUUID(id), TargetID: target, PlanID: dbUUID(plan), SnapshotSealID: dbUUID(in.SnapshotSealID), Status: status, CandidateRevision: in.CandidateRevision, ArtifactDigest: in.ArtifactDigest, QualificationDigest: pgText(qualificationDigest)})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(ctx, db, id, in)
}

func candidateAllocationInput(in CandidateInput) (id, target, plan string, err error) {
	if in.CandidateRevision != 0 {
		return "", "", "", ErrInvalid
	}
	id, err = uuidID(in.CandidateID, "candidate id", true)
	if err != nil {
		return "", "", "", err
	}
	target, err = textID(in.TargetID, "target id")
	if err != nil {
		return "", "", "", err
	}
	plan, err = uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return "", "", "", err
	}
	if _, err := digest(in.ArtifactDigest, "artifact digest"); err != nil {
		return "", "", "", err
	}
	return id, target, plan, nil
}

func candidateImmutableMatches(c DeliveryCandidate, in CandidateInput, id, target, plan string) bool {
	return c.CandidateID == id && c.TargetID == target && c.PlanID == plan && c.ArtifactDigest == in.ArtifactDigest
}

func loadCandidateForAllocation(ctx context.Context, db DBTX, id, target, plan string, in CandidateInput) (DeliveryCandidate, error) {
	row, err := depdb.New(db).GetCandidate(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	}
	if err != nil {
		return DeliveryCandidate{}, err
	}
	c := DeliveryCandidate{CandidateID: row.CandidateID, TargetID: row.TargetID, PlanID: row.PlanID, AttemptID: row.AttemptID, SnapshotSealID: row.SnapshotSealID, Status: row.Status, CandidateRevision: row.CandidateRevision, ArtifactDigest: row.ArtifactDigest, QualificationDigest: row.QualificationDigest, CreatedAt: dbTime(row.CreatedAt)}
	if row.QualifiedAt.Valid {
		c.QualifiedAt = row.QualifiedAt.Time.UTC()
	}
	if row.RetiredAt.Valid {
		c.RetiredAt = row.RetiredAt.Time.UTC()
	}
	if !candidateImmutableMatches(c, in, id, target, plan) {
		return DeliveryCandidate{}, ErrConflict
	}
	return c, nil
}

func createCandidateAllocated(ctx context.Context, db DBTX, in CandidateInput) (DeliveryCandidate, error) {
	id, target, plan, err := candidateAllocationInput(in)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	q := depdb.New(db)
	planTarget, err := q.GetPlanTarget(ctx, dbUUID(plan))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	}
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if planTarget != target {
		return DeliveryCandidate{}, fmt.Errorf("%w: candidate target differs from plan target", ErrConflict)
	}
	if err := q.EnsureTargetRevision(ctx, target); err != nil {
		return DeliveryCandidate{}, err
	}
	if _, err := q.LockTargetRevision(ctx, target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryCandidate{}, ErrNotFound
		}
		return DeliveryCandidate{}, err
	}
	if existing, lookupErr := loadCandidateForAllocation(ctx, db, id, target, plan, in); lookupErr == nil {
		return existing, nil
	} else if !errors.Is(lookupErr, ErrNotFound) {
		return DeliveryCandidate{}, lookupErr
	}
	status := in.Status
	if status == "" {
		status = "building"
	}
	if status != "building" || in.SnapshotSealID != "" || in.QualificationDigest != "" {
		return DeliveryCandidate{}, ErrInvalid
	}
	revision, err := q.NextCandidateRevision(ctx, target)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if err := q.InsertCandidate(ctx, depdb.InsertCandidateParams{CandidateID: dbUUID(id), TargetID: target, PlanID: dbUUID(plan), SnapshotSealID: dbUUID(""), Status: status, CandidateRevision: revision, ArtifactDigest: in.ArtifactDigest, QualificationDigest: pgText(nil)}); err != nil {
		return DeliveryCandidate{}, err
	}
	allocated := in
	allocated.CandidateID, allocated.TargetID, allocated.PlanID, allocated.CandidateRevision, allocated.Status = id, target, plan, revision, status
	return loadCandidate(ctx, db, id, allocated)
}
func loadCandidate(ctx context.Context, db DBTX, id string, expected CandidateInput) (DeliveryCandidate, error) {
	var c DeliveryCandidate
	row, err := depdb.New(db).GetCandidate(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryCandidate{}, ErrNotFound
	}
	if err != nil {
		return DeliveryCandidate{}, err
	}
	c.CandidateID, c.TargetID, c.PlanID, c.AttemptID, c.SnapshotSealID, c.Status, c.CandidateRevision, c.ArtifactDigest, c.QualificationDigest, c.CreatedAt = row.CandidateID, row.TargetID, row.PlanID, row.AttemptID, row.SnapshotSealID, row.Status, row.CandidateRevision, row.ArtifactDigest, row.QualificationDigest, dbTime(row.CreatedAt)
	if row.QualifiedAt.Valid {
		c.QualifiedAt = row.QualifiedAt.Time.UTC()
	}
	if row.RetiredAt.Valid {
		c.RetiredAt = row.RetiredAt.Time.UTC()
	}
	if expected.TargetID != "" && (c.TargetID != expected.TargetID || c.PlanID != expected.PlanID || c.CandidateRevision != expected.CandidateRevision || c.ArtifactDigest != expected.ArtifactDigest) {
		return DeliveryCandidate{}, ErrConflict
	}
	return c, nil
}
func (r *Repository) Candidate(ctx context.Context, id string) (DeliveryCandidate, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	id, err = uuidID(id, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(contextOrBackground(ctx), db, id, CandidateInput{})
}

// CandidateTx reads immutable candidate evidence through a caller-owned
// transaction, preserving publication lock ordering and snapshot visibility.
func (r *Repository) CandidateTx(ctx context.Context, tx Tx, id string) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	id, err := uuidID(id, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(contextOrBackground(ctx), tx, id, CandidateInput{})
}
func (r *Repository) LoadCandidate(ctx context.Context, id string) (DeliveryCandidate, error) {
	return r.Candidate(ctx, id)
}

// ResolveCandidateGeneration returns the exact immutable generation bound to
// a candidate. The named sqlc query carries the cardinality check so callers
// never silently select an arbitrary generation from malformed history.
func (r *Repository) ResolveCandidateGeneration(ctx context.Context, candidateID string) (CandidateGenerationResolution, error) {
	db, err := requireDB(r)
	if err != nil {
		return CandidateGenerationResolution{}, err
	}
	return resolveCandidateGeneration(contextOrBackground(ctx), db, candidateID)
}

// ResolveCandidateGenerationTx is the transaction-preserving form used by
// native publication requests while holding the target fence.
func (r *Repository) ResolveCandidateGenerationTx(ctx context.Context, tx Tx, candidateID string) (CandidateGenerationResolution, error) {
	if tx == nil {
		return CandidateGenerationResolution{}, ErrInvalid
	}
	return resolveCandidateGeneration(contextOrBackground(ctx), tx, candidateID)
}

func resolveCandidateGeneration(ctx context.Context, db DBTX, candidateID string) (CandidateGenerationResolution, error) {
	id, err := uuidID(candidateID, "candidate id", false)
	if err != nil {
		return CandidateGenerationResolution{}, err
	}
	row, err := depdb.New(db).ResolveCandidateGeneration(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return CandidateGenerationResolution{}, ErrNotFound
	}
	if err != nil {
		return CandidateGenerationResolution{}, err
	}
	result := CandidateGenerationResolution{
		CandidateID: row.CandidateID, TargetID: row.TargetID, PlanID: row.PlanID,
		SnapshotSealID: row.SnapshotSealID, Status: row.Status,
		CandidateRevision: row.CandidateRevision, ArtifactDigest: row.ArtifactDigest,
		ProjectID: row.ProjectID, Environment: row.Environment,
		GenerationCount: row.GenerationCount, GenerationID: row.GenerationID,
	}
	if result.GenerationCount != 1 || result.GenerationID == "" {
		return CandidateGenerationResolution{}, fmt.Errorf("%w: candidate must resolve exactly one generation", ErrConflict)
	}
	return result, nil
}
func (r *Repository) QualifyCandidate(ctx context.Context, candidateID, sealID, qualificationDigest string) (DeliveryCandidate, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	candidateID, err = uuidID(candidateID, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	sealID, err = uuidID(sealID, "seal id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if _, err := digest(qualificationDigest, "qualification digest"); err != nil {
		return DeliveryCandidate{}, err
	}
	c, err := loadCandidate(contextOrBackground(ctx), db, candidateID, CandidateInput{})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	s, err := loadSeal(contextOrBackground(ctx), db, sealID)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if s.CandidateID != "" && s.CandidateID != candidateID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.AttemptID != "" && c.AttemptID != s.AttemptID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.Status != "building" && c.Status != "ready" {
		if c.Status == "qualified" && c.SnapshotSealID == sealID && c.QualificationDigest == qualificationDigest {
			return c, nil
		}
		return DeliveryCandidate{}, ErrConflict
	}
	err = depdb.New(db).QualifyCandidate(contextOrBackground(ctx), depdb.QualifyCandidateParams{CandidateID: dbUUID(candidateID), SnapshotSealID: dbUUID(sealID), QualificationDigest: pgText(&qualificationDigest)})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(contextOrBackground(ctx), db, candidateID, CandidateInput{})
}

// QualifyCandidateTx is the transaction-aware qualification form. The
// caller owns the complete commit/rollback boundary.
func (r *Repository) QualifyCandidateTx(ctx context.Context, tx Tx, candidateID, sealID, qualificationDigest string) (DeliveryCandidate, error) {
	if tx == nil {
		return DeliveryCandidate{}, ErrInvalid
	}
	ctx = contextOrBackground(ctx)
	candidateID, err := uuidID(candidateID, "candidate id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	sealID, err = uuidID(sealID, "seal id", false)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if _, err := digest(qualificationDigest, "qualification digest"); err != nil {
		return DeliveryCandidate{}, err
	}
	c, err := loadCandidate(ctx, tx, candidateID, CandidateInput{})
	if err != nil {
		return DeliveryCandidate{}, err
	}
	s, err := loadSeal(ctx, tx, sealID)
	if err != nil {
		return DeliveryCandidate{}, err
	}
	if s.CandidateID != "" && s.CandidateID != candidateID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.AttemptID != "" && c.AttemptID != s.AttemptID {
		return DeliveryCandidate{}, ErrConflict
	}
	if c.Status != "building" && c.Status != "ready" {
		if c.Status == "qualified" && c.SnapshotSealID == sealID && c.QualificationDigest == qualificationDigest {
			return c, nil
		}
		return DeliveryCandidate{}, ErrConflict
	}
	if err = depdb.New(tx).QualifyCandidate(ctx, depdb.QualifyCandidateParams{CandidateID: dbUUID(candidateID), SnapshotSealID: dbUUID(sealID), QualificationDigest: pgText(&qualificationDigest)}); err != nil {
		return DeliveryCandidate{}, err
	}
	return loadCandidate(ctx, tx, candidateID, CandidateInput{})
}

// CreateGeneration binds the immutable seal and all compiler identities.
func (r *Repository) CreateGeneration(ctx context.Context, in GenerationInput) (DeliveryGeneration, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return createGeneration(contextOrBackground(ctx), db, in)
}

// CreateGenerationTx creates (or exactly replays) a serving generation
// through a caller-owned PostgreSQL transaction. It does not commit or roll
// back tx.
func (r *Repository) CreateGenerationTx(ctx context.Context, tx Tx, in GenerationInput) (DeliveryGeneration, error) {
	if tx == nil {
		return DeliveryGeneration{}, ErrInvalid
	}
	return createGeneration(contextOrBackground(ctx), tx, in)
}

// CreateGenerationAllocatedTx admits a serving generation using the next
// target-owned revision through a caller-owned transaction. Existing UUIDs
// replay immutable evidence before candidate lifecycle checks or allocation.
func (r *Repository) CreateGenerationAllocatedTx(ctx context.Context, tx Tx, in GenerationInput) (DeliveryGeneration, error) {
	if tx == nil {
		return DeliveryGeneration{}, ErrInvalid
	}
	return createGenerationAllocated(contextOrBackground(ctx), tx, in)
}

// CreateGenerationAllocated owns a short transaction around the Tx API.
func (r *Repository) CreateGenerationAllocated(ctx context.Context, in GenerationInput) (DeliveryGeneration, error) {
	tx, err := r.begin(contextOrBackground(ctx))
	if err != nil {
		return DeliveryGeneration{}, err
	}
	defer tx.Rollback(contextOrBackground(ctx))
	out, err := r.CreateGenerationAllocatedTx(ctx, tx, in)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return DeliveryGeneration{}, err
	}
	return out, nil
}

func createGeneration(ctx context.Context, db DBTX, in GenerationInput) (DeliveryGeneration, error) {
	id, err := uuidID(in.GenerationID, "generation id", true)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	target, err := textID(in.TargetID, "target id")
	if err != nil {
		return DeliveryGeneration{}, err
	}
	for n, v := range map[string]string{"plan digest": in.PlanDigest, "serving artifact digest": in.ServingArtifactDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint} {
		if _, err := digest(v, n); err != nil {
			return DeliveryGeneration{}, err
		}
	}
	if in.GenerationRevision <= 0 {
		return DeliveryGeneration{}, ErrInvalid
	}
	if in.ArtifactRoot == "" || in.ArtifactRootDigest == "" {
		return DeliveryGeneration{}, ErrInvalid
	}
	if _, err := digest(in.ArtifactRootDigest, "artifact root digest"); err != nil {
		return DeliveryGeneration{}, err
	}
	candidate, err := uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	seal, err := uuidID(in.SnapshotSealID, "seal id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	plan, err := uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	expected := in
	expected.GenerationID = id
	expected.TargetID = target
	expected.CandidateID = candidate
	expected.SnapshotSealID = seal
	expected.PlanID = plan
	// Generation identity is immutable and caller-owned. Resolve an exact
	// replay before inspecting the candidate's current lifecycle state: after
	// successful activation the candidate is admitted, but retrying the
	// already-committed generation must still return the original evidence.
	if existing, lookupErr := loadGeneration(ctx, db, id, expected); lookupErr == nil {
		return existing, nil
	} else if !errors.Is(lookupErr, ErrNotFound) {
		return DeliveryGeneration{}, lookupErr
	}
	cr, err := depdb.New(db).GetCandidateStatus(ctx, dbUUID(candidate))
	cstatus, ct, cp, cs := cr.Status, cr.TargetID, cr.PlanID, cr.SnapshotSealID
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if cstatus != "qualified" || ct != target || cp != plan || cs != seal {
		return DeliveryGeneration{}, ErrNotQualified
	}
	snapshotSeal, err := loadSeal(ctx, db, seal)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	pr, err := depdb.New(db).GetPlanDigests(ctx, dbUUID(plan))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	} else if err != nil {
		return DeliveryGeneration{}, err
	}
	if snapshotSeal.ServingArtifactDigest != in.ServingArtifactDigest || snapshotSeal.ArtifactRoot != in.ArtifactRoot || snapshotSeal.ArtifactRootDigest != in.ArtifactRootDigest || snapshotSeal.CompiledGraphDigest != in.CompiledGraphDigest || snapshotSeal.CompiledConfigDigest != in.CompiledConfigDigest || snapshotSeal.SecurityDomainFingerprint != in.SecurityDomainFingerprint || snapshotSeal.PlanDigest != in.PlanDigest || pr.PlanDigest != in.PlanDigest || pr.CompiledGraphDigest != in.CompiledGraphDigest || pr.CompiledConfigDigest != in.CompiledConfigDigest || pr.SecurityDomainFingerprint != in.SecurityDomainFingerprint || pr.ArtifactDigest != in.ServingArtifactDigest {
		return DeliveryGeneration{}, fmt.Errorf("%w: generation evidence differs from seal and plan", ErrConflict)
	}
	err = depdb.New(db).InsertGeneration(ctx, depdb.InsertGenerationParams{GenerationID: dbUUID(id), TargetID: target, CandidateID: dbUUID(candidate), SnapshotSealID: dbUUID(seal), PlanID: dbUUID(plan), PlanDigest: in.PlanDigest, ArtifactRoot: in.ArtifactRoot, ArtifactRootDigest: in.ArtifactRootDigest, ServingArtifactDigest: in.ServingArtifactDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, GenerationRevision: in.GenerationRevision})
	if err != nil {
		// A concurrent exact creator may have committed while this insert was
		// blocked on the primary key. Re-read the immutable row and accept only
		// byte-for-byte domain evidence; otherwise retain the original failure.
		if existing, lookupErr := loadGeneration(ctx, db, id, expected); lookupErr == nil {
			return existing, nil
		} else if !errors.Is(lookupErr, ErrNotFound) {
			return DeliveryGeneration{}, lookupErr
		}
		return DeliveryGeneration{}, err
	}
	return loadGeneration(ctx, db, id, expected)
}

func generationAllocationInput(in GenerationInput) (id, target, candidate, seal, plan string, err error) {
	if in.GenerationRevision != 0 {
		return "", "", "", "", "", ErrInvalid
	}
	id, err = uuidID(in.GenerationID, "generation id", true)
	if err != nil {
		return "", "", "", "", "", err
	}
	target, err = textID(in.TargetID, "target id")
	if err != nil {
		return "", "", "", "", "", err
	}
	for n, v := range map[string]string{"plan digest": in.PlanDigest, "serving artifact digest": in.ServingArtifactDigest, "compiled graph digest": in.CompiledGraphDigest, "compiled config digest": in.CompiledConfigDigest, "security fingerprint": in.SecurityDomainFingerprint, "artifact root digest": in.ArtifactRootDigest} {
		if _, err := digest(v, n); err != nil {
			return "", "", "", "", "", err
		}
	}
	if in.ArtifactRoot == "" {
		return "", "", "", "", "", ErrInvalid
	}
	candidate, err = uuidID(in.CandidateID, "candidate id", false)
	if err != nil {
		return "", "", "", "", "", err
	}
	seal, err = uuidID(in.SnapshotSealID, "seal id", false)
	if err != nil {
		return "", "", "", "", "", err
	}
	plan, err = uuidID(in.PlanID, "plan id", false)
	if err != nil {
		return "", "", "", "", "", err
	}
	return id, target, candidate, seal, plan, nil
}

func generationImmutableMatches(g DeliveryGeneration, in GenerationInput, id, target, candidate, seal, plan string) bool {
	return g.GenerationID == id && g.TargetID == target && g.CandidateID == candidate && g.SnapshotSealID == seal && g.PlanID == plan &&
		g.PlanDigest == in.PlanDigest && g.ArtifactRoot == in.ArtifactRoot && g.ArtifactRootDigest == in.ArtifactRootDigest &&
		g.ServingArtifactDigest == in.ServingArtifactDigest && g.CompiledGraphDigest == in.CompiledGraphDigest &&
		g.CompiledConfigDigest == in.CompiledConfigDigest && g.SecurityDomainFingerprint == in.SecurityDomainFingerprint
}

func loadGenerationForAllocation(ctx context.Context, db DBTX, id, target, candidate, seal, plan string, in GenerationInput) (DeliveryGeneration, error) {
	row, err := depdb.New(db).GetGeneration(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	g := DeliveryGeneration{GenerationID: row.GenerationID, TargetID: row.TargetID, CandidateID: row.CandidateID, SnapshotSealID: row.SnapshotSealID, PlanID: row.PlanID, PlanDigest: row.PlanDigest, ArtifactRoot: row.ArtifactRoot, ArtifactRootDigest: row.ArtifactRootDigest, ServingArtifactDigest: row.ServingArtifactDigest, CompiledGraphDigest: row.CompiledGraphDigest, CompiledConfigDigest: row.CompiledConfigDigest, SecurityDomainFingerprint: row.SecurityDomainFingerprint, GenerationRevision: row.GenerationRevision, CreatedAt: dbTime(row.CreatedAt)}
	if !generationImmutableMatches(g, in, id, target, candidate, seal, plan) {
		return DeliveryGeneration{}, ErrConflict
	}
	return g, nil
}

func createGenerationAllocated(ctx context.Context, db DBTX, in GenerationInput) (DeliveryGeneration, error) {
	id, target, candidate, seal, plan, err := generationAllocationInput(in)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	q := depdb.New(db)
	if err := q.EnsureTargetRevision(ctx, target); err != nil {
		return DeliveryGeneration{}, err
	}
	if _, err := q.LockTargetRevision(ctx, target); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return DeliveryGeneration{}, ErrNotFound
		}
		return DeliveryGeneration{}, err
	}
	if existing, lookupErr := loadGenerationForAllocation(ctx, db, id, target, candidate, seal, plan, in); lookupErr == nil {
		return existing, nil
	} else if !errors.Is(lookupErr, ErrNotFound) {
		return DeliveryGeneration{}, lookupErr
	}
	cr, err := q.GetCandidateStatus(ctx, dbUUID(candidate))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if cr.Status != "qualified" || cr.TargetID != target || cr.PlanID != plan || cr.SnapshotSealID != seal {
		return DeliveryGeneration{}, ErrNotQualified
	}
	snapshotSeal, err := loadSeal(ctx, db, seal)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	pr, err := q.GetPlanDigests(ctx, dbUUID(plan))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if snapshotSeal.ServingArtifactDigest != in.ServingArtifactDigest || snapshotSeal.ArtifactRoot != in.ArtifactRoot || snapshotSeal.ArtifactRootDigest != in.ArtifactRootDigest || snapshotSeal.CompiledGraphDigest != in.CompiledGraphDigest || snapshotSeal.CompiledConfigDigest != in.CompiledConfigDigest || snapshotSeal.SecurityDomainFingerprint != in.SecurityDomainFingerprint || snapshotSeal.PlanDigest != in.PlanDigest || pr.PlanDigest != in.PlanDigest || pr.CompiledGraphDigest != in.CompiledGraphDigest || pr.CompiledConfigDigest != in.CompiledConfigDigest || pr.SecurityDomainFingerprint != in.SecurityDomainFingerprint || pr.ArtifactDigest != in.ServingArtifactDigest {
		return DeliveryGeneration{}, fmt.Errorf("%w: generation evidence differs from seal and plan", ErrConflict)
	}
	revision, err := q.NextGenerationRevision(ctx, target)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	if err := q.InsertGeneration(ctx, depdb.InsertGenerationParams{GenerationID: dbUUID(id), TargetID: target, CandidateID: dbUUID(candidate), SnapshotSealID: dbUUID(seal), PlanID: dbUUID(plan), PlanDigest: in.PlanDigest, ArtifactRoot: in.ArtifactRoot, ArtifactRootDigest: in.ArtifactRootDigest, ServingArtifactDigest: in.ServingArtifactDigest, CompiledGraphDigest: in.CompiledGraphDigest, CompiledConfigDigest: in.CompiledConfigDigest, SecurityDomainFingerprint: in.SecurityDomainFingerprint, GenerationRevision: revision}); err != nil {
		return DeliveryGeneration{}, err
	}
	allocated := in
	allocated.GenerationID, allocated.TargetID, allocated.CandidateID, allocated.SnapshotSealID, allocated.PlanID, allocated.GenerationRevision = id, target, candidate, seal, plan, revision
	return loadGeneration(ctx, db, id, allocated)
}
func loadGeneration(ctx context.Context, db DBTX, id string, expected GenerationInput) (DeliveryGeneration, error) {
	var g DeliveryGeneration
	row, err := depdb.New(db).GetGeneration(ctx, dbUUID(id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryGeneration{}, ErrNotFound
	}
	if err != nil {
		return DeliveryGeneration{}, err
	}
	g.GenerationID, g.TargetID, g.CandidateID, g.SnapshotSealID, g.PlanID, g.PlanDigest, g.ArtifactRoot, g.ArtifactRootDigest, g.ServingArtifactDigest, g.CompiledGraphDigest, g.CompiledConfigDigest, g.SecurityDomainFingerprint, g.GenerationRevision, g.CreatedAt = row.GenerationID, row.TargetID, row.CandidateID, row.SnapshotSealID, row.PlanID, row.PlanDigest, row.ArtifactRoot, row.ArtifactRootDigest, row.ServingArtifactDigest, row.CompiledGraphDigest, row.CompiledConfigDigest, row.SecurityDomainFingerprint, row.GenerationRevision, dbTime(row.CreatedAt)
	if expected.TargetID != "" && (g.TargetID != expected.TargetID || g.CandidateID != expected.CandidateID || g.SnapshotSealID != expected.SnapshotSealID || g.PlanID != expected.PlanID || g.PlanDigest != expected.PlanDigest || g.ArtifactRoot != expected.ArtifactRoot || g.ArtifactRootDigest != expected.ArtifactRootDigest || g.ServingArtifactDigest != expected.ServingArtifactDigest || g.CompiledGraphDigest != expected.CompiledGraphDigest || g.CompiledConfigDigest != expected.CompiledConfigDigest || g.SecurityDomainFingerprint != expected.SecurityDomainFingerprint || g.GenerationRevision != expected.GenerationRevision) {
		return DeliveryGeneration{}, ErrConflict
	}
	return g, nil
}
func (r *Repository) Generation(ctx context.Context, id string) (DeliveryGeneration, error) {
	db, err := requireDB(r)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	id, err = uuidID(id, "generation id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return loadGeneration(contextOrBackground(ctx), db, id, GenerationInput{})
}

// GenerationTx reads immutable serving-generation evidence through a
// caller-owned transaction.
func (r *Repository) GenerationTx(ctx context.Context, tx Tx, id string) (DeliveryGeneration, error) {
	if tx == nil {
		return DeliveryGeneration{}, ErrInvalid
	}
	id, err := uuidID(id, "generation id", false)
	if err != nil {
		return DeliveryGeneration{}, err
	}
	return loadGeneration(contextOrBackground(ctx), tx, id, GenerationInput{})
}

// TargetTx reads the immutable project/environment identity for a delivery
// target through a caller-owned transaction.
func (r *Repository) TargetTx(ctx context.Context, tx Tx, id string) (DeliveryTarget, error) {
	if tx == nil {
		return DeliveryTarget{}, ErrInvalid
	}
	id, err := textID(id, "target id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	return loadTarget(contextOrBackground(ctx), tx, id)
}

// TargetForShareTx reads and share-locks the immutable delivery target row.
// Activation acquires the same row FOR UPDATE, so a canonical refresh proof
// that uses this projection cannot be overtaken before its transaction commits.
func (r *Repository) TargetForShareTx(ctx context.Context, tx Tx, id string) (DeliveryTarget, error) {
	if tx == nil {
		return DeliveryTarget{}, ErrInvalid
	}
	id, err := textID(id, "target id")
	if err != nil {
		return DeliveryTarget{}, err
	}
	var target DeliveryTarget
	row, err := depdb.New(tx).LockTargetForShare(contextOrBackground(ctx), id)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryTarget{}, ErrNotFound
	}
	if err != nil {
		return target, err
	}
	target.TargetID, target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID, target.ActivePublicationID, target.CreatedAt, target.UpdatedAt = row.TargetID, row.ProjectID, row.Environment, row.TargetRevision, row.ActiveGenerationID, row.ActivePublicationID, dbTime(row.CreatedAt), dbTime(row.UpdatedAt)
	return target, nil
}
func (r *Repository) LoadGeneration(ctx context.Context, id string) (DeliveryGeneration, error) {
	return r.Generation(ctx, id)
}

// CreatePublication records a pending request. Activation is the only path
// that advances the target pointer.
