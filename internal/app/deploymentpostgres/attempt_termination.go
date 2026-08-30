package deploymentpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	catalogartifact "github.com/flidai/leapview/internal/analytics/catalogartifact"
	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
)

// AttemptTerminationInput is the value-only evidence needed to close one
// native build attempt. The application does not accept database handles,
// catalog connections, or a caller-selected database in this input.
//
// Aborted is only valid for a deterministic validation failure before the
// native session could commit. A later positive session-termination decision
// uses AttemptReconciliationInput instead. Indeterminate records bounded
// evidence that the external outcome cannot be established. The evidence is
// canonicalized before either control ledger is touched.
type AttemptTerminationInput struct {
	AttemptID    string
	OwnerID      string
	FencingEpoch int64
	Evidence     json.RawMessage
}

// AttemptTerminationResult is the exact evidence returned by both control
// ledgers after one atomic transition in leapview_control.
type AttemptTerminationResult struct {
	DeliveryAttempt deploymentnative.DeliveryBuildAttempt
	DuckLakeAttempt ducklakepostgres.AttemptEvidence
}

// AttemptReconciliationInput carries an already-resolved external outcome
// for one build attempt. A committed decision must include the exact
// canonical DuckLake marker and snapshot identity. An aborted decision must
// include positive session-termination evidence; a lease timeout alone is
// never enough to authorize it.
type AttemptReconciliationInput struct {
	AttemptID           string
	OwnerID             string
	FencingEpoch        int64
	PhysicalPoolID      string
	CatalogID           string
	SnapshotID          int64
	CommitMarker        json.RawMessage
	TerminationEvidence json.RawMessage
	SessionTerminated   bool
	SessionIdentity     string
	State               deploymentnative.BuildAttemptState
}

// AttemptTermination is the application-owned native termination capability.
// The convenience methods own one delivery transaction; the Tx methods
// compose into a caller-owned transaction and pass that same native pgx
// transaction to the application-owned DuckLake ledger.
type AttemptTermination interface {
	AbortAttempt(context.Context, AttemptTerminationInput) (AttemptTerminationResult, error)
	MarkAttemptIndeterminate(context.Context, AttemptTerminationInput) (AttemptTerminationResult, error)
	AbortAttemptTx(context.Context, deploymentnative.Tx, AttemptTerminationInput) (AttemptTerminationResult, error)
	MarkAttemptIndeterminateTx(context.Context, deploymentnative.Tx, AttemptTerminationInput) (AttemptTerminationResult, error)
	ReconcileAttempt(context.Context, AttemptReconciliationInput) (AttemptTerminationResult, error)
	ReconcileAttemptTx(context.Context, deploymentnative.Tx, AttemptReconciliationInput) (AttemptTerminationResult, error)
}

type AttemptTerminationDuckLakeAuthority interface {
	Configured() bool
	AbortAttemptTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.TerminateAttemptInput) (ducklakepostgres.AttemptEvidence, error)
	MarkAttemptIndeterminateTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.TerminateAttemptInput) (ducklakepostgres.AttemptEvidence, error)
	ReconcileAttemptTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.ReconcileAttemptInput) (ducklakepostgres.AttemptEvidence, error)
}

type attemptTerminator struct {
	delivery *deploymentnative.Repository
	ducklake AttemptTerminationDuckLakeAuthority
}

var _ AttemptTermination = (*attemptTerminator)(nil)
var _ AttemptTerminationDuckLakeAuthority = (*ducklakepostgres.Repository)(nil)

// NewAttemptTermination constructs the application-owned native termination
// capability. Both authorities must be configured; no transaction or schema
// work is performed by the constructor.
func NewAttemptTermination(delivery *deploymentnative.Repository, ducklake AttemptTerminationDuckLakeAuthority) (AttemptTermination, error) {
	if delivery == nil || !delivery.Configured() || !delivery.TransactionCapable() {
		return nil, errors.New("attempt termination requires a configured, transaction-capable PostgreSQL delivery authority")
	}
	if ducklake == nil || !ducklake.Configured() {
		return nil, errors.New("attempt termination requires a configured DuckLake authority")
	}
	return &attemptTerminator{delivery: delivery, ducklake: ducklake}, nil
}

func (a *attemptTerminator) AbortAttempt(ctx context.Context, input AttemptTerminationInput) (AttemptTerminationResult, error) {
	return a.terminateAttempt(ctx, input, attemptTerminationAborted)
}

