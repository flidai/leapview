package deployment

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/release"
)

// DeliveryImpactResource is one graph node included in the target-specific
// explanation. IDs and kinds are portable, non-secret resource identities;
// paths and reasons are review evidence rather than execution identity.
type DeliveryImpactResource struct {
	ID     string   `json:"id"`
	Kind   string   `json:"kind"`
	Change string   `json:"change"`
	Reason string   `json:"reason,omitempty"`
	Paths  []string `json:"paths,omitempty"`
}

// DeliveryGraphImpact distinguishes authored changes from dependency impact.
// The slices are canonicalized by NewDeliveryPlan so equivalent graph walks
// produce the same evidence digest.
type DeliveryGraphImpact struct {
	Added              []DeliveryImpactResource `json:"added,omitempty"`
	Removed            []DeliveryImpactResource `json:"removed,omitempty"`
	DirectlyModified   []DeliveryImpactResource `json:"directlyModified,omitempty"`
	IndirectlyAffected []DeliveryImpactResource `json:"indirectlyAffected,omitempty"`
	RelationshipPaths  []string                 `json:"relationshipPaths,omitempty"`
}

// DeliveryCompatibilityImpact records compatibility, semantic, and policy
// consequences discovered while comparing the source graph to the target.
type DeliveryCompatibilityImpact struct {
	Breaking             bool     `json:"breaking,omitempty"`
	ContractChanges      []string `json:"contractChanges,omitempty"`
	SemanticChanges      []string `json:"semanticChanges,omitempty"`
	AuthorizationChanges []string `json:"authorizationChanges,omitempty"`
	PolicyChanges        []string `json:"policyChanges,omitempty"`
	CompatibilityNotes   []string `json:"compatibilityNotes,omitempty"`
}

// DeliveryEstimate is retained only when the planner has a defensible basis.
// A range is preferred to a falsely precise point estimate.
type DeliveryEstimate struct {
	Work       string  `json:"work"`
	LowerBound float64 `json:"lowerBound,omitempty"`
	UpperBound float64 `json:"upperBound,omitempty"`
	Expected   float64 `json:"expected,omitempty"`
	Unit       string  `json:"unit"`
	Basis      string  `json:"basis"`
	Confidence string  `json:"confidence,omitempty"`
}

// DeliveryPhysicalWork describes materialization and data work before build.
type DeliveryPhysicalWork struct {
	Materializations []string           `json:"materializations,omitempty"`
	Refreshes        []string           `json:"refreshes,omitempty"`
	Backfills        []string           `json:"backfills,omitempty"`
	Restatements     []string           `json:"restatements,omitempty"`
	Estimates        []DeliveryEstimate `json:"estimates,omitempty"`
}

// DeliveryReuseDecision explains every reuse decision that can affect review.
type DeliveryReuseDecision struct {
	ResourceID string `json:"resourceId"`
	Reusable   bool   `json:"reusable"`
	// RetainBase permits a changed execution to rebuild only affected
	// relations from the exact sealed base. It is false whenever the physical
	// identity is not exact or reuse is disabled by nondeterminism/observation.
	RetainBase     bool   `json:"retainBase,omitempty"`
	Reason         string `json:"reason"`
	ReuseKeyDigest string `json:"reuseKeyDigest,omitempty"`
}

