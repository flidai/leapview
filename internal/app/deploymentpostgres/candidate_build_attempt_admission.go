package deploymentpostgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/jackc/pgx/v5"
)

// CandidateBuildArtifactInput is the immutable serving-artifact identity
// produced for a candidate build. Delivery attempt and fencing identity are
// intentionally absent: admission derives those from the records admitted in
// the same transaction.
type CandidateBuildArtifactInput struct {
	ServingArtifactID     string
	ServingArtifactDigest string
	ServingStateID        string
}

// CandidateBuildAttemptAdmissionInput carries only value evidence for one
// candidate build attempt. Lease and Attempt provide the caller's immutable
// delivery inputs; the admission transaction assigns the fencing epoch, lease
// expiry, and relation namespace from the admitted lease. CatalogID is the
// sole DuckLake-specific value because all other DuckLake begin fields are
// derived from the admitted delivery attempt and lease.
type CandidateBuildAttemptAdmissionInput struct {
	Lease     deploymentnative.LeaseInput
	Attempt   deploymentnative.BuildAttemptInput
	Artifact  CandidateBuildArtifactInput
	CatalogID string
}

// CandidateBuildAttemptAdmissionResult is the value-only evidence returned
// after the lease, delivery attempt, artifact binding, and DuckLake attempt
// ledger commit together.
type CandidateBuildAttemptAdmissionResult struct {
	Lease           deploymentnative.DeliveryLease
	Attempt         deploymentnative.DeliveryBuildAttempt
	Artifact        deploymentnative.BuildArtifactBinding
	DuckLakeAttempt ducklakepostgres.AttemptEvidence
}

// CandidateBuildAttemptAdmission is the application-owned native capability
// for admitting a candidate build attempt across delivery and DuckLake. The
// convenience method owns its transaction; the Tx method composes into one
// supplied by the caller.
type CandidateBuildAttemptAdmission interface {
	AdmitCandidateBuildAttempt(context.Context, CandidateBuildAttemptAdmissionInput) (CandidateBuildAttemptAdmissionResult, error)
	AdmitCandidateBuildAttemptTx(context.Context, deploymentnative.Tx, CandidateBuildAttemptAdmissionInput) (CandidateBuildAttemptAdmissionResult, error)
}

// CandidateBuildAttemptDuckLakeAuthority is the narrow transaction-aware
// surface required by candidate admission. BeginAttemptTx receives the
// delivery-owned transaction, so this authority never opens or commits a
// second transaction.
type CandidateBuildAttemptDuckLakeAuthority interface {
	Configured() bool
	BeginAttemptTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.BeginAttemptInput) (ducklakepostgres.AttemptEvidence, error)
}

type candidateBuildAttemptAdmitter struct {
	delivery *deploymentnative.Repository
	ducklake CandidateBuildAttemptDuckLakeAuthority
}

var _ CandidateBuildAttemptAdmission = (*candidateBuildAttemptAdmitter)(nil)
var _ CandidateBuildAttemptDuckLakeAuthority = (*ducklakepostgres.Repository)(nil)

// NewCandidateBuildAttemptAdmission constructs the native candidate admission
// capability. It does not begin a transaction or perform schema work.
func NewCandidateBuildAttemptAdmission(delivery *deploymentnative.Repository, ducklake CandidateBuildAttemptDuckLakeAuthority) (CandidateBuildAttemptAdmission, error) {
	if !deliveryConfigured(delivery) {
		return nil, errors.New("candidate build-attempt admission requires a configured PostgreSQL delivery authority")
	}
	if ducklake == nil || !ducklake.Configured() {
		return nil, errors.New("candidate build-attempt admission requires a configured DuckLake authority")
	}
	return &candidateBuildAttemptAdmitter{delivery: delivery, ducklake: ducklake}, nil
}

func deliveryConfigured(delivery *deploymentnative.Repository) bool {
	return delivery != nil && delivery.Configured()
}

