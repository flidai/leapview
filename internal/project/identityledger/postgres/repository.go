// Package postgres persists the project identity ledger in the PostgreSQL
// control authority. It never falls back to SQLite or an in-memory store.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/project/identityledger"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type beginner interface {
	BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error)
}

// Repository owns transactional identity reconciliation for a PostgreSQL
// control database.
type Repository struct{ db beginner }

func New(db beginner) (*Repository, error) {
	if db == nil {
		return nil, errors.New("identity ledger PostgreSQL database is required")
	}
	return &Repository{db: db}, nil
}

// Plan previews candidate outcomes without mutating ledger state. Activation
// repeats every check under the instance transaction lock.
func (r *Repository) Plan(ctx context.Context, candidate identityledger.Candidate) (identityledger.Plan, error) {
	if err := identityledger.ValidateCandidate(candidate); err != nil {
		return identityledger.Plan{}, err
	}
	resources, _ := identityledger.NormalizeResources(candidate.Resources)
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.Plan{}, fmt.Errorf("begin identity plan: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	observed, err := activeBundle(ctx, tx, candidate.InstanceID, false)
	if err != nil {
		return identityledger.Plan{}, err
	}
	identities, err := loadIdentities(ctx, tx, candidate.InstanceID, false)
	if err != nil {
		return identityledger.Plan{}, err
	}
	outcomes := planOutcomes(resources, identities)
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Plan{}, fmt.Errorf("commit identity plan: %w", err)
	}
	return identityledger.Plan{
		InstanceID: candidate.InstanceID, ObservedBundleID: observed,
		CandidateBundleID: candidate.BundleID, Outcomes: outcomes,
	}, nil
}

// Activate atomically replaces the active source bundle. A tombstoned identity
// cannot be included; callers must use RestoreAndActivate for that operation.
func (r *Repository) Activate(ctx context.Context, candidate identityledger.Candidate) (identityledger.Plan, error) {
	plan, err := r.activate(ctx, candidate, nil, "", false)
	return plan, mapSerializationError("identity activation", err)
}

// RestoreAndActivate is the explicit audited path for including tombstoned
// identities in a candidate. Approved IDs must exist in the candidate and must
// retain their immutable kind.
func (r *Repository) RestoreAndActivate(ctx context.Context, request identityledger.Restore) (identityledger.Plan, error) {
	if request.Reason != strings.TrimSpace(request.Reason) || request.Reason == "" || len(request.Reason) > 2048 {
		return identityledger.Plan{}, fmt.Errorf("%w: restore reason", identityledger.ErrInvalidInput)
	}
	approvedIDs, err := identityledger.NormalizeApprovedAuthoredIDs(request.AuthoredIDs)
	if err != nil {
		return identityledger.Plan{}, err
	}
	if len(approvedIDs) == 0 {
		return identityledger.Plan{}, fmt.Errorf("%w: restore IDs are required", identityledger.ErrInvalidInput)
	}
	approved := make(map[projectgraph.ResourceID]struct{}, len(approvedIDs))
	for _, id := range approvedIDs {
		approved[id] = struct{}{}
	}
	plan, err := r.activate(ctx, request.Candidate, approved, request.Reason, false)
	return plan, mapSerializationError("identity restore", err)
}

// Rollback atomically reactivates the exact resource snapshot recorded for a
// historical bundle. Rollback is an explicit restore authority for identities
// that were tombstoned after that generation.
func (r *Repository) Rollback(ctx context.Context, request identityledger.Rollback) (identityledger.Plan, error) {
	plan, err := r.rollback(ctx, request)
	return plan, mapSerializationError("identity rollback", err)
}

