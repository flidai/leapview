package module

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	"github.com/flidai/leapview/internal/project"
	"github.com/flidai/leapview/internal/release"
)

// CandidateProvenance builds the same retained provenance used by the legacy
// candidate preparation path. Canonical delivery uses it only after the
// sealed delivery candidate is complete, to project readiness into the
// existing candidate API without making that API a second seal authority.
func CandidateProvenance(candidate deployment.Candidate, artifacts release.CandidateArtifactSet, receipt deployment.CandidateRuntimeReceipt, sourceRevision *project.CandidateSourceRevision) (release.Provenance, error) {
	return candidateReleaseProvenance(candidate, artifacts, receipt, sourceRevision)
}

// CanonicalDeliveryMutations is the durable API coordinator. CreatePlan only
// verifies the target-issued source attestation and persists a plan; Build is
// the first operation that starts a candidate and invokes the physical
// canonical adapter with that exact persisted plan.
type CanonicalDeliveryMutations struct {
	Lifecycle *deployment.DeliveryLifecycle
	Sources   deployment.CandidateSourceSynchronizer
	Adapter   *CanonicalDeliveryAdapter
	Artifacts release.CandidateArtifactPreparer
	Plan      func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error)
	// PlanPreview is the non-persisting form used when Build rechecks durable
	// compiler evidence. Keeping it separate prevents a retry from attempting
	// a second CreatePlan write with a new time-based governance digest.
	PlanPreview  func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error)
	BuildRequest func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error)
	Publish      func(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
	Rollback     func(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
}

type candidateArtifactInspector interface {
	InspectCandidateArtifacts(context.Context, release.CandidateArtifactRequest) (release.CandidateArtifactSet, error)
}
type candidateArtifactMaterializer interface {
	MaterializeCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet) (release.CandidateArtifactSet, error)
}
type candidateArtifactRehydrator interface {
	HydrateCandidateArtifacts(context.Context, release.CandidateArtifactRequest, release.CandidateArtifactSet, release.CandidateArtifactIdentity) (release.CandidateArtifactSet, error)
}