// ResolveDeliveryReuseDecision returns the exact candidate-level disposition
// represented by persisted reuse evidence. A single relation decision is not
// treated as candidate evidence when its resource ID differs; multiple
// relation decisions are aggregated so partial reuse retains the base while
// still reporting a non-reusable candidate execution.
func ResolveDeliveryReuseDecision(plan *DeliveryPlan, resourceID string) (DeliveryReuseDecision, bool) {
	if plan == nil {
		return DeliveryReuseDecision{}, false
	}
	for _, decision := range plan.Evidence.Reuse {
		if decision.ResourceID == resourceID {
			return decision, true
		}
	}
	if len(plan.Evidence.Reuse) == 1 {
		if plan.Evidence.Reuse[0].ResourceID != resourceID {
			return DeliveryReuseDecision{}, false
		}
		return plan.Evidence.Reuse[0], true
	}
	if len(plan.Evidence.Reuse) > 1 {
		aggregate := DeliveryReuseDecision{ResourceID: resourceID, Reusable: true, Reason: "all unchanged relation identities are reusable"}
		for _, decision := range plan.Evidence.Reuse {
			if !decision.Reusable {
				aggregate.Reusable = false
			}
			if decision.Reusable || decision.RetainBase {
				aggregate.RetainBase = true
			}
		}
		if aggregate.RetainBase {
			if !aggregate.Reusable {
				aggregate.Reason = "retain exact base for unchanged relations and rebuild impacted relations"
			}
			return aggregate, true
		}
	}
	return DeliveryReuseDecision{}, false
}

// DeliveryReuseInput is the target-owned physical identity used to decide
// whether one resource may retain its exact sealed base references. A reuse
// decision is stricter than an execution-digest comparison: catalog, pool,
// and compatibility identities must also remain unchanged. Determinism is
// explicit so undeclared nondeterministic work cannot be reused accidentally.
type DeliveryReuseInput struct {
	ResourceID string
	// RelationScoped requires both sides of the execution context identity.
	// Candidate-level legacy callers may omit context entirely, but a
	// per-relation decision must never fail open when one context is absent.
	RelationScoped          bool
	ExecutionDigest         string
	BaseExecutionDigest     string
	CatalogDigest           string
	BaseCatalogDigest       string
	PhysicalPoolID          string
	BasePhysicalPoolID      string
	CompatibilityDigest     string
	BaseCompatibilityDigest string
	Deterministic           bool
	Observed                bool
	EquivalenceToken        string
	ContextDigest           string
	BaseContextDigest       string
}

// EvaluateDeliveryReuse returns a reviewable, content-addressed reuse
// decision. A false decision is fail-closed and instructs the caller to
// rebuild immutable physical state rather than guessing from a partial key.
func EvaluateDeliveryReuse(input DeliveryReuseInput) (DeliveryReuseDecision, error) {
	if err := ValidateDeliveryID(input.ResourceID); err != nil {
		return DeliveryReuseDecision{}, err
	}
	for name, value := range map[string]string{
		"execution": input.ExecutionDigest, "base execution": input.BaseExecutionDigest,
		"catalog": input.CatalogDigest, "base catalog": input.BaseCatalogDigest, "physical pool": input.PhysicalPoolID,
		"base physical pool": input.BasePhysicalPoolID,
		"compatibility":      input.CompatibilityDigest, "base compatibility": input.BaseCompatibilityDigest,
	} {
		if strings.Contains(name, "physical pool") {
			if err := ValidateDeliveryID(value); err != nil {
				return DeliveryReuseDecision{}, fmt.Errorf("%s identity: %w", name, err)
			}
			continue
		}
		if err := ValidateDeliveryDigest(value); err != nil {
			return DeliveryReuseDecision{}, fmt.Errorf("%s digest: %w", name, err)
		}
	}
	if input.ContextDigest == "" || input.BaseContextDigest == "" {
		if input.ContextDigest != input.BaseContextDigest || input.RelationScoped {
			return DeliveryReuseDecision{}, fmt.Errorf("execution context identity is incomplete")
		}
	} else {
		if err := ValidateDeliveryDigest(input.ContextDigest); err != nil {
			return DeliveryReuseDecision{}, fmt.Errorf("execution context digest: %w", err)
		}
		if err := ValidateDeliveryDigest(input.BaseContextDigest); err != nil {
			return DeliveryReuseDecision{}, fmt.Errorf("base execution context digest: %w", err)
		}
		if input.ContextDigest != input.BaseContextDigest {
			return DeliveryReuseDecision{ResourceID: input.ResourceID, Reusable: false, Reason: "execution context identity changed"}, nil
		}
	}
	if input.Observed && strings.TrimSpace(input.EquivalenceToken) == "" {
		return DeliveryReuseDecision{ResourceID: input.ResourceID, Reusable: false, Reason: "observed input lacks a stable equivalence token"}, nil
	}
	if !input.Deterministic {
		return DeliveryReuseDecision{ResourceID: input.ResourceID, Reusable: false, Reason: "undeclared nondeterminism disables reuse"}, nil
	}
	if input.CatalogDigest != input.BaseCatalogDigest || input.PhysicalPoolID != input.BasePhysicalPoolID || input.CompatibilityDigest != input.BaseCompatibilityDigest {
		return DeliveryReuseDecision{ResourceID: input.ResourceID, Reusable: false, Reason: "catalog compatibility identity changed"}, nil
	}
	if input.ExecutionDigest != input.BaseExecutionDigest {
		return DeliveryReuseDecision{ResourceID: input.ResourceID, Reusable: false, RetainBase: true, Reason: "execution identity changed; rebuild affected relations from retained base"}, nil
	}
	key, err := canonicalJSONDigest(struct {
		ResourceID          string `json:"resourceId"`
		ExecutionDigest     string `json:"executionDigest"`
		BaseCatalogDigest   string `json:"baseCatalogDigest"`
		PhysicalPoolID      string `json:"physicalPoolId"`
		CompatibilityDigest string `json:"compatibilityDigest"`
		EquivalenceToken    string `json:"equivalenceToken,omitempty"`
	}{
		ResourceID: input.ResourceID, ExecutionDigest: input.ExecutionDigest,
		BaseCatalogDigest: input.BaseCatalogDigest, PhysicalPoolID: input.PhysicalPoolID,
		CompatibilityDigest: input.CompatibilityDigest, EquivalenceToken: strings.TrimSpace(input.EquivalenceToken),
	})
	if err != nil {
		return DeliveryReuseDecision{}, err
	}
	return DeliveryReuseDecision{ResourceID: input.ResourceID, Reusable: true, Reason: "exact execution, catalog, pool, and compatibility identities match", ReuseKeyDigest: key}, nil
}

