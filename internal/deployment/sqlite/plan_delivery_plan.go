package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// CreatePlan persists one canonical target-owned plan. A retry with the same
// canonical digest returns the original row; an identity or digest collision
// with different canonical inputs is a conflict.
func (r *Repository) CreatePlan(ctx context.Context, input deployment.DeliveryPlan) (deployment.DeliveryPlan, error) {
	if r == nil || r.db == nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("delivery repository is not open")
	}
	plan, err := deployment.NewDeliveryPlan(input)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	executionJSON, err := json.Marshal(plan.Execution)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	provenanceJSON, err := json.Marshal(plan.Provenance)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	governanceJSON, err := json.Marshal(plan.Governance)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	evidenceJSON, err := json.Marshal(plan.Evidence)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	defer tx.Rollback()
	now := deliveryTime(plan.CreatedAt)
	err = deploydb.New(tx).EnsureDeliveryTargetRevision(ctx, deploydb.EnsureDeliveryTargetRevisionParams{TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment, CreatedAt: now, UpdatedAt: now})
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	target, err := deploydb.New(tx).GetDeliveryTargetRevision(ctx, plan.TargetID)
	var targetProject, targetEnvironment, active string
	var revision int64
	if err == nil {
		targetProject, targetEnvironment, revision, active = target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	}
	if errors.Is(err, sql.ErrNoRows) {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if targetProject != plan.ProjectID.String() || targetEnvironment != plan.Environment {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: target scope differs", deployment.ErrDeliveryConflict)
	}
	if revision != plan.BaseTargetRevision || active != plan.BaseGenerationID {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: target base changed", deployment.ErrDeliveryStale)
	}
	actor := plan.ActorID
	if actor == "" {
		actor = plan.Provenance.Builder
	}
	if actor == "" {
		actor = "delivery"
	}
	plan.ActorID = actor
	sourceOwnerID := plan.SourceOwnerID
	if sourceOwnerID == "" {
		sourceOwnerID = actor
	}
	plan.SourceOwnerID = sourceOwnerID
	err = deploydb.New(tx).CreateDeliveryPlan(ctx, deploydb.CreateDeliveryPlanParams{
		ID: plan.ID, TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment, ActorID: actor, SourceOwnerID: sourceOwnerID,
		OperationKind: string(plan.Operation), SourceDigest: plan.SourceDigest, NULLIF: plan.BaseGenerationID,
		BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest, ExecutionInputsJson: string(executionJSON),
		ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, ProvenanceJson: string(provenanceJSON),
		GovernanceJson: string(governanceJSON), EvidenceJson: string(evidenceJSON), EvidenceDigest: plan.EvidenceDigest,
		PlanDigest: plan.Digest, Status: string(plan.Status), ExpiresAt: deliveryTime(plan.Governance.ExpiresAt), CreatedAt: now,
	})
	if err != nil {
		// Resolve both idempotency dimensions while still holding the write lock.
		var existing deployment.DeliveryPlan
		if same, readErr := deliveryPlanByTargetDigestTx(ctx, tx, plan.TargetID, plan.Digest); readErr == nil {
			if same.Digest == plan.Digest {
				if commitErr := tx.Commit(); commitErr != nil {
					return deployment.DeliveryPlan{}, commitErr
				}
				return same, nil
			}
			existing = same
		}
		if byID, readErr := deliveryPlanByIDTx(ctx, tx, plan.ID); readErr == nil {
			if byID.SameCanonicalIntent(plan) {
				if commitErr := tx.Commit(); commitErr != nil {
					return deployment.DeliveryPlan{}, commitErr
				}
				return byID, nil
			}
			existing = byID
		}
		if existing.ID != "" {
			return deployment.DeliveryPlan{}, fmt.Errorf("%w: plan id or idempotency digest is already bound", deployment.ErrDeliveryConflict)
		}
		return deployment.DeliveryPlan{}, err
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, plan.Digest, "plan_created", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: actor, EventKind: "plan_created", ObjectKind: "plan", ObjectID: plan.ID,
		RequestDigest: plan.Digest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"base_revision": plan.BaseTargetRevision}, CreatedAt: plan.CreatedAt,
	}); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if plan.Operation == deployment.DeliveryOperationRestatement {
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(plan.TargetID, plan.Digest, "restatement_requested", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
			ActorID: actor, EventKind: "restatement_requested", ObjectKind: "plan", ObjectID: plan.ID,
			RequestDigest: plan.Digest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"status": string(plan.Status)}, CreatedAt: plan.CreatedAt,
		}); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	return plan, nil
}

func (r *Repository) CreateDeliveryPlan(ctx context.Context, input deployment.DeliveryPlan) (deployment.DeliveryPlan, error) {
	return r.CreatePlan(ctx, input)
}