func (m *CanonicalDeliveryMutations) CreatePlan(ctx context.Context, intent DeliveryPlanIntent, idempotencyKey string) (deployment.DeliveryPlan, error) {
	if m == nil || m.Lifecycle == nil || m.Sources == nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("canonical delivery plan coordinator is unavailable")
	}
	reader, ok := m.Sources.(project.CandidateSourceAttestationReader)
	if !ok {
		return deployment.DeliveryPlan{}, fmt.Errorf("target source attestation reader is unavailable")
	}
	if intent.ProjectID.Validate() != nil || intent.PrincipalID == "" || intent.TargetID == "" || intent.SourceDigest == "" || intent.SourceAttestationDigest == "" {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: delivery plan intent is incomplete", deployment.ErrDeliveryInvalid)
	}
	source, err := reader.SnapshotAttestation(ctx, project.CandidateSourceScope{ProjectID: intent.ProjectID, OwnerID: intent.PrincipalID}, intent.SourceDigest, intent.SourceAttestationDigest)
	if err != nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("verify retained source attestation: %w", err)
	}
	if source.ArtifactDigest != intent.SourceDigest || source.SourceAttestationDigest != intent.SourceAttestationDigest {
		return deployment.DeliveryPlan{}, fmt.Errorf("retained source attestation identity changed")
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return deployment.DeliveryPlan{}, fmt.Errorf("idempotency key is required")
	}
	operation := intent.Operation
	if operation == "" {
		operation = deployment.DeliveryOperationCodeChange
	}
	if operation != deployment.DeliveryOperationCodeChange && operation != deployment.DeliveryOperationRestatement && operation != deployment.DeliveryOperationBindingChange && operation != deployment.DeliveryOperationPolicyChange {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: unsupported delivery operation %q", deployment.ErrDeliveryInvalid, operation)
	}
	inspector, inspectOK := m.Artifacts.(candidateArtifactInspector)
	if !inspectOK || m.Plan == nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("compiler evidence planner is unavailable")
	}
	target, err := m.Lifecycle.Targets.ResolveDeliveryTarget(ctx, intent.TargetID)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	planID := "plan-" + digestID(strings.Join([]string{intent.TargetID, intent.ProjectID.String(), intent.Environment, string(operation), idempotencyKey}, "\x00"))
	if existing, readErr := m.Lifecycle.Store.PlanByID(ctx, planID); readErr == nil {
		if existing.TargetID != intent.TargetID || existing.ProjectID != intent.ProjectID || existing.Environment != intent.Environment || existing.Operation != operation || existing.SourceDigest != intent.SourceDigest || existing.ActorID != intent.PrincipalID || existing.Provenance.AttestationDigest != intent.SourceAttestationDigest {
			return deployment.DeliveryPlan{}, fmt.Errorf("%w: idempotency key is bound to a different immutable plan", deployment.ErrDeliveryConflict)
		}
		return existing, nil
	} else if !errors.Is(readErr, deployment.ErrNotFound) && !errors.Is(readErr, sql.ErrNoRows) {
		return deployment.DeliveryPlan{}, readErr
	}
	now := time.Now().UTC()
	if m.Lifecycle.Now != nil {
		now = m.Lifecycle.Now().UTC()
	}
	candidate := deployment.Candidate{ID: strings.TrimPrefix(planID, "plan-"), Key: planID, TargetID: intent.TargetID, OwnerID: intent.PrincipalID, ArtifactDigest: intent.SourceDigest, Scope: deployment.CandidateScope{ProjectID: intent.ProjectID, Environment: intent.Environment, BaseGenerationID: target.ActiveGenerationID}, Revision: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	inspected, err := inspector.InspectCandidateArtifacts(ctx, release.CandidateArtifactRequest{CandidateID: planID, Scope: candidate.Scope, OwnerID: intent.PrincipalID, ArtifactDigest: intent.SourceDigest, Source: source})
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	planned, err := m.Plan(ctx, deployment.DeliveryCandidateBuildInput{ProjectID: intent.ProjectID, OwnerID: intent.PrincipalID, ArtifactDigest: intent.SourceDigest, Operation: operation, CandidateKey: planID, Candidate: candidate, Source: source}, inspected)
	if err != nil {
		return deployment.DeliveryPlan{}, err
	}
	planned.ActorID = intent.PrincipalID
	if planned.ID != planID || planned.Provenance.AttestationDigest != intent.SourceAttestationDigest {
		return deployment.DeliveryPlan{}, fmt.Errorf("compiler plan omitted source attestation binding")
	}
	return m.Lifecycle.Store.CreatePlan(ctx, planned)
}