func (r *Repository) rollback(ctx context.Context, request identityledger.Rollback) (identityledger.Plan, error) {
	if err := validateRollback(request); err != nil {
		return identityledger.Plan{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identityledger.Plan{}, fmt.Errorf("begin identity rollback: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockInstance(ctx, tx, request.InstanceID); err != nil {
		return identityledger.Plan{}, err
	}
	observed, err := activeBundle(ctx, tx, request.InstanceID, true)
	if err != nil {
		return identityledger.Plan{}, err
	}
	if observed != request.ExpectedBundleID {
		return identityledger.Plan{}, fmt.Errorf("%w: expected %q, found %q", identityledger.ErrActivationConflict, request.ExpectedBundleID, observed)
	}
	resources, err := bundleResources(ctx, tx, request.InstanceID, request.BundleID)
	if err != nil {
		return identityledger.Plan{}, err
	}
	identities, err := loadIdentities(ctx, tx, request.InstanceID, true)
	if err != nil {
		return identityledger.Plan{}, err
	}
	outcomes := planOutcomes(resources, identities)
	markRestoredOutcomes(outcomes, nil, true)
	if err := reconcile(ctx, tx, request.InstanceID, request.BundleID, request.ActorID, request.Reason, resources, identities, nil, true); err != nil {
		return identityledger.Plan{}, err
	}
	if err := switchActiveBundle(ctx, tx, request.InstanceID, observed, request.BundleID); err != nil {
		return identityledger.Plan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Plan{}, mapCommitError("identity rollback", err)
	}
	return identityledger.Plan{InstanceID: request.InstanceID, ObservedBundleID: observed, CandidateBundleID: request.BundleID, Outcomes: outcomes}, nil
}

func (r *Repository) activate(ctx context.Context, candidate identityledger.Candidate, approved map[projectgraph.ResourceID]struct{}, reason string, rollback bool) (identityledger.Plan, error) {
	if err := identityledger.ValidateCandidate(candidate); err != nil {
		return identityledger.Plan{}, err
	}
	resources, _ := identityledger.NormalizeResources(candidate.Resources)
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identityledger.Plan{}, fmt.Errorf("begin identity activation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockInstance(ctx, tx, candidate.InstanceID); err != nil {
		return identityledger.Plan{}, err
	}
	observed, err := activeBundle(ctx, tx, candidate.InstanceID, true)
	if err != nil {
		return identityledger.Plan{}, err
	}
	if observed != candidate.ExpectedBundleID {
		return identityledger.Plan{}, fmt.Errorf("%w: expected %q, found %q", identityledger.ErrActivationConflict, candidate.ExpectedBundleID, observed)
	}
	var bundleExists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM project.source_bundle WHERE instance_id=$1 AND bundle_id=$2
	)`, candidate.InstanceID, candidate.BundleID).Scan(&bundleExists); err != nil {
		return identityledger.Plan{}, fmt.Errorf("check source bundle: %w", err)
	}
	if bundleExists {
		return identityledger.Plan{}, fmt.Errorf("%w: bundle %q already exists", identityledger.ErrActivationConflict, candidate.BundleID)
	}
	identities, err := loadIdentities(ctx, tx, candidate.InstanceID, true)
	if err != nil {
		return identityledger.Plan{}, err
	}
	outcomes := planOutcomes(resources, identities)
	if err := validateCandidateAgainstLedger(resources, identities, approved); err != nil {
		return identityledger.Plan{}, err
	}
	markRestoredOutcomes(outcomes, approved, false)
	if _, err := tx.Exec(ctx, `
		INSERT INTO project.source_bundle(instance_id,bundle_id,state,activated_by)
		VALUES($1,$2,'superseded',$3)`, candidate.InstanceID, candidate.BundleID, candidate.ActorID); err != nil {
		return identityledger.Plan{}, fmt.Errorf("insert source bundle: %w", err)
	}
	if err := reconcile(ctx, tx, candidate.InstanceID, candidate.BundleID, candidate.ActorID, reason, resources, identities, approved, rollback); err != nil {
		return identityledger.Plan{}, err
	}
	for _, resource := range resources {
		if _, err := tx.Exec(ctx, `
			INSERT INTO project.source_bundle_resource(instance_id,bundle_id,authored_id,resource_kind)
			VALUES($1,$2,$3,$4)`, candidate.InstanceID, candidate.BundleID, resource.AuthoredID.String(), string(resource.Kind)); err != nil {
			return identityledger.Plan{}, fmt.Errorf("record bundle resource %q: %w", resource.AuthoredID, err)
		}
	}
	if err := switchActiveBundle(ctx, tx, candidate.InstanceID, observed, candidate.BundleID); err != nil {
		return identityledger.Plan{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Plan{}, mapCommitError("identity activation", err)
	}
	return identityledger.Plan{InstanceID: candidate.InstanceID, ObservedBundleID: observed, CandidateBundleID: candidate.BundleID, Outcomes: outcomes}, nil
}

func reconcile(ctx context.Context, tx pgx.Tx, instanceID, bundleID, actorID, reason string, resources []identityledger.Resource, identities map[projectgraph.ResourceID]identityledger.Identity, approved map[projectgraph.ResourceID]struct{}, rollback bool) error {
	candidate := make(map[projectgraph.ResourceID]identityledger.Resource, len(resources))
	for _, resource := range resources {
		candidate[resource.AuthoredID] = resource
		identity, exists := identities[resource.AuthoredID]
		action := "activated"
		if !exists {
			action = "created"
			if _, err := tx.Exec(ctx, `
				INSERT INTO project.resource_identity
				(instance_id,authored_id,resource_kind,lifecycle_state,active_bundle_id)
				VALUES($1,$2,$3,'active',$4)`, instanceID, resource.AuthoredID.String(), string(resource.Kind), bundleID); err != nil {
				return fmt.Errorf("create resource identity %q: %w", resource.AuthoredID, err)
			}
		} else if identity.Lifecycle == identityledger.LifecycleTombstoned {
			if rollback {
				action = "rollback_activated"
			} else {
				action = "restored"
			}
			if _, err := tx.Exec(ctx, `
				UPDATE project.resource_identity
				SET lifecycle_state='active',active_bundle_id=$3,tombstone_reason='',
				    tombstoned_at=NULL,restored_at=clock_timestamp(),updated_at=clock_timestamp()
				WHERE instance_id=$1 AND authored_id=$2`, instanceID, resource.AuthoredID.String(), bundleID); err != nil {
				return fmt.Errorf("restore resource identity %q: %w", resource.AuthoredID, err)
			}
		} else {
			if rollback {
				action = "rollback_activated"
			}
			if _, err := tx.Exec(ctx, `
				UPDATE project.resource_identity SET active_bundle_id=$3,updated_at=clock_timestamp()
				WHERE instance_id=$1 AND authored_id=$2`, instanceID, resource.AuthoredID.String(), bundleID); err != nil {
				return fmt.Errorf("activate resource identity %q: %w", resource.AuthoredID, err)
			}
		}
		historyReason := ""
		if _, ok := approved[resource.AuthoredID]; ok || rollback {
			historyReason = reason
		}
		if err := appendHistory(ctx, tx, instanceID, resource, action, bundleID, actorID, historyReason); err != nil {
			return err
		}
	}
	for id, identity := range identities {
		if identity.Lifecycle != identityledger.LifecycleActive {
			continue
		}
		if _, remains := candidate[id]; remains {
			continue
		}
		tombstoneReason := reason
		if tombstoneReason == "" {
			tombstoneReason = "removed from active source bundle"
		}
		if _, err := tx.Exec(ctx, `
			UPDATE project.resource_identity
			SET lifecycle_state='tombstoned',active_bundle_id=NULL,tombstone_reason=$3,
			    tombstoned_at=clock_timestamp(),updated_at=clock_timestamp()
			WHERE instance_id=$1 AND authored_id=$2`, instanceID, id.String(), tombstoneReason); err != nil {
			return fmt.Errorf("tombstone resource identity %q: %w", id, err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE project.durable_resource_reference
			SET lifecycle_state='suspended',suspended_at=clock_timestamp(),reactivated_at=NULL,updated_at=clock_timestamp()
			WHERE instance_id=$1 AND target_authored_id=$2 AND lifecycle_state='active'`, instanceID, id.String()); err != nil {
			return fmt.Errorf("suspend references for %q: %w", id, err)
		}
		action := "tombstoned"
		if rollback {
			action = "rollback_tombstoned"
		}
		if err := appendHistory(ctx, tx, instanceID, identityledger.Resource{AuthoredID: id, Kind: identity.Kind}, action, bundleID, actorID, tombstoneReason); err != nil {
			return err
		}
	}
	return nil
}

