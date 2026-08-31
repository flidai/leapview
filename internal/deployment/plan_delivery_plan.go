package deployment

import (
	"fmt"
	"time"

	"github.com/flidai/leapview/internal/project/graph"
)

// DeliveryPlanStatus is intentionally small: a plan is immutable evidence;
// expiry is a terminal observation and does not rewrite its canonical inputs.
type DeliveryPlanStatus string

const (
	DeliveryPlanPlanned DeliveryPlanStatus = "planned"
	DeliveryPlanExpired DeliveryPlanStatus = "expired"
)

// DeliveryPlan is a target-owned, immutable plan. Execution, provenance, and
// governance are represented by separate fields so provenance-only changes do
// not falsely invalidate physical reuse.
type DeliveryPlan struct {
	ID string `json:"id"`
	// ActorID is authenticated command evidence. It is intentionally excluded
	// from the plan content digest so retries/replays do not alter execution
	// identity, but repositories retain it in the append-only event ledger.
	ActorID string `json:"actorId,omitempty"`
	// SourceOwnerID is the retained-source namespace used to rehydrate the
	// exact attestation during later builds. It may differ from ActorID for
	// scheduler- or reviewer-initiated restatements.
	SourceOwnerID         string                  `json:"sourceOwnerId,omitempty"`
	TargetID              string                  `json:"targetId"`
	ProjectID             graph.ResourceID        `json:"projectId"`
	Environment           string                  `json:"environment"`
	Operation             DeliveryOperationKind   `json:"operation"`
	SourceDigest          string                  `json:"sourceDigest"`
	ServingArtifactDigest string                  `json:"servingArtifactDigest,omitempty"`
	BaseGenerationID      string                  `json:"baseGenerationId,omitempty"`
	BaseTargetRevision    int64                   `json:"baseTargetRevision"`
	Execution             DeliveryExecutionInputs `json:"execution"`
	Provenance            DeliveryProvenance      `json:"provenance"`
	Governance            DeliveryGovernance      `json:"governance"`
	Evidence              DeliveryPlanEvidence    `json:"evidence"`
	PipelinePlan          *PipelinePlan           `json:"pipelinePlan,omitempty"`
	ExecutionDigest       string                  `json:"executionDigest"`
	ProvenanceDigest      string                  `json:"provenanceDigest"`
	GovernanceDigest      string                  `json:"governanceDigest"`
	EvidenceDigest        string                  `json:"evidenceDigest"`
	Digest                string                  `json:"digest"`
	Status                DeliveryPlanStatus      `json:"status"`
	CreatedAt             time.Time               `json:"createdAt"`
}

// NewDeliveryPlan validates and computes all canonical identity digests. The
// returned value is ready to persist; callers should not mutate it afterward.
func NewDeliveryPlan(plan DeliveryPlan) (DeliveryPlan, error) {
	if err := validateDeliveryTime("plan created at", plan.CreatedAt, true); err != nil {
		return DeliveryPlan{}, err
	}
	if err := validateDeliveryTime("plan expiry", plan.Governance.ExpiresAt, true); err != nil {
		return DeliveryPlan{}, err
	}
	plan.Status = DeliveryPlanPlanned
	plan.CreatedAt = plan.CreatedAt.UTC()
	// Source ownership participates in the canonical plan digest and is
	// required by every durable repository. Resolve the documented legacy
	// default before any digest is computed; a constructor result must never
	// need an identity-changing persistence mutation.
	if plan.SourceOwnerID == "" {
		plan.SourceOwnerID = plan.ActorID
		if plan.SourceOwnerID == "" {
			plan.SourceOwnerID = plan.Provenance.Builder
		}
		if plan.SourceOwnerID == "" {
			plan.SourceOwnerID = "delivery"
		}
	}
	for i := range plan.Execution.DataInputs {
		plan.Execution.DataInputs[i] = plan.Execution.DataInputs[i].canonical()
	}
	plan.Evidence = plan.Evidence.canonical()
	if plan.PipelinePlan != nil {
		canonical := plan.PipelinePlan.Canonical()
		if err := canonical.Validate(); err != nil {
			return DeliveryPlan{}, err
		}
		plan.PipelinePlan = &canonical
		plan.Evidence.PipelinePlan = &canonical
	} else if plan.Evidence.PipelinePlan != nil {
		canonical := plan.Evidence.PipelinePlan.Canonical()
		if err := canonical.Validate(); err != nil {
			return DeliveryPlan{}, err
		}
		plan.PipelinePlan = &canonical
	}
	if err := plan.validateWithoutDigests(); err != nil {
		return DeliveryPlan{}, err
	}
	var err error
	if plan.ExecutionDigest, err = plan.Execution.ExecutionDigest(); err != nil {
		return DeliveryPlan{}, err
	}
	if plan.ProvenanceDigest, err = canonicalJSONDigest(plan.Provenance); err != nil {
		return DeliveryPlan{}, err
	}
	if plan.GovernanceDigest, err = canonicalJSONDigest(plan.Governance); err != nil {
		return DeliveryPlan{}, err
	}
	if plan.EvidenceDigest, err = plan.Evidence.Digest(); err != nil {
		return DeliveryPlan{}, err
	}
	plan.Digest, err = canonicalJSONDigest(deliveryPlanCanonical{
		ID: plan.ID, SourceOwnerID: plan.SourceOwnerID, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment,
		Operation: plan.Operation, SourceDigest: plan.SourceDigest, ServingArtifactDigest: plan.ServingArtifactDigest, BaseGenerationID: plan.BaseGenerationID,
		BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest,
		ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, EvidenceDigest: plan.EvidenceDigest,
		PipelinePlanDigest: pipelinePlanDigest(plan.PipelinePlan),
	})
	if err != nil {
		return DeliveryPlan{}, err
	}
	return plan, plan.Validate()
}