func (m *CanonicalDeliveryMutations) BuildPlan(ctx context.Context, projectID, planID, principalID, idempotencyKey string) (deployment.DeliveryBuildAttempt, error) {
	if m == nil || m.Lifecycle == nil || m.Artifacts == nil || m.BuildRequest == nil {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("canonical delivery build coordinator is unavailable")
	}
	plan, err := m.Lifecycle.Store.PlanByID(ctx, planID)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if plan.ProjectID.String() != strings.TrimSpace(projectID) {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: plan owner or project mismatch", deployment.ErrDeliveryConflict)
	}
	reader, ok := m.Sources.(project.CandidateSourceAttestationReader)
	if !ok {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("target source attestation reader is unavailable")
	}
	// The retained source owner is part of the durable plan's authenticated
	// author evidence. A reviewer/builder may differ from that author; never
	// substitute the caller's principal here or a valid plan becomes
	// unbuildable by a separate delivery role.
	if strings.TrimSpace(plan.ActorID) == "" {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("%w: durable plan source owner is missing", deployment.ErrDeliveryConflict)
	}
	source, err := reader.SnapshotAttestation(ctx, project.CandidateSourceScope{ProjectID: plan.ProjectID, OwnerID: plan.ActorID}, plan.SourceDigest, plan.Provenance.AttestationDigest)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	if strings.TrimSpace(idempotencyKey) == "" {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("idempotency key is required")
	}
	_, err = m.Lifecycle.Targets.ResolveDeliveryTarget(ctx, plan.TargetID)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	now := time.Now().UTC()
	candidateID := "candidate-" + digestID(plan.ID+"\x00"+idempotencyKey)
	candidate := deployment.Candidate{ID: candidateID, Key: plan.ID + "\x00" + idempotencyKey, TargetID: plan.TargetID, OwnerID: plan.ActorID, ArtifactDigest: plan.SourceDigest, Scope: deployment.CandidateScope{ProjectID: plan.ProjectID, Environment: plan.Environment, BaseGenerationID: plan.BaseGenerationID}, Revision: 1, CreatedAt: now, UpdatedAt: now, ExpiresAt: now.Add(time.Hour)}
	request := release.CandidateArtifactRequest{CandidateID: candidateID, Scope: candidate.Scope, OwnerID: principalID, ArtifactDigest: plan.SourceDigest, Source: source}
	inspector, inspectOK := m.Artifacts.(candidateArtifactInspector)
	if !inspectOK {
		return deployment.DeliveryBuildAttempt{}, fmt.Errorf("compiler evidence inspector is unavailable")
	}
	inspected, err := inspector.InspectCandidateArtifacts(ctx, request)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	buildInput := deployment.DeliveryCandidateBuildInput{ProjectID: plan.ProjectID, OwnerID: principalID, ArtifactDigest: plan.SourceDigest, Operation: plan.Operation, CandidateKey: plan.ID, Candidate: candidate, Source: source, Plan: &plan}
	planningInput := buildInput
	planningInput.Candidate.ID = strings.TrimPrefix(plan.ID, "plan-")
	if err := m.verifyPlanEvidence(ctx, plan, planningInput, inspected); err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	buildRequest, err := m.BuildRequest(ctx, buildInput, inspected)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	// Build idempotency belongs to this operation, not the plan-creation key.
	// Bind it on the request so the lifecycle/repository can persist it
	// atomically with the writer lease and attempt.
	buildRequest.IdempotencyKey = idempotencyKey
	buildRequest.PrepareArtifacts = func(prepareCtx context.Context, buildInput deployment.DeliveryBuildInput) (deployment.DeliveryArtifactIdentity, error) {
		if buildInput.Attempt.ServingArtifactID != "" {
			identity := deployment.DeliveryArtifactIdentity{ServingArtifactID: buildInput.Attempt.ServingArtifactID, ServingArtifactDigest: buildInput.Attempt.ServingArtifactDigest, ServingStateID: buildInput.Attempt.ServingStateID}
			rehydrator, ok := m.Artifacts.(candidateArtifactRehydrator)
			if !ok {
				return deployment.DeliveryArtifactIdentity{}, fmt.Errorf("durable candidate artifact rehydration is unavailable")
			}
			artifacts, hydrateErr := rehydrator.HydrateCandidateArtifacts(prepareCtx, request, inspected, identity)
			if hydrateErr != nil {
				return deployment.DeliveryArtifactIdentity{}, hydrateErr
			}
			if setter, ok := buildRequest.PhasedRunner.(interface{ SetCandidateArtifacts(any) error }); ok {
				if setErr := setter.SetCandidateArtifacts(artifacts); setErr != nil {
					return deployment.DeliveryArtifactIdentity{}, setErr
				}
			}
			return identity, nil
		}
		materializer, materializeOK := m.Artifacts.(candidateArtifactMaterializer)
		var artifacts release.CandidateArtifactSet
		if materializeOK {
			artifacts, err = materializer.MaterializeCandidateArtifacts(prepareCtx, request, inspected)
		} else {
			artifacts, err = m.Artifacts.PrepareCandidateArtifacts(prepareCtx, request)
		}
		if err != nil {
			return deployment.DeliveryArtifactIdentity{}, err
		}
		if setter, ok := buildRequest.PhasedRunner.(interface{ SetCandidateArtifacts(any) error }); ok {
			if setErr := setter.SetCandidateArtifacts(artifacts); setErr != nil {
				return deployment.DeliveryArtifactIdentity{}, setErr
			}
		}
		return deployment.DeliveryArtifactIdentity{ServingArtifactID: artifacts.Generation.ServingArtifactID, ServingArtifactDigest: artifacts.Generation.ArtifactDigest, ServingStateID: artifacts.Generation.Identity.GenerationID}, nil
	}
	result, err := m.Lifecycle.Build(ctx, buildRequest)
	if err != nil {
		return deployment.DeliveryBuildAttempt{}, err
	}
	return result.Attempt, nil
}