func appendHistory(ctx context.Context, tx pgx.Tx, instanceID string, resource identityledger.Resource, action, bundleID, actorID, reason string) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO project.resource_identity_history
		(instance_id,authored_id,sequence,resource_kind,action,bundle_id,actor_id,reason)
		SELECT $1,$2,COALESCE(max(sequence),0)+1,$3,$4,$5,$6,$7
		FROM project.resource_identity_history WHERE instance_id=$1 AND authored_id=$2`,
		instanceID, resource.AuthoredID.String(), string(resource.Kind), action, bundleID, actorID, reason); err != nil {
		return fmt.Errorf("append resource identity history %q: %w", resource.AuthoredID, err)
	}
	return nil
}

func validateCandidateAgainstLedger(resources []identityledger.Resource, identities map[projectgraph.ResourceID]identityledger.Identity, approved map[projectgraph.ResourceID]struct{}) error {
	candidate := make(map[projectgraph.ResourceID]identityledger.Resource, len(resources))
	for _, resource := range resources {
		candidate[resource.AuthoredID] = resource
		identity, exists := identities[resource.AuthoredID]
		if !exists {
			continue
		}
		if identity.Kind != resource.Kind {
			return fmt.Errorf("%w for %q: ledger=%s candidate=%s", identityledger.ErrKindConflict, resource.AuthoredID, identity.Kind, resource.Kind)
		}
		if identity.Lifecycle == identityledger.LifecycleTombstoned {
			if _, ok := approved[resource.AuthoredID]; !ok {
				return fmt.Errorf("%w for %q", identityledger.ErrRestoreRequired, resource.AuthoredID)
			}
		}
	}
	for id := range approved {
		resource, present := candidate[id]
		identity, exists := identities[id]
		if !present || !exists || identity.Lifecycle != identityledger.LifecycleTombstoned || identity.Kind != resource.Kind {
			return fmt.Errorf("%w: %q is not a matching tombstoned candidate identity", identityledger.ErrRestoreRequired, id)
		}
	}
	return nil
}

func planOutcomes(resources []identityledger.Resource, identities map[projectgraph.ResourceID]identityledger.Identity) []identityledger.Outcome {
	present := make(map[projectgraph.ResourceID]struct{}, len(resources))
	outcomes := make([]identityledger.Outcome, 0, len(resources)+len(identities))
	for _, resource := range resources {
		present[resource.AuthoredID] = struct{}{}
		identity, exists := identities[resource.AuthoredID]
		outcome := identityledger.OutcomeCreated
		detail := "new instance-qualified authored identity"
		if exists {
			switch {
			case identity.Kind != resource.Kind:
				outcome, detail = identityledger.OutcomeCollision, fmt.Sprintf("immutable kind is %s", identity.Kind)
			case identity.Lifecycle == identityledger.LifecycleTombstoned:
				outcome, detail = identityledger.OutcomeRestoreRequired, "identity is tombstoned"
			default:
				outcome, detail = identityledger.OutcomeUpdated, "same authored identity and kind"
			}
		}
		outcomes = append(outcomes, identityledger.Outcome{AuthoredID: resource.AuthoredID, Kind: resource.Kind, Outcome: outcome, Detail: detail})
	}
	for id, identity := range identities {
		if identity.Lifecycle != identityledger.LifecycleActive {
			continue
		}
		if _, ok := present[id]; !ok {
			outcomes = append(outcomes, identityledger.Outcome{AuthoredID: id, Kind: identity.Kind, Outcome: identityledger.OutcomeTombstoned, Detail: "removed from candidate bundle"})
		}
	}
	sort.Slice(outcomes, func(i, j int) bool { return outcomes[i].AuthoredID < outcomes[j].AuthoredID })
	return outcomes
}

func markRestoredOutcomes(outcomes []identityledger.Outcome, approved map[projectgraph.ResourceID]struct{}, rollback bool) {
	for i := range outcomes {
		if outcomes[i].Outcome != identityledger.OutcomeRestoreRequired {
			continue
		}
		if _, ok := approved[outcomes[i].AuthoredID]; rollback || ok {
			outcomes[i].Outcome = identityledger.OutcomeRestored
			outcomes[i].Detail = "explicitly restored with immutable kind"
		}
	}
}

func activeBundle(ctx context.Context, tx pgx.Tx, instanceID string, lock bool) (string, error) {
	query := `SELECT bundle_id FROM project.source_bundle WHERE instance_id=$1 AND state='active'`
	if lock {
		query += ` FOR UPDATE`
	}
	var bundleID string
	err := tx.QueryRow(ctx, query, instanceID).Scan(&bundleID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read active source bundle: %w", err)
	}
	return bundleID, nil
}

// ActiveBundle returns the currently active authored-resource bundle for an
// instance. An empty result means no bundle has been activated yet.
func (r *Repository) ActiveBundle(ctx context.Context, instanceID string) (string, error) {
	if err := validateInstanceToken(instanceID, "instance id"); err != nil {
		return "", err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return "", fmt.Errorf("begin active identity bundle read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	bundleID, err := activeBundle(ctx, tx, instanceID, false)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit active identity bundle read: %w", err)
	}
	return bundleID, nil
}

func loadIdentities(ctx context.Context, tx pgx.Tx, instanceID string, lock bool) (map[projectgraph.ResourceID]identityledger.Identity, error) {
	query := `SELECT authored_id,resource_kind,lifecycle_state,COALESCE(active_bundle_id,''),tombstone_reason,
		created_at,updated_at,tombstoned_at,restored_at
		FROM project.resource_identity WHERE instance_id=$1 ORDER BY authored_id`
	if lock {
		query += ` FOR UPDATE`
	}
	rows, err := tx.Query(ctx, query, instanceID)
	if err != nil {
		return nil, fmt.Errorf("read resource identities: %w", err)
	}
	defer rows.Close()
	result := make(map[projectgraph.ResourceID]identityledger.Identity)
	for rows.Next() {
		var identity identityledger.Identity
		var id, kind, lifecycle string
		if err := rows.Scan(&id, &kind, &lifecycle, &identity.ActiveBundleID, &identity.TombstoneReason,
			&identity.CreatedAt, &identity.UpdatedAt, &identity.TombstonedAt, &identity.RestoredAt); err != nil {
			return nil, fmt.Errorf("scan resource identity: %w", err)
		}
		identity.InstanceID = instanceID
		identity.AuthoredID = projectgraph.ResourceID(id)
		identity.Kind = projectgraph.Kind(kind)
		identity.Lifecycle = identityledger.Lifecycle(lifecycle)
		result[identity.AuthoredID] = identity
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resource identities: %w", err)
	}
	return result, nil
}

func bundleResources(ctx context.Context, tx pgx.Tx, instanceID, bundleID string) ([]identityledger.Resource, error) {
	rows, err := tx.Query(ctx, `
		SELECT authored_id,resource_kind FROM project.source_bundle_resource
		WHERE instance_id=$1 AND bundle_id=$2 ORDER BY authored_id`, instanceID, bundleID)
	if err != nil {
		return nil, fmt.Errorf("read historical source bundle: %w", err)
	}
	defer rows.Close()
	var resources []identityledger.Resource
	for rows.Next() {
		var id, kind string
		if err := rows.Scan(&id, &kind); err != nil {
			return nil, fmt.Errorf("scan historical source bundle: %w", err)
		}
		resources = append(resources, identityledger.Resource{AuthoredID: projectgraph.ResourceID(id), Kind: projectgraph.Kind(kind)})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate historical source bundle: %w", err)
	}
	if len(resources) == 0 {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM project.source_bundle WHERE instance_id=$1 AND bundle_id=$2)`, instanceID, bundleID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check historical source bundle: %w", err)
		}
		if !exists {
			return nil, fmt.Errorf("%w: %q", identityledger.ErrBundleNotFound, bundleID)
		}
	}
	return resources, nil
}

