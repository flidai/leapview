package deploymentpostgres

// This file owns the small control-plane transaction that prepares a native
// physical build for read-only recovery. It deliberately stops before marker
// resolution and generation admission: the caller receives the exact,
// normalized operation and delivery records needed by those later phases.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
)

// NativeBuildRecoveryPreparationInput is the pre-read identity of one native
// build whose operation has already been fenced to indeterminate.  The
// operation record is supplied by the caller's reservation/read phase; all
// dependent delivery records are read and checked before the atomic
// normalization transaction starts.
//
// PhysicalPoolID is bound to the admitted build contract. The attempt's
// fencing epoch, namespace, and session identity are immutable writer evidence
// and are therefore read from the deterministic attempt row, never supplied
// by a caller.
type NativeBuildRecoveryPreparationInput struct {
	Request       deploymentmodule.NativeDeliveryBuildRequest
	RequestDigest string
	Operation     deploymentmodule.NativeOperationRecord

	PhysicalPoolID string
}

// NativeBuildRecoveryPreparationResult contains the fresh records returned by
// the normalization transaction. Candidate and artifact binding are immutable
// pre-read evidence (the binding may be absent until final recovery admission);
// the operation, delivery attempt, and target lease are returned from the same
// transaction that changed their states.
type NativeBuildRecoveryPreparationResult struct {
	Operation       deploymentmodule.NativeOperationRecord
	Plan            deploymentnative.DeliveryPlan
	Candidate       deploymentnative.DeliveryCandidate
	Artifact        deploymentnative.BuildArtifactBinding
	DeliveryAttempt deploymentnative.DeliveryBuildAttempt
	Lease           deploymentnative.DeliveryLease

	CandidateID  string
	GenerationID string
	AttemptID    string
	LeaseID      string
}

// PrepareNativeBuildRecovery normalizes one already-indeterminate operation's
// delivery attempt and operation records and releases its deterministic target
// lease. The operation itself is only confirmed (never acquired or renewed);
// both direct and expiry fencing advance the durable generation once, so the
// predecessor lease is reconstructed solely to satisfy the confirmation
// predicate. The function owns one transaction and rolls it back on every mismatch or
// authority error.
func PrepareNativeBuildRecovery(
	ctx context.Context,
	repository *deploymentnative.Repository,
	operations deploymentmodule.NativeBuildOperationAuthority,
	attemptTermination AttemptTermination,
	input NativeBuildRecoveryPreparationInput,
) (NativeBuildRecoveryPreparationResult, error) {
	if repository == nil || !repository.Configured() || !repository.TransactionCapable() {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: recovery preparation requires a configured transaction-capable delivery authority", deploymentnative.ErrInvalid)
	}
	if nativeBuildOperationAuthorityIsNil(operations) || nativeBuildAuthorityNil(attemptTermination) {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: recovery preparation authorities are required", deploymentnative.ErrInvalid)
	}
	normalized, preRead, err := normalizeNativeBuildRecoveryPreparationInput(ctx, repository, input)
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	tx, err := repository.Begin(ctx)
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	result, err := prepareNativeBuildRecoveryTx(ctx, repository, tx, operations, attemptTermination, normalized, preRead)
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	committed = true
	return result, nil
}

// nativeBuildRecoveryPreRead is intentionally private so callers cannot bypass
// the identity checks performed by normalize... before the transaction starts.
type nativeBuildRecoveryPreRead struct {
	Plan      deploymentnative.DeliveryPlan
	Attempt   deploymentnative.DeliveryBuildAttempt
	Candidate deploymentnative.DeliveryCandidate
	Artifact  deploymentnative.BuildArtifactBinding
	Lease     deploymentnative.DeliveryLease

	CandidateID  string
	GenerationID string
	AttemptID    string
	LeaseID      string
	Evidence     json.RawMessage
}