func (m *CanonicalDeliveryMutations) PublishCandidate(ctx context.Context, projectID, candidateID, principalID, idempotencyKey string) (deployment.DeliveryPublication, error) {
	if m == nil || m.Publish == nil {
		return deployment.DeliveryPublication{}, fmt.Errorf("canonical delivery publication coordinator is unavailable")
	}
	return m.Publish(ctx, projectID, candidateID, principalID, idempotencyKey)
}

func (m *CanonicalDeliveryMutations) RollbackGeneration(ctx context.Context, projectID, generationID, principalID, idempotencyKey string) (deployment.DeliveryPublication, error) {
	if m == nil || m.Rollback == nil {
		return deployment.DeliveryPublication{}, fmt.Errorf("canonical delivery rollback coordinator is unavailable")
	}
	return m.Rollback(ctx, projectID, generationID, principalID, idempotencyKey)
}

func digestID(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func (m *CanonicalDeliveryMutations) verifyPlanEvidence(ctx context.Context, plan deployment.DeliveryPlan, input deployment.DeliveryCandidateBuildInput, artifacts release.CandidateArtifactSet) error {
	if m == nil || m.Plan == nil {
		return fmt.Errorf("%w: compiler evidence planner is unavailable", deployment.ErrDeliveryInvalid)
	}
	if plan.SourceDigest != artifacts.Artifact.SourceDigest || plan.Provenance.AttestationDigest != input.Source.SourceAttestationDigest {
		return fmt.Errorf("%w: inspected compiler evidence source binding changed", deployment.ErrDeliveryConflict)
	}
	planner := m.PlanPreview
	if planner == nil {
		planner = m.Plan
	}
	expected, err := planner(ctx, input, artifacts)
	if err != nil {
		return fmt.Errorf("recompute compiler evidence for build: %w", err)
	}
	if expected.Operation != plan.Operation || expected.SourceDigest != plan.SourceDigest || expected.ExecutionDigest != plan.ExecutionDigest || expected.ProvenanceDigest != plan.ProvenanceDigest || expected.GovernanceDigest != plan.GovernanceDigest || expected.EvidenceDigest != plan.EvidenceDigest {
		return fmt.Errorf("%w: inspected compiler evidence differs from durable plan", deployment.ErrDeliveryConflict)
	}
	return nil
}

// CanonicalDeliveryAdapter is the composition seam for production candidate
// synchronization. It reuses release's authoritative compiler/artifact
// preparation, then delegates private DuckLake construction and sealing to
// the DeliveryLifecycle. Physical-pool credentials and object stores remain
// target-owned callbacks.
type CanonicalDeliveryAdapter struct {
	Lifecycle      *deployment.DeliveryLifecycle
	Artifacts      release.CandidateArtifactPreparer
	Plan           func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error)
	PlanPreview    func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error)
	BuildRequest   func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryBuildRequest, error)
	ReadyCandidate func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet, deployment.DeliveryBuildResult) (deployment.Candidate, error)
	Publish        func(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
	Rollback       func(context.Context, string, string, string, string) (deployment.DeliveryPublication, error)
}