// AdmitCandidateBuildAttempt atomically admits the target lease, delivery
// build attempt, immutable serving-artifact binding, and DuckLake attempt
// ledger. The transaction returned by delivery.Begin is the only transaction
// used; this convenience method owns its commit and rollback.
func (a *candidateBuildAttemptAdmitter) AdmitCandidateBuildAttempt(ctx context.Context, input CandidateBuildAttemptAdmissionInput) (CandidateBuildAttemptAdmissionResult, error) {
	if a == nil || !deliveryConfigured(a.delivery) || a.ducklake == nil || !a.ducklake.Configured() {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: candidate build-attempt admission authorities are not configured", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeCandidateBuildAttemptAdmissionInput(input)
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	tx, err := a.delivery.Begin(ctx)
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	result, err := a.AdmitCandidateBuildAttemptTx(ctx, tx, normalized)
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	committed = true
	return result, nil
}

// AdmitCandidateBuildAttemptTx admits the target lease, delivery build
// attempt, immutable serving-artifact binding, and DuckLake attempt ledger
// into the caller-owned transaction. It never commits or rolls back tx.
func (a *candidateBuildAttemptAdmitter) AdmitCandidateBuildAttemptTx(ctx context.Context, tx deploymentnative.Tx, input CandidateBuildAttemptAdmissionInput) (CandidateBuildAttemptAdmissionResult, error) {
	if a == nil || !deliveryConfigured(a.delivery) || a.ducklake == nil || !a.ducklake.Configured() {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: candidate build-attempt admission authorities are not configured", deploymentnative.ErrInvalid)
	}
	if tx == nil {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: candidate build-attempt admission requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeCandidateBuildAttemptAdmissionInput(input)
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: candidate build-attempt admission requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}

	lease, err := a.delivery.AcquireLeaseTx(ctx, tx, normalized.Lease)
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	if lease.LeaseID != normalized.Lease.LeaseID || lease.TargetID != normalized.Lease.TargetID || lease.OwnerID != normalized.Lease.OwnerID || lease.FencingEpoch <= 0 || lease.State != "active" || !lease.ExpiresAt.Equal(normalized.Lease.ExpiresAt) {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: admitted delivery lease identity drifted", deploymentnative.ErrConflict)
	}
	attemptInput := normalized.Attempt
	attemptInput.FencingEpoch = lease.FencingEpoch
	attemptInput.LeaseExpiresAt = lease.ExpiresAt
	attemptInput.Namespace, err = deploymentdomain.DeriveRelationNamespace(deploymentdomain.RelationNamespaceInput{
		CandidateID: attemptInput.CandidateID, AttemptID: attemptInput.AttemptID, FencingEpoch: attemptInput.FencingEpoch,
	})
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: derive relation namespace: %v", deploymentnative.ErrInvalid, err)
	}
	attempt, err := a.delivery.BeginBuildAttemptTx(ctx, tx, attemptInput)
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	if attempt.AttemptID != attemptInput.AttemptID || attempt.PlanID != attemptInput.PlanID || attempt.CandidateID != attemptInput.CandidateID || attempt.OwnerID != attemptInput.OwnerID || attempt.PhysicalPoolID != attemptInput.PhysicalPoolID || attempt.FencingEpoch != attemptInput.FencingEpoch || attempt.RequestDigest != attemptInput.RequestDigest || attempt.PlanDigest != attemptInput.PlanDigest || attempt.Namespace != attemptInput.Namespace || attempt.SessionIdentity != attemptInput.SessionIdentity || attempt.State != deploymentnative.AttemptRunning || !attempt.LeaseExpiresAt.Equal(lease.ExpiresAt) {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: admitted delivery build attempt identity drifted", deploymentnative.ErrConflict)
	}

	binding, err := a.delivery.BindBuildArtifactTx(ctx, tx, deploymentnative.BuildArtifactBindingInput{
		AttemptID:             attempt.AttemptID,
		ServingArtifactID:     normalized.Artifact.ServingArtifactID,
		ServingArtifactDigest: normalized.Artifact.ServingArtifactDigest,
		ServingStateID:        normalized.Artifact.ServingStateID,
		OwnerID:               attempt.OwnerID,
		FencingEpoch:          attempt.FencingEpoch,
	})
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	if binding.AttemptID != attempt.AttemptID || binding.ServingArtifactID != normalized.Artifact.ServingArtifactID || binding.ServingArtifactDigest != normalized.Artifact.ServingArtifactDigest || binding.ServingStateID != normalized.Artifact.ServingStateID {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: admitted serving artifact identity drifted", deploymentnative.ErrConflict)
	}

	duckAttempt, err := a.ducklake.BeginAttemptTx(ctx, tx, ducklakepostgres.BeginAttemptInput{
		AttemptID:       attempt.AttemptID,
		RequestDigest:   attempt.RequestDigest,
		PlanDigest:      attempt.PlanDigest,
		PhysicalPoolID:  attempt.PhysicalPoolID,
		CatalogID:       normalized.CatalogID,
		OwnerID:         attempt.OwnerID,
		FencingEpoch:    attempt.FencingEpoch,
		SessionIdentity: attempt.SessionIdentity,
		LeaseExpiresAt:  attempt.LeaseExpiresAt,
	})
	if err != nil {
		return CandidateBuildAttemptAdmissionResult{}, err
	}
	if duckAttempt.State != ducklakepostgres.AttemptRunning ||
		duckAttempt.AttemptID != attempt.AttemptID ||
		duckAttempt.RequestDigest != attempt.RequestDigest ||
		duckAttempt.PlanDigest != attempt.PlanDigest ||
		duckAttempt.PhysicalPoolID != attempt.PhysicalPoolID ||
		duckAttempt.CatalogID != normalized.CatalogID ||
		duckAttempt.OwnerID != attempt.OwnerID ||
		duckAttempt.FencingEpoch != attempt.FencingEpoch ||
		duckAttempt.SessionIdentity != attempt.SessionIdentity ||
		!duckAttempt.LeaseExpiresAt.Equal(attempt.LeaseExpiresAt) {
		return CandidateBuildAttemptAdmissionResult{}, fmt.Errorf("%w: DuckLake attempt identity drifted from admitted delivery attempt", deploymentnative.ErrConflict)
	}

	return CandidateBuildAttemptAdmissionResult{Lease: lease, Attempt: attempt, Artifact: binding, DuckLakeAttempt: duckAttempt}, nil
}

