package module

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/deployment/apiadapter"
	deploymenthttp "github.com/flidai/leapview/internal/deployment/http"
	deploymentpostgres "github.com/flidai/leapview/internal/deployment/postgres"
	platformdigest "github.com/flidai/leapview/internal/platform/digest"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// nativeCoordinator is the HTTP compatibility boundary for the clean-slate
// delivery authority.  It projects the existing public deployment wire shape
// onto delivery_publication while keeping all writes on one caller-owned
// PostgreSQL transaction.  The legacy service/coordinator is deliberately not
// referenced here.
type nativeCoordinator struct {
	repository      *deploymentpostgres.Repository
	targetID        string
	instanceEnv     string
	events          NativeDeliveryEventAppender
	audit           NativeDeliveryAuditAppender
	workflow        NativeDeliveryWorkflowRecorder
	operations      NativeOperationAuthority
	eventReader     nativeDeliveryEventReader
	auditReader     nativeDeliveryAuditReader
	operationReader nativeOperationLookupReader
}

type nativeDeliveryEventReader interface {
	GetDeliveryEvent(context.Context, deploymentpostgres.Tx, NativeDeliveryEventInput) (deploymentpostgres.Event, error)
}
type nativeDeliveryAuditReader interface {
	GetMutationAudit(context.Context, deploymentpostgres.Tx, NativeDeliveryAuditInput) (deploymentpostgres.AuditEvent, error)
}
type nativeOperationLookupReader interface {
	Lookup(context.Context, NativeOperationAcquireInput) (NativeOperationRecord, bool, error)
}

type nativeCoordinatorCapabilities struct {
	events     NativeDeliveryEventAppender
	audit      NativeDeliveryAuditAppender
	workflow   NativeDeliveryWorkflowRecorder
	operations NativeOperationAuthority
}

var _ NativeDeliveryPublicationPort = (*nativeCoordinator)(nil)
var _ NativeDeliveryPublicationCommandCompleter = (*nativeCoordinator)(nil)

func newNativeCoordinator(repository *deploymentpostgres.Repository, targetID, instanceEnv string, capabilities nativeCoordinatorCapabilities) (deploymenthttp.Coordinator, error) {
	if repository == nil || !repository.Configured() {
		return nil, errors.New("native PostgreSQL deployment repository is required")
	}
	if targetID == "" || targetID != strings.TrimSpace(targetID) || instanceEnv == "" || instanceEnv != strings.TrimSpace(instanceEnv) {
		return nil, errors.New("native deployment target and instance environment are required")
	}
	if capabilities.events == nil || capabilities.audit == nil || capabilities.workflow == nil || capabilities.operations == nil {
		return nil, errors.New("native deployment event, audit, workflow, and operation authorities are required")
	}
	coordinator := &nativeCoordinator{repository: repository, targetID: targetID, instanceEnv: instanceEnv, events: capabilities.events, audit: capabilities.audit, workflow: capabilities.workflow, operations: capabilities.operations}
	coordinator.eventReader, _ = capabilities.events.(nativeDeliveryEventReader)
	coordinator.auditReader, _ = capabilities.audit.(nativeDeliveryAuditReader)
	coordinator.operationReader, _ = capabilities.operations.(nativeOperationLookupReader)
	return coordinator, nil
}

