package query

import (
	"fmt"
	"sort"
	"strings"

	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// SemanticAccessAuditOperation names the request-bound authorization
// boundaries. These values are intentionally stable: callers may use them as
// durable audit action names without depending on an error string or a plan
// renderer detail.
const (
	SemanticAccessAuditDiscovery        = "semantic_access.discovery"
	SemanticAccessAuditAuthorization    = "semantic_access.authorization"
	SemanticAccessAuditPlanAdmission    = "semantic_access.plan_admission"
	SemanticAccessAuditPlanValidation   = "semantic_access.plan_validation"
	SemanticAccessAuditPlanInvalidation = "semantic_access.plan_invalidation"
	SemanticAccessAuditConsumerBind     = "semantic_access.consumer_bind"
)

const maxSemanticAccessAuditEvidenceBytes = 64 << 10

// SemanticAccessAuditObservation is a redacted observation of one consumer
// boundary. EvidenceJSON is the deterministic decision identity projection;
// it contains digests and object outcomes, never canonical attribute values,
// claim payloads, predicates, or SQL.
type SemanticAccessAuditObservation struct {
	Operation      string
	Target         SemanticAccessTarget
	Allowed        bool
	Reason         string
	PrincipalID    string
	ActorID        string
	PolicyDigest   string
	DecisionDigest string
	EvidenceJSON   []byte
	// DecisionAvailable is false only for composition failures where no
	// authority decision was captured. Such observations intentionally carry no
	// evidence or digests; consumers must not fabricate them. A stale decision
	// may still be available as pinned evidence for an invalidation event.
	DecisionAvailable bool
	// Datasets is the sorted, bounded dependency set for a plan observation.
	// Target remains the typed primary target for compatibility.
	Datasets []string
}

// SemanticAccessObserver persists a required authorization observation. An
// observer error is returned to the caller before a grant, executable plan,
// discovery projection, or validated output is released.
type SemanticAccessObserver func(SemanticAccessAuditObservation) error

var (
	semanticAccessAuditReasonAuthorityUnavailable = "authority_unavailable"
	semanticAccessAuditReasonAuthorityChanged     = "authority_changed"
	semanticAccessAuditReasonTargetInvalid        = "target_invalid"
	semanticAccessAuditReasonTargetUnknown        = "target_unknown"
	semanticAccessAuditReasonAccessDenied         = "access_denied"
	semanticAccessAuditReasonPlanNotAdmitted      = "plan_not_admitted"
	semanticAccessAuditReasonPlanChanged          = "plan_changed"
	semanticAccessAuditReasonAuthorizationFailed  = "authorization_failed"
	semanticAccessAuditReasonDecisionUnavailable  = "decision_unavailable"
)

func semanticAccessAuditReason(err error) string {
	if err == nil {
		return ""
	}
	// The error is used only to select a closed, bounded reason. It is never
	// copied into an observation or durable metadata.
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "not admitted") {
		return semanticAccessAuditReasonPlanNotAdmitted
	}
	if strings.Contains(message, "stale") || strings.Contains(message, "changed") || strings.Contains(message, "inconsistent") {
		return semanticAccessAuditReasonAuthorityChanged
	}
	if strings.Contains(message, "unknown") {
		return semanticAccessAuditReasonTargetUnknown
	}
	if strings.Contains(message, "denied") {
		return semanticAccessAuditReasonAccessDenied
	}
	if strings.Contains(message, "authority") || strings.Contains(message, "unavailable") || strings.Contains(message, "required") {
		return semanticAccessAuditReasonAuthorityUnavailable
	}
	return semanticAccessAuditReasonAuthorizationFailed
}

func semanticAccessAuditReasonForTarget(err error) string {
	if err == nil {
		return ""
	}
	if strings.Contains(strings.ToLower(err.Error()), "target") {
		if strings.Contains(strings.ToLower(err.Error()), "unknown") {
			return semanticAccessAuditReasonTargetUnknown
		}
		return semanticAccessAuditReasonTargetInvalid
	}
	return semanticAccessAuditReason(err)
}

func semanticAccessPlanTarget(graph *planir.Graph) SemanticAccessTarget {
	if graph == nil {
		return SemanticAccessTarget{}
	}
	dependencies, err := graph.Dependencies()
	if err != nil || len(dependencies.Datasets) == 0 {
		return SemanticAccessTarget{}
	}
	return SemanticAccessTarget{Dataset: dependencies.Datasets[0]}
}

