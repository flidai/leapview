package deploymentpostgres

// This file owns the narrow control-plane admission boundary used when a
// native physical marker lookup completed successfully but found no marker.
// Both operation and delivery successors are appended in one transaction;
// no successor is admitted for an unresolved resolver/error outcome.

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"time"

	deploymentdomain "github.com/flidai/leapview/internal/deployment"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/google/uuid"
)

// NativeBuildSuccessorAdmissionInput contains the exact predecessor ledgers
// and marker-resolution proof needed to append a successor.  The operation
// and delivery repositories re-check every field while holding their row
// locks; these values are only a value-only hand-off between recovery phases.
type NativeBuildSuccessorAdmissionInput struct {
	Operation       deploymentmodule.NativeOperationRecord
	DeliveryAttempt deploymentnative.DeliveryBuildAttempt
	DeliveryLease   deploymentnative.DeliveryLease
	// Artifact is rebound to the successor attempt in the same transaction,
	// before any external physical work starts.
	Artifact   CandidateBuildArtifactInput
	CatalogID  string
	Physical   CandidateBuildAttemptPhysicalAdmission
	Resolution []byte

	// LeaseExpiresAt and SessionIdentity are optional convenience overrides for
	// callers/tests.  Production callers leave them empty so the helper derives
	// deterministic, fresh identities and a bounded expiry.  On replay an
	// existing operation successor's expiry is reused exactly.
	LeaseExpiresAt  time.Time
	SessionIdentity string
}

// NativeBuildSuccessorAdmissionResult contains the immutable operation leaf
// and delivery predecessor->successor edge returned after the transaction
// commits.  The predecessor/public operation identity is never rewritten.
type NativeBuildSuccessorAdmissionResult struct {
	Operation deploymentmodule.NativeOperationSuccessor
	Delivery  deploymentnative.BuildAttemptSuccessorResult
	Artifact  deploymentnative.BuildArtifactBinding
}