type DeliveryQualificationStep struct {
	ID          string `json:"id"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
	Required    bool   `json:"required"`
	Blocking    bool   `json:"blocking"`
}

// DeliveryQualificationEvidence records policy and the checks required to
// make a candidate publication eligible.
type DeliveryQualificationEvidence struct {
	Policy string                      `json:"policy"`
	Steps  []DeliveryQualificationStep `json:"steps,omitempty"`
}

type DeliveryStalePolicy struct {
	Mode              string `json:"mode"`
	AllowRetainedBase bool   `json:"allowRetainedBase,omitempty"`
	Description       string `json:"description,omitempty"`
}

// DeliveryRollbackEvidence keeps rollback claims precise instead of making a
// generic "rollbackable" assertion.
type DeliveryRollbackEvidence struct {
	Class           DeliveryRollbackClass `json:"class"`
	RetentionWindow string                `json:"retentionWindow,omitempty"`
	ExternalEffects []string              `json:"externalEffects,omitempty"`
	Description     string                `json:"description,omitempty"`
}

// DeliveryRestatementEvidence is present for an explicit restatement plan.
// Requested and effective intervals are strings because connectors may use an
// instant, date, watermark, or opaque interval token.
type DeliveryRestatementEvidence struct {
	RequestedStart      string            `json:"requestedStart,omitempty"`
	RequestedEnd        string            `json:"requestedEnd,omitempty"`
	EffectiveStart      string            `json:"effectiveStart,omitempty"`
	EffectiveEnd        string            `json:"effectiveEnd,omitempty"`
	DownstreamScope     []string          `json:"downstreamScope,omitempty"`
	Strategy            string            `json:"strategy"`
	IdempotencyKey      string            `json:"idempotencyKey,omitempty"`
	WideningExplanation string            `json:"wideningExplanation,omitempty"`
	Estimate            *DeliveryEstimate `json:"estimate,omitempty"`
}

// DeliveryPlanEvidence is structured, non-secret review evidence. It is
// persisted separately from execution/provenance/governance and is included
// in the immutable plan digest without becoming a CAS authority.
type DeliveryPlanEvidence struct {
	// Statements make empty impact/work/reuse explicit rather than allowing a
	// missing analysis section to be mistaken for no analysis.
	ImpactStatement       string                        `json:"impactStatement"`
	PhysicalWorkStatement string                        `json:"physicalWorkStatement"`
	ReuseStatement        string                        `json:"reuseStatement"`
	GraphImpact           DeliveryGraphImpact           `json:"graphImpact,omitempty"`
	Compatibility         DeliveryCompatibilityImpact   `json:"compatibility,omitempty"`
	PhysicalWork          DeliveryPhysicalWork          `json:"physicalWork,omitempty"`
	Reuse                 []DeliveryReuseDecision       `json:"reuse,omitempty"`
	Qualification         DeliveryQualificationEvidence `json:"qualification,omitempty"`
	StalePolicy           DeliveryStalePolicy           `json:"stalePolicy,omitempty"`
	Rollback              DeliveryRollbackEvidence      `json:"rollback,omitempty"`
	Restatement           *DeliveryRestatementEvidence  `json:"restatement,omitempty"`
	// PipelinePlan is duplicated in evidence JSON so older delivery storage
	// rows can persist the immutable refresh selection without a migration.
	// DeliveryPlan mirrors this pointer as the execution-facing contract.
	PipelinePlan *PipelinePlan `json:"pipelinePlan,omitempty"`
}

func canonicalTextList(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	out := append([]string(nil), values...)
	for i := range out {
		out[i] = strings.TrimSpace(out[i])
	}
	sort.Strings(out)
	return out
}

func canonicalImpactResources(values []DeliveryImpactResource) []DeliveryImpactResource {
	if len(values) == 0 {
		return nil
	}
	out := append([]DeliveryImpactResource(nil), values...)
	for i := range out {
		out[i].ID = strings.TrimSpace(out[i].ID)
		out[i].Kind = strings.TrimSpace(out[i].Kind)
		out[i].Change = strings.TrimSpace(out[i].Change)
		out[i].Reason = strings.TrimSpace(out[i].Reason)
		out[i].Paths = canonicalTextList(out[i].Paths)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ID != out[j].ID {
			return out[i].ID < out[j].ID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Change < out[j].Change
	})
	return out
}

func (e DeliveryPlanEvidence) canonical() DeliveryPlanEvidence {
	e.GraphImpact.Added = canonicalImpactResources(e.GraphImpact.Added)
	e.GraphImpact.Removed = canonicalImpactResources(e.GraphImpact.Removed)
	e.GraphImpact.DirectlyModified = canonicalImpactResources(e.GraphImpact.DirectlyModified)
	e.GraphImpact.IndirectlyAffected = canonicalImpactResources(e.GraphImpact.IndirectlyAffected)
	e.GraphImpact.RelationshipPaths = canonicalTextList(e.GraphImpact.RelationshipPaths)
	e.Compatibility.ContractChanges = canonicalTextList(e.Compatibility.ContractChanges)
	e.Compatibility.SemanticChanges = canonicalTextList(e.Compatibility.SemanticChanges)
	e.Compatibility.AuthorizationChanges = canonicalTextList(e.Compatibility.AuthorizationChanges)
	e.Compatibility.PolicyChanges = canonicalTextList(e.Compatibility.PolicyChanges)
	e.Compatibility.CompatibilityNotes = canonicalTextList(e.Compatibility.CompatibilityNotes)
	e.PhysicalWork.Materializations = canonicalTextList(e.PhysicalWork.Materializations)
	e.PhysicalWork.Refreshes = canonicalTextList(e.PhysicalWork.Refreshes)
	e.PhysicalWork.Backfills = canonicalTextList(e.PhysicalWork.Backfills)
	e.PhysicalWork.Restatements = canonicalTextList(e.PhysicalWork.Restatements)
	sort.Slice(e.PhysicalWork.Estimates, func(i, j int) bool { return e.PhysicalWork.Estimates[i].Work < e.PhysicalWork.Estimates[j].Work })
	sort.Slice(e.Reuse, func(i, j int) bool { return e.Reuse[i].ResourceID < e.Reuse[j].ResourceID })
	sort.Slice(e.Qualification.Steps, func(i, j int) bool { return e.Qualification.Steps[i].ID < e.Qualification.Steps[j].ID })
	e.Rollback.ExternalEffects = canonicalTextList(e.Rollback.ExternalEffects)
	if e.Restatement != nil {
		r := *e.Restatement
		r.DownstreamScope = canonicalTextList(r.DownstreamScope)
		e.Restatement = &r
	}
	if e.PipelinePlan != nil {
		canonical := e.PipelinePlan.Canonical()
		e.PipelinePlan = &canonical
	}
	return e
}

func (e DeliveryPlanEvidence) Validate() error {
	e = e.canonical()
	if strings.TrimSpace(e.ImpactStatement) == "" || strings.TrimSpace(e.PhysicalWorkStatement) == "" || strings.TrimSpace(e.ReuseStatement) == "" {
		return fmt.Errorf("%w: impact, physical-work, and reuse statements are required", ErrDeliveryInvalid)
	}
	for _, group := range [][]DeliveryImpactResource{e.GraphImpact.Added, e.GraphImpact.Removed, e.GraphImpact.DirectlyModified, e.GraphImpact.IndirectlyAffected} {
		for _, item := range group {
			if err := ValidateDeliveryID(item.ID); err != nil {
				return fmt.Errorf("impact resource: %w", err)
			}
			if item.Kind == "" || item.Change == "" {
				return fmt.Errorf("%w: impact resource kind and change are required", ErrDeliveryInvalid)
			}
		}
	}
	for _, item := range e.Reuse {
		if err := ValidateDeliveryID(item.ResourceID); err != nil {
			return fmt.Errorf("reuse resource: %w", err)
		}
		if strings.TrimSpace(item.Reason) == "" {
			return fmt.Errorf("%w: reuse reason is required", ErrDeliveryInvalid)
		}
		if err := validateOptionalDigest("reuse key", item.ReuseKeyDigest); err != nil {
			return err
		}
	}
	for i := 1; i < len(e.Reuse); i++ {
		if e.Reuse[i-1].ResourceID == e.Reuse[i].ResourceID {
			return fmt.Errorf("%w: duplicate reuse resource %q", ErrDeliveryInvalid, e.Reuse[i].ResourceID)
		}
	}
	for _, step := range e.Qualification.Steps {
		if err := ValidateDeliveryID(step.ID); err != nil {
			return fmt.Errorf("qualification step: %w", err)
		}
		if strings.TrimSpace(step.Kind) == "" || strings.TrimSpace(step.Description) == "" {
			return fmt.Errorf("%w: qualification step is incomplete", ErrDeliveryInvalid)
		}
	}
	if e.Qualification.Policy == "" || len(e.Qualification.Steps) == 0 {
		return fmt.Errorf("%w: qualification policy is required", ErrDeliveryInvalid)
	}
	if e.StalePolicy.Mode != "reject" && e.StalePolicy.Mode != "allow_retained_base" {
		return fmt.Errorf("%w: stale policy mode %q is unsupported", ErrDeliveryInvalid, e.StalePolicy.Mode)
	}
	switch e.Rollback.Class {
	case DeliveryRollbackSafe, DeliveryServingSafe, DeliveryNonReversible:
	default:
		return fmt.Errorf("%w: unsupported rollback class %q", ErrDeliveryInvalid, e.Rollback.Class)
	}
	if e.Restatement != nil {
		if e.Restatement.Strategy == "" {
			return fmt.Errorf("%w: restatement strategy is required", ErrDeliveryInvalid)
		}
		if e.Restatement.IdempotencyKey != "" {
			if err := ValidateDeliveryID(e.Restatement.IdempotencyKey); err != nil {
				return fmt.Errorf("restatement idempotency key: %w", err)
			}
		}
	}
	return nil
}

func (e DeliveryPlanEvidence) Digest() (string, error) {
	e = e.canonical()
	if err := e.Validate(); err != nil {
		return "", err
	}
	return canonicalJSONDigest(e)
}

// DeliveryResolvedDataInput records what a build actually read and the
// constraint it enforced. Planning declarations never masquerade as actual
// pinned values.
type DeliveryResolvedDataInput struct {
	ID                string                `json:"id"`
	Mode              DeliveryDataInputMode `json:"mode"`
	PlannedRevision   string                `json:"plannedRevision,omitempty"`
	PlannedBound      string                `json:"plannedBound,omitempty"`
	ActualRevision    string                `json:"actualRevision,omitempty"`
	ActualBound       string                `json:"actualBound,omitempty"`
	ObservationDigest string                `json:"observationDigest,omitempty"`
	Explanation       string                `json:"explanation"`
}

type DeliveryResolvedBuildInputs struct {
	Inputs         []DeliveryResolvedDataInput `json:"inputs,omitempty"`
	PolicyDigest   string                      `json:"policyDigest"`
	EvidenceDigest string                      `json:"evidenceDigest,omitempty"`
	// GateEvidence is required for every v1 resolved-input record. No-gate
	// projects persist canonical success evidence with empty components.
	GateEvidence *release.GateEvidence `json:"gateEvidence,omitempty"`
}

func (inputs DeliveryResolvedBuildInputs) canonical() DeliveryResolvedBuildInputs {
	inputs.Inputs = append([]DeliveryResolvedDataInput(nil), inputs.Inputs...)
	sort.Slice(inputs.Inputs, func(i, j int) bool { return inputs.Inputs[i].ID < inputs.Inputs[j].ID })
	if inputs.GateEvidence != nil {
		canonical, err := inputs.GateEvidence.Canonical()
		if err == nil {
			inputs.GateEvidence = &canonical
		}
	}
	return inputs
}

func (inputs DeliveryResolvedBuildInputs) Validate() error {
	if inputs.PolicyDigest != "" {
		if err := ValidateDeliveryDigest(inputs.PolicyDigest); err != nil {
			return fmt.Errorf("resolved input policy: %w", err)
		}
	}
	if inputs.GateEvidence == nil {
		return fmt.Errorf("%w: resolved gate evidence is required", ErrDeliveryInvalid)
	}
	canonical, err := inputs.GateEvidence.Canonical()
	if err != nil {
		return fmt.Errorf("resolved gate evidence: %w", err)
	}
	if canonical.Outcome != release.GateSuccess && canonical.Outcome != release.GateWarning {
		return fmt.Errorf("%w: resolved gate evidence outcome %q cannot qualify", ErrDeliveryConflict, canonical.Outcome)
	}
	if canonical.Digest != inputs.GateEvidence.Digest {
		return fmt.Errorf("%w: resolved gate evidence is not canonical", ErrDeliveryConflict)
	}
	seen := map[string]bool{}
	for _, input := range inputs.Inputs {
		if err := ValidateDeliveryID(input.ID); err != nil {
			return err
		}
		if seen[input.ID] {
			return fmt.Errorf("%w: duplicate resolved data input %q", ErrDeliveryInvalid, input.ID)
		}
		seen[input.ID] = true
		if input.Explanation == "" {
			return fmt.Errorf("%w: resolved input %q lacks explanation", ErrDeliveryInvalid, input.ID)
		}
		switch input.Mode {
		case DeliveryDataPinned:
			if input.ActualRevision == "" || input.ActualRevision != input.PlannedRevision {
				return fmt.Errorf("%w: pinned input %q did not resolve its planned revision", ErrDeliveryInvalid, input.ID)
			}
		case DeliveryDataBounded:
			if input.ActualBound == "" || input.ActualBound != input.PlannedBound {
				return fmt.Errorf("%w: bounded input %q did not enforce its planned bound", ErrDeliveryInvalid, input.ID)
			}
		case DeliveryDataObserved:
			if err := ValidateDeliveryDigest(input.ObservationDigest); err != nil {
				return fmt.Errorf("observed input %q: %w", input.ID, err)
			}
		default:
			return fmt.Errorf("%w: unsupported resolved input mode %q", ErrDeliveryInvalid, input.Mode)
		}
	}
	if inputs.EvidenceDigest != "" {
		canonical := inputs
		canonical.EvidenceDigest = ""
		expected, err := canonical.Digest()
		if err != nil || expected != inputs.EvidenceDigest {
			return fmt.Errorf("%w: resolved input evidence digest does not match canonical inputs", ErrDeliveryConflict)
		}
	}
	return nil
}

func (inputs DeliveryResolvedBuildInputs) Digest() (string, error) {
	canonical := inputs.canonical()
	canonical.EvidenceDigest = ""
	if err := canonical.Validate(); err != nil {
		return "", err
	}
	return canonicalJSONDigest(canonical)
}

func NewDeliveryResolvedBuildInputs(inputs DeliveryResolvedBuildInputs) (DeliveryResolvedBuildInputs, error) {
	inputs = inputs.canonical()
	digest, err := inputs.Digest()
	if err != nil {
		return DeliveryResolvedBuildInputs{}, err
	}
	inputs.EvidenceDigest = digest
	return inputs, nil
}

// ValidateDeliveryResolvedBuildInputs binds build-time observations to the
// exact data-input declarations in a plan. Every planned input must resolve
// once, no undeclared input may appear, and the planned mode/constraint cannot
// be rewritten by a runner. A plan with no data inputs still receives a
// canonical empty evidence digest so ready candidates never carry ambiguous
// zero-value evidence.
func ValidateDeliveryResolvedBuildInputs(plan DeliveryPlan, inputs DeliveryResolvedBuildInputs) (DeliveryResolvedBuildInputs, error) {
	if inputs.GateEvidence == nil {
		return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved gate evidence is required", ErrDeliveryInvalid)
	}
	canonical, gateErr := inputs.GateEvidence.Canonical()
	if gateErr != nil {
		return DeliveryResolvedBuildInputs{}, gateErr
	}
	if canonical.Outcome != release.GateSuccess && canonical.Outcome != release.GateWarning {
		return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved gate evidence outcome %q cannot qualify", ErrDeliveryConflict, canonical.Outcome)
	}
	if canonical.SourceDigest != plan.SourceDigest {
		return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved gate evidence source digest does not match plan", ErrDeliveryConflict)
	}
	if canonical.Digest != inputs.GateEvidence.Digest {
		return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved gate evidence digest does not match canonical evidence", ErrDeliveryConflict)
	}
	resolved, err := NewDeliveryResolvedBuildInputs(inputs)
	if err != nil {
		return DeliveryResolvedBuildInputs{}, err
	}
	if resolved.PolicyDigest != plan.Governance.PolicyDigest {
		return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved-input policy digest does not match planned governance policy", ErrDeliveryConflict)
	}
	planned := make(map[string]DeliveryDataInput, len(plan.Execution.DataInputs))
	for _, declaration := range plan.Execution.DataInputs {
		declaration = declaration.canonical()
		if _, exists := planned[declaration.ID]; exists {
			return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: duplicate planned data input %q", ErrDeliveryConflict, declaration.ID)
		}
		planned[declaration.ID] = declaration
	}
	if len(resolved.Inputs) != len(planned) {
		return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved data inputs count %d does not match plan count %d", ErrDeliveryConflict, len(resolved.Inputs), len(planned))
	}
	for _, actual := range resolved.Inputs {
		expected, ok := planned[actual.ID]
		if !ok {
			return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved undeclared data input %q", ErrDeliveryConflict, actual.ID)
		}
		if actual.Mode != expected.Mode || actual.PlannedRevision != expected.Revision || actual.PlannedBound != expected.Bound {
			return DeliveryResolvedBuildInputs{}, fmt.Errorf("%w: resolved data input %q changed its planned mode or constraint", ErrDeliveryConflict, actual.ID)
		}
	}
	return resolved, nil
}
