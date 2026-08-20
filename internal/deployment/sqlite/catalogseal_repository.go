package sqlite

// This file adapts the generic catalogseal boundary to the plan-delivery
// control state.  The adapter deliberately owns one SQLite transaction for
// every durable transition; object bytes and remote verification remain
// outside SQLite and are represented only by their exact digests and sizes.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/analytics/catalogseal"
	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	"github.com/flidai/leapview/internal/release"
)

var _ catalogseal.SealRepository = (*Repository)(nil)
var _ deployment.DeliveryCompletionReader = (*Repository)(nil)
var _ deployment.DeliveryCompletionEvidenceReader = (*Repository)(nil)

// durableCatalogSeal contains the generic record plus the deployment rows
// whose composite bindings are checked on every read.  Keeping these values
// together prevents a retry from accidentally releasing a lease belonging to
// another attempt or pool.
type durableCatalogSeal struct {
	record  catalogseal.SealRecord
	seal    deployment.CatalogSeal
	attempt deployment.DeliveryBuildAttempt
	lease   deployment.DeliveryWriterLease
}

func catalogSealRepositoryError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, catalogseal.ErrSealNotFound) || errors.Is(err, catalogseal.ErrIdentityConflict) || errors.Is(err, catalogseal.ErrRepositoryTransition) {
		return err
	}
	return fmt.Errorf("%w: %v", catalogseal.ErrSealRepository, err)
}

func catalogSealIdentityConflict(format string, args ...any) error {
	return fmt.Errorf("%w: %s", catalogseal.ErrIdentityConflict, fmt.Sprintf(format, args...))
}

func catalogSealTransition(format string, args ...any) error {
	return fmt.Errorf("%w: %s", catalogseal.ErrRepositoryTransition, fmt.Sprintf(format, args...))
}

func validateCatalogSealIdentity(identity catalogseal.SealIdentity) error {
	for name, value := range map[string]string{
		"seal": identity.SealID, "attempt": identity.Attempt.ID,
		"writer lease": identity.Attempt.WriterLeaseID, "plan": identity.Plan.ID,
		"pool": identity.Pool.ID, "candidate": identity.Candidate.ID,
		"serving artifact": identity.Candidate.ServingArtifactID,
		"serving state":    identity.Candidate.ServingStateID,
	} {
		if err := deployment.ValidateDeliveryID(value); err != nil {
			return fmt.Errorf("%w: %s identity: %v", catalogseal.ErrInvalidRequest, name, err)
		}
	}
	for name, value := range map[string]string{
		"plan": identity.Plan.Digest, "execution": identity.Plan.ExecutionDigest,
		"compatibility": identity.Pool.CompatibilityDigest,
		"qualification": identity.Qualification.Digest, "closure": identity.Closure.Digest,
		"serving artifact": identity.Candidate.ServingArtifactDigest,
		"catalog":          identity.CatalogDigest,
	} {
		if err := deployment.ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%w: %s digest: %v", catalogseal.ErrInvalidRequest, name, err)
		}
	}
	if identity.ObjectSize <= 0 || identity.ObjectKey != catalogseal.CanonicalObjectKey(identity.CatalogDigest) {
		return fmt.Errorf("%w: catalog object identity is not canonical", catalogseal.ErrInvalidRequest)
	}
	return nil
}

func sameCatalogSealIdentity(a, b catalogseal.SealIdentity) bool {
	return a.SealID == b.SealID && a.Attempt == b.Attempt && a.Plan == b.Plan &&
		a.Pool == b.Pool && a.Qualification == b.Qualification && a.Closure == b.Closure &&
		a.Candidate == b.Candidate && a.CatalogDigest == b.CatalogDigest &&
		a.ObjectKey == b.ObjectKey && a.ObjectSize == b.ObjectSize
}