func normalizeNativeBuildRecoveryPreparationInput(
	ctx context.Context,
	repository *deploymentnative.Repository,
	input NativeBuildRecoveryPreparationInput,
) (NativeBuildRecoveryPreparationInput, nativeBuildRecoveryPreRead, error) {
	if repository == nil {
		return normalizeNativeBuildRecoveryPreparationValues(input, nativeBuildRecoveryPreRead{})
	}
	normalized, empty, err := normalizeNativeBuildRecoveryPreparationValues(input, nativeBuildRecoveryPreRead{})
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	plan, err := repository.LoadPlan(ctx, normalized.Request.PlanID.String())
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, fmt.Errorf("load recovery build plan: %w", err)
	}
	if plan.PlanID != normalized.Request.PlanID.String() || plan.TargetID != normalized.Request.TargetID {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, fmt.Errorf("%w: recovery plan identity differs from request", deploymentnative.ErrConflict)
	}
	if err := validateDigest(plan.PlanDigest, "plan digest"); err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	candidateID, err := nativeBuildConsequenceID(normalized.Operation.OperationID, "candidate")
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	generationID, err := nativeBuildConsequenceID(normalized.Operation.OperationID, "generation")
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	attemptID, err := nativeBuildConsequenceID(normalized.Operation.OperationID, "attempt")
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	leaseID, err := nativeBuildConsequenceID(normalized.Operation.OperationID, "lease")
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	attempt, err := repository.LoadBuildAttempt(ctx, attemptID)
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, fmt.Errorf("load recovery build attempt: %w", err)
	}
	candidate, err := repository.LoadCandidate(ctx, candidateID)
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, fmt.Errorf("load recovery candidate: %w", err)
	}
	binding, err := repository.LoadBuildArtifactBinding(ctx, attemptID)
	if err != nil && !errors.Is(err, deploymentnative.ErrNotFound) {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, fmt.Errorf("load recovery artifact binding: %w", err)
	}
	if errors.Is(err, deploymentnative.ErrNotFound) {
		binding = deploymentnative.BuildArtifactBinding{}
	}
	lease, err := repository.Lease(ctx, leaseID)
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, fmt.Errorf("load recovery target lease: %w", err)
	}
	evidence := append(json.RawMessage(nil), normalized.Operation.AttemptEvidence...)
	empty = nativeBuildRecoveryPreRead{Plan: plan, Attempt: attempt, Candidate: candidate, Artifact: binding, Lease: lease, CandidateID: candidateID, GenerationID: generationID, AttemptID: attemptID, LeaseID: leaseID, Evidence: evidence}
	if err := validateNativeBuildRecoveryPreRead(normalized, empty); err != nil {
		return NativeBuildRecoveryPreparationInput{}, nativeBuildRecoveryPreRead{}, err
	}
	return normalized, empty, nil
}

func normalizeNativeBuildRecoveryPreparationValues(input NativeBuildRecoveryPreparationInput, preRead nativeBuildRecoveryPreRead) (NativeBuildRecoveryPreparationInput, nativeBuildRecoveryPreRead, error) {
	request, err := normalizeNativeBuildRequest(input.Request)
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, preRead, err
	}
	digest, err := nativeBuildRequestDigest(request)
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, preRead, err
	}
	if input.RequestDigest != digest {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery request digest differs from canonical request", deploymentnative.ErrConflict)
	}
	op := input.Operation
	if op.State != deploymentmodule.NativeOperationStateIndeterminate {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery requires an indeterminate operation", deploymentnative.ErrConflict)
	}
	if op.Scope != request.TargetID || op.OperationType != nativeBuildOperationType || op.IdempotencyKey != request.IdempotencyKey || op.RequestDigest != digest {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery operation identity differs from request", deploymentnative.ErrConflict)
	}
	if _, err := canonicalUUIDv7(op.OperationID); err != nil {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery operation identity: %v", deploymentnative.ErrInvalid, err)
	}
	if _, err := canonicalUUIDv7(op.OwnerID); err != nil {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery operation owner identity: %v", deploymentnative.ErrInvalid, err)
	}
	if op.FencingGeneration <= 1 || op.LeaseExpiresAt.IsZero() || !op.LeaseExpiresAt.Equal(op.LeaseExpiresAt.UTC()) {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery operation predecessor fence is incomplete", deploymentnative.ErrInvalid)
	}
	attemptID, err := nativeBuildConsequenceID(op.OperationID, "attempt")
	if err != nil {
		return NativeBuildRecoveryPreparationInput{}, preRead, err
	}
	if op.AttemptID != attemptID || op.AttemptIdentity != "native-build/"+op.OperationID {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery operation attempt identity is not deterministic", deploymentnative.ErrConflict)
	}
	evidence, err := canonicalTerminationEvidence(op.AttemptEvidence)
	if err != nil || !bytes.Equal(evidence, op.AttemptEvidence) {
		return NativeBuildRecoveryPreparationInput{}, preRead, fmt.Errorf("%w: recovery operation attempt evidence is not canonical", deploymentnative.ErrInvalid)
	}
	if err := validateText(input.PhysicalPoolID, "physical pool id", 255); err != nil {
		return NativeBuildRecoveryPreparationInput{}, preRead, err
	}
	return NativeBuildRecoveryPreparationInput{Request: request, RequestDigest: digest, Operation: op, PhysicalPoolID: input.PhysicalPoolID}, preRead, nil
}