// AdmitNativeBuildSuccessor atomically appends one executable operation leaf
// and one delivery build-attempt successor.  IDs are deterministic UUIDv7
// values derived from the predecessor attempt so an exact retry can replay
// the same successor after a crash.  A transaction is committed only after
// both authorities return matching successor identities.
func AdmitNativeBuildSuccessor(
	ctx context.Context,
	repository *deploymentnative.Repository,
	operations deploymentmodule.NativeBuildOperationSuccessorAuthority,
	input NativeBuildSuccessorAdmissionInput,
) (NativeBuildSuccessorAdmissionResult, error) {
	if repository == nil || !repository.Configured() || !repository.TransactionCapable() {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor admission requires a configured transaction-capable delivery authority", deploymentnative.ErrInvalid)
	}
	if nativeBuildAuthorityNil(operations) {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor operation authority is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	if input.Physical == nil || !input.Physical.Configured() {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor physical admission guard is unavailable", deploymentmodule.ErrDeliveryInputUnavailable)
	}
	if err := validateNativeBuildSuccessorPredecessor(input); err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}

	// Derive a fresh UUIDv7 attempt and lease from the predecessor identity.
	// Keeping the derivation deterministic is what makes a post-crash retry an
	// exact replay rather than a second child of the same predecessor.
	successorAttemptID, err := nativeBuildSuccessorID(input.DeliveryAttempt.AttemptID, "attempt")
	if err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}
	successorLeaseID, err := nativeBuildSuccessorID(input.DeliveryAttempt.AttemptID, "lease")
	if err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}
	successorIdentity := "native-build-successor/" + successorAttemptID
	if len(successorIdentity) > 512 {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor attempt identity is oversized", deploymentdomain.ErrDeliveryInvalid)
	}
	sessionIdentity := strings.TrimSpace(input.SessionIdentity)
	if sessionIdentity == "" {
		sessionIdentity = successorIdentity
	}
	if sessionIdentity == input.DeliveryAttempt.SessionIdentity {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor must use a fresh session identity", deploymentmodule.ErrNativeOperationConflict)
	}
	if input.SessionIdentity != "" && sessionIdentity != input.SessionIdentity {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor session identity is not canonical", deploymentmodule.ErrNativeOperationConflict)
	}
	if sessionIdentity == "" || sessionIdentity != strings.TrimSpace(sessionIdentity) || len(sessionIdentity) > 512 {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor session identity is invalid", deploymentdomain.ErrDeliveryInvalid)
	}

	// If an operation successor already exists, reuse its authoritative expiry
	// and identities before entering the transaction.  This is essential for
	// exact replay because both lease authorities require the same deadline.
	leaseExpiresAt := input.LeaseExpiresAt.UTC().Truncate(time.Microsecond)
	if current, found, readErr := operations.CurrentSuccessorAttempt(ctx, input.Operation.OperationID); readErr != nil {
		return NativeBuildSuccessorAdmissionResult{}, readErr
	} else if found {
		// The current leaf can be either the predecessor we are appending from
		// (the normal second-and-later generation path) or the deterministically
		// derived child (an exact replay after admission committed).  Reject any
		// unrelated active leaf so a retry cannot fork the append-only chain.
		switch {
		case current.AttemptID == input.Operation.AttemptID && current.AttemptIdentity == input.Operation.AttemptIdentity:
			if current.State != deploymentmodule.NativeOperationStateIndeterminate || current.Lease.OwnerID != input.Operation.OwnerID || current.Lease.FencingGeneration != input.Operation.FencingGeneration || !current.Lease.LeaseExpiresAt.Equal(input.Operation.LeaseExpiresAt) {
				return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: current successor predecessor differs", deploymentmodule.ErrNativeOperationConflict)
			}
		case current.AttemptID == successorAttemptID && current.AttemptIdentity == successorIdentity:
			if current.PredecessorID != input.Operation.AttemptID || current.PredecessorIdentity != input.Operation.AttemptIdentity || current.State != deploymentmodule.NativeOperationStatePending && current.State != deploymentmodule.NativeOperationStateIndeterminate || current.Lease.OwnerID != input.Operation.OwnerID || current.Lease.FencingGeneration != input.Operation.FencingGeneration+1 {
				return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: existing successor identity differs", deploymentmodule.ErrNativeOperationConflict)
			}
		default:
			return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: existing successor identity differs", deploymentmodule.ErrNativeOperationConflict)
		}
		// On replay preserve the exact lease deadline chosen by the first
		// transaction.  For a fresh append from the current predecessor, the
		// caller's bounded deadline remains authoritative.
		if current.AttemptID == successorAttemptID {
			leaseExpiresAt = current.Lease.LeaseExpiresAt.UTC().Truncate(time.Microsecond)
		}
	}
	if leaseExpiresAt.IsZero() {
		leaseExpiresAt = time.Now().UTC().Add(30 * time.Minute).Truncate(time.Microsecond)
	}

	tx, err := repository.Begin(ctx)
	if err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(context.Background())
		}
	}()
	// Preserve quarantine-before-attempt ordering: marker quarantine holds the
	// pool scope while its canonical-attempt FK is checked, so acquire the
	// physical fence before locking operation/delivery predecessor rows.
	if err := input.Physical.ValidateBuildAdmissionTx(ctx, tx, input.DeliveryAttempt.PhysicalPoolID, input.CatalogID); err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}

	operationPredecessor := deploymentmodule.NativeOperationLease{
		Scope: input.Operation.Scope, IdempotencyKey: input.Operation.IdempotencyKey,
		OperationID: input.Operation.OperationID, OwnerID: input.Operation.OwnerID,
		FencingGeneration: input.Operation.FencingGeneration, LeaseExpiresAt: input.Operation.LeaseExpiresAt,
		AttemptID: input.Operation.AttemptID, AttemptIdentity: input.Operation.AttemptIdentity,
	}
	operationSuccessor, err := operations.AdmitSuccessorAttemptTx(ctx, tx, deploymentmodule.NativeOperationSuccessorInput{
		Predecessor: operationPredecessor, PredecessorID: input.Operation.AttemptID,
		PredecessorIdentity: input.Operation.AttemptIdentity, AttemptID: successorAttemptID,
		AttemptIdentity: successorIdentity, OwnerID: input.Operation.OwnerID, LeaseExpiresAt: leaseExpiresAt,
	})
	if err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}
	if operationSuccessor.AttemptID != successorAttemptID || operationSuccessor.AttemptIdentity != successorIdentity || operationSuccessor.Lease.FencingGeneration != input.Operation.FencingGeneration+1 || !operationSuccessor.Lease.LeaseExpiresAt.Equal(leaseExpiresAt) {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: operation successor identity or fence differs", deploymentmodule.ErrNativeOperationConflict)
	}

	deliverySuccessor, err := repository.AdmitSuccessorBuildAttemptTx(ctx, tx, deploymentnative.BuildAttemptSuccessorInput{
		Predecessor:          deploymentnative.LeaseFence{LeaseID: input.DeliveryLease.LeaseID, TargetID: input.DeliveryLease.TargetID, OwnerID: input.DeliveryLease.OwnerID, FencingEpoch: input.DeliveryLease.FencingEpoch},
		PredecessorAttemptID: input.DeliveryAttempt.AttemptID, CatalogID: input.CatalogID, ResolutionEvidence: input.Resolution,
		SuccessorLease:   deploymentnative.LeaseInput{LeaseID: successorLeaseID, TargetID: input.DeliveryLease.TargetID, OwnerID: input.DeliveryAttempt.OwnerID, ExpiresAt: leaseExpiresAt},
		SuccessorAttempt: deploymentnative.BuildAttemptInput{AttemptID: successorAttemptID, PlanID: input.DeliveryAttempt.PlanID, CandidateID: input.DeliveryAttempt.CandidateID, OwnerID: input.DeliveryAttempt.OwnerID, PhysicalPoolID: input.DeliveryAttempt.PhysicalPoolID, CatalogID: input.CatalogID, RequestDigest: input.DeliveryAttempt.RequestDigest, PlanDigest: input.DeliveryAttempt.PlanDigest, SessionIdentity: sessionIdentity},
	})
	if err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}
	if deliverySuccessor.Successor.AttemptID != successorAttemptID || deliverySuccessor.Successor.SessionIdentity != sessionIdentity || deliverySuccessor.Successor.FencingEpoch != input.DeliveryAttempt.FencingEpoch+1 || deliverySuccessor.Successor.Namespace == input.DeliveryAttempt.Namespace || deliverySuccessor.SuccessorLease.FencingEpoch != deliverySuccessor.Successor.FencingEpoch || !deliverySuccessor.SuccessorLease.ExpiresAt.Equal(leaseExpiresAt) {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: delivery successor identity or fence differs", deploymentmodule.ErrNativeOperationConflict)
	}
	artifact, artifactErr := repository.BindBuildArtifactTx(ctx, tx, deploymentnative.BuildArtifactBindingInput{
		AttemptID: successorAttemptID, ServingArtifactID: input.Artifact.ServingArtifactID,
		ServingArtifactDigest: input.Artifact.ServingArtifactDigest, ServingStateID: input.Artifact.ServingStateID,
		OwnerID: deliverySuccessor.Successor.OwnerID, FencingEpoch: deliverySuccessor.Successor.FencingEpoch,
	})
	if artifactErr != nil {
		return NativeBuildSuccessorAdmissionResult{}, artifactErr
	}
	if artifact.AttemptID != successorAttemptID || artifact.ServingArtifactID != input.Artifact.ServingArtifactID || artifact.ServingArtifactDigest != input.Artifact.ServingArtifactDigest || artifact.ServingStateID != input.Artifact.ServingStateID {
		return NativeBuildSuccessorAdmissionResult{}, fmt.Errorf("%w: successor artifact binding identity differs", deploymentmodule.ErrNativeOperationConflict)
	}
	if err := tx.Commit(ctx); err != nil {
		return NativeBuildSuccessorAdmissionResult{}, err
	}
	committed = true
	return NativeBuildSuccessorAdmissionResult{Operation: operationSuccessor, Delivery: deliverySuccessor, Artifact: artifact}, nil
}