func semanticAccessPlanDatasets(graph *planir.Graph) []string {
	if graph == nil {
		return nil
	}
	dependencies, err := graph.Dependencies()
	if err != nil || len(dependencies.Datasets) == 0 {
		return nil
	}
	datasets := append([]string(nil), dependencies.Datasets...)
	sort.Strings(datasets)
	return datasets
}

func (consumer *SemanticAccessConsumer) observeSemanticAccess(observation SemanticAccessAuditObservation) error {
	if consumer == nil || !consumer.protected || consumer.config.Observer == nil {
		return nil
	}
	if observation.Operation == "" {
		return fmt.Errorf("semantic access audit operation is required")
	}
	if len(observation.EvidenceJSON) > maxSemanticAccessAuditEvidenceBytes {
		return fmt.Errorf("semantic access audit evidence exceeds bounded size")
	}
	consumer.mu.RLock()
	if observation.PrincipalID == "" {
		observation.PrincipalID = consumer.config.PrincipalID
	}
	if observation.PolicyDigest == "" {
		observation.PolicyDigest = consumer.auditPolicyDigest
	}
	if observation.DecisionDigest == "" {
		observation.DecisionDigest = consumer.auditDecisionDigest
	}
	if observation.ActorID == "" {
		observation.ActorID = consumer.auditActorID
	}
	if len(observation.EvidenceJSON) == 0 {
		observation.EvidenceJSON = append([]byte(nil), consumer.auditEvidenceJSON...)
	}
	if len(observation.EvidenceJSON) != 0 {
		observation.DecisionAvailable = true
	}
	if len(observation.Datasets) != 0 {
		observation.Datasets = append([]string(nil), observation.Datasets...)
		sort.Strings(observation.Datasets)
	}
	consumer.mu.RUnlock()
	if observation.ActorID == "" {
		observation.ActorID = observation.PrincipalID
	}
	if len(observation.EvidenceJSON) > maxSemanticAccessAuditEvidenceBytes {
		return fmt.Errorf("semantic access audit evidence exceeds bounded size")
	}
	if consumer.config.Observer == nil {
		return nil
	}
	if err := consumer.config.Observer(observation); err != nil {
		return fmt.Errorf("semantic access audit: %w", err)
	}
	return nil
}

func (consumer *SemanticAccessConsumer) setSemanticAccessAuditDecision(policy *CompiledSemanticAccessPolicy, decision *SemanticAccessDecision, evidence []byte) {
	if consumer == nil {
		return
	}
	consumer.mu.Lock()
	if policy != nil {
		consumer.auditPolicyDigest = policy.Digest()
	}
	if decision != nil {
		consumer.auditDecisionDigest = decision.IdentityDigest
		if decision.ActorID != "" {
			consumer.auditActorID = decision.ActorID
		}
	}
	if len(evidence) != 0 {
		consumer.auditEvidenceJSON = append([]byte(nil), evidence...)
	}
	consumer.mu.Unlock()
}

func (consumer *SemanticAccessConsumer) observeSemanticAccessFailure(operation string, target SemanticAccessTarget, err error) error {
	if err == nil {
		return nil
	}
	return consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: operation, Target: target, Allowed: false, Reason: semanticAccessAuditReason(err)})
}

// observeSemanticAccessPlanFailure is attached to the planner's existing
// admission entrypoint. It records failures that occur after authority has
// been obtained (member admission, graph construction, or barrier placement)
// without changing planner authorization semantics.
func (consumer *SemanticAccessConsumer) observeSemanticAccessPlanFailure(graph *planir.Graph, err error) error {
	if err == nil {
		return nil
	}
	operation := SemanticAccessAuditPlanAdmission
	reason := semanticAccessAuditReason(err)
	if reason == semanticAccessAuditReasonAuthorityChanged {
		operation = SemanticAccessAuditPlanInvalidation
	}
	return consumer.observeSemanticAccess(SemanticAccessAuditObservation{
		Operation: operation, Target: semanticAccessPlanTarget(graph), Datasets: semanticAccessPlanDatasets(graph),
		Allowed: false, Reason: reason,
	})
}

