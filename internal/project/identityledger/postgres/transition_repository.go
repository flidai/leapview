package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// PrepareTransition durably records the immutable parameters and authored
// resource evidence for a transition. Repeating the exact request is a
// no-op, including after the transition has advanced; changing any prepared
// value returns ErrTransitionConflict.
func (r *Repository) PrepareTransition(ctx context.Context, input identityledger.Transition) (identityledger.Transition, error) {
	transition, err := identityledger.NormalizeTransition(input)
	if err != nil {
		return identityledger.Transition{}, err
	}
	if transition.Phase != identityledger.PhasePrepared || transition.Error != "" {
		return identityledger.Transition{}, fmt.Errorf("%w: prepare requires prepared phase", identityledger.ErrInvalidTransition)
	}
	resourcesJSON, err := identityledger.ResourceEvidenceJSON(transition.Resources)
	if err != nil {
		return identityledger.Transition{}, err
	}
	referencesJSON, err := identityledger.ReferenceEvidenceJSON(transition.InstanceID, transition.References)
	if err != nil {
		return identityledger.Transition{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("begin activation transition prepare: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, `
		INSERT INTO project.identity_activation_transition
		(instance_id, transition_id, operation, candidate_id, bundle_id, expected_bundle_id,
		 actor_id, reason, authored_resources_json, durable_references_json, graph_digest)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (instance_id, transition_id) DO NOTHING`,
		transition.InstanceID, transition.TransitionID, string(transition.Operation), transition.CandidateID,
		transition.BundleID, transition.ExpectedBundleID, transition.ActorID,
		transition.Reason, string(resourcesJSON), string(referencesJSON), transition.GraphDigest)
	if err != nil {
		return identityledger.Transition{}, mapTransitionWriteError("prepare activation transition", identityledger.ErrTransitionConflict, err)
	}
	stored, err := readTransition(ctx, tx, transition.InstanceID, transition.TransitionID, true)
	if err != nil {
		return identityledger.Transition{}, err
	}
	if !identityledger.SameTransitionEvidence(stored, transition) {
		return identityledger.Transition{}, fmt.Errorf("%w: transition %q already has different parameters or evidence", identityledger.ErrTransitionConflict, transition.TransitionID)
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Transition{}, mapCommitError("activation transition prepare", err)
	}
	return stored, nil
}

// LoadTransition loads one transition from the PostgreSQL authority.
func (r *Repository) LoadTransition(ctx context.Context, instanceID, transitionID string) (identityledger.Transition, error) {
	if err := validateInstanceID(instanceID); err != nil {
		return identityledger.Transition{}, err
	}
	if err := validateTransitionID(transitionID); err != nil {
		return identityledger.Transition{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("begin activation transition load: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	transition, err := readTransition(ctx, tx, instanceID, transitionID, false)
	if err != nil {
		return identityledger.Transition{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Transition{}, fmt.Errorf("commit activation transition load: %w", err)
	}
	return transition, nil
}

// BundlePublishTransition returns the immutable publish-transition evidence
// prepared for a bundle. The publication path uses it to start or resume that
// exact transition.
func (r *Repository) BundlePublishTransition(ctx context.Context, instanceID, bundleID string) (identityledger.Transition, error) {
	return r.bundlePublishTransition(ctx, instanceID, bundleID, false)
}

// PublishedBundleTransition returns only completed publish evidence. Rollback
// reuses this evidence instead of reconstructing a graph digest or accepting a
// candidate that never completed target publication.
func (r *Repository) PublishedBundleTransition(ctx context.Context, instanceID, bundleID string) (identityledger.Transition, error) {
	return r.bundlePublishTransition(ctx, instanceID, bundleID, true)
}

func (r *Repository) bundlePublishTransition(ctx context.Context, instanceID, bundleID string, completed bool) (identityledger.Transition, error) {
	if err := validateInstanceID(instanceID); err != nil {
		return identityledger.Transition{}, err
	}
	if err := validateTransitionID(bundleID); err != nil {
		return identityledger.Transition{}, fmt.Errorf("bundle id: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("begin published bundle transition load: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	query := `
		SELECT transition_id, operation, instance_id, candidate_id, bundle_id, expected_bundle_id,
		       actor_id, reason, authored_resources_json, durable_references_json, graph_digest, phase,
		       phase_error, prepared_at, phase_at, updated_at, completed_at
		FROM project.identity_activation_transition
		WHERE instance_id=$1 AND bundle_id=$2 AND operation='publish'`
	if completed {
		query += " AND phase='completed'"
	}
	transition, err := scanTransition(tx.QueryRow(ctx, query, instanceID, bundleID))
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.Transition{}, fmt.Errorf("%w: published bundle %q", identityledger.ErrTransitionNotFound, bundleID)
	}
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("read published bundle transition: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Transition{}, fmt.Errorf("commit published bundle transition load: %w", err)
	}
	return transition, nil
}

// ListNonTerminalTransitions returns prepared or in-flight transitions for an
// instance. Terminal rows remain available through LoadTransition for audit.
func (r *Repository) ListNonTerminalTransitions(ctx context.Context, instanceID string) ([]identityledger.Transition, error) {
	return r.listTransitions(ctx, instanceID, "phase <> 'completed'", "nonterminal")
}

// ListInFlightTransitions returns only transitions that have claimed the
// instance cutover fence. Prepared candidate evidence does not make a target
// unready until publication or rollback actually begins.
func (r *Repository) ListInFlightTransitions(ctx context.Context, instanceID string) ([]identityledger.Transition, error) {
	return r.listTransitions(ctx, instanceID, "phase IN ('identity_pending','identity_active','delivery_active')", "in-flight")
}

func (r *Repository) listTransitions(ctx context.Context, instanceID, predicate, label string) ([]identityledger.Transition, error) {
	if strings.TrimSpace(instanceID) != instanceID || instanceID == "" || len(instanceID) > 255 {
		return nil, fmt.Errorf("%w: instance id", identityledger.ErrInvalidInput)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin activation transition list: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	query := `
		SELECT transition_id, operation, instance_id, candidate_id, bundle_id, expected_bundle_id,
		       actor_id, reason, authored_resources_json, durable_references_json, graph_digest, phase,
		       phase_error, prepared_at, phase_at, updated_at, completed_at
		FROM project.identity_activation_transition
		WHERE instance_id=$1 AND ` + predicate + `
		ORDER BY updated_at, transition_id`
	rows, err := tx.Query(ctx, query, instanceID)
	if err != nil {
		return nil, fmt.Errorf("list %s activation transitions: %w", label, err)
	}
	defer rows.Close()
	result := make([]identityledger.Transition, 0)
	for rows.Next() {
		transition, err := scanTransition(rows)
		if err != nil {
			return nil, fmt.Errorf("scan activation transition: %w", err)
		}
		result = append(result, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activation transitions: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit activation transition list: %w", err)
	}
	return result, nil
}

// AdvanceTransitionPhase performs a compare-and-set of one forward phase.
// expectedPhase is the fencing token: a stale worker cannot overwrite a
// newer worker's progress. Repeating an already-applied phase/error is a
// no-op and returns the stored row.
func (r *Repository) AdvanceTransitionPhase(ctx context.Context, instanceID, transitionID string, expectedPhase, nextPhase identityledger.TransitionPhase, phaseError string) (identityledger.Transition, error) {
	if err := validateInstanceID(instanceID); err != nil {
		return identityledger.Transition{}, err
	}
	if err := validateTransitionID(transitionID); err != nil {
		return identityledger.Transition{}, err
	}
	if !validTransitionPhase(expectedPhase) || !validTransitionPhase(nextPhase) {
		return identityledger.Transition{}, fmt.Errorf("%w: unknown phase", identityledger.ErrInvalidTransition)
	}
	if len(phaseError) > 4096 || strings.TrimSpace(phaseError) != phaseError {
		return identityledger.Transition{}, fmt.Errorf("%w: phase error", identityledger.ErrInvalidInput)
	}
	if expectedPhase != nextPhase && !identityledger.CanAdvance(expectedPhase, nextPhase) {
		return identityledger.Transition{}, fmt.Errorf("%w: %s to %s", identityledger.ErrInvalidTransition, expectedPhase, nextPhase)
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("begin activation transition advance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	completedAtExpr := "completed_at=completed_at"
	if nextPhase == identityledger.PhaseCompleted {
		completedAtExpr = "completed_at=COALESCE(completed_at,clock_timestamp())"
	}
	query := fmt.Sprintf(`
		UPDATE project.identity_activation_transition
		SET phase=$3, phase_error=$4, phase_at=clock_timestamp(), updated_at=clock_timestamp(), %s
		WHERE instance_id=$1 AND transition_id=$2 AND phase=$5
		RETURNING transition_id, operation, instance_id, candidate_id, bundle_id, expected_bundle_id,
		          actor_id, reason, authored_resources_json, durable_references_json, graph_digest, phase,
		          phase_error, prepared_at, phase_at, updated_at, completed_at`, completedAtExpr)
	row := tx.QueryRow(ctx, query, instanceID, transitionID, string(nextPhase), phaseError, string(expectedPhase))
	transition, err := scanTransition(row)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return identityledger.Transition{}, mapTransitionWriteError("advance activation transition", identityledger.ErrPhaseConflict, err)
		}
		current, loadErr := readTransition(ctx, tx, instanceID, transitionID, true)
		if loadErr != nil {
			return identityledger.Transition{}, loadErr
		}
		if current.Phase == nextPhase && current.Error == phaseError {
			if commitErr := tx.Commit(ctx); commitErr != nil {
				return identityledger.Transition{}, mapCommitError("activation transition replay", commitErr)
			}
			return current, nil
		}
		return identityledger.Transition{}, fmt.Errorf("%w: transition %q is %s, expected %s", identityledger.ErrPhaseConflict, transitionID, current.Phase, expectedPhase)
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Transition{}, mapCommitError("activation transition advance", err)
	}
	return transition, nil
}

func mapTransitionWriteError(operation string, conflict error, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && (pgErr.Code == "23505" || pgErr.Code == "40001") {
		return fmt.Errorf("%w: %s: %v", conflict, operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

// AdvanceTransition is the concise alias for AdvanceTransitionPhase.
func (r *Repository) AdvanceTransition(ctx context.Context, instanceID, transitionID string, expectedPhase, nextPhase identityledger.TransitionPhase, phaseError string) (identityledger.Transition, error) {
	return r.AdvanceTransitionPhase(ctx, instanceID, transitionID, expectedPhase, nextPhase, phaseError)
}

func readTransition(ctx context.Context, tx pgx.Tx, instanceID, transitionID string, forUpdate bool) (identityledger.Transition, error) {
	query := `
		SELECT transition_id, operation, instance_id, candidate_id, bundle_id, expected_bundle_id,
		       actor_id, reason, authored_resources_json, durable_references_json, graph_digest, phase,
		       phase_error, prepared_at, phase_at, updated_at, completed_at
		FROM project.identity_activation_transition WHERE instance_id=$1 AND transition_id=$2`
	if forUpdate {
		query += " FOR UPDATE"
	}
	transition, err := scanTransition(tx.QueryRow(ctx, query, instanceID, transitionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return identityledger.Transition{}, fmt.Errorf("%w: %q", identityledger.ErrTransitionNotFound, transitionID)
	}
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("read activation transition: %w", err)
	}
	return transition, nil
}

type transitionScanner interface{ Scan(...any) error }

func scanTransition(row transitionScanner) (identityledger.Transition, error) {
	var transition identityledger.Transition
	var operation, resourcesJSON, referencesJSON, phase string
	if err := row.Scan(
		&transition.TransitionID, &operation, &transition.InstanceID, &transition.CandidateID, &transition.BundleID,
		&transition.ExpectedBundleID, &transition.ActorID, &transition.Reason, &resourcesJSON, &referencesJSON,
		&transition.GraphDigest, &phase, &transition.Error, &transition.PreparedAt,
		&transition.PhaseAt, &transition.UpdatedAt, &transition.CompletedAt,
	); err != nil {
		return identityledger.Transition{}, err
	}
	var evidence []struct {
		AuthoredID string `json:"authored_id"`
		Kind       string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(resourcesJSON), &evidence); err != nil {
		return identityledger.Transition{}, fmt.Errorf("decode transition evidence: %w", err)
	}
	transition.Resources = make([]identityledger.Resource, len(evidence))
	for i, item := range evidence {
		transition.Resources[i] = identityledger.Resource{AuthoredID: projectgraph.ResourceID(item.AuthoredID), Kind: projectgraph.Kind(item.Kind)}
	}
	var referenceEvidence []struct {
		ReferenceID      string `json:"reference_id"`
		OwnerAuthoredID  string `json:"owner_authored_id"`
		OwnerKind        string `json:"owner_kind"`
		TargetAuthoredID string `json:"target_authored_id"`
		ExpectedKind     string `json:"expected_kind"`
	}
	if err := json.Unmarshal([]byte(referencesJSON), &referenceEvidence); err != nil {
		return identityledger.Transition{}, fmt.Errorf("decode transition reference evidence: %w", err)
	}
	transition.References = make([]identityledger.DurableReference, len(referenceEvidence))
	for i, item := range referenceEvidence {
		transition.References[i] = identityledger.DurableReference{
			InstanceID: transition.InstanceID, ReferenceID: item.ReferenceID,
			OwnerAuthoredID: item.OwnerAuthoredID, OwnerKind: item.OwnerKind,
			TargetAuthoredID: projectgraph.ResourceID(item.TargetAuthoredID), ExpectedKind: projectgraph.Kind(item.ExpectedKind),
		}
	}
	transition.Operation = identityledger.TransitionOperation(operation)
	transition.Phase = identityledger.TransitionPhase(phase)
	normalized, err := identityledger.NormalizeTransition(transition)
	if err != nil {
		return identityledger.Transition{}, fmt.Errorf("validate activation transition evidence: %w", err)
	}
	return normalized, nil
}

func validateTransitionID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 255 {
		return fmt.Errorf("%w: transition id", identityledger.ErrInvalidInput)
	}
	return nil
}

func validateInstanceID(value string) error {
	if value == "" || strings.TrimSpace(value) != value || len(value) > 255 {
		return fmt.Errorf("%w: instance id", identityledger.ErrInvalidInput)
	}
	return nil
}

func validTransitionPhase(phase identityledger.TransitionPhase) bool {
	switch phase {
	case identityledger.PhasePrepared, identityledger.PhaseIdentityPending, identityledger.PhaseIdentityActive,
		identityledger.PhaseDeliveryActive, identityledger.PhaseCompleted:
		return true
	default:
		return false
	}
}