// loadCatalogSealTx reconstructs identity from the seal, build attempt, and
// exact writer lease rows.  It is intentionally strict about the three
// identity_* columns: a row from before the adapter migration cannot be
// silently treated as a new generic identity.
func loadCatalogSealTx(ctx context.Context, q deploydb.DBTX, id string) (durableCatalogSeal, error) {
	if strings.TrimSpace(id) == "" {
		return durableCatalogSeal{}, catalogseal.ErrSealNotFound
	}
	var (
		seal                                   deployment.CatalogSeal
		base, basePool, closure, qualification sql.NullString
		failure, created, verified             sql.NullString
		identityCandidate, identityClosure     sql.NullString
		identityQualification                  sql.NullString
	)
	row, err := deploydb.New(q).GetDeliveryCatalogSealWithIdentity(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return durableCatalogSeal{}, catalogseal.ErrSealNotFound
	}
	if err != nil {
		return durableCatalogSeal{}, err
	}
	seal.ID, seal.AttemptID, seal.PlanID, seal.PlanDigest = row.ID, row.AttemptID, row.PlanID, row.PlanDigest
	seal.ExecutionDigest, seal.PhysicalPoolID, seal.CatalogDigest = row.ExecutionDigest, row.PhysicalPoolID, row.CatalogDigest
	seal.CompatibilityDigest, seal.ServingArtifactID, seal.ServingArtifactDigest, seal.ServingStateID, seal.ObjectKey, seal.ObjectSize, seal.Status, seal.FailureCode = row.CompatibilityDigest, row.ServingArtifactID, row.ServingArtifactDigest, row.ServingStateID, row.ObjectKey, row.ObjectSize, deployment.CatalogSealStatus(row.Status), row.FailureCode
	base, basePool, closure, qualification, created, verified = row.BaseCatalogDigest, row.BasePhysicalPoolID, row.ClosureDigest, row.QualificationDigest, sql.NullString{String: row.CreatedAt, Valid: true}, row.VerifiedAt
	identityCandidate, identityClosure, identityQualification = row.IdentityCandidateID, row.IdentityClosureDigest, row.IdentityQualificationDigest
	if base.Valid {
		seal.BaseCatalogDigest = base.String
	}
	if basePool.Valid {
		seal.BasePhysicalPoolID = basePool.String
	}
	if closure.Valid {
		seal.ClosureDigest = closure.String
	}
	if qualification.Valid {
		seal.QualificationDigest = qualification.String
	}
	if failure.Valid {
		seal.FailureCode = failure.String
	}
	var errTime error
	seal.CreatedAt, errTime = parseNullableDeliveryTime(created)
	if errTime != nil {
		return durableCatalogSeal{}, errTime
	}
	seal.VerifiedAt, errTime = parseNullableDeliveryTime(verified)
	if errTime != nil {
		return durableCatalogSeal{}, errTime
	}
	if err := seal.Validate(); err != nil {
		return durableCatalogSeal{}, catalogSealRepositoryError(err)
	}
	attempt, err := deliveryBuildAttemptByIDTx(ctx, q, seal.AttemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return durableCatalogSeal{}, catalogSealTransition("seal build attempt is missing")
	}
	if err != nil {
		return durableCatalogSeal{}, err
	}
	lease, err := deliveryWriterLeaseByIDTx(ctx, q, attempt.WriterLeaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return durableCatalogSeal{}, catalogSealTransition("seal writer lease is missing")
	}
	if err != nil {
		return durableCatalogSeal{}, err
	}
	if seal.AttemptID != attempt.ID || seal.PlanID != attempt.PlanID ||
		seal.PlanDigest != attempt.PlanDigest || seal.ExecutionDigest != attempt.ExecutionDigest ||
		seal.PhysicalPoolID != attempt.PhysicalPoolID || attempt.WriterLeaseID != lease.ID ||
		lease.AttemptID != attempt.ID || lease.PhysicalPoolID != attempt.PhysicalPoolID {
		return durableCatalogSeal{}, catalogSealIdentityConflict("seal bindings do not match build attempt and writer lease")
	}
	if !identityCandidate.Valid || !identityClosure.Valid || !identityQualification.Valid ||
		identityCandidate.String == "" || identityClosure.String == "" || identityQualification.String == "" {
		// A verified row created by the older deployment adapter may reconstruct
		// these values from its terminal evidence and sealed attempt. It is safe
		// to recover only that terminal state; preparing/uploaded rows must have
		// the complete identity persisted before they can be resumed.
		if seal.Status != deployment.CatalogSealVerified || !closure.Valid || !qualification.Valid || attempt.CandidateID == "" {
			return durableCatalogSeal{}, catalogSealTransition("seal identity evidence is incomplete")
		}
		identityCandidate = sql.NullString{String: attempt.CandidateID, Valid: true}
		identityClosure = sql.NullString{String: closure.String, Valid: true}
		identityQualification = sql.NullString{String: qualification.String, Valid: true}
	}
	identity := catalogseal.SealIdentity{
		SealID:        seal.ID,
		Attempt:       catalogseal.AttemptIdentity{ID: attempt.ID, WriterLeaseID: attempt.WriterLeaseID},
		Plan:          catalogseal.PlanIdentity{ID: seal.PlanID, Digest: seal.PlanDigest, ExecutionDigest: seal.ExecutionDigest},
		Pool:          catalogseal.PoolIdentity{ID: seal.PhysicalPoolID, CompatibilityDigest: seal.CompatibilityDigest},
		Qualification: catalogseal.QualificationIdentity{Digest: identityQualification.String},
		Closure:       catalogseal.ClosureIdentity{Digest: identityClosure.String},
		Candidate:     catalogseal.CandidateIdentity{ID: identityCandidate.String, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest, ServingStateID: row.ServingStateID},
		CatalogDigest: seal.CatalogDigest, ObjectKey: seal.ObjectKey, ObjectSize: seal.ObjectSize,
	}
	if err := validateCatalogSealIdentity(identity); err != nil {
		return durableCatalogSeal{}, catalogSealRepositoryError(err)
	}
	if seal.Status == deployment.CatalogSealVerified &&
		(seal.ClosureDigest != identity.Closure.Digest || seal.QualificationDigest != identity.Qualification.Digest) {
		return durableCatalogSeal{}, catalogSealIdentityConflict("terminal verification evidence differs from seal identity")
	}
	var status catalogseal.SealStatus
	switch seal.Status {
	case deployment.CatalogSealPreparing:
		status = catalogseal.SealPreparing
	case deployment.CatalogSealUploaded:
		status = catalogseal.SealUploaded
	case deployment.CatalogSealVerified:
		status = catalogseal.SealVerified
	default:
		return durableCatalogSeal{}, catalogSealTransition("unsupported persisted seal status %q", seal.Status)
	}
	return durableCatalogSeal{record: catalogseal.SealRecord{Identity: identity, Status: status}, seal: seal, attempt: attempt, lease: lease}, nil
}