// BundleResources returns the exact immutable authored-resource membership of
// a historical bundle for rollback evidence and durable replay.
func (r *Repository) BundleResources(ctx context.Context, instanceID, bundleID string) ([]identityledger.Resource, error) {
	if err := validateInstanceToken(instanceID, "instance id"); err != nil {
		return nil, err
	}
	if err := validateInstanceToken(bundleID, "bundle id"); err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, fmt.Errorf("begin historical identity bundle read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	resources, err := bundleResources(ctx, tx, instanceID, bundleID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit historical identity bundle read: %w", err)
	}
	return resources, nil
}

func validateInstanceToken(value, label string) error {
	if value == "" || value != strings.TrimSpace(value) || len(value) > 255 {
		return fmt.Errorf("%w: %s", identityledger.ErrInvalidInput, label)
	}
	return nil
}

func switchActiveBundle(ctx context.Context, tx pgx.Tx, instanceID, prior, next string) error {
	if prior != "" && prior != next {
		if _, err := tx.Exec(ctx, `UPDATE project.source_bundle SET state='superseded' WHERE instance_id=$1 AND bundle_id=$2 AND state='active'`, instanceID, prior); err != nil {
			return fmt.Errorf("supersede source bundle: %w", err)
		}
	}
	tag, err := tx.Exec(ctx, `UPDATE project.source_bundle SET state='active',activated_at=clock_timestamp() WHERE instance_id=$1 AND bundle_id=$2`, instanceID, next)
	if err != nil {
		return fmt.Errorf("activate source bundle: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("%w: %q", identityledger.ErrBundleNotFound, next)
	}
	return nil
}

func lockInstance(ctx context.Context, tx pgx.Tx, instanceID string) error {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, instanceID); err != nil {
		return fmt.Errorf("lock identity instance: %w", err)
	}
	return nil
}