func (r *Repository) PlanByID(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
	return deliveryPlanByIDTx(ctx, r.db, id)
}

func (r *Repository) GetPlan(ctx context.Context, id string) (deployment.DeliveryPlan, error) {
	return r.PlanByID(ctx, id)
}

func deliveryPlanByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryPlan, error) {
	if strings.TrimSpace(id) == "" {
		return deployment.DeliveryPlan{}, sql.ErrNoRows
	}
	row, err := deploydb.New(q).GetDeliveryPlan(ctx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	projectID, err := projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	var plan deployment.DeliveryPlan
	if err := json.Unmarshal([]byte(row.ExecutionInputsJson), &plan.Execution); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if strings.TrimSpace(row.ProvenanceJson) != "" {
		if err := json.Unmarshal([]byte(row.ProvenanceJson), &plan.Provenance); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	if strings.TrimSpace(row.GovernanceJson) != "" {
		if err := json.Unmarshal([]byte(row.GovernanceJson), &plan.Governance); err != nil {
			return deployment.DeliveryPlan{}, err
		}
	}
	if strings.TrimSpace(row.EvidenceJson) != "" {
		if err := json.Unmarshal([]byte(row.EvidenceJson), &plan.Evidence); err != nil {
			return deployment.DeliveryPlan{}, err
		}
		if plan.Evidence.PipelinePlan != nil {
			pipelinePlan := plan.Evidence.PipelinePlan.Canonical()
			plan.PipelinePlan = &pipelinePlan
		}
	}
	createdAt, err := parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	expiresAt, err := parseDeliveryTime(row.ExpiresAt)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	plan.ID, plan.TargetID, plan.ProjectID, plan.Environment, plan.ActorID, plan.SourceOwnerID = row.ID, row.TargetID, projectID, row.Environment, row.ActorID, row.SourceOwnerID
	plan.Operation, plan.SourceDigest, plan.BaseTargetRevision = deployment.DeliveryOperationKind(row.OperationKind), row.SourceDigest, row.BaseTargetRevision
	if row.BaseGenerationID.Valid {
		plan.BaseGenerationID = row.BaseGenerationID.String
	}
	plan.ExecutionDigest, plan.ProvenanceDigest, plan.GovernanceDigest, plan.Digest = row.ExecutionDigest, row.ProvenanceDigest, row.GovernanceDigest, row.PlanDigest
	plan.EvidenceDigest = row.EvidenceDigest
	plan.Status, plan.CreatedAt = deployment.DeliveryPlanStatus(row.Status), createdAt
	plan.Governance.ExpiresAt = expiresAt
	return plan, plan.Validate()
}

func deliveryPlanByTargetDigestTx(ctx context.Context, q deploydb.DBTX, targetID, digest string) (deployment.DeliveryPlan, error) {
	id, err := deploydb.New(q).GetDeliveryPlanIDByTargetDigest(ctx, deploydb.GetDeliveryPlanIDByTargetDigestParams{TargetID: targetID, PlanDigest: digest})
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	return deliveryPlanByIDTx(ctx, q, id)
}

func (r *Repository) PlanByTargetDigest(ctx context.Context, targetID, digest string) (deployment.DeliveryPlan, error) {
	return deliveryPlanByTargetDigestTx(ctx, r.db, targetID, digest)
}

func (r *Repository) ExpirePlan(ctx context.Context, id string, now time.Time) (deployment.DeliveryPlan, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	defer tx.Rollback()
	plan, err := deliveryPlanByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	updated, err := plan.Expire(now)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	res, err := deploydb.New(tx).ExpireDeliveryPlan(ctx, id)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		if plan.Status == deployment.DeliveryPlanExpired {
			if err := tx.Commit(); err != nil {
				return deployment.DeliveryPlan{}, err
			}
			return plan, nil
		}
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: plan expiry CAS failed", deployment.ErrDeliveryConflict)
	}
	requestDigest := deployment.CanonicalDeliveryDigest([]byte("plan-expired:" + plan.ID))
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(plan.TargetID, requestDigest, "plan_expired", "plan", plan.ID), TargetID: plan.TargetID, ProjectID: plan.ProjectID.String(), Environment: plan.Environment,
		ActorID: eventActor(plan.ActorID), EventKind: "plan_expired", ObjectKind: "plan", ObjectID: plan.ID, RequestDigest: requestDigest, PlanDigest: plan.Digest, Outcome: "accepted", Details: map[string]any{"status": string(updated.Status)}, CreatedAt: now,
	}); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPlan{}, err
	}
	return updated, nil
}

// CreateWriterLeaseAndBuildAttempt creates the lease and attempt under one
// transaction.  The composite foreign key makes it impossible to persist an
// attempt under another attempt's lease or physical pool.
