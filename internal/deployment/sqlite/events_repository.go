package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
)

// AppendDeliveryEvent records one immutable lifecycle observation. A replay
// with the same target/request/object identity returns the original row and
// never updates it, which is the crash/restart contract for callers.
func (r *Repository) AppendDeliveryEvent(ctx context.Context, event deployment.DeliveryEvent) (deployment.DeliveryEvent, error) {
	if err := event.Validate(); err != nil {
		return deployment.DeliveryEvent{}, err
	}
	if r == nil || r.db == nil {
		return deployment.DeliveryEvent{}, deployment.ErrDeliveryInvalid
	}
	result, err := appendDeliveryEventTx(ctx, r.db, event)
	if err != nil {
		return deployment.DeliveryEvent{}, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 1 {
		return event, nil
	}
	existing, err := r.DeliveryEventByRequest(ctx, event.TargetID, event.RequestDigest, event.EventKind, event.ObjectKind, event.ObjectID)
	if err != nil {
		return deployment.DeliveryEvent{}, err
	}
	if !deployment.DeliveryEventsEqual(event, existing) {
		return deployment.DeliveryEvent{}, fmt.Errorf("%w: delivery event replay identity differs", deployment.ErrDeliveryConflict)
	}
	return existing, nil
}

func appendDeliveryEventTx(ctx context.Context, q deploydb.DBTX, event deployment.DeliveryEvent) (sql.Result, error) {
	// Validate/canonicalize the value that is actually persisted. DeliveryEvent
	// validation intentionally has a value receiver, so a nil Details map would
	// otherwise marshal as JSON null and violate the append-only table's object
	// check despite Validate accepting it as an empty object.
	if event.Details == nil {
		event.Details = map[string]any{}
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}
	details, err := json.Marshal(event.Details)
	if err != nil {
		return nil, err
	}
	result, err := deploydb.New(q).AppendDeliveryEvent(ctx, deploydb.AppendDeliveryEventParams{
		ID: event.ID, TargetID: event.TargetID, ProjectID: event.ProjectID, Environment: event.Environment,
		ActorID: event.ActorID, EventKind: event.EventKind, ObjectKind: event.ObjectKind, ObjectID: event.ObjectID,
		RequestDigest: event.RequestDigest, NULLIF: event.PlanDigest, NULLIF_2: event.ResultDigest,
		Outcome: event.Outcome, DetailsJson: string(details), CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		return nil, err
	}
	if affected, affectedErr := result.RowsAffected(); affectedErr == nil && affected == 0 {
		row, readErr := deploydb.New(q).GetDeliveryEventByRequest(ctx, deploydb.GetDeliveryEventByRequestParams{TargetID: event.TargetID, RequestDigest: event.RequestDigest, EventKind: event.EventKind, ObjectKind: event.ObjectKind, ObjectID: event.ObjectID})
		if readErr != nil {
			return nil, readErr
		}
		existing, mapErr := mapDeliveryEvent(row)
		if mapErr != nil {
			return nil, mapErr
		}
		if !deployment.DeliveryEventsEqual(event, existing) {
			return nil, fmt.Errorf("%w: delivery event replay identity differs", deployment.ErrDeliveryConflict)
		}
	}
	return result, nil
}

func (r *Repository) DeliveryEventByRequest(ctx context.Context, targetID, requestDigest, eventKind, objectKind, objectID string) (deployment.DeliveryEvent, error) {
	if r == nil || r.queries == nil {
		return deployment.DeliveryEvent{}, deployment.ErrDeliveryInvalid
	}
	row, err := r.queries.GetDeliveryEventByRequest(ctx, deploydb.GetDeliveryEventByRequestParams{
		TargetID: targetID, RequestDigest: requestDigest, EventKind: eventKind, ObjectKind: objectKind, ObjectID: objectID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.DeliveryEvent{}, deployment.ErrNotFound
	}
	if err != nil {
		return deployment.DeliveryEvent{}, err
	}
	return mapDeliveryEvent(row)
}

// DeliveryEventsByObject returns the immutable event history for one scoped
// object.  Command transports use this only to verify durable evidence that a
// lifecycle mutation already appended transactionally; it never appends or
// mutates an event.
func (r *Repository) DeliveryEventsByObject(ctx context.Context, targetID, objectKind, objectID string) ([]deployment.DeliveryEvent, error) {
	if r == nil || r.db == nil {
		return nil, deployment.ErrDeliveryInvalid
	}
	rows, err := r.queries.ListDeliveryEventsByObject(ctx, deploydb.ListDeliveryEventsByObjectParams{TargetID: targetID, ObjectKind: objectKind, ObjectID: objectID})
	if err != nil {
		return nil, err
	}
	var events []deployment.DeliveryEvent
	for _, row := range rows {
		event, err := mapDeliveryEvent(row)
		if err != nil {
			return nil, err
		}
		if err := event.Validate(); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	if len(events) == 0 {
		return nil, deployment.ErrNotFound
	}
	return events, nil
}

func mapDeliveryEvent(row deploydb.DeliveryEvent) (deployment.DeliveryEvent, error) {
	var details map[string]any
	if err := json.Unmarshal([]byte(row.DetailsJson), &details); err != nil {
		return deployment.DeliveryEvent{}, err
	}
	createdAt, err := time.Parse(time.RFC3339Nano, row.CreatedAt)
	if err != nil {
		return deployment.DeliveryEvent{}, err
	}
	event := deployment.DeliveryEvent{
		ID: row.ID, TargetID: row.TargetID, ProjectID: row.ProjectID, Environment: row.Environment,
		ActorID: row.ActorID, EventKind: row.EventKind, ObjectKind: row.ObjectKind, ObjectID: row.ObjectID,
		RequestDigest: row.RequestDigest, Outcome: row.Outcome, Details: details, CreatedAt: createdAt,
	}
	if row.PlanDigest.Valid {
		event.PlanDigest = row.PlanDigest.String
	}
	if row.ResultDigest.Valid {
		event.ResultDigest = row.ResultDigest.String
	}
	if err := event.Validate(); err != nil {
		return deployment.DeliveryEvent{}, err
	}
	return event, nil
}

// appendQueryLeaseEventTx records a query-root lifecycle transition while the
// lease projection mutation is still inside the caller's transaction. The
// candidate/generation rows carry the target scope and plan digest; resolving
// them here prevents a caller from supplying an unrelated event scope.
func appendQueryLeaseEventTx(ctx context.Context, q deploydb.DBTX, lease deployment.DeliveryQueryLease, kind, requestDigest, outcome string, at time.Time) error {
	var targetID, projectID, environment, planDigest string
	if lease.CandidateID != "" {
		candidate, err := deliveryCandidateByIDTx(ctx, q, lease.CandidateID)
		if err != nil {
			return err
		}
		targetID, projectID, environment, planDigest = candidate.TargetID, candidate.ProjectID.String(), candidate.Environment, candidate.PlanDigest
	} else {
		generation, err := deliveryGenerationByIDTx(ctx, q, lease.GenerationID)
		if err != nil {
			return err
		}
		targetID, projectID, environment, planDigest = generation.TargetID, generation.ProjectID.String(), generation.Environment, generation.PlanDigest
	}
	_, err := appendDeliveryEventTx(ctx, q, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(targetID, requestDigest, kind, "query_lease", lease.ID), TargetID: targetID,
		ProjectID: projectID, Environment: environment, ActorID: lease.HolderID, EventKind: kind,
		ObjectKind: "query_lease", ObjectID: lease.ID, RequestDigest: requestDigest, PlanDigest: planDigest,
		Outcome: outcome, Details: map[string]any{"status": string(lease.Status)}, CreatedAt: at,
	})
	return err
}

// appendGCCycleEventTx resolves a pool's target scope from its durable roots.
// A fresh pool can legitimately have no delivery target yet; in that legacy
// case there is no valid target_id foreign key and the lifecycle mutation is
// retained without fabricating a cross-scope event.
func appendGCCycleEventTx(ctx context.Context, q deploydb.DBTX, cycle deployment.DeliveryGCCycle, kind, objectID, requestDigest, outcome, actor string, details map[string]any, at time.Time) error {
	var targetID, projectID, environment, planDigest string
	row, err := deploydb.New(q).GetDeliveryTargetScopeByPoolCandidate(ctx, cycle.PhysicalPoolID)
	if err == nil {
		targetID, projectID, environment, planDigest = row.TargetID, row.ProjectID, row.Environment, row.PlanDigest
	}
	if errors.Is(err, sql.ErrNoRows) {
		generation, generationErr := deploydb.New(q).GetDeliveryTargetScopeByPoolGeneration(ctx, cycle.PhysicalPoolID)
		err = generationErr
		if err == nil {
			targetID, projectID, environment, planDigest = generation.TargetID, generation.ProjectID, generation.Environment, generation.PlanDigest
		}
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if actor == "" {
		actor = cycle.ActorID
	}
	if actor == "" {
		actor = "gc"
	}
	if details == nil {
		details = map[string]any{}
	}
	_, err = appendDeliveryEventTx(ctx, q, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(targetID, requestDigest, kind, "gc_cycle", objectID), TargetID: targetID,
		ProjectID: projectID, Environment: environment, ActorID: actor, EventKind: kind,
		ObjectKind: "gc_cycle", ObjectID: objectID, RequestDigest: requestDigest, PlanDigest: planDigest,
		Outcome: outcome, Details: details, CreatedAt: at,
	})
	return err
}

// appendRootQuarantineEventTx records the operator/GC action that moved a
// corrupt root out of the queryable lifecycle. Root rows intentionally do not
// carry a target foreign key, so scope is resolved through their immutable
// candidate or generation binding before appending. A legacy/fresh pool with
// no such binding has no valid audit scope and is left projection-only.
func appendRootQuarantineEventTx(ctx context.Context, q deploydb.DBTX, root deployment.DeliveryRoot, objectID, reason, actor string, at time.Time) error {
	var targetID, projectID, environment, planDigest string
	var err error
	if root.Kind == "build" {
		seal, sealErr := deploydb.New(q).GetDeliveryCatalogSeal(ctx, root.SourceID)
		err = sealErr
		if err == nil {
			plan, planErr := deploydb.New(q).GetDeliveryPlan(ctx, seal.PlanID)
			err = planErr
			if err == nil {
				targetID, projectID, environment, planDigest = plan.TargetID, plan.ProjectID, plan.Environment, plan.PlanDigest
			}
		}
	} else if root.CandidateID != "" {
		candidate, candidateErr := deploydb.New(q).GetDeliveryCandidate(ctx, root.CandidateID)
		err = candidateErr
		if err == nil {
			targetID, projectID, environment, planDigest = candidate.TargetID, candidate.ProjectID, candidate.Environment, candidate.PlanDigest
		}
	} else if root.GenerationID != "" {
		generation, generationErr := deploydb.New(q).GetDeliveryGeneration(ctx, root.GenerationID)
		err = generationErr
		if err == nil {
			targetID, projectID, environment, planDigest = generation.TargetID, generation.ProjectID, generation.Environment, generation.PlanDigest
		}
	} else {
		// A truly unbound legacy/fresh root has no target foreign key and is
		// retained projection-only rather than fabricating an audit scope.
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if actor == "" {
		actor = "gc"
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("delivery-root-quarantine\x00" + root.PhysicalPoolID + "\x00" + root.Kind + "\x00" + root.SourceID + "\x00" + reason))
	_, err = appendDeliveryEventTx(ctx, q, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(targetID, requestDigest, "gc_aborted", "gc_cycle", objectID), TargetID: targetID,
		ProjectID: projectID, Environment: environment, ActorID: actor, EventKind: "gc_aborted", ObjectKind: "gc_cycle", ObjectID: objectID,
		RequestDigest: requestDigest, PlanDigest: planDigest, ResultDigest: root.CatalogDigest, Outcome: "accepted",
		Details: map[string]any{"reason_code": reason, "status": "quarantined"}, CreatedAt: at,
	})
	return err
}