func (a *attemptTerminator) MarkAttemptIndeterminate(ctx context.Context, input AttemptTerminationInput) (AttemptTerminationResult, error) {
	return a.terminateAttempt(ctx, input, attemptTerminationIndeterminate)
}

// AbortAttemptTx transitions both control ledgers to aborted in the
// caller-owned transaction. It never begins, commits, or rolls back tx.
func (a *attemptTerminator) AbortAttemptTx(ctx context.Context, tx deploymentnative.Tx, input AttemptTerminationInput) (AttemptTerminationResult, error) {
	return a.terminateAttemptTx(ctx, tx, input, attemptTerminationAborted)
}

// MarkAttemptIndeterminateTx transitions both control ledgers to indeterminate
// in the caller-owned transaction. It never begins, commits, or rolls back tx.
func (a *attemptTerminator) MarkAttemptIndeterminateTx(ctx context.Context, tx deploymentnative.Tx, input AttemptTerminationInput) (AttemptTerminationResult, error) {
	return a.terminateAttemptTx(ctx, tx, input, attemptTerminationIndeterminate)
}

// ReconcileAttempt applies an already-resolved exact marker or positive
// session-termination outcome to both control ledgers in one transaction.
// Marker resolution itself is deliberately outside this capability.
func (a *attemptTerminator) ReconcileAttempt(ctx context.Context, input AttemptReconciliationInput) (AttemptTerminationResult, error) {
	if a == nil || a.delivery == nil || !a.delivery.Configured() || !a.delivery.TransactionCapable() || a.ducklake == nil || !a.ducklake.Configured() {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt reconciliation authorities are not configured", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeAttemptReconciliationInput(input)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	tx, err := a.delivery.Begin(ctx)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	result, err := a.ReconcileAttemptTx(ctx, tx, normalized)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AttemptTerminationResult{}, err
	}
	committed = true
	return result, nil
}