func validateNativeBuildRecoveryPreRead(input NativeBuildRecoveryPreparationInput, preRead nativeBuildRecoveryPreRead) error {
	attempt := preRead.Attempt
	if preRead.AttemptID == "" || attempt.AttemptID != preRead.AttemptID || attempt.PlanID != input.Request.PlanID.String() || attempt.CandidateID != preRead.CandidateID || attempt.OwnerID != input.Request.PrincipalID || attempt.PhysicalPoolID != input.PhysicalPoolID || attempt.FencingEpoch <= 0 || attempt.RequestDigest != input.RequestDigest || attempt.PlanDigest != preRead.Plan.PlanDigest || attempt.Namespace == "" || attempt.SessionIdentity == "" || attempt.LeaseExpiresAt.IsZero() || !attempt.LeaseExpiresAt.Equal(input.Operation.LeaseExpiresAt) {
		return fmt.Errorf("%w: recovery attempt identity differs", deploymentnative.ErrConflict)
	}
	if attempt.State != deploymentnative.AttemptRunning && attempt.State != deploymentnative.AttemptIndeterminate {
		return fmt.Errorf("%w: recovery attempt is not recoverable", deploymentnative.ErrConflict)
	}
	if preRead.Lease.State == "released" && attempt.State != deploymentnative.AttemptIndeterminate {
		return fmt.Errorf("%w: direct indeterminate recovery requires an indeterminate delivery attempt", deploymentnative.ErrConflict)
	}
	if preRead.Candidate.CandidateID != preRead.CandidateID || preRead.Candidate.TargetID != input.Request.TargetID || preRead.Candidate.PlanID != input.Request.PlanID.String() || preRead.Candidate.ArtifactDigest != preRead.Plan.ArtifactDigest || (preRead.Candidate.Status != "building" && preRead.Candidate.Status != "ready") {
		return fmt.Errorf("%w: recovery candidate identity differs", deploymentnative.ErrConflict)
	}
	if preRead.Artifact.AttemptID != "" {
		if preRead.Artifact.AttemptID != preRead.AttemptID || preRead.Artifact.ServingStateID != preRead.GenerationID || preRead.Artifact.ServingArtifactDigest != preRead.Candidate.ArtifactDigest || preRead.Artifact.ServingArtifactID == "" {
			return fmt.Errorf("%w: recovery artifact or generation identity differs", deploymentnative.ErrConflict)
		}
	}
	if preRead.Lease.LeaseID != preRead.LeaseID || preRead.Lease.TargetID != input.Request.TargetID || preRead.Lease.OwnerID != input.Request.PrincipalID || preRead.Lease.FencingEpoch != attempt.FencingEpoch || preRead.Lease.ExpiresAt.IsZero() || !preRead.Lease.ExpiresAt.Equal(attempt.LeaseExpiresAt) || !preRead.Lease.ExpiresAt.Equal(input.Operation.LeaseExpiresAt) || (preRead.Lease.State != "active" && preRead.Lease.State != "released") {
		return fmt.Errorf("%w: recovery target lease identity differs", deploymentnative.ErrConflict)
	}
	return nil
}