func validateRollback(request identityledger.Rollback) error {
	for label, value := range map[string]string{
		"instance id": request.InstanceID, "bundle id": request.BundleID,
		"expected bundle id": request.ExpectedBundleID, "actor id": request.ActorID,
	} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 255 {
			return fmt.Errorf("%w: %s", identityledger.ErrInvalidInput, label)
		}
	}
	if strings.TrimSpace(request.Reason) == "" || len(request.Reason) > 2048 {
		return fmt.Errorf("%w: rollback reason", identityledger.ErrInvalidInput)
	}
	return nil
}

func mapCommitError(operation string, err error) error {
	if mapped := mapSerializationError(operation, err); !errors.Is(mapped, err) {
		return mapped
	}
	return fmt.Errorf("commit %s: %w", operation, err)
}

func mapSerializationError(operation string, err error) error {
	if err == nil {
		return nil
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "40001" {
		return fmt.Errorf("%w: %s serialization failure: %v", identityledger.ErrActivationConflict, operation, err)
	}
	return err
}

// Identity reads one instance-qualified resource identity.
func (r *Repository) Identity(ctx context.Context, instanceID string, authoredID projectgraph.ResourceID) (identityledger.Identity, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.Identity{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	identities, err := loadIdentities(ctx, tx, instanceID, false)
	if err != nil {
		return identityledger.Identity{}, err
	}
	identity, ok := identities[authoredID]
	if !ok {
		return identityledger.Identity{}, pgx.ErrNoRows
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.Identity{}, err
	}
	return identity, nil
}

// ReconcileReferences atomically validates and applies the supplied reviewed
// bindings for one activation. It never treats absence from the supplied list
// as a control-plane deletion: omitted bindings remain durable and retain
// their current lifecycle. Desired bindings are only active when their target
// is an active identity with the expected immutable kind. FAI-616 follow-up
// work must provide a complete reviewed set from live control repositories if
// deployments need to reconcile control-plane removals.
func (r *Repository) ReconcileReferences(ctx context.Context, instanceID string, references []identityledger.DurableReference) ([]identityledger.DurableReference, error) {
	normalized, err := identityledger.NormalizeReferences(instanceID, references)
	if err != nil {
		return nil, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return nil, fmt.Errorf("begin durable reference reconciliation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockInstance(ctx, tx, instanceID); err != nil {
		return nil, err
	}

	result := make([]identityledger.DurableReference, 0, len(normalized))
	for _, reference := range normalized {
		if !identityledger.IsReviewedReferenceOwnerKind(reference.OwnerKind) {
			return nil, fmt.Errorf("%w: unsupported reviewed reference owner kind %q", identityledger.ErrInvalidInput, reference.OwnerKind)
		}
		stored, readErr := readReference(ctx, tx, instanceID, reference.ReferenceID)
		exists := readErr == nil
		if readErr != nil && !errors.Is(readErr, pgx.ErrNoRows) {
			return nil, readErr
		}
		if exists && !sameReferenceBinding(stored, reference) {
			return nil, fmt.Errorf("%w: reference %q is already bound", identityledger.ErrReferenceConflict, reference.ReferenceID)
		}

		var kind, lifecycle string
		if err := tx.QueryRow(ctx, `
			SELECT resource_kind,lifecycle_state
			FROM project.resource_identity
			WHERE instance_id=$1 AND authored_id=$2 FOR UPDATE`, instanceID, reference.TargetAuthoredID.String()).Scan(&kind, &lifecycle); err != nil {
			return nil, fmt.Errorf("resolve durable reference target: %w", err)
		}
		if projectgraph.Kind(kind) != reference.ExpectedKind {
			return nil, fmt.Errorf("%w: target %q is %s, expected %s", identityledger.ErrKindConflict, reference.TargetAuthoredID, kind, reference.ExpectedKind)
		}
		desiredLifecycle := identityledger.ReferenceActive
		if identityledger.Lifecycle(lifecycle) == identityledger.LifecycleTombstoned {
			desiredLifecycle = identityledger.ReferenceSuspended
		}
		if !exists {
			if _, err := tx.Exec(ctx, `
				INSERT INTO project.durable_resource_reference
				(instance_id,reference_id,owner_authored_id,owner_kind,target_authored_id,expected_kind,lifecycle_state,suspended_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,CASE WHEN $7='suspended' THEN clock_timestamp() ELSE NULL END)`,
				instanceID, reference.ReferenceID, reference.OwnerAuthoredID, reference.OwnerKind,
				reference.TargetAuthoredID.String(), string(reference.ExpectedKind), string(desiredLifecycle)); err != nil {
				return nil, fmt.Errorf("insert durable reference: %w", err)
			}
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE project.durable_resource_reference
				SET lifecycle_state=$3,
					suspended_at=CASE WHEN $3='suspended' THEN COALESCE(suspended_at,clock_timestamp()) ELSE NULL END,
					reactivated_at=CASE WHEN $3='suspended' THEN NULL
						WHEN lifecycle_state='suspended' THEN clock_timestamp()
						ELSE reactivated_at END,
					updated_at=clock_timestamp()
				WHERE instance_id=$1 AND reference_id=$2`, instanceID, reference.ReferenceID, string(desiredLifecycle)); err != nil {
				return nil, fmt.Errorf("update durable reference %q: %w", reference.ReferenceID, err)
			}
		}
		stored, err = readReference(ctx, tx, instanceID, reference.ReferenceID)
		if err != nil {
			return nil, err
		}
		if !sameReferenceBinding(stored, reference) {
			return nil, fmt.Errorf("%w: durable reference writer changed binding for %q", identityledger.ErrReferenceConflict, reference.ReferenceID)
		}
		result = append(result, stored)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, mapCommitError("durable reference reconciliation", err)
	}
	return result, nil
}

func sameReferenceBinding(left, right identityledger.DurableReference) bool {
	return left.InstanceID == right.InstanceID && left.ReferenceID == right.ReferenceID &&
		left.OwnerAuthoredID == right.OwnerAuthoredID && left.OwnerKind == right.OwnerKind &&
		left.TargetAuthoredID == right.TargetAuthoredID && left.ExpectedKind == right.ExpectedKind
}

// PutReference creates or verifies one durable control-plane reference. Its
// target kind and authored identity cannot be retargeted on retry. New callers
// should use ReconcileReferences when they have a reviewed batch to validate
// and apply atomically; omission never implies control-plane deletion.
func (r *Repository) PutReference(ctx context.Context, reference identityledger.DurableReference) (identityledger.DurableReference, error) {
	if err := validateReference(reference); err != nil {
		return identityledger.DurableReference{}, err
	}
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return identityledger.DurableReference{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := lockInstance(ctx, tx, reference.InstanceID); err != nil {
		return identityledger.DurableReference{}, err
	}
	var kind, lifecycle string
	if err := tx.QueryRow(ctx, `SELECT resource_kind,lifecycle_state FROM project.resource_identity WHERE instance_id=$1 AND authored_id=$2 FOR UPDATE`, reference.InstanceID, reference.TargetAuthoredID.String()).Scan(&kind, &lifecycle); err != nil {
		return identityledger.DurableReference{}, fmt.Errorf("resolve durable reference target: %w", err)
	}
	if projectgraph.Kind(kind) != reference.ExpectedKind {
		return identityledger.DurableReference{}, fmt.Errorf("%w: target %q is %s, expected %s", identityledger.ErrKindConflict, reference.TargetAuthoredID, kind, reference.ExpectedKind)
	}
	state := identityledger.ReferenceActive
	if identityledger.Lifecycle(lifecycle) == identityledger.LifecycleTombstoned {
		state = identityledger.ReferenceSuspended
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO project.durable_resource_reference
		(instance_id,reference_id,owner_authored_id,owner_kind,target_authored_id,expected_kind,lifecycle_state,suspended_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,CASE WHEN $7='suspended' THEN clock_timestamp() ELSE NULL END)
		ON CONFLICT(instance_id,reference_id) DO NOTHING`, reference.InstanceID, reference.ReferenceID,
		reference.OwnerAuthoredID, reference.OwnerKind, reference.TargetAuthoredID.String(), string(reference.ExpectedKind), string(state))
	if err != nil {
		return identityledger.DurableReference{}, fmt.Errorf("insert durable reference: %w", err)
	}
	stored, err := readReference(ctx, tx, reference.InstanceID, reference.ReferenceID)
	if err != nil {
		return identityledger.DurableReference{}, err
	}
	if !sameReferenceBinding(stored, reference) {
		return identityledger.DurableReference{}, fmt.Errorf("%w: reference %q is already bound", identityledger.ErrReferenceConflict, reference.ReferenceID)
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.DurableReference{}, mapCommitError("durable reference", err)
	}
	return stored, nil
}

// Reference reads one durable reference including suspended/reactivated state.
func (r *Repository) Reference(ctx context.Context, instanceID, referenceID string) (identityledger.DurableReference, error) {
	tx, err := r.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return identityledger.DurableReference{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := readReference(ctx, tx, instanceID, referenceID)
	if err != nil {
		return identityledger.DurableReference{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return identityledger.DurableReference{}, err
	}
	return result, nil
}

func readReference(ctx context.Context, tx pgx.Tx, instanceID, referenceID string) (identityledger.DurableReference, error) {
	var result identityledger.DurableReference
	var target, kind, lifecycle string
	err := tx.QueryRow(ctx, `
		SELECT owner_authored_id,owner_kind,target_authored_id,expected_kind,lifecycle_state,suspended_at,reactivated_at
		FROM project.durable_resource_reference WHERE instance_id=$1 AND reference_id=$2`, instanceID, referenceID).
		Scan(&result.OwnerAuthoredID, &result.OwnerKind, &target, &kind, &lifecycle, &result.SuspendedAt, &result.ReactivatedAt)
	if err != nil {
		return identityledger.DurableReference{}, fmt.Errorf("read durable reference: %w", err)
	}
	result.InstanceID, result.ReferenceID = instanceID, referenceID
	result.TargetAuthoredID, result.ExpectedKind = projectgraph.ResourceID(target), projectgraph.Kind(kind)
	result.Lifecycle = identityledger.ReferenceLifecycle(lifecycle)
	return result, nil
}

func validateReference(reference identityledger.DurableReference) error {
	for label, value := range map[string]string{
		"instance id": reference.InstanceID, "reference id": reference.ReferenceID,
		"owner authored id": reference.OwnerAuthoredID, "owner kind": reference.OwnerKind,
	} {
		if value == "" || value != strings.TrimSpace(value) || len(value) > 255 {
			return fmt.Errorf("%w: %s", identityledger.ErrInvalidInput, label)
		}
	}
	if err := reference.TargetAuthoredID.Validate(); err != nil || !identityledger.IsAuthoredKind(reference.ExpectedKind) {
		return fmt.Errorf("%w: durable reference target", identityledger.ErrInvalidInput)
	}
	return nil
}