// NewPlan is retained as a concise constructor for callers implementing the
// canonical plan -> build -> publish workflow.
func NewPlan(plan DeliveryPlan) (DeliveryPlan, error) { return NewDeliveryPlan(plan) }

type deliveryPlanCanonical struct {
	ID                    string                `json:"id"`
	SourceOwnerID         string                `json:"sourceOwnerId,omitempty"`
	TargetID              string                `json:"targetId"`
	ProjectID             graph.ResourceID      `json:"projectId"`
	Environment           string                `json:"environment"`
	Operation             DeliveryOperationKind `json:"operation"`
	SourceDigest          string                `json:"sourceDigest"`
	ServingArtifactDigest string                `json:"servingArtifactDigest,omitempty"`
	BaseGenerationID      string                `json:"baseGenerationId,omitempty"`
	BaseTargetRevision    int64                 `json:"baseTargetRevision"`
	ExecutionDigest       string                `json:"executionDigest"`
	ProvenanceDigest      string                `json:"provenanceDigest"`
	GovernanceDigest      string                `json:"governanceDigest"`
	EvidenceDigest        string                `json:"evidenceDigest"`
	PipelinePlanDigest    string                `json:"pipelinePlanDigest,omitempty"`
}

func pipelinePlanDigest(plan *PipelinePlan) string {
	if plan == nil {
		return ""
	}
	return plan.Digest
}

func (plan DeliveryPlan) validateWithoutDigests() error {
	if err := ValidateDeliveryID(plan.ID); err != nil {
		return fmt.Errorf("plan id: %w", err)
	}
	if plan.ActorID != "" {
		if err := ValidateDeliveryID(plan.ActorID); err != nil {
			return fmt.Errorf("plan actor id: %w", err)
		}
	}
	if plan.SourceOwnerID != "" {
		if err := ValidateDeliveryID(plan.SourceOwnerID); err != nil {
			return fmt.Errorf("plan source owner id: %w", err)
		}
	}
	if err := ValidateDeliveryID(plan.TargetID); err != nil {
		return fmt.Errorf("plan target id: %w", err)
	}
	if err := validateDeliveryScope(plan.ProjectID, plan.Environment); err != nil {
		return err
	}
	if !plan.Operation.valid() {
		return fmt.Errorf("%w: unsupported plan operation %q", ErrDeliveryInvalid, plan.Operation)
	}
	if err := ValidateDeliveryDigest(plan.SourceDigest); err != nil {
		return fmt.Errorf("source digest: %w", err)
	}
	if plan.ServingArtifactDigest != "" {
		if err := ValidateDeliveryDigest(plan.ServingArtifactDigest); err != nil {
			return fmt.Errorf("serving artifact digest: %w", err)
		}
	}
	if plan.BaseGenerationID != "" {
		if err := ValidateDeliveryID(plan.BaseGenerationID); err != nil {
			return fmt.Errorf("base generation: %w", err)
		}
	}
	if plan.BaseTargetRevision < 0 {
		return fmt.Errorf("%w: base target revision cannot be negative", ErrDeliveryInvalid)
	}
	if err := plan.Execution.Validate(); err != nil {
		return err
	}
	if plan.Execution.SourceArtifactDigest != plan.SourceDigest {
		return fmt.Errorf("%w: source digest differs from execution source artifact", ErrDeliveryConflict)
	}
	if err := plan.Provenance.Validate(); err != nil {
		return err
	}
	if err := plan.Governance.Validate(); err != nil {
		return err
	}
	if err := plan.Evidence.Validate(); err != nil {
		return err
	}
	if plan.PipelinePlan != nil {
		if err := plan.PipelinePlan.Validate(); err != nil {
			return err
		}
		if plan.BaseGenerationID == "" || plan.PipelinePlan.ServingGenerationID != plan.BaseGenerationID {
			return fmt.Errorf("%w: pipeline plan generation does not match delivery base", ErrDeliveryStale)
		}
		if plan.Evidence.PipelinePlan == nil || plan.Evidence.PipelinePlan.Digest != plan.PipelinePlan.Digest {
			return fmt.Errorf("%w: pipeline plan evidence differs from execution contract", ErrDeliveryConflict)
		}
	}
	if !plan.Governance.ObservedInputsAllowed {
		for _, input := range plan.Execution.DataInputs {
			if input.Mode == DeliveryDataObserved {
				return fmt.Errorf("%w: observed data input %q is forbidden by target policy", ErrDeliveryInvalid, input.ID)
			}
		}
	}
	if err := validateDeliveryTime("plan created at", plan.CreatedAt, true); err != nil {
		return err
	}
	if !plan.Governance.ExpiresAt.After(plan.CreatedAt) {
		return fmt.Errorf("%w: plan expiry must be after creation", ErrDeliveryInvalid)
	}
	return nil
}

