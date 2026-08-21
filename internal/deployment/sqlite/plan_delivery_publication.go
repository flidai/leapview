package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	deploydb "github.com/flidai/leapview/internal/deployment/internal/db"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (r *Repository) CreatePublication(ctx context.Context, input deployment.DeliveryPublication, supplied ...deployment.DeliveryGeneration) (deployment.DeliveryPublication, error) {
	publication, err := deployment.NewDeliveryPublication(input)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	plan, err := r.PlanByID(ctx, publication.PlanID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if publication.ActorID == "" {
		publication.ActorID = plan.ActorID
	}
	candidate, err := r.DeliveryCandidateByID(ctx, publication.CandidateID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if candidate.Status != deployment.DeliveryCandidateReady || candidate.PlanID != plan.ID || candidate.PlanDigest != publication.PlanDigest || candidate.TargetID != publication.TargetID || candidate.ProjectID != publication.ProjectID || candidate.Environment != publication.Environment {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication candidate is not the exact ready candidate", deployment.ErrDeliveryConflict)
	}
	var generation deployment.DeliveryGeneration
	if len(supplied) > 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: one generation may be supplied", deployment.ErrDeliveryInvalid)
	}
	if len(supplied) == 1 {
		generation = supplied[0]
	} else {
		rollbackClass, rollbackUntil, rollbackEffects, evidenceErr := rollbackEvidenceForPlan(plan, publication.CreatedAt)
		if evidenceErr != nil {
			return deployment.DeliveryPublication{}, evidenceErr
		}
		generation = deployment.DeliveryGeneration{
			ID: publication.GenerationID, CandidateID: candidate.ID, PlanID: plan.ID, PlanDigest: plan.Digest,
			TargetID: candidate.TargetID, ProjectID: candidate.ProjectID, Environment: candidate.Environment,
			CatalogDigest: candidate.CatalogDigest, CatalogObjectKey: candidate.CatalogObjectKey,
			PhysicalPoolID: candidate.PhysicalPoolID, ServingArtifactID: candidate.ServingArtifactID, ServingArtifactDigest: candidate.ServingArtifactDigest, ServingStateID: candidate.ServingStateID, CompatibilityDigest: candidate.CompatibilityDigest, RollbackClass: rollbackClass, RollbackExternalEffects: rollbackEffects, RollbackUntil: rollbackUntil,
			CreatedAt: publication.CreatedAt,
		}
	}
	generation, err = deployment.NewDeliveryGeneration(generation)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	expectedClass, expectedUntil, expectedEffects, evidenceErr := rollbackEvidenceForPlan(plan, generation.CreatedAt)
	if evidenceErr != nil {
		return deployment.DeliveryPublication{}, evidenceErr
	}
	if generation.RollbackClass != expectedClass || !generation.RollbackUntil.Equal(expectedUntil) || !sameStringSlice(generation.RollbackExternalEffects, expectedEffects) {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation rollback evidence differs from reviewed plan", deployment.ErrDeliveryConflict)
	}
	if generation.ID != publication.GenerationID || generation.CandidateID != candidate.ID || generation.PlanID != plan.ID || generation.PlanDigest != plan.Digest || generation.TargetID != candidate.TargetID || generation.ProjectID != candidate.ProjectID || generation.Environment != candidate.Environment || generation.CatalogDigest != candidate.CatalogDigest || generation.CatalogObjectKey != candidate.CatalogObjectKey || generation.PhysicalPoolID != candidate.PhysicalPoolID || generation.ServingArtifactID != candidate.ServingArtifactID || generation.ServingArtifactDigest != candidate.ServingArtifactDigest || generation.ServingStateID != candidate.ServingStateID || generation.CompatibilityDigest != candidate.CompatibilityDigest {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation does not match candidate", deployment.ErrDeliveryConflict)
	}
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	// Resolve a committed request before evaluating mutable plan expiry. A
	// retry after the governance window has elapsed must return the original
	// durable result, while a new request is checked against authoritative now
	// below. Identity drift never converges silently.
	if existing, readErr := deliveryPublicationByIDTx(ctx, tx, publication.ID); readErr == nil {
		if !samePublicationIdentity(existing, publication) {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication request identity conflict", deployment.ErrDeliveryConflict)
		}
		if existing.Status == deployment.DeliveryPublicationCommitted {
			if err := tx.Commit(); err != nil {
				return deployment.DeliveryPublication{}, err
			}
			return existing, nil
		}
		if existing.Status != deployment.DeliveryPublicationPending && existing.Status != deployment.DeliveryPublicationIndeterminate {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication request is %s", deployment.ErrDeliveryTransition, existing.Status)
		}
	} else if !errors.Is(readErr, sql.ErrNoRows) {
		return deployment.DeliveryPublication{}, readErr
	}
	var active string
	var revision int64
	var targetProject, targetEnvironment string
	target, targetErr := deploydb.New(tx).GetDeliveryTargetRevision(ctx, publication.TargetID)
	err = targetErr
	if err == nil {
		targetProject, targetEnvironment, revision, active = target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	}
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	now := time.Now().UTC()
	if r != nil && r.deliveryNow != nil {
		now = r.deliveryNow().UTC()
	}
	if err := candidate.PublicationEligible(plan, active, revision, now); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if targetProject != publication.ProjectID.String() || targetEnvironment != publication.Environment || active != publication.ExpectedBaseGenerationID || revision != publication.ExpectedTargetRevision {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication target fence changed", deployment.ErrDeliveryStale)
	}
	externalEffectsJSON, marshalEffectsErr := json.Marshal(generation.RollbackExternalEffects)
	if marshalEffectsErr != nil {
		return deployment.DeliveryPublication{}, marshalEffectsErr
	}
	err = deploydb.New(tx).CreateDeliveryGeneration(ctx, deploydb.CreateDeliveryGenerationParams{ID: generation.ID, CandidateID: generation.CandidateID, PlanID: generation.PlanID, PlanDigest: generation.PlanDigest, TargetID: generation.TargetID, ProjectID: generation.ProjectID.String(), Environment: generation.Environment, CatalogDigest: generation.CatalogDigest, CatalogObjectKey: generation.CatalogObjectKey, PhysicalPoolID: generation.PhysicalPoolID, ServingArtifactID: generation.ServingArtifactID, ServingArtifactDigest: generation.ServingArtifactDigest, ServingStateID: generation.ServingStateID, CompatibilityDigest: generation.CompatibilityDigest, RollbackClass: string(generation.RollbackClass), RollbackExternalEffectsJson: string(externalEffectsJSON), CreatedAt: deliveryTime(generation.CreatedAt), ActivatedAt: sql.NullString{}, RollbackUntil: nullableString(generation.RollbackUntil)})
	if err != nil {
		if existing, readErr := deliveryGenerationByIDTx(ctx, tx, generation.ID); readErr == nil && sameGenerationIdentity(existing, generation) {
			// A retry may have created the prepared root before its response was
			// lost. Continue to the publication idempotency lookup.
		} else {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation identity conflict", deployment.ErrDeliveryConflict)
		}
	}
	err = deploydb.New(tx).CreateDeliveryPublication(ctx, deploydb.CreateDeliveryPublicationParams{ID: publication.ID, RequestDigest: publication.RequestDigest, TargetID: publication.TargetID, ProjectID: publication.ProjectID.String(), Environment: publication.Environment, PlanID: publication.PlanID, PlanDigest: publication.PlanDigest, CandidateID: publication.CandidateID, GenerationID: publication.GenerationID, NULLIF: publication.ExpectedBaseGenerationID, ExpectedTargetRevision: publication.ExpectedTargetRevision, CreatedAt: deliveryTime(publication.CreatedAt)})
	if err != nil {
		if existing, readErr := deliveryPublicationByRequestTx(ctx, tx, publication.TargetID, publication.RequestDigest); readErr == nil {
			if samePublicationIdentity(existing, publication) {
				if commitErr := tx.Commit(); commitErr != nil {
					return deployment.DeliveryPublication{}, commitErr
				}
				return existing, nil
			}
		}
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication idempotency conflict", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(publication.TargetID, publication.RequestDigest, "publish_requested", "publication", publication.ID), TargetID: publication.TargetID, ProjectID: publication.ProjectID.String(), Environment: publication.Environment,
		ActorID: eventActor(publication.ActorID), EventKind: "publish_requested", ObjectKind: "publication", ObjectID: publication.ID,
		RequestDigest: publication.RequestDigest, PlanDigest: publication.PlanDigest, Outcome: "accepted",
		Details: map[string]any{"candidate_id": publication.CandidateID, "generation_id": publication.GenerationID}, CreatedAt: publication.CreatedAt,
	}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return publication, nil
}

func rollbackEvidenceForPlan(plan deployment.DeliveryPlan, createdAt time.Time) (deployment.DeliveryRollbackClass, time.Time, []string, error) {
	class := plan.Evidence.Rollback.Class
	if class == "" {
		return "", time.Time{}, nil, fmt.Errorf("%w: delivery plan rollback class is missing", deployment.ErrDeliveryInvalid)
	}
	var until time.Time
	if window := strings.TrimSpace(plan.Evidence.Rollback.RetentionWindow); window != "" {
		duration, err := time.ParseDuration(window)
		if err != nil || duration <= 0 {
			return "", time.Time{}, nil, fmt.Errorf("%w: invalid rollback retention window %q", deployment.ErrDeliveryInvalid, window)
		}
		until = createdAt.Add(duration)
	}
	effects := append([]string(nil), plan.Evidence.Rollback.ExternalEffects...)
	if effects == nil {
		effects = []string{}
	}
	sort.Strings(effects)
	return class, until, effects, nil
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (r *Repository) CreatePublicationIntent(ctx context.Context, input deployment.DeliveryPublication, supplied ...deployment.DeliveryGeneration) (deployment.DeliveryPublication, error) {
	return r.CreatePublication(ctx, input, supplied...)
}

func samePublicationIdentity(a, b deployment.DeliveryPublication) bool {
	return a.ID == b.ID && a.RequestDigest == b.RequestDigest && a.TargetID == b.TargetID && a.ProjectID == b.ProjectID && a.Environment == b.Environment && a.PlanID == b.PlanID && a.PlanDigest == b.PlanDigest && a.CandidateID == b.CandidateID && a.GenerationID == b.GenerationID && a.ExpectedBaseGenerationID == b.ExpectedBaseGenerationID && a.ExpectedTargetRevision == b.ExpectedTargetRevision
}

func (r *Repository) DeliveryPublicationByID(ctx context.Context, id string) (deployment.DeliveryPublication, error) {
	return deliveryPublicationByIDTx(ctx, r.db, id)
}

func (r *Repository) DeliveryPublicationByRequest(ctx context.Context, targetID, requestDigest string) (deployment.DeliveryPublication, error) {
	return deliveryPublicationByRequestTx(ctx, r.db, targetID, requestDigest)
}

func deliveryPublicationByRequestTx(ctx context.Context, q deploydb.DBTX, targetID, requestDigest string) (deployment.DeliveryPublication, error) {
	id, err := deploydb.New(q).GetDeliveryPublicationIDByTargetDigest(ctx, deploydb.GetDeliveryPublicationIDByTargetDigestParams{TargetID: targetID, RequestDigest: requestDigest})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return deliveryPublicationByIDTx(ctx, q, id)
}

func deliveryPublicationByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryPublication, error) {
	row, err := deploydb.New(q).GetDeliveryPublication(ctx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	p := deployment.DeliveryPublication{ID: row.ID, RequestDigest: row.RequestDigest, TargetID: row.TargetID, Environment: row.Environment, PlanID: row.PlanID, PlanDigest: row.PlanDigest, CandidateID: row.CandidateID, GenerationID: row.GenerationID, ExpectedTargetRevision: row.ExpectedTargetRevision, ResultTargetRevision: row.ResultTargetRevision, Status: deployment.DeliveryPublicationStatus(row.Status), Reason: row.Reason}
	p.ProjectID, err = projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if row.ExpectedBaseGenerationID.Valid {
		p.ExpectedBaseGenerationID = row.ExpectedBaseGenerationID.String
	}
	p.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	p.CompletedAt, err = parseNullableDeliveryTime(row.CompletedAt)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return p, p.Validate()
}

func sameGenerationIdentity(a, b deployment.DeliveryGeneration) bool {
	return a.ID == b.ID && a.CandidateID == b.CandidateID && a.PlanID == b.PlanID && a.PlanDigest == b.PlanDigest && a.TargetID == b.TargetID && a.ProjectID == b.ProjectID && a.Environment == b.Environment && a.CatalogDigest == b.CatalogDigest && a.CatalogObjectKey == b.CatalogObjectKey && a.PhysicalPoolID == b.PhysicalPoolID && a.ServingArtifactID == b.ServingArtifactID && a.ServingArtifactDigest == b.ServingArtifactDigest && a.ServingStateID == b.ServingStateID && a.CompatibilityDigest == b.CompatibilityDigest && a.RollbackClass == b.RollbackClass && sameStringSlice(a.RollbackExternalEffects, b.RollbackExternalEffects) && a.RollbackUntil.Equal(b.RollbackUntil)
}

func (r *Repository) DeliveryGenerationByID(ctx context.Context, id string) (deployment.DeliveryGeneration, error) {
	return deliveryGenerationByIDTx(ctx, r.db, id)
}

func deliveryGenerationByIDTx(ctx context.Context, q deploydb.DBTX, id string) (deployment.DeliveryGeneration, error) {
	row, err := deploydb.New(q).GetDeliveryGeneration(ctx, id)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	var rollbackEffects []string
	if strings.TrimSpace(row.RollbackExternalEffectsJson) != "" {
		if err := json.Unmarshal([]byte(row.RollbackExternalEffectsJson), &rollbackEffects); err != nil {
			return deployment.DeliveryGeneration{}, err
		}
	}
	g := deployment.DeliveryGeneration{ID: row.ID, CandidateID: row.CandidateID, PlanID: row.PlanID, PlanDigest: row.PlanDigest, TargetID: row.TargetID, Environment: row.Environment, CatalogDigest: row.CatalogDigest, CatalogObjectKey: row.CatalogObjectKey, PhysicalPoolID: row.PhysicalPoolID, ServingArtifactID: row.ServingArtifactID, ServingArtifactDigest: row.ServingArtifactDigest, ServingStateID: row.ServingStateID, CompatibilityDigest: row.CompatibilityDigest, RollbackClass: deployment.DeliveryRollbackClass(row.RollbackClass), RollbackExternalEffects: rollbackEffects, Status: deployment.DeliveryGenerationStatus(row.Status)}
	g.ProjectID, err = projectgraph.NewResourceID(row.ProjectID)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.CreatedAt, err = parseDeliveryTime(row.CreatedAt)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.ActivatedAt, err = parseNullableDeliveryTime(row.ActivatedAt)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.RetiredAt, err = parseNullableDeliveryTime(row.RetiredAt)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	g.RollbackUntil, err = parseNullableDeliveryTime(row.RollbackUntil)
	if err != nil {
		return deployment.DeliveryGeneration{}, err
	}
	return g, g.Validate()
}

// CommitPublication performs the target CAS, generation activation, previous
// generation retirement, and publication completion as one SQLite commit.
func (r *Repository) CommitPublication(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	p, err := deliveryPublicationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if p.Status == deployment.DeliveryPublicationCommitted {
		return p, nil
	}
	if p.Status != deployment.DeliveryPublicationPending && p.Status != deployment.DeliveryPublicationIndeterminate {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication is %s", deployment.ErrDeliveryTransition, p.Status)
	}
	actor := "delivery"
	if persistedActor, actorErr := deploydb.New(tx).GetDeliveryPublicationRequestActor(ctx, p.ID); actorErr == nil {
		actor = persistedActor
	}
	actor = eventActor(actor)
	var active string
	var revision int64
	var targetProject, targetEnvironment string
	target, targetErr := deploydb.New(tx).GetDeliveryTargetRevision(ctx, p.TargetID)
	err = targetErr
	if err == nil {
		targetProject, targetEnvironment, revision, active = target.ProjectID, target.Environment, target.TargetRevision, target.ActiveGenerationID
	}
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if targetProject != p.ProjectID.String() || targetEnvironment != p.Environment {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication scope changed", deployment.ErrDeliveryConflict)
	}
	if p.Status == deployment.DeliveryPublicationIndeterminate && active == p.GenerationID && revision == p.ExpectedTargetRevision+1 {
		committed, commitErr := p.Commit(p.ExpectedBaseGenerationID, p.ExpectedTargetRevision, now)
		if commitErr != nil {
			return deployment.DeliveryPublication{}, commitErr
		}
		res, execErr := deploydb.New(tx).CommitIndeterminateDeliveryPublication(ctx, deploydb.CommitIndeterminateDeliveryPublicationParams{ResultTargetRevision: committed.ResultTargetRevision, CompletedAt: presentString(deliveryTime(committed.CompletedAt)), ID: id})
		if execErr != nil {
			return deployment.DeliveryPublication{}, execErr
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: indeterminate publication CAS failed", deployment.ErrDeliveryConflict)
		}
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "activation_committed", "generation", p.GenerationID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
			ActorID: actor, EventKind: "activation_committed", ObjectKind: "generation", ObjectID: p.GenerationID,
			RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, Outcome: "accepted",
			Details: map[string]any{"publication_id": p.ID, "target_revision": committed.ResultTargetRevision}, CreatedAt: committed.CompletedAt,
		}); err != nil {
			return deployment.DeliveryPublication{}, err
		}
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_committed", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
			ActorID: actor, EventKind: "publish_committed", ObjectKind: "publication", ObjectID: p.ID,
			RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ResultDigest: deployment.CanonicalDeliveryDigest([]byte(fmt.Sprintf("%d", committed.ResultTargetRevision))), Outcome: "accepted",
			Details: map[string]any{"generation_id": p.GenerationID, "target_revision": committed.ResultTargetRevision}, CreatedAt: committed.CompletedAt,
		}); err != nil {
			return deployment.DeliveryPublication{}, err
		}
		if err := r.commitPublicationTx(ctx, tx); err != nil {
			_ = tx.Rollback()
			return r.reconcilePublicationCommitError(ctx, id, now.UTC(), err)
		}
		return committed, nil
	}
	if active != p.ExpectedBaseGenerationID || revision != p.ExpectedTargetRevision {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication CAS fence changed", deployment.ErrDeliveryStale)
	}
	plan, err := deliveryPlanByIDTx(ctx, tx, p.PlanID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	candidate, err := deliveryCandidateByIDTx(ctx, tx, p.CandidateID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if candidate.PlanID != p.PlanID || candidate.PlanDigest != p.PlanDigest {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: candidate is not bound to publication plan", deployment.ErrDeliveryConflict)
	}
	if err := candidate.PublicationEligible(plan, active, revision, now.UTC()); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	g, err := deliveryGenerationByIDTx(ctx, tx, p.GenerationID)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if g.Status != deployment.DeliveryGenerationPrepared {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation is %s", deployment.ErrDeliveryTransition, g.Status)
	}
	if err := validateDeliveryTimeForCommit(now, p.CreatedAt); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	committed, err := p.Commit(active, revision, now)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	res, err := deploydb.New(tx).ActivateDeliveryGeneration(ctx, deploydb.ActivateDeliveryGenerationParams{ActivatedAt: presentString(deliveryTime(now)), ID: g.ID})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: generation activation CAS failed", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "activation_committed", "generation", p.GenerationID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
		ActorID: actor, EventKind: "activation_committed", ObjectKind: "generation", ObjectID: g.ID,
		RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ResultDigest: g.CatalogDigest, Outcome: "accepted",
		Details: map[string]any{"publication_id": p.ID, "target_revision": p.ExpectedTargetRevision + 1}, CreatedAt: now,
	}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if active != "" {
		res, execErr := deploydb.New(tx).RetireDeliveryGeneration(ctx, deploydb.RetireDeliveryGenerationParams{RetiredAt: presentString(deliveryTime(now)), ID: active})
		if execErr != nil {
			return deployment.DeliveryPublication{}, execErr
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return deployment.DeliveryPublication{}, fmt.Errorf("%w: prior generation retirement CAS failed", deployment.ErrDeliveryConflict)
		}
		if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
			ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "retirement_committed", "generation", active), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
			ActorID: actor, EventKind: "retirement_committed", ObjectKind: "generation", ObjectID: active,
			RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, Outcome: "accepted",
			Details: map[string]any{"replaced_by_generation_id": g.ID, "publication_id": p.ID}, CreatedAt: now,
		}); err != nil {
			return deployment.DeliveryPublication{}, err
		}
	}
	res, err = deploydb.New(tx).AdvanceDeliveryTargetRevision(ctx, deploydb.AdvanceDeliveryTargetRevisionParams{ActiveGenerationID: presentString(p.GenerationID), UpdatedAt: deliveryTime(now), TargetID: p.TargetID, TargetRevision: p.ExpectedTargetRevision, ActiveGenerationID_2: sql.NullString{}, ActiveGenerationID_3: presentString(p.ExpectedBaseGenerationID)})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: target revision CAS failed", deployment.ErrDeliveryStale)
	}
	res, err = deploydb.New(tx).CommitDeliveryPublication(ctx, deploydb.CommitDeliveryPublicationParams{ResultTargetRevision: committed.ResultTargetRevision, CompletedAt: presentString(deliveryTime(committed.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication completion CAS failed", deployment.ErrDeliveryConflict)
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{
		ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_committed", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment,
		ActorID: actor, EventKind: "publish_committed", ObjectKind: "publication", ObjectID: p.ID,
		RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, ResultDigest: deployment.CanonicalDeliveryDigest([]byte(fmt.Sprintf("%d", committed.ResultTargetRevision))), Outcome: "accepted",
		Details: map[string]any{"generation_id": p.GenerationID, "target_revision": committed.ResultTargetRevision}, CreatedAt: committed.CompletedAt,
	}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := r.commitPublicationTx(ctx, tx); err != nil {
		_ = tx.Rollback()
		return r.reconcilePublicationCommitError(ctx, id, now.UTC(), err)
	}
	return committed, nil
}

func (r *Repository) reconcilePublicationCommitError(ctx context.Context, id string, now time.Time, commitErr error) (deployment.DeliveryPublication, error) {
	// A provider/database timeout often cancels the request context. Recovery
	// is deliberately detached from cancellation, but bounded, so the durable
	// indeterminate marker can still be written without allowing an operator
	// request to hang indefinitely.
	reconcileCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	observed, observeErr := r.DeliveryPublicationByID(reconcileCtx, id)
	if observeErr != nil {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: activation commit acknowledgement was lost: %w", deployment.ErrDeliveryOutcomeUnknown, errors.Join(commitErr, observeErr))
	}
	if observed.Status == deployment.DeliveryPublicationCommitted {
		return observed, nil
	}
	if observed.Status == deployment.DeliveryPublicationPending {
		marked, markErr := r.MarkPublicationIndeterminate(reconcileCtx, id, now)
		if markErr != nil {
			return observed, fmt.Errorf("%w: activation commit acknowledgement was lost: %w", deployment.ErrDeliveryOutcomeUnknown, errors.Join(commitErr, markErr))
		}
		observed = marked
	}
	return observed, fmt.Errorf("%w: activation commit acknowledgement was lost: %w", deployment.ErrDeliveryOutcomeUnknown, commitErr)
}

// commitPublicationTx exists solely to make the final acknowledgement
// boundary testable. Production uses sql.Tx.Commit directly; a controlled
// hook may commit and then return an error to model a crash/timeout after the
// durable transaction completed.
func (r *Repository) commitPublicationTx(ctx context.Context, tx *sql.Tx) error {
	if r != nil && r.hooks.CommitPublication != nil {
		return r.hooks.CommitPublication(ctx, tx)
	}
	return tx.Commit()
}

// ReconcilePublication resolves one durable indeterminate activation without
// touching DuckLake or object storage. It commits only when the exact target
// pointer/revision prove that this request committed. It rejects only when
// the exact expected base/revision prove that no activation committed. Any
// other state remains indeterminate and is never used to activate or clean
// the intended candidate.
func (r *Repository) ReconcilePublication(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	publication, err := r.DeliveryPublicationByID(ctx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if publication.Status == deployment.DeliveryPublicationCommitted || publication.Status == deployment.DeliveryPublicationRejected {
		return publication, nil
	}
	if publication.Status == deployment.DeliveryPublicationPending {
		return publication, fmt.Errorf("%w: publication has not been marked indeterminate", deployment.ErrDeliveryOutcomeUnknown)
	}
	if publication.Status != deployment.DeliveryPublicationIndeterminate {
		return publication, fmt.Errorf("%w: unsupported publication state %s", deployment.ErrDeliveryOutcomeUnknown, publication.Status)
	}
	target, err := r.DeliveryTargetRevision(ctx, publication.TargetID)
	if err != nil {
		return publication, err
	}
	switch {
	case target.ActiveGenerationID == publication.GenerationID && target.TargetRevision == publication.ExpectedTargetRevision+1:
		return r.CommitPublication(ctx, id, now.UTC())
	case target.ActiveGenerationID == publication.ExpectedBaseGenerationID && target.TargetRevision == publication.ExpectedTargetRevision:
		// Durable target state proves the activation transaction did not commit.
		// Terminal rejection is control-plane evidence only; the candidate and
		// all physical objects remain untouched for a later explicit build.
		return r.RejectPublication(ctx, id, "activation_not_committed", now.UTC())
	default:
		return publication, fmt.Errorf("%w: target pointer/revision do not prove commit or non-commit", deployment.ErrDeliveryOutcomeUnknown)
	}
}

func validateDeliveryTimeForCommit(now, created time.Time) error {
	if now.IsZero() || now.UTC().Before(created) {
		return fmt.Errorf("%w: publication completion time is invalid", deployment.ErrDeliveryInvalid)
	}
	return nil
}

func eventActor(actor string) string {
	if strings.TrimSpace(actor) == "" {
		// Legacy callers predating authenticated command evidence remain
		// replayable; new coordinators set ActorID before persistence.
		return "delivery"
	}
	return actor
}

func (r *Repository) RejectPublication(ctx context.Context, id, reason string, now time.Time) (deployment.DeliveryPublication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	p, err := deliveryPublicationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	updated, err := p.Reject(reason, now)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	res, err := deploydb.New(tx).RejectDeliveryPublication(ctx, deploydb.RejectDeliveryPublicationParams{Reason: updated.Reason, CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication rejection CAS failed", deployment.ErrDeliveryConflict)
	}
	actor := "delivery"
	if persistedActor, actorErr := deploydb.New(tx).GetDeliveryPublicationRequestActor(ctx, p.ID); actorErr == nil {
		actor = persistedActor
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_rejected", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment, ActorID: eventActor(actor), EventKind: "publish_rejected", ObjectKind: "publication", ObjectID: p.ID, RequestDigest: p.RequestDigest, PlanDigest: p.PlanDigest, Outcome: "rejected", Details: map[string]any{"reason_code": "publication_rejected"}, CreatedAt: updated.CompletedAt}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return updated, nil
}
func (r *Repository) MarkPublicationIndeterminate(ctx context.Context, id string, now time.Time) (deployment.DeliveryPublication, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	defer tx.Rollback()
	p, err := deliveryPublicationByIDTx(ctx, tx, id)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	updated, err := p.MarkIndeterminate(now)
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	res, err := deploydb.New(tx).MarkDeliveryPublicationIndeterminate(ctx, deploydb.MarkDeliveryPublicationIndeterminateParams{CompletedAt: presentString(deliveryTime(updated.CompletedAt)), ID: id})
	if err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return deployment.DeliveryPublication{}, fmt.Errorf("%w: publication indeterminate CAS failed", deployment.ErrDeliveryConflict)
	}
	actor := "delivery"
	if persistedActor, actorErr := deploydb.New(tx).GetDeliveryPublicationRequestActor(ctx, p.ID); actorErr == nil {
		actor = persistedActor
	}
	if _, err := appendDeliveryEventTx(ctx, tx, deployment.DeliveryEvent{ID: deployment.DeliveryEventID(p.TargetID, p.RequestDigest, "publish_indeterminate", "publication", p.ID), TargetID: p.TargetID, ProjectID: p.ProjectID.String(), Environment: p.Environment, ActorID: eventActor(actor), EventKind: "publish_indeterminate", ObjectKind: "publication", ObjectID: p.ID, RequestDigest: p.RequestDigest, Outcome: "indeterminate", Details: map[string]any{}, CreatedAt: updated.CompletedAt}); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	if err := tx.Commit(); err != nil {
		return deployment.DeliveryPublication{}, err
	}
	return updated, nil
}

// CreateQueryLease is the root/GC fence for long-running reads.  It accepts
// exactly one complete candidate or generation catalog and validates the
// catalog/pool binding before inserting the root.