func normalizeCandidateBuildAttemptAdmissionInput(input CandidateBuildAttemptAdmissionInput) (CandidateBuildAttemptAdmissionInput, error) {
	out := input
	for label, value := range map[string]string{
		"lease id":   out.Lease.LeaseID,
		"attempt id": out.Attempt.AttemptID,
		"plan id":    out.Attempt.PlanID,
	} {
		canonical, err := canonicalUUID(value, label)
		if err != nil {
			return CandidateBuildAttemptAdmissionInput{}, err
		}
		switch label {
		case "lease id":
			out.Lease.LeaseID = canonical
		case "attempt id":
			out.Attempt.AttemptID = canonical
		case "plan id":
			out.Attempt.PlanID = canonical
		}
	}
	if out.Attempt.CandidateID == "" {
		return CandidateBuildAttemptAdmissionInput{}, fmt.Errorf("%w: candidate id is required", deploymentnative.ErrInvalid)
	}
	canonicalCandidate, err := canonicalUUID(out.Attempt.CandidateID, "candidate id")
	if err != nil {
		return CandidateBuildAttemptAdmissionInput{}, err
	}
	out.Attempt.CandidateID = canonicalCandidate
	if err := validateText(out.Lease.TargetID, "target id", 255); err != nil {
		return CandidateBuildAttemptAdmissionInput{}, err
	}
	for label, value := range map[string]string{
		"lease owner id":      out.Lease.OwnerID,
		"attempt owner id":    out.Attempt.OwnerID,
		"physical pool id":    out.Attempt.PhysicalPoolID,
		"serving artifact id": out.Artifact.ServingArtifactID,
		"serving state id":    out.Artifact.ServingStateID,
		"catalog id":          out.CatalogID,
	} {
		if err := validateText(value, label, 255); err != nil {
			return CandidateBuildAttemptAdmissionInput{}, err
		}
	}
	if out.Attempt.Namespace != "" {
		return CandidateBuildAttemptAdmissionInput{}, fmt.Errorf("%w: build attempt relation namespace is authority-derived and must be empty", deploymentnative.ErrInvalid)
	}
	for label, value := range map[string]string{
		"session identity": out.Attempt.SessionIdentity,
	} {
		if err := validateText(value, label, 512); err != nil {
			return CandidateBuildAttemptAdmissionInput{}, err
		}
	}
	for label, value := range map[string]string{
		"request digest":          out.Attempt.RequestDigest,
		"plan digest":             out.Attempt.PlanDigest,
		"serving artifact digest": out.Artifact.ServingArtifactDigest,
	} {
		if err := validateDigest(value, label); err != nil {
			return CandidateBuildAttemptAdmissionInput{}, err
		}
	}
	if out.Lease.OwnerID != out.Attempt.OwnerID {
		return CandidateBuildAttemptAdmissionInput{}, conflict("lease and build attempt owners differ")
	}
	leaseExpiry := out.Lease.ExpiresAt.UTC().Truncate(time.Microsecond)
	if leaseExpiry.IsZero() {
		return CandidateBuildAttemptAdmissionInput{}, fmt.Errorf("%w: lease expiry is required", deploymentnative.ErrInvalid)
	}
	if !out.Attempt.LeaseExpiresAt.IsZero() && !out.Attempt.LeaseExpiresAt.UTC().Truncate(time.Microsecond).Equal(leaseExpiry) {
		return CandidateBuildAttemptAdmissionInput{}, conflict("lease and build attempt expiries differ")
	}
	out.Lease.ExpiresAt = leaseExpiry
	out.Attempt.LeaseExpiresAt = leaseExpiry
	if out.Attempt.FencingEpoch != 0 {
		return CandidateBuildAttemptAdmissionInput{}, fmt.Errorf("%w: build attempt fencing epoch is authority-derived and must be zero", deploymentnative.ErrInvalid)
	}
	return out, nil
}