func (plan DeliveryPlan) Validate() error {
	if err := plan.validateWithoutDigests(); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"execution": plan.ExecutionDigest, "provenance": plan.ProvenanceDigest,
		"governance": plan.GovernanceDigest, "plan": plan.Digest,
	} {
		if err := ValidateDeliveryDigest(value); err != nil {
			return fmt.Errorf("%s digest: %w", name, err)
		}
	}
	expectedExecution, err := plan.Execution.ExecutionDigest()
	if err != nil || expectedExecution != plan.ExecutionDigest {
		return fmt.Errorf("%w: execution digest does not match canonical inputs", ErrDeliveryConflict)
	}
	expectedProvenance, err := canonicalJSONDigest(plan.Provenance)
	if err != nil || expectedProvenance != plan.ProvenanceDigest {
		return fmt.Errorf("%w: provenance digest does not match canonical inputs", ErrDeliveryConflict)
	}
	expectedGovernance, err := canonicalJSONDigest(plan.Governance)
	if err != nil || expectedGovernance != plan.GovernanceDigest {
		return fmt.Errorf("%w: governance digest does not match canonical inputs", ErrDeliveryConflict)
	}
	expectedEvidence, err := plan.Evidence.Digest()
	if err != nil || expectedEvidence != plan.EvidenceDigest {
		return fmt.Errorf("%w: evidence digest does not match canonical inputs", ErrDeliveryConflict)
	}
	expectedPlan, err := canonicalJSONDigest(deliveryPlanCanonical{
		ID: plan.ID, SourceOwnerID: plan.SourceOwnerID, TargetID: plan.TargetID, ProjectID: plan.ProjectID, Environment: plan.Environment,
		Operation: plan.Operation, SourceDigest: plan.SourceDigest, ServingArtifactDigest: plan.ServingArtifactDigest, BaseGenerationID: plan.BaseGenerationID,
		BaseTargetRevision: plan.BaseTargetRevision, ExecutionDigest: plan.ExecutionDigest,
		ProvenanceDigest: plan.ProvenanceDigest, GovernanceDigest: plan.GovernanceDigest, EvidenceDigest: plan.EvidenceDigest,
		PipelinePlanDigest: pipelinePlanDigest(plan.PipelinePlan),
	})
	if err != nil || expectedPlan != plan.Digest {
		return fmt.Errorf("%w: plan digest does not match canonical inputs", ErrDeliveryConflict)
	}
	if plan.Status != DeliveryPlanPlanned && plan.Status != DeliveryPlanExpired {
		return fmt.Errorf("%w: unsupported plan status %q", ErrDeliveryInvalid, plan.Status)
	}
	return nil
}