// ReconcileAttemptTx applies exact recovery evidence to both ledgers through
// the caller-owned native PostgreSQL transaction. It never commits or rolls
// back tx.
func (a *attemptTerminator) ReconcileAttemptTx(ctx context.Context, tx deploymentnative.Tx, input AttemptReconciliationInput) (AttemptTerminationResult, error) {
	if a == nil || a.delivery == nil || !a.delivery.Configured() || !a.delivery.TransactionCapable() || a.ducklake == nil || !a.ducklake.Configured() {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt reconciliation authorities are not configured", deploymentnative.ErrInvalid)
	}
	if tx == nil {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt reconciliation requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, err := normalizeAttemptReconciliationInput(input)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt reconciliation requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	deliveryAttempt, err := a.delivery.ReconcileBuildAttemptTx(ctx, tx, deploymentnative.ReconcileBuildAttemptInput{
		AttemptID: normalized.AttemptID, OwnerID: normalized.OwnerID, FencingEpoch: normalized.FencingEpoch,
		SnapshotID: normalized.SnapshotID, CommitMarker: normalized.CommitMarker, TerminationEvidence: normalized.TerminationEvidence,
		SessionTerminated: normalized.SessionTerminated, SessionIdentity: normalized.SessionIdentity, State: normalized.State,
	})
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := verifyDeliveryReconciliation(deliveryAttempt, normalized); err != nil {
		return AttemptTerminationResult{}, err
	}
	duckAttempt, err := a.ducklake.ReconcileAttemptTx(ctx, tx, ducklakepostgres.ReconcileAttemptInput{
		AttemptID: normalized.AttemptID, OwnerID: normalized.OwnerID, FencingEpoch: normalized.FencingEpoch,
		Snapshot:     ducklakepostgres.SnapshotRef{PhysicalPoolID: normalized.PhysicalPoolID, CatalogID: normalized.CatalogID, SnapshotID: normalized.SnapshotID},
		CommitMarker: string(normalized.CommitMarker), TerminationEvidence: normalized.TerminationEvidence,
		SessionTerminated: normalized.SessionTerminated, SessionIdentity: normalized.SessionIdentity, State: ducklakepostgres.AttemptState(normalized.State),
	})
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := verifyDuckLakeReconciliation(duckAttempt, normalized); err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := verifyReconciliationLedgerAgreement(deliveryAttempt, duckAttempt, normalized); err != nil {
		return AttemptTerminationResult{}, err
	}
	return AttemptTerminationResult{DeliveryAttempt: deliveryAttempt, DuckLakeAttempt: duckAttempt}, nil
}

func normalizeAttemptReconciliationInput(input AttemptReconciliationInput) (AttemptReconciliationInput, error) {
	attemptID, err := canonicalUUID(input.AttemptID, "attempt id")
	if err != nil {
		return AttemptReconciliationInput{}, err
	}
	if err := validateText(input.OwnerID, "owner id", 255); err != nil {
		return AttemptReconciliationInput{}, err
	}
	if input.FencingEpoch <= 0 {
		return AttemptReconciliationInput{}, fmt.Errorf("%w: fencing epoch must be positive", deploymentnative.ErrInvalid)
	}
	state := input.State
	if state != deploymentnative.AttemptCommitted && state != deploymentnative.AttemptAborted {
		return AttemptReconciliationInput{}, fmt.Errorf("%w: reconciliation outcome must be committed or aborted", deploymentnative.ErrInvalid)
	}
	marker := input.CommitMarker
	if state == deploymentnative.AttemptCommitted {
		if err := validateText(input.PhysicalPoolID, "physical pool id", 255); err != nil {
			return AttemptReconciliationInput{}, err
		}
		if err := validateText(input.CatalogID, "catalog id", 255); err != nil {
			return AttemptReconciliationInput{}, err
		}
		if input.SnapshotID <= 0 || len(marker) == 0 || len(input.TerminationEvidence) != 0 || input.SessionTerminated {
			return AttemptReconciliationInput{}, fmt.Errorf("%w: committed recovery requires an exact marker and snapshot", deploymentnative.ErrInvalid)
		}
		parsed, canonical, markerErr := decodeRecoveryMarker(marker)
		if markerErr != nil {
			return AttemptReconciliationInput{}, markerErr
		}
		if parsed.AttemptID != attemptID || parsed.LeaseEpoch != input.FencingEpoch || parsed.PhysicalPoolID != input.PhysicalPoolID {
			return AttemptReconciliationInput{}, fmt.Errorf("%w: recovery commit marker identity mismatch", deploymentnative.ErrConflict)
		}
		marker = canonical
	} else {
		if input.SnapshotID != 0 || input.PhysicalPoolID != "" || input.CatalogID != "" || len(marker) != 0 || !input.SessionTerminated {
			return AttemptReconciliationInput{}, fmt.Errorf("%w: aborted recovery requires positive session-termination evidence", deploymentnative.ErrInvalid)
		}
		if err := validateText(input.SessionIdentity, "session identity", 512); err != nil {
			return AttemptReconciliationInput{}, err
		}
		if err := validateRecoverySessionEvidence(input.TerminationEvidence, attemptID, input.OwnerID, input.FencingEpoch, input.SessionIdentity); err != nil {
			return AttemptReconciliationInput{}, err
		}
		evidence, _ := canonicalTerminationEvidence(input.TerminationEvidence)
		input.TerminationEvidence = evidence
	}
	input.AttemptID, input.State, input.CommitMarker = attemptID, state, marker
	return input, nil
}

func decodeRecoveryMarker(raw json.RawMessage) (catalogartifact.CommitMarker, json.RawMessage, error) {
	marker, err := catalogartifact.DecodeCommitMarker(raw)
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("%w: invalid recovery commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	canonical, err := marker.CanonicalJSON()
	if err != nil {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("%w: invalid recovery commit marker: %v", deploymentnative.ErrInvalid, err)
	}
	if !bytes.Equal(raw, []byte(canonical)) {
		return catalogartifact.CommitMarker{}, nil, fmt.Errorf("%w: recovery commit marker must be canonical JSON", deploymentnative.ErrInvalid)
	}
	return marker, []byte(canonical), nil
}

type recoverySessionTerminationEvidence struct {
	SchemaVersion     int    `json:"schema_version"`
	AttemptID         string `json:"attempt_id"`
	OwnerID           string `json:"owner_id"`
	FencingEpoch      int64  `json:"fencing_epoch"`
	SessionIdentity   string `json:"session_identity"`
	SessionTerminated bool   `json:"session_terminated"`
}

func validateRecoverySessionEvidence(raw json.RawMessage, attemptID, ownerID string, fencingEpoch int64, sessionIdentity string) error {
	evidence, err := canonicalTerminationEvidence(raw)
	if err != nil {
		return fmt.Errorf("%w: positive session-termination evidence is required", deploymentnative.ErrInvalid)
	}
	var document recoverySessionTerminationEvidence
	if err := strictjson.Decode(evidence, &document); err != nil {
		return fmt.Errorf("%w: positive session-termination evidence is invalid", deploymentnative.ErrInvalid)
	}
	if document.SchemaVersion != 1 || document.AttemptID != attemptID || document.OwnerID != ownerID || document.FencingEpoch != fencingEpoch || document.SessionIdentity != sessionIdentity || !document.SessionTerminated {
		return fmt.Errorf("%w: positive session-termination evidence identity differs", deploymentnative.ErrInvalid)
	}
	return nil
}

func verifyDeliveryReconciliation(got deploymentnative.DeliveryBuildAttempt, input AttemptReconciliationInput) error {
	if got.AttemptID != input.AttemptID || got.OwnerID != input.OwnerID || got.FencingEpoch != input.FencingEpoch || got.State != input.State || got.LeaseExpiresAt.IsZero() || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.FinishedAt.IsZero() {
		return fmt.Errorf("%w: delivery reconciliation evidence identity differs", deploymentnative.ErrConflict)
	}
	if input.State == deploymentnative.AttemptCommitted {
		if got.SnapshotID != input.SnapshotID || len(got.TerminationEvidence) != 0 || !sameCommitMarker(got.CommitMarker, input.CommitMarker) {
			return fmt.Errorf("%w: delivery committed reconciliation evidence differs", deploymentnative.ErrConflict)
		}
	} else if got.SnapshotID != 0 || len(got.CommitMarker) != 0 || !sameTerminationEvidence(got.TerminationEvidence, input.TerminationEvidence) {
		return fmt.Errorf("%w: delivery aborted reconciliation evidence differs", deploymentnative.ErrConflict)
	}
	return nil
}

func verifyDuckLakeReconciliation(got ducklakepostgres.AttemptEvidence, input AttemptReconciliationInput) error {
	if got.AttemptID != input.AttemptID || got.OwnerID != input.OwnerID || got.FencingEpoch != input.FencingEpoch || got.State != ducklakepostgres.AttemptState(input.State) || got.LeaseExpiresAt.IsZero() || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.TerminalAt.IsZero() {
		return fmt.Errorf("%w: DuckLake reconciliation evidence identity differs", deploymentnative.ErrConflict)
	}
	if input.State == deploymentnative.AttemptCommitted {
		if got.PhysicalPoolID != input.PhysicalPoolID || got.CatalogID != input.CatalogID || got.SnapshotID != input.SnapshotID || got.TerminationEvidence != nil || !sameCommitMarker([]byte(got.CommitMarker), input.CommitMarker) {
			return fmt.Errorf("%w: DuckLake committed reconciliation evidence differs", deploymentnative.ErrConflict)
		}
	} else if got.SnapshotID != 0 || got.CommitMarker != "" || !sameTerminationEvidence(got.TerminationEvidence, input.TerminationEvidence) {
		return fmt.Errorf("%w: DuckLake aborted reconciliation evidence differs", deploymentnative.ErrConflict)
	}
	return nil
}

func verifyReconciliationLedgerAgreement(delivery deploymentnative.DeliveryBuildAttempt, ducklake ducklakepostgres.AttemptEvidence, input AttemptReconciliationInput) error {
	if delivery.AttemptID != ducklake.AttemptID || delivery.OwnerID != ducklake.OwnerID || delivery.FencingEpoch != ducklake.FencingEpoch || delivery.RequestDigest != ducklake.RequestDigest || delivery.PlanDigest != ducklake.PlanDigest || delivery.PhysicalPoolID != ducklake.PhysicalPoolID || delivery.SessionIdentity != ducklake.SessionIdentity || !delivery.LeaseExpiresAt.Equal(ducklake.LeaseExpiresAt) {
		return fmt.Errorf("%w: delivery and DuckLake reconciliation ledgers disagree", deploymentnative.ErrConflict)
	}
	if input.State == deploymentnative.AttemptCommitted {
		if !sameCommitMarker(delivery.CommitMarker, input.CommitMarker) || !sameCommitMarker([]byte(ducklake.CommitMarker), input.CommitMarker) {
			return fmt.Errorf("%w: committed reconciliation markers disagree", deploymentnative.ErrConflict)
		}
	} else if !sameTerminationEvidence(delivery.TerminationEvidence, input.TerminationEvidence) || !sameTerminationEvidence(ducklake.TerminationEvidence, input.TerminationEvidence) {
		return fmt.Errorf("%w: aborted reconciliation evidence disagrees", deploymentnative.ErrConflict)
	}
	return nil
}

type attemptTerminationOutcome string

const (
	attemptTerminationAborted       attemptTerminationOutcome = "aborted"
	attemptTerminationIndeterminate attemptTerminationOutcome = "indeterminate"
)

// terminateAttempt transitions the delivery and DuckLake ledgers in one
// control transaction owned by the convenience wrapper. The external
// leapview_ducklake catalog is never opened or mutated by this operation.
func (a *attemptTerminator) terminateAttempt(ctx context.Context, input AttemptTerminationInput, outcome attemptTerminationOutcome) (AttemptTerminationResult, error) {
	if a == nil || a.delivery == nil || !a.delivery.Configured() || !a.delivery.TransactionCapable() || a.ducklake == nil || !a.ducklake.Configured() {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt termination authorities are not configured", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, _, err := normalizeAttemptTerminationInput(input)
	if err != nil {
		return AttemptTerminationResult{}, err
	}

	tx, err := a.delivery.Begin(ctx)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(ctx)
		}
	}()
	result, err := a.terminateAttemptTx(ctx, tx, normalized, outcome)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return AttemptTerminationResult{}, err
	}
	committed = true
	return result, nil
}