func prepareNativeBuildRecoveryTx(
	ctx context.Context,
	repository *deploymentnative.Repository,
	tx deploymentnative.Tx,
	operations deploymentmodule.NativeBuildOperationAuthority,
	attemptTermination AttemptTermination,
	input NativeBuildRecoveryPreparationInput,
	preRead nativeBuildRecoveryPreRead,
) (NativeBuildRecoveryPreparationResult, error) {
	lockedOperation, err := lockNativeBuildOperationTx(ctx, tx, operations, input.Operation, deploymentmodule.NativeOperationStateIndeterminate)
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	if !sameTerminationEvidence(lockedOperation.AttemptEvidence, preRead.Evidence) {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: locked recovery operation evidence differs", deploymentnative.ErrConflict)
	}
	lockedLease, err := lockNativeBuildLeaseTx(ctx, tx, repository, preRead.Lease, "active", "released")
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	if !lockedLease.ExpiresAt.Equal(lockedOperation.LeaseExpiresAt) {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: recovery operation and target lease deadlines differ", deploymentnative.ErrConflict)
	}
	preRead.Lease = lockedLease
	if err := validateNativeBuildRecoveryPreRead(input, preRead); err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	termination, err := attemptTermination.MarkAttemptIndeterminateTx(ctx, tx, AttemptTerminationInput{
		AttemptID: preRead.AttemptID, OwnerID: preRead.Attempt.OwnerID, FencingEpoch: preRead.Attempt.FencingEpoch, Evidence: preRead.Evidence,
	})
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	if termination.DeliveryAttempt.State != deploymentnative.AttemptIndeterminate {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: recovery delivery attempt was not normalized to indeterminate", deploymentnative.ErrConflict)
	}
	if termination.DeliveryAttempt.AttemptID != preRead.AttemptID || termination.DeliveryAttempt.OwnerID != preRead.Attempt.OwnerID || termination.DeliveryAttempt.FencingEpoch != preRead.Attempt.FencingEpoch || termination.DeliveryAttempt.PlanID != preRead.Attempt.PlanID || termination.DeliveryAttempt.CandidateID != preRead.Attempt.CandidateID || termination.DeliveryAttempt.PhysicalPoolID != input.PhysicalPoolID || termination.DeliveryAttempt.Namespace != preRead.Attempt.Namespace || termination.DeliveryAttempt.SessionIdentity != preRead.Attempt.SessionIdentity || !sameTerminationEvidence(termination.DeliveryAttempt.TerminationEvidence, preRead.Evidence) {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: normalized delivery attempt identity differs", deploymentnative.ErrConflict)
	}
	// Both direct MarkIndeterminateTx and expiry fencing advance the operation
	// generation exactly once. ConfirmExpiredAttemptTx therefore locks and
	// projects either kind of indeterminate row using the common predecessor
	// lease predicate; the target-lease state above distinguishes direct
	// replay (released) from first expiry recovery (active).
	predecessor := deploymentmodule.NativeOperationLease{
		Scope: input.Operation.Scope, IdempotencyKey: input.Operation.IdempotencyKey,
		OperationID: input.Operation.OperationID, OwnerID: input.Operation.OwnerID,
		FencingGeneration: input.Operation.FencingGeneration - 1,
		LeaseExpiresAt:    input.Operation.LeaseExpiresAt,
		AttemptID:         input.Operation.AttemptID, AttemptIdentity: input.Operation.AttemptIdentity,
	}
	confirmed, err := operations.ConfirmExpiredAttemptTx(ctx, tx, predecessor, input.Operation.FencingGeneration)
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	if confirmed.State != deploymentmodule.NativeOperationStateIndeterminate || confirmed.Scope != input.Request.TargetID || confirmed.OperationType != nativeBuildOperationType || confirmed.IdempotencyKey != input.Request.IdempotencyKey || confirmed.RequestDigest != input.RequestDigest || confirmed.OperationID != input.Operation.OperationID || confirmed.OwnerID != input.Operation.OwnerID || confirmed.FencingGeneration != input.Operation.FencingGeneration || !confirmed.LeaseExpiresAt.Equal(input.Operation.LeaseExpiresAt) || confirmed.AttemptID != preRead.AttemptID || confirmed.AttemptIdentity != input.Operation.AttemptIdentity || !bytes.Equal(confirmed.AttemptEvidence, preRead.Evidence) {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: confirmed recovery operation identity differs", deploymentnative.ErrConflict)
	}

	if err := repository.ReleaseLeaseAfterAttemptTerminationTx(ctx, tx, deploymentnative.LeaseFence{LeaseID: preRead.Lease.LeaseID, TargetID: preRead.Lease.TargetID, OwnerID: preRead.Lease.OwnerID, FencingEpoch: preRead.Lease.FencingEpoch}); err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	lease, err := repository.LeaseTx(ctx, tx, preRead.LeaseID)
	if err != nil {
		return NativeBuildRecoveryPreparationResult{}, err
	}
	if lease.State != "released" {
		return NativeBuildRecoveryPreparationResult{}, fmt.Errorf("%w: recovery target lease was not released", deploymentnative.ErrConflict)
	}
	return NativeBuildRecoveryPreparationResult{
		Operation: confirmed, Plan: preRead.Plan, Candidate: preRead.Candidate, Artifact: preRead.Artifact,
		DeliveryAttempt: termination.DeliveryAttempt, Lease: lease,
		CandidateID: preRead.CandidateID, GenerationID: preRead.GenerationID, AttemptID: preRead.AttemptID, LeaseID: preRead.LeaseID,
	}, nil
}
