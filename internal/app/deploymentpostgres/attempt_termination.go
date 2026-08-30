package deploymentpostgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ducklakepostgres "github.com/flidai/leapview/internal/analytics/ducklake/postgres"
	deploymentnative "github.com/flidai/leapview/internal/deployment/postgres"
	"github.com/flidai/leapview/pkg/strictjson"
	"github.com/jackc/pgx/v5"
)

// AttemptTerminationInput is the value-only evidence needed to close one
// native build attempt. The application does not accept database handles,
// catalog connections, or a caller-selected database in this input.
//
// Aborted is only valid when Evidence positively establishes that the native
// session did not commit and has terminated. Indeterminate records bounded
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

// AttemptTermination is the application-owned native termination capability.
// Each operation owns one delivery transaction and passes that same native
// pgx transaction to the application-owned DuckLake ledger.
type AttemptTermination interface {
	AbortAttempt(context.Context, AttemptTerminationInput) (AttemptTerminationResult, error)
	MarkAttemptIndeterminate(context.Context, AttemptTerminationInput) (AttemptTerminationResult, error)
}

type AttemptTerminationDuckLakeAuthority interface {
	Configured() bool
	AbortAttemptTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.TerminateAttemptInput) (ducklakepostgres.AttemptEvidence, error)
	MarkAttemptIndeterminateTx(context.Context, ducklakepostgres.Tx, ducklakepostgres.TerminateAttemptInput) (ducklakepostgres.AttemptEvidence, error)
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

type attemptTerminationOutcome string

const (
	attemptTerminationAborted       attemptTerminationOutcome = "aborted"
	attemptTerminationIndeterminate attemptTerminationOutcome = "indeterminate"
)

// terminateAttempt transitions the delivery and DuckLake ledgers in one
// caller-owned control transaction. The external leapview_ducklake catalog is
// never opened or mutated by this operation.
func (a *attemptTerminator) terminateAttempt(ctx context.Context, input AttemptTerminationInput, outcome attemptTerminationOutcome) (AttemptTerminationResult, error) {
	if a == nil || a.delivery == nil || !a.delivery.Configured() || !a.delivery.TransactionCapable() || a.ducklake == nil || !a.ducklake.Configured() {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt termination authorities are not configured", deploymentnative.ErrInvalid)
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
	if _, ok := tx.(pgx.Tx); !ok {
		return AttemptTerminationResult{}, fmt.Errorf("%w: attempt termination requires a native PostgreSQL transaction", deploymentnative.ErrInvalid)
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

	if err := tx.Commit(ctx); err != nil {
		return AttemptTerminationResult{}, err
	}
	committed = true
	return AttemptTerminationResult{DeliveryAttempt: deliveryAttempt, DuckLakeAttempt: duckAttempt}, nil
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