// CompletedDelivery reconstructs the exact terminal completion needed by a
// sealed-attempt retry. It reads one transaction snapshot so a restart cannot
// observe a verified seal, candidate, and writer-lease release from different
// durable states.
func (r *Repository) CompletedDelivery(ctx context.Context, attemptID, candidateID string) (catalogseal.Completion, error) {
	if r == nil || r.db == nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(errors.New("repository is not open"))
	}
	if strings.TrimSpace(attemptID) == "" || strings.TrimSpace(candidateID) == "" {
		return catalogseal.Completion{}, catalogSealIdentityConflict("completion attempt and candidate identities are required")
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	defer tx.Rollback()
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, attemptID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogseal.Completion{}, catalogSealTransition("completion build attempt is missing")
	}
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	if attempt.Status != deployment.DeliveryBuildSealed {
		return catalogseal.Completion{}, catalogSealTransition("completion build attempt is not sealed")
	}
	if attempt.CandidateID != candidateID || attempt.SealID == "" {
		return catalogseal.Completion{}, catalogSealIdentityConflict("completion build attempt identity differs from request")
	}
	state, err := loadCatalogSealTx(ctx, tx, attempt.SealID)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	if state.record.Status != catalogseal.SealVerified {
		return catalogseal.Completion{}, catalogSealTransition("completion seal is not verified")
	}
	identity := state.record.Identity
	if state.attempt.ID != attempt.ID || state.attempt.Status != deployment.DeliveryBuildSealed ||
		state.attempt.SealID != identity.SealID || state.attempt.CandidateID != candidateID ||
		state.lease.ID != identity.Attempt.WriterLeaseID || state.lease.AttemptID != attempt.ID ||
		state.lease.PhysicalPoolID != attempt.PhysicalPoolID {
		return catalogseal.Completion{}, catalogSealIdentityConflict("completion seal bindings differ from sealed attempt and writer lease")
	}
	if state.lease.Status != deployment.DeliveryLeaseReleased {
		return catalogseal.Completion{}, catalogSealTransition("completion writer lease is not released")
	}
	candidate, err := deliveryCandidateByIDTx(ctx, tx, candidateID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogseal.Completion{}, catalogSealTransition("completion candidate is missing")
	}
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	if candidate.Status != deployment.DeliveryCandidateReady {
		return catalogseal.Completion{}, catalogSealTransition("completion candidate is not ready")
	}
	if candidate.ID != identity.Candidate.ID || candidate.PlanID != identity.Plan.ID ||
		candidate.PlanDigest != identity.Plan.Digest || candidate.ExecutionDigest != identity.Plan.ExecutionDigest ||
		candidate.SealID != identity.SealID || candidate.CatalogDigest != identity.CatalogDigest ||
		candidate.CatalogObjectKey != identity.ObjectKey || candidate.PhysicalPoolID != identity.Pool.ID ||
		candidate.CompatibilityDigest != identity.Pool.CompatibilityDigest ||
		candidate.ServingArtifactID != identity.Candidate.ServingArtifactID ||
		candidate.ServingArtifactDigest != identity.Candidate.ServingArtifactDigest ||
		candidate.ServingStateID != identity.Candidate.ServingStateID ||
		candidate.QualificationDigest != identity.Qualification.Digest ||
		candidate.BaseCatalogDigest != state.seal.BaseCatalogDigest || candidate.BasePhysicalPoolID != state.seal.BasePhysicalPoolID {
		return catalogseal.Completion{}, catalogSealIdentityConflict("completion candidate identity differs from verified seal")
	}
	return catalogseal.Completion{Seal: state.record, CandidateID: candidateID, LeaseReleased: true}, nil
}