func (consumer *SemanticAccessConsumer) observeSemanticAccessTargetFailure(operation string, target SemanticAccessTarget, err error) error {
	if err == nil {
		return nil
	}
	return consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: operation, Target: target, Allowed: false, Reason: semanticAccessAuditReasonForTarget(err)})
}

func (consumer *SemanticAccessConsumer) observeSemanticAccessAllowed(operation string, target SemanticAccessTarget) error {
	return consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: operation, Target: target, Allowed: true})
}

// Assets returns a stable, detached, authorization-filtered asset projection.
// Protected discovery is released only after the required observer accepts
// every returned asset observation.
func (consumer *SemanticAccessConsumer) Assets() ([]SemanticAccessAsset, error) {
	assets, denied, err := consumer.assets()
	if err != nil {
		if auditErr := consumer.observeSemanticAccessFailure(SemanticAccessAuditDiscovery, SemanticAccessTarget{}, err); auditErr != nil {
			return nil, auditErr
		}
		return nil, err
	}
	for _, asset := range assets {
		target := SemanticAccessTarget{Dataset: asset.Dataset, Dimension: asset.Dimension, Metric: asset.Metric}
		if err := consumer.observeSemanticAccessAllowed(SemanticAccessAuditDiscovery, target); err != nil {
			return nil, err
		}
	}
	for _, target := range denied {
		if err := consumer.observeSemanticAccess(SemanticAccessAuditObservation{
			Operation: SemanticAccessAuditDiscovery, Target: target, Allowed: false,
			Reason: semanticAccessAuditReasonAccessDenied,
		}); err != nil {
			return nil, err
		}
	}
	return assets, nil
}

// Authorize applies the same typed FAI-639 decision used by discovery and
// planner admission. Malformed combinations and unknown state fail closed.
func (consumer *SemanticAccessConsumer) Authorize(target SemanticAccessTarget) error {
	canonical, err := canonicalSemanticAccessTarget(target)
	if err != nil {
		if auditErr := consumer.observeSemanticAccessTargetFailure(SemanticAccessAuditAuthorization, SemanticAccessTarget{}, err); auditErr != nil {
			return auditErr
		}
		return err
	}
	err = consumer.authorizeTarget(canonical)
	if err != nil {
		if auditErr := consumer.observeSemanticAccessTargetFailure(SemanticAccessAuditAuthorization, canonical, err); auditErr != nil {
			return auditErr
		}
		return err
	}
	if auditErr := consumer.observeSemanticAccessAllowed(SemanticAccessAuditAuthorization, canonical); auditErr != nil {
		return auditErr
	}
	return nil
}

// ValidatePlan proves origin, unchanged graph identity, current authority,
// and exact renderer SQL/arguments/columns immediately before execution.
func (consumer *SemanticAccessConsumer) ValidatePlan(plan Plan) error {
	err := consumer.validatePlan(plan)
	if err != nil {
		reason := semanticAccessAuditReason(err)
		if semanticAccessAuditErrorIsPlanChange(err) {
			reason = semanticAccessAuditReasonPlanChanged
		}
		target := SemanticAccessTarget{}
		var datasets []string
		if plan.IR != nil {
			target = semanticAccessPlanTarget(plan.IR)
			datasets = semanticAccessPlanDatasets(plan.IR)
		}
		if auditErr := consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: SemanticAccessAuditPlanInvalidation, Target: target, Datasets: datasets, Allowed: false, Reason: reason}); auditErr != nil {
			return auditErr
		}
		return err
	}
	if err := consumer.observeSemanticAccess(SemanticAccessAuditObservation{Operation: SemanticAccessAuditPlanValidation, Target: semanticAccessPlanTarget(plan.IR), Datasets: semanticAccessPlanDatasets(plan.IR), Allowed: true}); err != nil {
		return err
	}
	return nil
}

func semanticAccessAuditEvidence(decision *SemanticAccessDecision) ([]byte, error) {
	if decision == nil {
		return nil, fmt.Errorf("semantic access decision is required for audit evidence")
	}
	evidence, err := semanticAccessDecisionIdentity(decision)
	if err != nil {
		return nil, fmt.Errorf("semantic access audit evidence: %w", err)
	}
	return evidence, nil
}

func semanticAccessAuditErrorIsPlanChange(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "graph changed") || strings.Contains(message, "does not match") || strings.Contains(message, "not admitted")
}