// terminateAttemptTx applies the delivery and DuckLake transitions to tx.
// It performs all input and native-transaction validation but never invokes
// a transaction lifecycle method, leaving commit or rollback to the caller.
func (a *attemptTerminator) terminateAttemptTx(ctx context.Context, tx deploymentnative.Tx, input AttemptTerminationInput, outcome attemptTerminationOutcome) (AttemptTerminationResult, error) {
	if a == nil || a.delivery == nil || !a.delivery.Configured() || !a.delivery.TransactionCapable() || a.ducklake == nil || !a.ducklake.Configured() {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt termination authorities are not configured", deploymentnative.ErrInvalid)
	}
	if tx == nil {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt termination requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	ctx = contextOrBackground(ctx)
	normalized, canonical, err := normalizeAttemptTerminationInput(input)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	state, err := terminationStates(outcome)
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if _, ok := tx.(pgx.Tx); !ok {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt termination requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
	}
	var deterministicEvidence nativeBuildTerminationEvidence
	if state == deploymentnative.AttemptAborted {
		deterministicEvidence, err = validateDeterministicAbortEvidence(canonical, normalized)
		if err != nil {
			return AttemptTerminationResult{}, err
		}
	}

	var deliveryAttempt deploymentnative.DeliveryBuildAttempt
	if state == deploymentnative.AttemptAborted {
		deliveryAttempt, err = a.delivery.AbortBuildAttemptTx(ctx, tx, deploymentnative.TerminateAttemptInput{AttemptID: normalized.AttemptID, OwnerID: normalized.OwnerID, FencingEpoch: normalized.FencingEpoch, Evidence: canonical})
	} else {
		deliveryAttempt, err = a.delivery.MarkAttemptIndeterminateTx(ctx, tx, deploymentnative.TerminateAttemptInput{AttemptID: normalized.AttemptID, OwnerID: normalized.OwnerID, FencingEpoch: normalized.FencingEpoch, Evidence: canonical})
	}
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := verifyDeliveryTermination(deliveryAttempt, normalized, canonical, state); err != nil {
		return AttemptTerminationResult{}, err
	}
	if state == deploymentnative.AttemptAborted {
		if err := verifyDeterministicAbortEvidence(deterministicEvidence, deliveryAttempt); err != nil {
			return AttemptTerminationResult{}, err
		}
	}

	duckInput := ducklakepostgres.TerminateAttemptInput{AttemptID: normalized.AttemptID, OwnerID: normalized.OwnerID, FencingEpoch: normalized.FencingEpoch, Evidence: canonical}
	var duckAttempt ducklakepostgres.AttemptEvidence
	duckState := ducklakeTerminationState(outcome)
	if duckState == ducklakepostgres.AttemptAborted {
		duckAttempt, err = a.ducklake.AbortAttemptTx(ctx, tx, duckInput)
	} else {
		duckAttempt, err = a.ducklake.MarkAttemptIndeterminateTx(ctx, tx, duckInput)
	}
	if err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := verifyDuckLakeTermination(duckAttempt, normalized, canonical, duckState); err != nil {
		return AttemptTerminationResult{}, err
	}
	if err := verifyTerminationLedgerAgreement(deliveryAttempt, duckAttempt, canonical); err != nil {
		return AttemptTerminationResult{}, err
	}

	return AttemptTerminationResult{DeliveryAttempt: deliveryAttempt, DuckLakeAttempt: duckAttempt}, nil
}

func validateDeterministicAbortEvidence(raw json.RawMessage, input AttemptTerminationInput) (nativeBuildTerminationEvidence, error) {
	var document nativeBuildTerminationEvidence
	if err := strictjson.Decode(raw, &document); err != nil {
		return nativeBuildTerminationEvidence{}, fmt.Errorf("%w: deterministic no-commit evidence is invalid", deploymentnative.ErrInvalid)
	}
	if document.SchemaVersion != 1 || document.AttemptID != input.AttemptID || document.OwnerID != input.OwnerID || document.FencingEpoch != input.FencingEpoch ||
		document.Phase != NativePhysicalBuildPhaseValidation || document.Classification != NativePhysicalFailureDeterministic || document.SnapshotID != 0 || len(document.CommitMarker) != 0 {
		return nativeBuildTerminationEvidence{}, fmt.Errorf("%w: deterministic no-commit evidence identity differs", deploymentnative.ErrInvalid)
	}
	if validateDigest(document.RequestDigest, "request digest") != nil || validateDigest(document.PlanDigest, "plan digest") != nil || validateDigest(document.ErrorDigest, "error digest") != nil ||
		validateText(document.PhysicalPoolID, "physical pool id", 255) != nil || validateText(document.Namespace, "relation namespace", 512) != nil || validateText(document.SessionIdentity, "session identity", 512) != nil {
		return nativeBuildTerminationEvidence{}, fmt.Errorf("%w: deterministic no-commit evidence is incomplete", deploymentnative.ErrInvalid)
	}
	return document, nil
}

func verifyDeterministicAbortEvidence(document nativeBuildTerminationEvidence, attempt deploymentnative.DeliveryBuildAttempt) error {
	if document.RequestDigest != attempt.RequestDigest || document.PlanDigest != attempt.PlanDigest || document.PhysicalPoolID != attempt.PhysicalPoolID ||
		document.Namespace != attempt.Namespace || document.SessionIdentity != attempt.SessionIdentity {
		return fmt.Errorf("%w: deterministic no-commit evidence differs from the build attempt", deploymentnative.ErrConflict)
	}
	return nil
}

func terminationStates(outcome attemptTerminationOutcome) (deploymentnative.BuildAttemptState, error) {
	switch outcome {
	case attemptTerminationAborted:
		return deploymentnative.AttemptAborted, nil
	case attemptTerminationIndeterminate:
		return deploymentnative.AttemptIndeterminate, nil
	default:
		return "", fmt.Errorf("%w: unsupported attempt termination outcome %q", deploymentnative.ErrInvalid, outcome)
	}
}

func ducklakeTerminationState(outcome attemptTerminationOutcome) ducklakepostgres.AttemptState {
	if outcome == attemptTerminationAborted {
		return ducklakepostgres.AttemptAborted
	}
	return ducklakepostgres.AttemptIndeterminate
}

func normalizeAttemptTerminationInput(input AttemptTerminationInput) (AttemptTerminationInput, json.RawMessage, error) {
	attemptID, err := canonicalUUID(input.AttemptID, "attempt id")
	if err != nil {
		return AttemptTerminationInput{}, nil, err
	}
	if err := validateText(input.OwnerID, "owner id", 255); err != nil {
		return AttemptTerminationInput{}, nil, err
	}
	if input.FencingEpoch <= 0 {
		return AttemptTerminationInput{}, nil, fmt.Errorf("%w: fencing epoch must be positive", deploymentnative.ErrInvalid)
	}
	evidence, err := canonicalTerminationEvidence(input.Evidence)
	if err != nil {
		return AttemptTerminationInput{}, nil, fmt.Errorf("%w: termination evidence is invalid", deploymentnative.ErrInvalid)
	}
	return AttemptTerminationInput{AttemptID: attemptID, OwnerID: input.OwnerID, FencingEpoch: input.FencingEpoch, Evidence: evidence}, evidence, nil
}

func canonicalTerminationEvidence(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > 32768 {
		return nil, deploymentnative.ErrInvalid
	}
	var object map[string]any
	var validated map[string]json.RawMessage
	if err := strictjson.Decode(raw, &validated); err != nil || validated == nil || len(validated) == 0 {
		return nil, deploymentnative.ErrInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&object); err != nil || object == nil || len(object) == 0 {
		return nil, deploymentnative.ErrInvalid
	}
	encoded, err := json.Marshal(object)
	if err != nil || len(encoded) > 32768 {
		return nil, deploymentnative.ErrInvalid
	}
	return encoded, nil
}

func verifyDeliveryTermination(got deploymentnative.DeliveryBuildAttempt, input AttemptTerminationInput, evidence json.RawMessage, state deploymentnative.BuildAttemptState) error {
	_, planErr := canonicalUUID(got.PlanID, "plan id")
	_, candidateErr := canonicalUUID(got.CandidateID, "candidate id")
	if got.AttemptID != input.AttemptID || got.OwnerID != input.OwnerID || got.FencingEpoch != input.FencingEpoch || got.State != state || got.SnapshotID != 0 || len(got.CommitMarker) != 0 ||
		planErr != nil || candidateErr != nil || validateText(got.PhysicalPoolID, "physical pool id", 255) != nil || validateDigest(got.RequestDigest, "request digest") != nil || validateDigest(got.PlanDigest, "plan digest") != nil ||
		validateText(got.Namespace, "relation namespace", 512) != nil || validateText(got.SessionIdentity, "session identity", 512) != nil || got.LeaseExpiresAt.IsZero() || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.FinishedAt.IsZero() ||
		!sameTerminationEvidence(got.TerminationEvidence, evidence) {
		return fmt.Errorf("%w: delivery termination evidence identity differs", deploymentnative.ErrConflict)
	}
	return nil
}

func verifyDuckLakeTermination(got ducklakepostgres.AttemptEvidence, input AttemptTerminationInput, evidence json.RawMessage, state ducklakepostgres.AttemptState) error {
	if got.AttemptID != input.AttemptID || got.OwnerID != input.OwnerID || got.FencingEpoch != input.FencingEpoch || got.State != state || got.SnapshotID != 0 || got.CommitMarker != "" ||
		validateDigest(got.RequestDigest, "request digest") != nil || validateDigest(got.PlanDigest, "plan digest") != nil || validateText(got.PhysicalPoolID, "physical pool id", 255) != nil || validateText(got.CatalogID, "catalog id", 255) != nil ||
		validateText(got.SessionIdentity, "session identity", 512) != nil || got.LeaseExpiresAt.IsZero() || got.CreatedAt.IsZero() || got.UpdatedAt.IsZero() || got.TerminalAt.IsZero() ||
		!sameTerminationEvidence(got.TerminationEvidence, evidence) {
		return fmt.Errorf("%w: DuckLake termination evidence identity differs", deploymentnative.ErrConflict)
	}
	return nil
}

func verifyTerminationLedgerAgreement(delivery deploymentnative.DeliveryBuildAttempt, ducklake ducklakepostgres.AttemptEvidence, evidence json.RawMessage) error {
	if delivery.AttemptID != ducklake.AttemptID || delivery.OwnerID != ducklake.OwnerID || delivery.FencingEpoch != ducklake.FencingEpoch ||
		delivery.RequestDigest != ducklake.RequestDigest || delivery.PlanDigest != ducklake.PlanDigest || delivery.PhysicalPoolID != ducklake.PhysicalPoolID ||
		delivery.SessionIdentity != ducklake.SessionIdentity || !delivery.LeaseExpiresAt.Equal(ducklake.LeaseExpiresAt) ||
		!sameTerminationEvidence(delivery.TerminationEvidence, evidence) || !sameTerminationEvidence(ducklake.TerminationEvidence, evidence) {
		return fmt.Errorf("%w: delivery and DuckLake termination ledgers disagree", deploymentnative.ErrConflict)
	}
	return nil
}

func sameTerminationEvidence(left, right json.RawMessage) bool {
	if bytes.Equal(left, right) {
		return true
	}
	leftCanonical, leftErr := canonicalTerminationEvidence(left)
	rightCanonical, rightErr := canonicalTerminationEvidence(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftCanonical, rightCanonical)
}