func (a *CanonicalDeliveryAdapter) BuildCandidate(ctx context.Context, input deployment.DeliveryCandidateBuildInput) (deployment.Candidate, error) {
	if a == nil || a.Lifecycle == nil || a.Artifacts == nil || a.BuildRequest == nil || a.ReadyCandidate == nil {
		return deployment.Candidate{}, deployment.ErrCandidateUnavailable
	}
	if input.ProjectID.Validate() != nil || input.OwnerID == "" || input.ArtifactDigest == "" || input.Candidate.ID == "" {
		return deployment.Candidate{}, fmt.Errorf("%w: canonical delivery input is incomplete", deployment.ErrCandidateInvalid)
	}
	artifacts, err := a.Artifacts.PrepareCandidateArtifacts(ctx, release.CandidateArtifactRequest{
		CandidateID: input.Candidate.ID, Scope: input.Candidate.Scope, OwnerID: input.OwnerID,
		ArtifactDigest: input.ArtifactDigest, Source: input.Source,
	})
	if err != nil {
		return deployment.Candidate{}, err
	}
	// A serving artifact is immutable evidence produced by compilation. Never
	// infer it from runtime input, candidate IDs, or a later activation.
	if artifacts.Generation.Identity.GenerationID == "" || artifacts.Generation.ServingArtifactID == "" || artifacts.Generation.ArtifactDigest == "" {
		return deployment.Candidate{}, fmt.Errorf("%w: compiler returned incomplete serving artifact identity", deployment.ErrCandidateInvalid)
	}
	// DeliveryMutationPort Build supplies the exact durable plan created by
	// CreatePlan. Only the legacy candidate-sync path (which has no persisted
	// plan yet) is allowed to derive one here.
	if input.Plan == nil && a.Plan != nil {
		plan, planErr := resolveLegacyCandidatePlan(ctx, input, artifacts, a.Lifecycle.Store, a.Plan)
		if planErr != nil {
			return deployment.Candidate{}, planErr
		}
		input.Plan = &plan
	}
	request, err := a.BuildRequest(ctx, input, artifacts)
	if err != nil {
		return deployment.Candidate{}, err
	}
	request.ServingArtifactID = artifacts.Generation.ServingArtifactID
	request.ServingArtifactDigest = artifacts.Generation.ArtifactDigest
	request.ServingStateID = artifacts.Generation.Identity.GenerationID
	result, err := a.Lifecycle.Build(ctx, request)
	if err != nil {
		return deployment.Candidate{}, err
	}
	return a.ReadyCandidate(ctx, input, artifacts, result)
}

// resolveLegacyCandidatePlan makes the legacy candidate-sync path durable and
// replay-safe. A failed physical build leaves its plan row behind; retries
// must load that exact row instead of recomputing a time-dependent digest.
func resolveLegacyCandidatePlan(
	ctx context.Context,
	input deployment.DeliveryCandidateBuildInput,
	artifacts release.CandidateArtifactSet,
	store deployment.DeliveryPlanRepository,
	planner func(context.Context, deployment.DeliveryCandidateBuildInput, release.CandidateArtifactSet) (deployment.DeliveryPlan, error),
) (deployment.DeliveryPlan, error) {
	if store == nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("canonical delivery plan repository is unavailable")
	}
	if planner == nil {
		return deployment.DeliveryPlan{}, fmt.Errorf("canonical delivery plan planner is unavailable")
	}
	planID := "plan-" + input.Candidate.ID
	if existing, readErr := store.PlanByID(ctx, planID); readErr == nil {
		if existing.ID != planID || existing.ProjectID != input.ProjectID || existing.TargetID != input.Candidate.TargetID || existing.Environment != input.Candidate.Scope.Environment || existing.SourceDigest != input.ArtifactDigest || existing.Provenance.AttestationDigest != input.Source.SourceAttestationDigest || (input.Operation != "" && existing.Operation != input.Operation) {
			return deployment.DeliveryPlan{}, fmt.Errorf("%w: persisted legacy plan does not match candidate scope", deployment.ErrDeliveryConflict)
		}
		return existing, nil
	} else if !errors.Is(readErr, deployment.ErrNotFound) && !errors.Is(readErr, sql.ErrNoRows) {
		return deployment.DeliveryPlan{}, readErr
	}
	plan, planErr := planner(ctx, input, artifacts)
	if planErr != nil {
		return deployment.DeliveryPlan{}, planErr
	}
	if plan.ID != planID {
		return deployment.DeliveryPlan{}, fmt.Errorf("%w: legacy plan identity does not match candidate", deployment.ErrDeliveryConflict)
	}
	persisted, persistErr := store.CreatePlan(ctx, plan)
	if persistErr != nil {
		return deployment.DeliveryPlan{}, persistErr
	}
	return persisted, nil
}

// CandidateDeliveryBuilder returns a module-compatible function and keeps the
// adapter's physical dependencies in composition rather than HTTP handlers.
func (a *CanonicalDeliveryAdapter) CandidateDeliveryBuilder() func(context.Context, deployment.DeliveryCandidateBuildInput) (deployment.Candidate, error) {
	if a == nil {
		return nil
	}
	return a.BuildCandidate
}