func (c *nativeCoordinator) Create(ctx context.Context, request apiadapter.CreateRequest) (apiadapter.Deployment, error) {
	projectID, err := projectgraph.NewResourceID(request.Project)
	if err != nil || request.Project != strings.TrimSpace(request.Project) || request.Environment != strings.TrimSpace(request.Environment) || request.GenerationID != strings.TrimSpace(request.GenerationID) || request.ArtifactDigest != strings.TrimSpace(request.ArtifactDigest) || request.PriorGenerationID != strings.TrimSpace(request.PriorGenerationID) || request.Actor != strings.TrimSpace(request.Actor) || request.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) {
		return apiadapter.Deployment{}, fmt.Errorf("%w: identity fields must be canonical", apiadapter.ErrInvalid)
	}
	if request.Project == "" || request.Environment == "" || request.GenerationID == "" || request.ArtifactDigest == "" || request.Actor == "" || request.IdempotencyKey == "" {
		return apiadapter.Deployment{}, fmt.Errorf("%w: project, environment, generation, artifact, actor, and idempotency key are required", apiadapter.ErrInvalid)
	}
	if c == nil || c.repository == nil || strings.TrimSpace(c.targetID) == "" {
		return apiadapter.Deployment{}, fmt.Errorf("%w: native delivery target is not configured", apiadapter.ErrInvalid)
	}
	if c.instanceEnv != "" && request.Environment != c.instanceEnv {
		return apiadapter.Deployment{}, fmt.Errorf("%w: environment does not match instance", apiadapter.ErrInvalid)
	}
	if err := platformdigest.ValidateSHA256Identity(request.ArtifactDigest); err != nil {
		return apiadapter.Deployment{}, fmt.Errorf("%w: artifact digest is invalid", apiadapter.ErrInvalid)
	}
	generationID, err := canonicalUUID(request.GenerationID, "generation id")
	if err != nil {
		return apiadapter.Deployment{}, fmt.Errorf("%w: %v", apiadapter.ErrInvalid, err)
	}
	priorGenerationID := ""
	if request.PriorGenerationID != "" {
		priorGenerationID, err = canonicalUUID(request.PriorGenerationID, "prior generation id")
		if err != nil {
			return apiadapter.Deployment{}, fmt.Errorf("%w: %v", apiadapter.ErrInvalid, err)
		}
	}
	requestDigest, err := apiadapter.RequestDigest(request)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	target, err := c.repository.Target(ctx, c.targetID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if target.ProjectID != projectID.String() || target.Environment != request.Environment {
		return apiadapter.Deployment{}, fmt.Errorf("%w: project/environment do not match target", apiadapter.ErrInvalid)
	}
	generation, err := c.repository.Generation(ctx, generationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if generation.TargetID != c.targetID || generation.ServingArtifactDigest != request.ArtifactDigest {
		return apiadapter.Deployment{}, fmt.Errorf("%w: generation identity differs from request", deployment.ErrConflict)
	}
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	operationInput := NativeOperationAcquireInput{Scope: c.targetID, OperationType: "deployment.create", IdempotencyKey: request.IdempotencyKey, RequestDigest: nativeOperationDigest("create", requestDigest, request.Actor, request.IdempotencyKey), OwnerID: request.Actor}
	operation, err := c.operations.AcquireTx(ctx, tx, operationInput)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	replay, err := nativeOperationDisposition(operation, operationInput)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if replay {
		outcome, decodeErr := decodeNativeOutcome(operation.Operation.Outcome)
		if decodeErr != nil {
			return apiadapter.Deployment{}, decodeErr
		}
		publication, readErr := deploymentpostgres.New(tx).Publication(ctx, outcome.PublicationID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		if publication.TargetID != c.targetID || publication.GenerationID != generationID || publication.ActorID != request.Actor || publication.RequestDigest != requestDigest || publication.ExpectedBaseGenerationID != priorGenerationID {
			return apiadapter.Deployment{}, fmt.Errorf("%w: create replay identity differs", deployment.ErrConflict)
		}
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return apiadapter.Deployment{}, mapNativeError(err)
		}
		committed = true
		return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
	}
	lockedTarget, err := deploymentpostgres.New(tx).TargetForShareTx(ctx, tx, c.targetID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if lockedTarget.ProjectID != projectID.String() || lockedTarget.Environment != request.Environment || lockedTarget.TargetRevision != target.TargetRevision {
		return apiadapter.Deployment{}, fmt.Errorf("%w: target revision changed while creating publication", deployment.ErrConflict)
	}
	target = lockedTarget
	lockedGeneration, err := deploymentpostgres.New(tx).Generation(ctx, generationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if !sameNativeGenerationIdentity(lockedGeneration, generation) {
		return apiadapter.Deployment{}, fmt.Errorf("%w: generation identity changed while creating publication", deployment.ErrConflict)
	}
	generation = lockedGeneration
	publicationID := operation.Operation.OperationID
	eventID, err := newNativeUUIDv7()
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	auditID, err := newNativeUUIDv7()
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	publication, err := deploymentpostgres.New(tx).CreatePublication(ctx, deploymentpostgres.PublicationInput{
		PublicationID: publicationID, TargetID: c.targetID, GenerationID: generationID,
		ExpectedBaseGenerationID: priorGenerationID, CandidateID: generation.CandidateID,
		SnapshotSealID: generation.SnapshotSealID, ExpectedTargetRevision: target.TargetRevision,
		ActorID: request.Actor, RequestDigest: requestDigest,
	})
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if err := c.appendMutationEvidence(ctx, tx, publication, "publication_created", request.Actor, publication.RequestDigest, eventID, auditID, request.Workflow); err != nil {
		return apiadapter.Deployment{}, err
	}
	outcome, _ := json.Marshal(nativeMutationOutcome{PublicationID: publication.PublicationID, EventID: eventID, AuditID: auditID})
	if err := c.operations.CompleteTx(ctx, tx, operation.Lease, outcome); err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	committed = true
	return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
}

// PublishCandidate creates a pending native publication for the sole
// generation bound to candidateID. It never approves or activates the
// publication; those are separate authorities and command phases.
func (c *nativeCoordinator) PublishCandidate(ctx context.Context, request NativeDeliveryPublishRequest) (NativeDeliveryPublication, error) {
	if c == nil || c.repository == nil || c.operations == nil || c.events == nil || c.audit == nil {
		return NativeDeliveryPublication{}, ErrDeliveryInputUnavailable
	}
	if err := request.validate(c.instanceEnv); err != nil {
		return NativeDeliveryPublication{}, err
	}
	return c.createNativePublication(ctx, request.ProjectID, request.TargetID, request.Environment, request.CandidateID, uuid.Nil, request.PrincipalID, request.IdempotencyKey, false)
}

// RollbackGeneration creates a pending native rollback publication targeting
// the explicit existing generation. The current active generation and target
// revision are captured as the expected CAS fence in the same transaction.
func (c *nativeCoordinator) RollbackGeneration(ctx context.Context, request NativeDeliveryRollbackRequest) (NativeDeliveryPublication, error) {
	if c == nil || c.repository == nil || c.operations == nil || c.events == nil || c.audit == nil {
		return NativeDeliveryPublication{}, ErrDeliveryInputUnavailable
	}
	if err := request.validate(c.instanceEnv); err != nil {
		return NativeDeliveryPublication{}, err
	}
	return c.createNativePublication(ctx, request.ProjectID, request.TargetID, request.Environment, uuid.Nil, request.GenerationID, request.PrincipalID, request.IdempotencyKey, true)
}

func (c *nativeCoordinator) createNativePublication(ctx context.Context, project projectgraph.ResourceID, targetID, environment string, candidateID, generationID uuid.UUID, actor, idempotencyKey string, rollback bool) (NativeDeliveryPublication, error) {
	if targetID != c.targetID || environment != c.instanceEnv {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: native publication target scope differs from instance", deployment.ErrDeliveryConflict)
	}
	requestKind := "publish"
	operationType := "delivery.publication.create"
	if rollback {
		requestKind = "rollback"
		operationType = "delivery.rollback.create"
	}
	requestDigest := nativeOperationDigest(requestKind, project.String(), targetID, environment, candidateID.String(), generationID.String(), actor, idempotencyKey)
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return NativeDeliveryPublication{}, mapNativeError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	operation, err := c.operations.AcquireTx(ctx, tx, NativeOperationAcquireInput{Scope: targetID, OperationType: operationType, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, OwnerID: actor})
	if err != nil {
		return NativeDeliveryPublication{}, mapNativeError(err)
	}
	replay, err := nativeOperationDisposition(operation, NativeOperationAcquireInput{Scope: targetID, OperationType: operationType, IdempotencyKey: idempotencyKey, RequestDigest: requestDigest, OwnerID: actor})
	if err != nil {
		return NativeDeliveryPublication{}, err
	}
	if replay {
		outcome, decodeErr := decodeNativeOutcome(operation.Operation.Outcome)
		if decodeErr != nil {
			return NativeDeliveryPublication{}, decodeErr
		}
		publication, readErr := c.repository.PublicationTx(ctx, tx, outcome.PublicationID)
		if readErr != nil {
			return NativeDeliveryPublication{}, mapNativeError(readErr)
		}
		if publication.TargetID != targetID || publication.ActorID != actor || publication.RequestDigest != requestDigest || (candidateID != uuid.Nil && publication.CandidateID != candidateID.String()) || (generationID != uuid.Nil && publication.GenerationID != generationID.String()) {
			return NativeDeliveryPublication{}, fmt.Errorf("%w: native publication replay identity differs", deployment.ErrDeliveryConflict)
		}
		_, readErr = c.repository.TargetTx(ctx, tx, targetID)
		if readErr != nil {
			return NativeDeliveryPublication{}, mapNativeError(readErr)
		}
		generation, readErr := c.repository.GenerationTx(ctx, tx, publication.GenerationID)
		if readErr != nil {
			return NativeDeliveryPublication{}, mapNativeError(readErr)
		}
		if rollback {
			if readErr = c.repository.RequireRollbackRootTx(ctx, tx, publication.PublicationID, targetID, publication.GenerationID, publication.CandidateID, publication.SnapshotSealID); readErr != nil {
				return NativeDeliveryPublication{}, mapNativeError(readErr)
			}
		}
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
		committed = true
		projection, projectionErr := nativePublicationProjection(publication, project, environment, generation.PlanID, generation.PlanDigest, outcome.EventID, outcome.AuditID)
		if projectionErr != nil {
			return NativeDeliveryPublication{}, projectionErr
		}
		projection.IdempotencyKey = idempotencyKey
		return projection, nil
	}
	target, err := c.repository.TargetForShareTx(ctx, tx, targetID)
	if err != nil {
		return NativeDeliveryPublication{}, mapNativeError(err)
	}
	if target.ProjectID != project.String() || target.Environment != environment {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: target project/environment scope differs from request", deployment.ErrDeliveryConflict)
	}
	var generation deploymentpostgres.DeliveryGeneration
	var candidate deploymentpostgres.DeliveryCandidate
	if rollback {
		generation, err = c.repository.GenerationTx(ctx, tx, generationID.String())
		if err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
		if err = c.repository.RequireGenerationRootTx(ctx, tx, targetID, generation.GenerationID); err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
		candidate, err = c.repository.CandidateTx(ctx, tx, generation.CandidateID)
		if err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
		if generation.TargetID != targetID || candidate.TargetID != targetID {
			// Candidate has no project column; target identity above is the
			// authoritative project/environment scope.
			return NativeDeliveryPublication{}, fmt.Errorf("%w: rollback generation scope differs from request", deployment.ErrDeliveryConflict)
		}
	} else {
		resolved, resolveErr := c.repository.ResolveCandidateGenerationTx(ctx, tx, candidateID.String())
		if resolveErr != nil {
			return NativeDeliveryPublication{}, mapNativeError(resolveErr)
		}
		if resolved.ProjectID != project.String() || resolved.Environment != environment || resolved.TargetID != targetID || resolved.GenerationCount != 1 {
			return NativeDeliveryPublication{}, fmt.Errorf("%w: candidate/generation scope differs from request", deployment.ErrDeliveryConflict)
		}
		if resolved.Status != "qualified" && resolved.Status != "ready" && resolved.Status != "admitted" {
			return NativeDeliveryPublication{}, fmt.Errorf("%w: candidate is not qualified", deployment.ErrDeliveryConflict)
		}
		generation, err = c.repository.GenerationTx(ctx, tx, resolved.GenerationID)
		if err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
		candidate, err = c.repository.CandidateTx(ctx, tx, resolved.CandidateID)
		if err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
		if generation.TargetID != targetID || generation.CandidateID != candidateID.String() || candidate.SnapshotSealID != generation.SnapshotSealID || candidate.PlanID != generation.PlanID {
			return NativeDeliveryPublication{}, fmt.Errorf("%w: candidate/generation identity differs", deployment.ErrDeliveryConflict)
		}
	}
	if generationID != uuid.Nil && generation.GenerationID != generationID.String() {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: rollback generation identity differs", deployment.ErrDeliveryConflict)
	}
	// The target row is share-locked above, so this active pointer and revision
	// remain the expected CAS fence for the pending request.
	publicationID := operation.Operation.OperationID
	eventID, err := newNativeUUIDv7()
	if err != nil {
		return NativeDeliveryPublication{}, err
	}
	auditID, err := newNativeUUIDv7()
	if err != nil {
		return NativeDeliveryPublication{}, err
	}
	publication, err := c.repository.CreatePublicationTx(ctx, tx, deploymentpostgres.PublicationInput{PublicationID: publicationID, TargetID: targetID, GenerationID: generation.GenerationID, ExpectedBaseGenerationID: target.ActiveGenerationID, CandidateID: generation.CandidateID, SnapshotSealID: generation.SnapshotSealID, ExpectedTargetRevision: target.TargetRevision, ActorID: actor, RequestDigest: requestDigest})
	if err != nil {
		return NativeDeliveryPublication{}, mapNativeError(err)
	}
	if rollback {
		if _, err := c.repository.CreateRetentionRootTx(ctx, tx, deploymentpostgres.DeliveryRetentionRoot{RootID: publication.PublicationID, TargetID: targetID, CandidateID: generation.CandidateID, GenerationID: generation.GenerationID, SnapshotSealID: generation.SnapshotSealID, RootKind: "rollback", State: "live", Evidence: json.RawMessage(`{"purpose":"pending rollback publication"}`)}); err != nil {
			return NativeDeliveryPublication{}, mapNativeError(err)
		}
	}
	eventType := "delivery.publication.requested"
	if rollback {
		eventType = "delivery.rollback.requested"
	}
	if err := c.appendMutationEvidence(ctx, tx, publication, eventType, actor, requestDigest, eventID, auditID, nil); err != nil {
		return NativeDeliveryPublication{}, err
	}
	outcome, _ := json.Marshal(nativeMutationOutcome{PublicationID: publication.PublicationID, EventID: eventID, AuditID: auditID})
	if err := c.operations.CompleteTx(ctx, tx, operation.Lease, outcome); err != nil {
		return NativeDeliveryPublication{}, mapNativeError(err)
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return NativeDeliveryPublication{}, mapNativeError(err)
	}
	committed = true
	projection, projectionErr := nativePublicationProjection(publication, project, environment, generation.PlanID, generation.PlanDigest, eventID, auditID)
	if projectionErr != nil {
		return NativeDeliveryPublication{}, projectionErr
	}
	projection.IdempotencyKey = idempotencyKey
	return projection, nil
}

func (c *nativeCoordinator) CompleteNativePublishCommand(ctx context.Context, publication NativeDeliveryPublication) error {
	return c.completeNativePublicationCommand(ctx, publication, "delivery.publication.requested", "delivery.publication.create")
}

func (c *nativeCoordinator) CompleteNativeRollbackCommand(ctx context.Context, publication NativeDeliveryPublication) error {
	return c.completeNativePublicationCommand(ctx, publication, "delivery.rollback.requested", "delivery.rollback.create")
}

func (c *nativeCoordinator) completeNativePublicationCommand(ctx context.Context, publication NativeDeliveryPublication, eventType, operationType string) error {
	if c == nil || c.repository == nil || c.eventReader == nil || c.auditReader == nil || c.operationReader == nil {
		return ErrDeliveryInputUnavailable
	}
	if publication.ID == uuid.Nil || publication.OperationID != publication.ID || publication.EventID == uuid.Nil || publication.AuditID == uuid.Nil || publication.IdempotencyKey == "" || publication.RequestDigest == "" {
		return fmt.Errorf("%w: native publication completion evidence is incomplete", deployment.ErrDeliveryInvalid)
	}
	operation, found, err := c.operationReader.Lookup(contextOrBackground(ctx), NativeOperationAcquireInput{Scope: publication.TargetID, OperationType: operationType, IdempotencyKey: publication.IdempotencyKey, RequestDigest: publication.RequestDigest, OwnerID: publication.ActorID})
	if err != nil {
		return mapNativeError(err)
	}
	if !found || operation.OperationID != publication.ID.String() {
		return fmt.Errorf("%w: native publication operation evidence is unavailable", deployment.ErrDeliveryConflict)
	}
	outcome, err := decodeNativeOutcome(operation.Outcome)
	if err != nil || outcome.PublicationID != publication.ID.String() || outcome.EventID != publication.EventID.String() || outcome.AuditID != publication.AuditID.String() {
		return fmt.Errorf("%w: native publication operation outcome differs", deployment.ErrDeliveryConflict)
	}
	tx, err := c.repository.Begin(contextOrBackground(ctx))
	if err != nil {
		return mapNativeError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	payload, _ := json.Marshal(map[string]any{"publication_id": publication.ID.String(), "generation_id": publication.GenerationID.String(), "target_revision": publication.ExpectedTargetRevision})
	event, err := c.eventReader.GetDeliveryEvent(ctx, tx, NativeDeliveryEventInput{EventID: publication.EventID.String(), ScopeID: publication.TargetID, AggregateType: "delivery_publication", AggregateID: publication.ID.String(), EventType: eventType, SchemaVersion: 1, CorrelationID: publication.EventID.String(), Payload: payload})
	if err != nil || event.EventID != publication.EventID.String() || event.EventType != eventType || event.AggregateID != publication.ID.String() || event.AggregateVersion <= 0 || !sameJSON(event.Payload, payload) {
		if err == nil {
			err = fmt.Errorf("%w: native publication event identity differs", deployment.ErrDeliveryConflict)
		}
		return err
	}
	audit, err := c.auditReader.GetMutationAudit(ctx, tx, NativeDeliveryAuditInput{AuditID: publication.AuditID.String(), DomainEventID: publication.EventID.String(), ScopeID: publication.TargetID, ActorID: publication.ActorID, Action: eventType, ResourceKind: "publication", ResourceID: publication.ID.String(), Outcome: "accepted", RequestDigest: publication.RequestDigest, CorrelationID: publication.EventID.String(), AggregateKey: publication.ID.String(), AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil || audit.AuditID != publication.AuditID.String() || audit.EventID != publication.EventID.String() || audit.Action != eventType || audit.ResourceID != publication.ID.String() || audit.Outcome != "accepted" || audit.RequestDigest != publication.RequestDigest || !sameJSON(audit.Metadata, payload) {
		if err == nil {
			err = fmt.Errorf("%w: native publication audit identity differs", deployment.ErrDeliveryConflict)
		}
		return err
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return mapNativeError(err)
	}
	committed = true
	return nil
}

func nativePublicationProjection(publication deploymentpostgres.DeliveryPublication, project projectgraph.ResourceID, environment, planID, planDigest, eventID, auditID string) (NativeDeliveryPublication, error) {
	publicationID, err := uuid.Parse(publication.PublicationID)
	if err != nil {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: publication identity: %v", deployment.ErrDeliveryConflict, err)
	}
	planUUID, err := uuid.Parse(planID)
	if err != nil {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: plan identity: %v", deployment.ErrDeliveryConflict, err)
	}
	candidateUUID, err := uuid.Parse(publication.CandidateID)
	if err != nil {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: candidate identity: %v", deployment.ErrDeliveryConflict, err)
	}
	generationUUID, err := uuid.Parse(publication.GenerationID)
	if err != nil {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: generation identity: %v", deployment.ErrDeliveryConflict, err)
	}
	eventUUID, err := uuid.Parse(eventID)
	if err != nil {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: event identity: %v", deployment.ErrDeliveryConflict, err)
	}
	auditUUID, err := uuid.Parse(auditID)
	if err != nil {
		return NativeDeliveryPublication{}, fmt.Errorf("%w: audit identity: %v", deployment.ErrDeliveryConflict, err)
	}
	result := NativeDeliveryPublication{ID: publicationID, OperationID: publicationID, EventID: eventUUID, AuditID: auditUUID, ActorID: publication.ActorID, RequestDigest: publication.RequestDigest, ProjectID: project, TargetID: publication.TargetID, Environment: environment, PlanID: planUUID, PlanDigest: planDigest, CandidateID: candidateUUID, GenerationID: generationUUID, ExpectedTargetRevision: publication.ExpectedTargetRevision, ResultTargetRevision: publication.ResultTargetRevision, Status: publication.State, CreatedAt: publication.CreatedAt, CompletedAt: publication.CommittedAt}
	if publication.ExpectedBaseGenerationID != "" {
		result.ExpectedBaseGenerationID, err = uuid.Parse(publication.ExpectedBaseGenerationID)
		if err != nil {
			return NativeDeliveryPublication{}, fmt.Errorf("%w: base generation identity: %v", deployment.ErrDeliveryConflict, err)
		}
	}
	return result, nil
}

func (c *nativeCoordinator) Get(ctx context.Context, scope apiadapter.Scope) (apiadapter.Deployment, error) {
	projectID, err := validateNativeScope(scope)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if c == nil || c.repository == nil || strings.TrimSpace(c.targetID) == "" {
		return apiadapter.Deployment{}, fmt.Errorf("%w: native delivery target is not configured", apiadapter.ErrInvalid)
	}
	publicationID, err := canonicalUUID(scope.DeploymentID, "deployment id")
	if err != nil {
		return apiadapter.Deployment{}, fmt.Errorf("%w: %v", apiadapter.ErrInvalid, err)
	}
	publication, err := c.repository.Publication(ctx, publicationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	target, err := c.repository.Target(ctx, publication.TargetID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if publication.TargetID != c.targetID || target.ProjectID != projectID.String() {
		return apiadapter.Deployment{}, deployment.ErrNotFound
	}
	if c.instanceEnv != "" && target.Environment != c.instanceEnv {
		return apiadapter.Deployment{}, deployment.ErrNotFound
	}
	generation, err := c.repository.Generation(ctx, publication.GenerationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
}

func (c *nativeCoordinator) Activate(ctx context.Context, request apiadapter.ActivateRequest) (apiadapter.Deployment, error) {
	if request.Actor == "" || request.IdempotencyKey == "" || request.Actor != strings.TrimSpace(request.Actor) || request.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) {
		return apiadapter.Deployment{}, fmt.Errorf("%w: actor and idempotency key are required", apiadapter.ErrInvalid)
	}
	projectID, err := validateNativeScope(request.Scope)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if c == nil || c.repository == nil || strings.TrimSpace(c.targetID) == "" {
		return apiadapter.Deployment{}, fmt.Errorf("%w: native delivery target is not configured", apiadapter.ErrInvalid)
	}
	publicationID, _ := canonicalUUID(request.DeploymentID, "deployment id")
	operationDigest := nativeOperationDigest("activate", publicationID, request.Actor, request.IdempotencyKey)
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	operationInput := NativeOperationAcquireInput{Scope: c.targetID, OperationType: "deployment.activate", IdempotencyKey: request.IdempotencyKey, RequestDigest: operationDigest, OwnerID: request.Actor}
	operation, err := c.operations.AcquireTx(ctx, tx, operationInput)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	replay, err := nativeOperationDisposition(operation, operationInput)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if replay {
		outcome, decodeErr := decodeNativeOutcome(operation.Operation.Outcome)
		if decodeErr != nil {
			return apiadapter.Deployment{}, decodeErr
		}
		publication, readErr := deploymentpostgres.New(tx).Publication(ctx, outcome.PublicationID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		target, readErr := deploymentpostgres.New(tx).Target(ctx, publication.TargetID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		generation, readErr := deploymentpostgres.New(tx).Generation(ctx, publication.GenerationID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		if outcome.PublicationID != publicationID || publication.State != "committed" {
			return apiadapter.Deployment{}, fmt.Errorf("%w: activation replay outcome differs", deployment.ErrConflict)
		}
		if target.ProjectID != projectID.String() || publication.TargetID != c.targetID || (c.instanceEnv != "" && target.Environment != c.instanceEnv) {
			return apiadapter.Deployment{}, deployment.ErrNotFound
		}
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return apiadapter.Deployment{}, mapNativeError(err)
		}
		committed = true
		return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
	}
	publication, err := deploymentpostgres.New(tx).Publication(ctx, publicationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	target, err := deploymentpostgres.New(tx).Target(ctx, publication.TargetID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if publication.TargetID != c.targetID || target.ProjectID != projectID.String() || (c.instanceEnv != "" && target.Environment != c.instanceEnv) {
		return apiadapter.Deployment{}, deployment.ErrNotFound
	}
	generation, err := deploymentpostgres.New(tx).Generation(ctx, publication.GenerationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if publication.State == "committed" {
		outcome, _ := json.Marshal(nativeMutationOutcome{PublicationID: publication.PublicationID})
		if err := c.operations.CompleteTx(ctx, tx, operation.Lease, outcome); err != nil {
			return apiadapter.Deployment{}, mapNativeError(err)
		}
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return apiadapter.Deployment{}, mapNativeError(err)
		}
		committed = true
		return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
	}
	leaseID, err := newNativeUUIDv7()
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	lease, err := c.repository.AcquireLeaseTx(ctx, tx, deploymentpostgres.LeaseInput{LeaseID: leaseID, TargetID: publication.TargetID, OwnerID: request.Actor, ExpiresAt: time.Now().UTC().Add(5 * time.Minute)})
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	correlationID, err := newNativeUUIDv7()
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	result, err := c.repository.ActivateTx(ctx, tx, deploymentpostgres.ActivationInput{PublicationID: publicationID, TargetID: publication.TargetID, GenerationID: publication.GenerationID, ExpectedTargetRevision: publication.ExpectedTargetRevision, RequestDigest: publication.RequestDigest, ActorID: request.Actor, CorrelationID: correlationID, LeaseID: lease.LeaseID, OwnerID: lease.OwnerID, FencingEpoch: lease.FencingEpoch})
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	outcome, _ := json.Marshal(nativeMutationOutcome{PublicationID: result.Publication.PublicationID, EventID: result.Event.EventID, AuditID: result.Audit.AuditID})
	if err := c.operations.CompleteTx(ctx, tx, operation.Lease, outcome); err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	committed = true
	return mapNativePublication(result.Publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
}

func (c *nativeCoordinator) CancelRequest(ctx context.Context, request apiadapter.CancelRequest) (apiadapter.Deployment, error) {
	scope := request.Scope
	projectID, err := validateNativeScope(scope)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if request.Actor == "" || request.Actor != strings.TrimSpace(request.Actor) || request.IdempotencyKey == "" || request.IdempotencyKey != strings.TrimSpace(request.IdempotencyKey) {
		return apiadapter.Deployment{}, fmt.Errorf("%w: actor and idempotency key are required", apiadapter.ErrInvalid)
	}
	if c == nil || c.repository == nil || strings.TrimSpace(c.targetID) == "" {
		return apiadapter.Deployment{}, fmt.Errorf("%w: native delivery target is not configured", apiadapter.ErrInvalid)
	}
	publicationID, _ := canonicalUUID(scope.DeploymentID, "deployment id")
	operationDigest := nativeOperationDigest("cancel", publicationID, request.Actor, request.IdempotencyKey)
	tx, err := c.repository.Begin(ctx)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback(contextOrBackground(ctx))
		}
	}()
	operationInput := NativeOperationAcquireInput{Scope: c.targetID, OperationType: "deployment.cancel", IdempotencyKey: request.IdempotencyKey, RequestDigest: operationDigest, OwnerID: request.Actor}
	operation, err := c.operations.AcquireTx(ctx, tx, operationInput)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	replay, err := nativeOperationDisposition(operation, operationInput)
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if replay {
		outcome, decodeErr := decodeNativeOutcome(operation.Operation.Outcome)
		if decodeErr != nil {
			return apiadapter.Deployment{}, decodeErr
		}
		publication, readErr := deploymentpostgres.New(tx).Publication(ctx, outcome.PublicationID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		target, readErr := deploymentpostgres.New(tx).Target(ctx, publication.TargetID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		generation, readErr := deploymentpostgres.New(tx).Generation(ctx, publication.GenerationID)
		if readErr != nil {
			return apiadapter.Deployment{}, mapNativeError(readErr)
		}
		if outcome.PublicationID != publicationID || publication.State != "rejected" {
			return apiadapter.Deployment{}, fmt.Errorf("%w: cancellation replay outcome differs", deployment.ErrConflict)
		}
		if target.ProjectID != projectID.String() || publication.TargetID != c.targetID || (c.instanceEnv != "" && target.Environment != c.instanceEnv) {
			return apiadapter.Deployment{}, deployment.ErrNotFound
		}
		if err := tx.Commit(contextOrBackground(ctx)); err != nil {
			return apiadapter.Deployment{}, mapNativeError(err)
		}
		committed = true
		return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
	}
	publication, err := c.repository.CancelPublicationTx(ctx, tx, publicationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	target, err := deploymentpostgres.New(tx).Target(ctx, publication.TargetID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if target.ProjectID != projectID.String() || publication.TargetID != c.targetID || (c.instanceEnv != "" && target.Environment != c.instanceEnv) {
		return apiadapter.Deployment{}, deployment.ErrNotFound
	}
	eventID, err := newNativeUUIDv7()
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	auditID, err := newNativeUUIDv7()
	if err != nil {
		return apiadapter.Deployment{}, err
	}
	if err := c.appendMutationEvidence(ctx, tx, publication, "publication_cancelled", request.Actor, operationDigest, eventID, auditID, request.Workflow); err != nil {
		return apiadapter.Deployment{}, err
	}
	outcome, _ := json.Marshal(nativeMutationOutcome{PublicationID: publication.PublicationID})
	if err := c.operations.CompleteTx(ctx, tx, operation.Lease, outcome); err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	if err := tx.Commit(contextOrBackground(ctx)); err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	committed = true
	generation, err := c.repository.Generation(ctx, publication.GenerationID)
	if err != nil {
		return apiadapter.Deployment{}, mapNativeError(err)
	}
	return mapNativePublication(publication, target.ProjectID, target.Environment, generation.ServingArtifactDigest), nil
}

func (c *nativeCoordinator) appendMutationEvidence(ctx context.Context, tx deploymentpostgres.Tx, publication deploymentpostgres.DeliveryPublication, eventType, actor, requestDigest, eventID, auditID string, workflowFn func(string) (jobs.WorkflowIntent, error)) error {
	payload, _ := json.Marshal(map[string]any{"publication_id": publication.PublicationID, "generation_id": publication.GenerationID, "target_revision": publication.ExpectedTargetRevision})
	event, err := c.events.AppendDeliveryEvent(ctx, tx, NativeDeliveryEventInput{EventID: eventID, ScopeID: publication.TargetID, AggregateType: "delivery_publication", AggregateID: publication.PublicationID, EventType: eventType, SchemaVersion: 1, CorrelationID: eventID, Payload: payload})
	if err != nil {
		return mapNativeError(err)
	}
	if event.EventID != eventID || event.ScopeID != publication.TargetID || event.AggregateType != "delivery_publication" || event.AggregateID != publication.PublicationID || event.EventType != eventType || event.SchemaVersion != 1 || event.CorrelationID != eventID || !sameJSON(event.Payload, payload) || event.AggregateVersion <= 0 {
		return fmt.Errorf("%w: native delivery event identity differs", deployment.ErrConflict)
	}
	audit, err := c.audit.AppendMutationAudit(ctx, tx, NativeDeliveryAuditInput{AuditID: auditID, DomainEventID: event.EventID, ScopeID: publication.TargetID, ActorID: actor, Action: eventType, ResourceKind: "publication", ResourceID: publication.PublicationID, Outcome: "accepted", RequestDigest: requestDigest, CorrelationID: event.CorrelationID, AggregateKey: event.AggregateID, AggregateSequence: event.AggregateVersion, Metadata: payload})
	if err != nil {
		return mapNativeError(err)
	}
	if audit.AuditID != auditID || audit.EventID != event.EventID || audit.ScopeID != publication.TargetID || audit.ActorID != actor || audit.Action != eventType || audit.ResourceKind != "publication" || audit.ResourceID != publication.PublicationID || audit.Outcome != "accepted" || audit.RequestDigest != requestDigest || !sameJSON(audit.Metadata, payload) {
		return fmt.Errorf("%w: native delivery audit identity differs", deployment.ErrConflict)
	}
	if workflowFn != nil {
		workflow, err := workflowFn(publication.PublicationID)
		if err != nil {
			return err
		}
		if err := c.workflow.RecordWorkflow(ctx, tx, workflow); err != nil {
			return mapNativeError(err)
		}
	}
	return nil
}

func validateNativeScope(scope apiadapter.Scope) (projectgraph.ResourceID, error) {
	if scope.Project == "" || scope.DeploymentID == "" || scope.Project != strings.TrimSpace(scope.Project) || scope.DeploymentID != strings.TrimSpace(scope.DeploymentID) {
		return "", fmt.Errorf("%w: scope identity must be canonical", apiadapter.ErrInvalid)
	}
	projectID, err := projectgraph.NewResourceID(scope.Project)
	if err != nil {
		return "", fmt.Errorf("%w: scope identity must be canonical", apiadapter.ErrInvalid)
	}
	if _, err := canonicalUUID(scope.DeploymentID, "deployment id"); err != nil {
		return "", fmt.Errorf("%w: %v", apiadapter.ErrInvalid, err)
	}
	return projectID, nil
}

func canonicalUUID(value, label string) (string, error) {
	parsed, err := uuid.Parse(value)
	if err != nil || parsed.String() != value {
		return "", fmt.Errorf("%s must be a canonical UUID", label)
	}
	return parsed.String(), nil
}

type nativeMutationOutcome struct {
	PublicationID string `json:"publicationId"`
	EventID       string `json:"eventId,omitempty"`
	AuditID       string `json:"auditId,omitempty"`
}

func nativeOperationDisposition(result NativeOperationAcquireResult, input NativeOperationAcquireInput) (bool, error) {
	switch result.Status {
	case NativeOperationAcquired:
		if err := validateNativeOperationRecord(result.Operation, input, false); err != nil {
			return false, err
		}
		if result.Lease.Scope != input.Scope || result.Lease.IdempotencyKey != input.IdempotencyKey || result.Lease.OperationID != result.Operation.OperationID || result.Lease.OwnerID != input.OwnerID || result.Lease.FencingGeneration <= 0 || result.Lease.LeaseExpiresAt.IsZero() {
			return false, fmt.Errorf("%w: acquired operation lease identity differs", deployment.ErrConflict)
		}
		return false, nil
	case NativeOperationReplay:
		if err := validateNativeOperationRecord(result.Operation, input, true); err != nil {
			return false, err
		}
		return true, nil
	case NativeOperationBusy:
		return false, fmt.Errorf("%w: operation is owned by another worker", deployment.ErrConflict)
	case NativeOperationIndeterminate:
		return false, fmt.Errorf("%w: operation outcome requires reconciliation", deployment.ErrConflict)
	default:
		return false, fmt.Errorf("%w: operation status %q is unavailable", deployment.ErrConflict, result.Status)
	}
}

func validateNativeOperationRecord(record NativeOperationRecord, input NativeOperationAcquireInput, replay bool) error {
	if _, err := canonicalUUID(record.OperationID, "operation id"); err != nil {
		return fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	if record.Scope != input.Scope || record.OperationType != input.OperationType || record.IdempotencyKey != input.IdempotencyKey || record.RequestDigest != input.RequestDigest || record.OwnerID != input.OwnerID {
		return fmt.Errorf("%w: operation identity differs", deployment.ErrConflict)
	}
	if replay && len(strings.TrimSpace(string(record.Outcome))) == 0 {
		return fmt.Errorf("%w: replay operation outcome is empty", deployment.ErrConflict)
	}
	return nil
}

func decodeNativeOutcome(raw json.RawMessage) (nativeMutationOutcome, error) {
	var outcome nativeMutationOutcome
	if len(raw) == 0 || json.Unmarshal(raw, &outcome) != nil || outcome.PublicationID == "" {
		return nativeMutationOutcome{}, fmt.Errorf("%w: operation outcome is invalid", deployment.ErrConflict)
	}
	if _, err := canonicalUUID(outcome.PublicationID, "operation publication id"); err != nil {
		return nativeMutationOutcome{}, fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	return outcome, nil
}

func nativeOperationDigest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func newNativeUUIDv7() (string, error) {
	id, err := uuid.NewV7()
	if err != nil {
		return "", fmt.Errorf("generate UUIDv7 identity: %w", err)
	}
	return id.String(), nil
}

func mapNativePublication(publication deploymentpostgres.DeliveryPublication, project, environment, artifactDigest string) apiadapter.Deployment {
	status := apiadapter.StatusPending
	if publication.State == "committed" {
		status = apiadapter.StatusActive
	} else if publication.State == "rejected" {
		status = apiadapter.StatusCancelled
	} else if publication.State == "indeterminate" {
		status = apiadapter.StatusFailed
	}
	createdAt := publication.CreatedAt.UTC().Format(time.RFC3339Nano)
	result := apiadapter.Deployment{ID: publication.PublicationID, Project: project, Environment: environment, GenerationID: publication.GenerationID, ArtifactDigest: artifactDigest, PriorGenerationID: publication.ExpectedBaseGenerationID, RequestDigest: publication.RequestDigest, Status: status, CreatedBy: publication.ActorID, CreatedAt: createdAt}
	if !publication.CommittedAt.IsZero() {
		result.ActivatedAt = publication.CommittedAt.UTC().Format(time.RFC3339Nano)
		result.ActivationPrincipal = publication.ActorID
	}
	return result
}

func sameNativeGenerationIdentity(left, right deploymentpostgres.DeliveryGeneration) bool {
	return left.GenerationID == right.GenerationID && left.TargetID == right.TargetID && left.CandidateID == right.CandidateID && left.SnapshotSealID == right.SnapshotSealID && left.PlanID == right.PlanID && left.PlanDigest == right.PlanDigest && left.ArtifactRoot == right.ArtifactRoot && left.ArtifactRootDigest == right.ArtifactRootDigest && left.ServingArtifactDigest == right.ServingArtifactDigest && left.CompiledGraphDigest == right.CompiledGraphDigest && left.CompiledConfigDigest == right.CompiledConfigDigest && left.SecurityDomainFingerprint == right.SecurityDomainFingerprint && left.GenerationRevision == right.GenerationRevision
}

func mapNativeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, deploymentpostgres.ErrInvalid) {
		return fmt.Errorf("%w: %v", apiadapter.ErrInvalid, err)
	}
	if errors.Is(err, deploymentpostgres.ErrNotQualified) {
		return fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	if errors.Is(err, deploymentpostgres.ErrNotFound) || errors.Is(err, pgx.ErrNoRows) {
		return deployment.ErrNotFound
	}
	if errors.Is(err, deploymentpostgres.ErrConflict) || errors.Is(err, deploymentpostgres.ErrCASConflict) || errors.Is(err, deploymentpostgres.ErrStaleFence) || errors.Is(err, deploymentpostgres.ErrLeaseExpired) || errors.Is(err, deploymentpostgres.ErrAlreadyActive) {
		return fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	if errors.Is(err, ErrNativeOperationConflict) || errors.Is(err, ErrNativeOperationBusy) || errors.Is(err, ErrNativeOperationStaleFence) || errors.Is(err, ErrNativeOperationLeaseExpired) || errors.Is(err, ErrNativeOperationAlreadyTerminal) {
		return fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	if errors.Is(err, ErrNativeOperationInvalid) {
		return fmt.Errorf("%w: %v", apiadapter.ErrInvalid, err)
	}
	if errors.Is(err, ErrNativeOperationNotFound) {
		return deployment.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return fmt.Errorf("%w: %v", deployment.ErrConflict, err)
	}
	return err
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func sameJSON(left, right []byte) bool {
	var a, b any
	if json.Unmarshal(left, &a) != nil || json.Unmarshal(right, &b) != nil {
		return false
	}
	leftCanonical, err := json.Marshal(a)
	if err != nil {
		return false
	}
	rightCanonical, err := json.Marshal(b)
	return err == nil && bytes.Equal(leftCanonical, rightCanonical)
}

var _ deploymenthttp.Coordinator = (*nativeCoordinator)(nil)