func (r *Repository) CompletedDeliveryGateEvidence(ctx context.Context, candidateID string) (*release.GateEvidence, error) {
	candidate, err := r.DeliveryCandidateByID(ctx, candidateID)
	if err != nil {
		return nil, catalogSealRepositoryError(err)
	}
	if candidate.Status != deployment.DeliveryCandidateReady || candidate.ResolvedInputs.GateEvidence == nil {
		return nil, catalogSealTransition("completion candidate gate evidence is missing")
	}
	canonical, err := candidate.ResolvedInputs.GateEvidence.Canonical()
	if err != nil {
		return nil, catalogSealRepositoryError(err)
	}
	if canonical.Outcome != release.GateSuccess && canonical.Outcome != release.GateWarning {
		return nil, catalogSealTransition("completion candidate gate evidence is not successful")
	}
	return &canonical, nil
}

// Lookup implements catalogseal.SealRepository. A missing row is the only
// condition represented by ErrSealNotFound; malformed or incomplete durable
// state is a repository transition failure and is never treated as new work.
func (r *Repository) Lookup(ctx context.Context, id string) (catalogseal.SealRecord, error) {
	if r == nil || r.db == nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(errors.New("repository is not open"))
	}
	state, err := loadCatalogSealTx(ctx, r.db, id)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	return state.record, nil
}