// SameCanonicalIntent reports whether two plans carry the same stable
// idempotency intent. Planner wall-clock fields are deliberately excluded:
// retries can cross a second and derive different CreatedAt/ExpiresAt values,
// but must still converge on the first durable plan for one idempotency key.
func (plan DeliveryPlan) SameCanonicalIntent(other DeliveryPlan) bool {
	if plan.ID != other.ID || plan.SourceOwnerID != other.SourceOwnerID || plan.TargetID != other.TargetID || plan.ProjectID != other.ProjectID ||
		plan.Environment != other.Environment || plan.Operation != other.Operation ||
		plan.SourceDigest != other.SourceDigest || plan.ServingArtifactDigest != other.ServingArtifactDigest || plan.BaseGenerationID != other.BaseGenerationID ||
		plan.BaseTargetRevision != other.BaseTargetRevision {
		return false
	}
	if plan.ExecutionDigest != other.ExecutionDigest || plan.ProvenanceDigest != other.ProvenanceDigest ||
		plan.EvidenceDigest != other.EvidenceDigest {
		return false
	}
	// Governance expiry is planner wall-clock evidence, rather than stable
	// idempotency intent. Compare the remaining governance policy canonically.
	leftGovernance, rightGovernance := plan.Governance, other.Governance
	leftGovernance.ExpiresAt, rightGovernance.ExpiresAt = time.Time{}, time.Time{}
	leftGovernanceDigest, leftErr := canonicalJSONDigest(leftGovernance)
	rightGovernanceDigest, rightErr := canonicalJSONDigest(rightGovernance)
	if leftErr != nil || rightErr != nil || leftGovernanceDigest != rightGovernanceDigest {
		return false
	}
	return true
}

// Expired reports the derived expiry condition at now. It does not mutate the
// plan and is therefore safe to call from publication checks.
func (plan DeliveryPlan) Expired(now time.Time) bool {
	return !now.UTC().Before(plan.Governance.ExpiresAt)
}

// Expire records the terminal observation while preserving all plan identity
// fields. Repeating it is idempotent.
func (plan DeliveryPlan) Expire(now time.Time) (DeliveryPlan, error) {
	if err := plan.Validate(); err != nil {
		return DeliveryPlan{}, err
	}
	if !plan.Expired(now) {
		return DeliveryPlan{}, fmt.Errorf("%w: plan has not expired", ErrDeliveryTransition)
	}
	plan.Status = DeliveryPlanExpired
	return plan, nil
}

// Stale compares the plan's one authoritative publication fence. Component
// digests are explanatory evidence, never a second CAS authority.
func (plan DeliveryPlan) Stale(activeGenerationID string, targetRevision int64) bool {
	return activeGenerationID != plan.BaseGenerationID || targetRevision != plan.BaseTargetRevision
}

// ValidateRetainedBaseRequest is the allow-retained-base half of stale
// qualification. A stale plan may proceed only when policy explicitly opts
// in and the build has supplied both exact sealed-base identities. Input
// declarations remain those persisted on the plan; callers cannot substitute
// a new base or silently widen the planned inputs during qualification.
func (plan DeliveryPlan) ValidateRetainedBaseRequest(baseCatalogDigest, basePhysicalPoolID string) error {
	if plan.Evidence.StalePolicy.Mode != "allow_retained_base" || !plan.Evidence.StalePolicy.AllowRetainedBase {
		return fmt.Errorf("%w: stale policy does not permit retained base", ErrDeliveryStale)
	}
	if plan.BaseGenerationID == "" {
		return fmt.Errorf("%w: retained base requires a planned base generation", ErrDeliveryStale)
	}
	if err := ValidateDeliveryDigest(baseCatalogDigest); err != nil {
		return fmt.Errorf("%w: retained base catalog is required: %v", ErrDeliveryStale, err)
	}
	if err := ValidateDeliveryID(basePhysicalPoolID); err != nil {
		return fmt.Errorf("%w: retained base physical pool is required: %v", ErrDeliveryStale, err)
	}
	return nil
}

// PublicationEligible is the fail-closed plan-side half of publication.
func (plan DeliveryPlan) PublicationEligible(activeGenerationID string, targetRevision int64, now time.Time) error {
	if err := plan.Validate(); err != nil {
		return err
	}
	if plan.Status != DeliveryPlanPlanned || plan.Expired(now) {
		return fmt.Errorf("%w: plan is expired", ErrDeliveryPlanExpired)
	}
	if plan.Stale(activeGenerationID, targetRevision) {
		if plan.Evidence.StalePolicy.Mode != "allow_retained_base" || !plan.Evidence.StalePolicy.AllowRetainedBase {
			return fmt.Errorf("%w: base generation or target revision changed", ErrDeliveryStale)
		}
	}
	return nil
}