func validateNativeBuildSuccessorPredecessor(input NativeBuildSuccessorAdmissionInput) error {
	op := input.Operation
	attempt := input.DeliveryAttempt
	lease := input.DeliveryLease
	if op.State != deploymentmodule.NativeOperationStateIndeterminate || op.OperationID == "" || op.AttemptID == "" || op.AttemptIdentity == "" || op.FencingGeneration <= 0 || op.LeaseExpiresAt.IsZero() {
		return fmt.Errorf("%w: successor requires an indeterminate operation predecessor", deploymentmodule.ErrNativeOperationConflict)
	}
	if attempt.State != deploymentnative.AttemptIndeterminate || attempt.AttemptID != op.AttemptID || attempt.RequestDigest != op.RequestDigest || attempt.PlanDigest == "" || attempt.PhysicalPoolID == "" || attempt.CatalogID != input.CatalogID || attempt.SessionIdentity == "" || attempt.FencingEpoch <= 0 || len(attempt.TerminationEvidence) == 0 {
		return fmt.Errorf("%w: successor delivery predecessor is not indeterminate", deploymentnative.ErrConflict)
	}
	if lease.LeaseID == "" || lease.TargetID == "" || lease.OwnerID != attempt.OwnerID || lease.FencingEpoch != attempt.FencingEpoch || lease.State != "released" || !lease.ExpiresAt.Equal(op.LeaseExpiresAt) {
		return fmt.Errorf("%w: successor delivery predecessor lease differs", deploymentnative.ErrConflict)
	}
	if input.CatalogID == "" || input.CatalogID != strings.TrimSpace(input.CatalogID) {
		return fmt.Errorf("%w: successor catalog identity is invalid", deploymentnative.ErrInvalid)
	}
	if input.Artifact.ServingArtifactID == "" || input.Artifact.ServingArtifactDigest == "" || input.Artifact.ServingStateID == "" {
		return fmt.Errorf("%w: successor artifact identity is required", deploymentnative.ErrInvalid)
	}
	if len(input.Resolution) == 0 {
		return fmt.Errorf("%w: successor marker resolution evidence is required", deploymentnative.ErrInvalid)
	}
	return nil
}

func nativeBuildSuccessorID(seed, role string) (string, error) {
	parsed, err := canonicalUUIDv7(seed)
	if err != nil {
		return "", err
	}
	if role != "attempt" && role != "lease" {
		return "", errors.New("native build successor role is invalid")
	}
	id, err := uuid.Parse(parsed)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256([]byte("leapview/native-build-successor/" + parsed + "/" + role))
	copy(id[6:], digest[:10])
	id[6] = id[6]&0x0f | 0x70
	id[8] = id[8]&0x3f | 0x80
	return id.String(), nil
}