// Prepare durably records the full generic identity before upload. Existing
// identical rows converge; every differing field is an identity conflict.
func (r *Repository) Prepare(ctx context.Context, identity catalogseal.SealIdentity) (catalogseal.SealRecord, error) {
	if r == nil || r.db == nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(errors.New("repository is not open"))
	}
	if err := validateCatalogSealIdentity(identity); err != nil {
		return catalogseal.SealRecord{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	defer tx.Rollback()
	if existing, readErr := loadCatalogSealTx(ctx, tx, identity.SealID); readErr == nil {
		if !sameCatalogSealIdentity(existing.record.Identity, identity) {
			return catalogseal.SealRecord{}, catalogSealIdentityConflict("seal identity differs from durable state")
		}
		if err := tx.Commit(); err != nil {
			return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
		}
		return existing.record, nil
	} else if !errors.Is(readErr, catalogseal.ErrSealNotFound) {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(readErr)
	}
	attempt, err := deliveryBuildAttemptByIDTx(ctx, tx, identity.Attempt.ID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogseal.SealRecord{}, catalogSealTransition("build attempt is missing")
	}
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	if attempt.PlanID != identity.Plan.ID || attempt.PlanDigest != identity.Plan.Digest ||
		attempt.ExecutionDigest != identity.Plan.ExecutionDigest || attempt.PhysicalPoolID != identity.Pool.ID ||
		attempt.WriterLeaseID != identity.Attempt.WriterLeaseID || attempt.Status != deployment.DeliveryBuildSealing ||
		attempt.SealID != "" || attempt.CandidateID != "" {
		return catalogseal.SealRecord{}, catalogSealIdentityConflict("build attempt does not match seal identity")
	}
	lease, err := deliveryWriterLeaseByIDTx(ctx, tx, identity.Attempt.WriterLeaseID)
	if errors.Is(err, sql.ErrNoRows) {
		return catalogseal.SealRecord{}, catalogSealTransition("writer lease is missing")
	}
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	created := time.Now().UTC()
	if r.catalogSealNow != nil {
		created = r.catalogSealNow().UTC()
	}
	if created.IsZero() {
		return catalogseal.SealRecord{}, catalogSealTransition("seal clock returned zero time")
	}
	if lease.AttemptID != attempt.ID || lease.PhysicalPoolID != attempt.PhysicalPoolID || lease.Status != deployment.DeliveryLeaseActive || !lease.ExpiresAt.After(created) {
		return catalogseal.SealRecord{}, catalogSealTransition("writer lease is not the exact active lease")
	}
	err = deploydb.New(tx).CreateDeliveryCatalogSealWithIdentity(ctx, deploydb.CreateDeliveryCatalogSealWithIdentityParams{
		ID: identity.SealID, AttemptID: identity.Attempt.ID, PlanID: identity.Plan.ID, PlanDigest: identity.Plan.Digest,
		ExecutionDigest: identity.Plan.ExecutionDigest, PhysicalPoolID: identity.Pool.ID, NULLIF: identity.CatalogDigest,
		NULLIF_2: attempt.BaseCatalogDigest, NULLIF_3: attempt.BasePhysicalPoolID, CompatibilityDigest: identity.Pool.CompatibilityDigest,
		ObjectKey: identity.ObjectKey, ObjectSize: identity.ObjectSize, CreatedAt: deliveryTime(created), IdentityCandidateID: presentString(identity.Candidate.ID),
		IdentityClosureDigest: presentString(identity.Closure.Digest), IdentityQualificationDigest: presentString(identity.Qualification.Digest),
		ServingArtifactID: identity.Candidate.ServingArtifactID, ServingArtifactDigest: identity.Candidate.ServingArtifactDigest, ServingStateID: identity.Candidate.ServingStateID,
	})
	if err != nil {
		if existing, readErr := loadCatalogSealTx(ctx, tx, identity.SealID); readErr == nil {
			if sameCatalogSealIdentity(existing.record.Identity, identity) {
				if commitErr := tx.Commit(); commitErr != nil {
					return catalogseal.SealRecord{}, catalogSealRepositoryError(commitErr)
				}
				return existing.record, nil
			}
			return catalogseal.SealRecord{}, catalogSealIdentityConflict("seal identity differs from durable state")
		}
		// UNIQUE(attempt_id) and UNIQUE(object_key) are identity fences too.
		// Resolve those collisions explicitly so a retry with another seal ID
		// cannot surface a provider-specific SQLite constraint as a generic
		// repository failure.
		if bound, bindErr := deploydb.New(tx).GetDeliveryCatalogSealIDByAttempt(ctx, identity.Attempt.ID); bindErr == nil && bound != identity.SealID {
			return catalogseal.SealRecord{}, catalogSealIdentityConflict("build attempt is already bound to another seal")
		}
		if bound, keyErr := deploydb.New(tx).GetDeliveryCatalogSealIDByObjectKey(ctx, identity.ObjectKey); keyErr == nil && bound != identity.SealID {
			return catalogseal.SealRecord{}, catalogSealIdentityConflict("catalog object key is already bound to another seal")
		}
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	state, err := loadCatalogSealTx(ctx, tx, identity.SealID)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	if !sameCatalogSealIdentity(state.record.Identity, identity) || state.record.Status != catalogseal.SealPreparing {
		return catalogseal.SealRecord{}, catalogSealIdentityConflict("inserted seal identity did not round-trip")
	}
	if err := tx.Commit(); err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	return state.record, nil
}

// MarkUploaded is a one-way, idempotent CAS. It never changes identity or
// evidence and therefore remains safe to retry after a lost acknowledgement.
func (r *Repository) MarkUploaded(ctx context.Context, id string) (catalogseal.SealRecord, error) {
	if r == nil || r.db == nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(errors.New("repository is not open"))
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	defer tx.Rollback()
	state, err := loadCatalogSealTx(ctx, tx, id)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	if state.record.Status == catalogseal.SealUploaded || state.record.Status == catalogseal.SealVerified {
		if err := tx.Commit(); err != nil {
			return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
		}
		return state.record, nil
	}
	if state.record.Status != catalogseal.SealPreparing {
		return catalogseal.SealRecord{}, catalogSealTransition("seal is not preparing")
	}
	result, err := deploydb.New(tx).MarkDeliveryCatalogSealUploaded(ctx, id)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return catalogseal.SealRecord{}, catalogSealTransition("seal upload CAS failed")
	}
	state, err = loadCatalogSealTx(ctx, tx, id)
	if err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	if err := tx.Commit(); err != nil {
		return catalogseal.SealRecord{}, catalogSealRepositoryError(err)
	}
	return state.record, nil
}

func (r *Repository) catalogSealNowUTC() (time.Time, error) {
	now := time.Now().UTC()
	if r != nil && r.catalogSealNow != nil {
		now = r.catalogSealNow().UTC()
	}
	if now.IsZero() {
		return time.Time{}, catalogSealTransition("seal clock returned zero time")
	}
	return now, nil
}

func deploymentSealForCompletion(state durableCatalogSeal, closure, qualification string, verifiedAt time.Time) (deployment.CatalogSeal, error) {
	seal := state.seal
	seal.Status = deployment.CatalogSealVerified
	seal.ClosureDigest, seal.QualificationDigest, seal.VerifiedAt = closure, qualification, verifiedAt
	seal.FailureCode = ""
	if err := seal.Validate(); err != nil {
		return deployment.CatalogSeal{}, err
	}
	return seal, nil
}

func deliveryCandidateImmutableEqual(a, b deployment.DeliveryCandidate) bool {
	return a.ID == b.ID && a.PlanID == b.PlanID && a.PlanDigest == b.PlanDigest && a.TargetID == b.TargetID &&
		a.ProjectID == b.ProjectID && a.Environment == b.Environment && a.SourceDigest == b.SourceDigest &&
		a.ExecutionDigest == b.ExecutionDigest && a.BaseGenerationID == b.BaseGenerationID &&
		a.BaseTargetRevision == b.BaseTargetRevision && a.SealID == b.SealID && a.CatalogDigest == b.CatalogDigest &&
		a.BaseCatalogDigest == b.BaseCatalogDigest && a.BasePhysicalPoolID == b.BasePhysicalPoolID &&
		a.CompatibilityDigest == b.CompatibilityDigest && a.CatalogObjectKey == b.CatalogObjectKey &&
		a.PhysicalPoolID == b.PhysicalPoolID && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.ServingStateID == b.ServingStateID && a.CreatedAt.Equal(b.CreatedAt)
}

func insertDeliveryCandidateReadyTx(ctx context.Context, tx *sql.Tx, candidate deployment.DeliveryCandidate) error {
	resolvedJSON, err := json.Marshal(candidate.ResolvedInputs)
	if err != nil {
		return err
	}
	err = deploydb.New(tx).CreateDeliveryCandidateReady(ctx, deploydb.CreateDeliveryCandidateReadyParams{ID: candidate.ID, PlanID: candidate.PlanID, PlanDigest: candidate.PlanDigest, TargetID: candidate.TargetID, ProjectID: candidate.ProjectID.String(), Environment: candidate.Environment, SourceDigest: candidate.SourceDigest, ExecutionDigest: candidate.ExecutionDigest, NULLIF: candidate.BaseGenerationID, BaseTargetRevision: candidate.BaseTargetRevision, SealID: candidate.SealID, CatalogDigest: candidate.CatalogDigest, NULLIF_2: candidate.BaseCatalogDigest, NULLIF_3: candidate.BasePhysicalPoolID, CompatibilityDigest: candidate.CompatibilityDigest, CatalogObjectKey: candidate.CatalogObjectKey, PhysicalPoolID: candidate.PhysicalPoolID, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest, ServingStateID: candidate.ServingStateID, QualificationDigest: presentString(candidate.QualificationDigest), ResolvedInputsJson: string(resolvedJSON), ResolvedInputsDigest: candidate.ResolvedInputs.EvidenceDigest, CreatedAt: deliveryTime(candidate.CreatedAt), ReadyAt: presentString(deliveryTime(candidate.ReadyAt))})
	return err
}

// CompleteVerified is the sole generic operation which can expose a ready
// candidate. Seal verification, candidate readiness, build sealing, and exact
// writer-lease release all commit or roll back together.
func (r *Repository) CompleteVerified(ctx context.Context, input catalogseal.CompleteInput) (catalogseal.Completion, error) {
	if r == nil || r.db == nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(errors.New("repository is not open"))
	}
	if input.SealID == "" || input.CandidateID == "" || input.SealID != input.Seal.SealID || input.CandidateID != input.Seal.Candidate.ID ||
		input.ClosureDigest != input.Seal.Closure.Digest || input.QualificationDigest != input.Seal.Qualification.Digest {
		return catalogseal.Completion{}, catalogSealIdentityConflict("completion input identity is inconsistent")
	}
	if err := validateCatalogSealIdentity(input.Seal); err != nil {
		return catalogseal.Completion{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	defer tx.Rollback()
	state, err := loadCatalogSealTx(ctx, tx, input.SealID)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	if !sameCatalogSealIdentity(state.record.Identity, input.Seal) {
		return catalogseal.Completion{}, catalogSealIdentityConflict("completion identity differs from durable state")
	}
	if state.record.Status != catalogseal.SealUploaded && state.record.Status != catalogseal.SealVerified {
		return catalogseal.Completion{}, catalogSealTransition("seal is not uploaded or verified")
	}
	now, err := r.catalogSealNowUTC()
	if err != nil {
		return catalogseal.Completion{}, err
	}
	if state.lease.Status == deployment.DeliveryLeaseActive && !state.lease.ExpiresAt.After(now) {
		return catalogseal.Completion{}, catalogSealTransition("writer lease is expired")
	}
	if state.lease.Status == deployment.DeliveryLeaseReleased &&
		(state.record.Status != catalogseal.SealVerified || state.attempt.Status != deployment.DeliveryBuildSealed) {
		return catalogseal.Completion{}, catalogSealTransition("writer lease was released before verified completion")
	}
	terminalSeal := state.seal
	if state.record.Status == catalogseal.SealUploaded {
		terminalSeal, err = deploymentSealForCompletion(state, input.ClosureDigest, input.QualificationDigest, now)
		if err != nil {
			return catalogseal.Completion{}, catalogSealRepositoryError(err)
		}
		result, updateErr := deploydb.New(tx).MarkDeliveryCatalogSealVerified(ctx, deploydb.MarkDeliveryCatalogSealVerifiedParams{ClosureDigest: presentString(input.ClosureDigest), QualificationDigest: presentString(input.QualificationDigest), VerifiedAt: presentString(deliveryTime(now)), ID: input.SealID})
		if updateErr != nil {
			return catalogseal.Completion{}, catalogSealRepositoryError(updateErr)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return catalogseal.Completion{}, catalogSealTransition("seal verification CAS failed")
		}
	} else {
		if state.seal.ClosureDigest != input.ClosureDigest || state.seal.QualificationDigest != input.QualificationDigest {
			return catalogseal.Completion{}, catalogSealIdentityConflict("verification evidence differs from durable state")
		}
		terminalSeal = state.seal
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, state.attempt.PlanID)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	var resolvedInputs deployment.DeliveryResolvedBuildInputs
	if input.ResolvedInputsJSON != "" {
		if err := json.Unmarshal([]byte(input.ResolvedInputsJSON), &resolvedInputs); err != nil {
			return catalogseal.Completion{}, catalogSealIdentityConflict("resolved input evidence is invalid JSON")
		}
		if input.ResolvedInputsDigest != "" {
			resolvedInputs.EvidenceDigest = input.ResolvedInputsDigest
		}
	} else if input.ResolvedInputsDigest != "" {
		return catalogseal.Completion{}, catalogSealIdentityConflict("resolved input digest has no evidence bytes")
	}
	resolvedInputs, err = deployment.ValidateDeliveryResolvedBuildInputs(plan, resolvedInputs)
	if err != nil {
		return catalogseal.Completion{}, catalogSealIdentityConflict("%s", err.Error())
	}
	candidateTemplate := deployment.DeliveryCandidate{
		ID: input.CandidateID, PlanID: plan.ID, PlanDigest: plan.Digest, TargetID: plan.TargetID,
		ProjectID: plan.ProjectID, Environment: plan.Environment, SourceDigest: plan.SourceDigest,
		ExecutionDigest: plan.ExecutionDigest, BaseGenerationID: plan.BaseGenerationID,
		BaseTargetRevision: plan.BaseTargetRevision, SealID: terminalSeal.ID,
		CatalogDigest: terminalSeal.CatalogDigest, BaseCatalogDigest: state.attempt.BaseCatalogDigest,
		BasePhysicalPoolID: state.attempt.BasePhysicalPoolID, CompatibilityDigest: terminalSeal.CompatibilityDigest,
		CatalogObjectKey: terminalSeal.ObjectKey, PhysicalPoolID: terminalSeal.PhysicalPoolID,
		ServingArtifactID: state.record.Identity.Candidate.ServingArtifactID, ServingArtifactDigest: state.record.Identity.Candidate.ServingArtifactDigest, ServingStateID: state.record.Identity.Candidate.ServingStateID,
		ResolvedInputs: resolvedInputs,
		CreatedAt:      terminalSeal.CreatedAt,
	}
	preparedCandidate, err := deployment.NewDeliveryCandidate(candidateTemplate)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	readyCandidate, err := preparedCandidate.MarkReady(terminalSeal, now)
	if err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	existingCandidate, candidateErr := deliveryCandidateByIDTx(ctx, tx, input.CandidateID)
	if candidateErr == nil {
		if !deliveryCandidateImmutableEqual(existingCandidate, preparedCandidate) {
			return catalogseal.Completion{}, catalogSealIdentityConflict("candidate identity differs from durable state")
		}
		if existingCandidate.ResolvedInputs.EvidenceDigest != readyCandidate.ResolvedInputs.EvidenceDigest {
			return catalogseal.Completion{}, catalogSealIdentityConflict("candidate resolved-input evidence differs from durable state")
		}
		switch existingCandidate.Status {
		case deployment.DeliveryCandidateReady:
			if existingCandidate.QualificationDigest != readyCandidate.QualificationDigest {
				return catalogseal.Completion{}, catalogSealIdentityConflict("candidate qualification differs from durable state")
			}
			readyCandidate = existingCandidate
		case deployment.DeliveryCandidatePreparing:
			result, updateErr := deploydb.New(tx).MarkDeliveryCandidateReady(ctx, deploydb.MarkDeliveryCandidateReadyParams{QualificationDigest: presentString(readyCandidate.QualificationDigest), ReadyAt: presentString(deliveryTime(readyCandidate.ReadyAt)), ID: input.CandidateID})
			if updateErr != nil {
				return catalogseal.Completion{}, catalogSealRepositoryError(updateErr)
			}
			if n, _ := result.RowsAffected(); n != 1 {
				return catalogseal.Completion{}, catalogSealTransition("candidate ready CAS failed")
			}
		default:
			return catalogseal.Completion{}, catalogSealTransition("candidate is not preparing or ready")
		}
	} else if errors.Is(candidateErr, sql.ErrNoRows) {
		if err := insertDeliveryCandidateReadyTx(ctx, tx, readyCandidate); err != nil {
			if existing, readErr := deliveryCandidateByIDTx(ctx, tx, input.CandidateID); readErr == nil && existing.Status == deployment.DeliveryCandidateReady && deliveryCandidateImmutableEqual(existing, preparedCandidate) && existing.QualificationDigest == readyCandidate.QualificationDigest {
				readyCandidate = existing
			} else {
				return catalogseal.Completion{}, catalogSealRepositoryError(err)
			}
		}
	} else {
		return catalogseal.Completion{}, catalogSealRepositoryError(candidateErr)
	}
	sealedAttempt := state.attempt
	switch sealedAttempt.Status {
	case deployment.DeliveryBuildSealing:
		sealedAttempt, err = sealedAttempt.SealCandidate(input.SealID, input.CandidateID, now)
		if err != nil {
			return catalogseal.Completion{}, catalogSealRepositoryError(err)
		}
		result, updateErr := deploydb.New(tx).SealDeliveryBuildAttempt(ctx, deploydb.SealDeliveryBuildAttemptParams{SealID: presentString(input.SealID), CandidateID: presentString(input.CandidateID), Revision: sealedAttempt.Revision, UpdatedAt: deliveryTime(sealedAttempt.UpdatedAt), TerminalAt: nullableString(sealedAttempt.TerminalAt), ID: sealedAttempt.ID, Revision_2: state.attempt.Revision})
		if updateErr != nil {
			return catalogseal.Completion{}, catalogSealRepositoryError(updateErr)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return catalogseal.Completion{}, catalogSealTransition("build seal CAS failed")
		}
	case deployment.DeliveryBuildSealed:
		if state.attempt.SealID != input.SealID || state.attempt.CandidateID != input.CandidateID {
			return catalogseal.Completion{}, catalogSealIdentityConflict("sealed build attempt points at another seal or candidate")
		}
	default:
		return catalogseal.Completion{}, catalogSealTransition("build attempt is not sealing or sealed")
	}
	leaseReleased := false
	switch state.lease.Status {
	case deployment.DeliveryLeaseActive:
		result, updateErr := deploydb.New(tx).ReleaseDeliveryWriterLeaseExact(ctx, deploydb.ReleaseDeliveryWriterLeaseExactParams{ReleasedAt: presentString(deliveryTime(now)), ID: state.lease.ID, AttemptID: state.attempt.ID, PhysicalPoolID: state.attempt.PhysicalPoolID, OwnerID: state.lease.OwnerID, Epoch: state.lease.Epoch, Julianday: deliveryTime(now)})
		if updateErr != nil {
			return catalogseal.Completion{}, catalogSealRepositoryError(updateErr)
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return catalogseal.Completion{}, catalogSealTransition("exact writer lease release CAS failed")
		}
		leaseReleased = true
	case deployment.DeliveryLeaseReleased:
		if state.lease.AttemptID != state.attempt.ID || state.lease.PhysicalPoolID != state.attempt.PhysicalPoolID {
			return catalogseal.Completion{}, catalogSealIdentityConflict("released writer lease binding differs from build attempt")
		}
		leaseReleased = true
	default:
		return catalogseal.Completion{}, catalogSealTransition("writer lease is not active or already released")
	}
	qualificationRequest := deployment.CanonicalDeliveryDigest([]byte("qualification:" + readyCandidate.ID + ":" + readyCandidate.QualificationDigest))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, qualificationRequest, "candidate_qualified", "candidate", readyCandidate.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(state.lease.OwnerID), EventKind: "candidate_qualified", ObjectKind: "candidate", ObjectID: readyCandidate.ID,
		RequestDigest: qualificationRequest, PlanDigest: plan.Digest, ResultDigest: readyCandidate.QualificationDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(readyCandidate.Status)}, CreatedAt: readyCandidate.ReadyAt,
	}); err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	sealRequest := deployment.CanonicalDeliveryDigest([]byte("seal:" + readyCandidate.ID + ":" + readyCandidate.SealID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, sealRequest, "candidate_sealed", "candidate", readyCandidate.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(state.lease.OwnerID), EventKind: "candidate_sealed", ObjectKind: "candidate", ObjectID: readyCandidate.ID,
		RequestDigest: sealRequest, PlanDigest: plan.Digest, ResultDigest: readyCandidate.CatalogDigest, Outcome: "accepted",
		Details: map[string]any{"status": string(readyCandidate.Status)}, CreatedAt: readyCandidate.ReadyAt,
	}); err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	leaseRequest := deployment.CanonicalDeliveryDigest([]byte("lease-released:" + state.lease.ID + ":" + now.UTC().Format(time.RFC3339Nano)))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, leaseRequest, "lease_released", "writer_lease", state.lease.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(state.lease.OwnerID), EventKind: "lease_released", ObjectKind: "writer_lease", ObjectID: state.lease.ID,
		RequestDigest: leaseRequest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"status": "released"}, CreatedAt: now,
	}); err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	if err := tx.Commit(); err != nil {
		return catalogseal.Completion{}, catalogSealRepositoryError(err)
	}
	return catalogseal.Completion{Seal: catalogseal.SealRecord{Identity: state.record.Identity, Status: catalogseal.SealVerified}, CandidateID: input.CandidateID, LeaseReleased: leaseReleased}, nil
}
