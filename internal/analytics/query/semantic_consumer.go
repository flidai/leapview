package query

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/analytics/query/planir"
)

// SemanticAccessConsumer is the request-bound execution boundary for a
// protected semantic model. It owns one detached evaluation context and one
// private in-process capability. The capability is attached to PlanIR only
// after the request planner has completed the existing security placement.
//
// PrincipalID and GenerationIdentity are retained as consumer provenance for
// callers that construct downstream result/audit identities. They do not
// alter the FAI-639 evaluator, whose compiled policy remains the sole access
// decision authority.
type SemanticAccessConsumer struct {
	planner            *Planner
	context            SemanticAccessEvaluationContext
	principalID        string
	generationIdentity string
	capability         *semanticAccessConsumerCapability
}

// semanticAccessConsumerCapability is intentionally non-zero-sized so each
// allocation is a distinct pointer even on runtimes that coalesce zero-size
// allocations. Its value is never exposed or serialized.
type semanticAccessConsumerCapability struct {
	private byte
}

// NewSemanticAccessConsumer binds a protected consumer to an activation
// planner, detached runtime attributes, an authenticated principal, and the
// existing serving-generation identity. The planner's compiled model and
// table-relation option are retained; no model or policy is recompiled.
func NewSemanticAccessConsumer(planner *Planner, context SemanticAccessEvaluationContext, principalID, generationIdentity string) (*SemanticAccessConsumer, error) {
	if planner == nil || planner.compiled == nil {
		return nil, fmt.Errorf("compiled semantic planner is required")
	}
	policy := planner.compiled.semanticAccess
	if policy == nil || !policy.Protected() {
		return nil, fmt.Errorf("protected semantic access policy is required")
	}
	principalID = strings.TrimSpace(principalID)
	if principalID == "" {
		return nil, fmt.Errorf("authenticated principal ID is required")
	}
	generationIdentity = strings.TrimSpace(generationIdentity)
	if generationIdentity == "" {
		return nil, fmt.Errorf("serving generation identity is required")
	}
	detached := cloneSemanticAccessEvaluationContext(context)
	if _, reason := policy.runtimeAttributes(detached); reason != "" {
		return nil, fmt.Errorf("semantic access context is invalid: %s", reason)
	}

	requestPlanner, err := newConsumerPlanner(planner, detached)
	if err != nil {
		return nil, err
	}
	capability := &semanticAccessConsumerCapability{private: 1}
	requestPlanner.semanticAccessConsumerToken = capability
	return &SemanticAccessConsumer{
		planner: requestPlanner, context: detached,
		principalID: principalID, generationIdentity: generationIdentity,
		capability: capability,
	}, nil
}

// ModelRequiresSemanticAccess reports whether the authored model contains a
// protected semantic-access policy. It is the activation selection predicate;
// models without policy retain the ordinary planner path.
func ModelRequiresSemanticAccess(model *semanticmodel.Model) bool {
	return semanticAccessPolicyPresent(model)
}

// Protected reports whether an activation-compiled policy is protected. A nil
// policy and an ordinary context-aware compile both return false.
func (policy *CompiledSemanticAccessPolicy) Protected() bool {
	return policy != nil && policy.protected
}

// PrincipalID returns the authenticated principal bound to this consumer.
func (consumer *SemanticAccessConsumer) PrincipalID() string {
	if consumer == nil {
		return ""
	}
	return consumer.principalID
}

// GenerationIdentity returns the existing serving-generation identity bound
// to this consumer.
func (consumer *SemanticAccessConsumer) GenerationIdentity() string {
	if consumer == nil {
		return ""
	}
	return consumer.generationIdentity
}

// Planner returns a fresh request-bound planner copy. The copy preserves the
// activation compiled graph, table-relation resolver, detached access
// context, and private consumer capability. A fresh copy prevents a caller
// from sharing mutable context backing arrays across requests.
func (consumer *SemanticAccessConsumer) Planner() *Planner {
	if consumer == nil || consumer.planner == nil {
		return nil
	}
	planner, err := newConsumerPlanner(consumer.planner, consumer.context)
	if err != nil {
		return nil
	}
	planner.semanticAccessConsumerToken = consumer.capability
	return planner
}

// Allows is the fail-closed discovery projection of the existing compiled
// semantic-access evaluator. It never probes data and cannot widen a query.
func (consumer *SemanticAccessConsumer) Allows(target SemanticAccessTarget) bool {
	if consumer == nil || consumer.planner == nil || consumer.planner.compiled == nil {
		return false
	}
	policy := consumer.planner.compiled.semanticAccess
	return policy != nil && policy.Protected() && policy.Allows(target, consumer.context)
}

// ValidatePlan admits only a plan produced by this consumer's request-bound
// planner. It validates the existing private admission capability, the
// protected PlanIR topology and the exact renderer envelope supplied alongside
// the graph. The private capability proves that policy evaluation and barrier
// placement ran in this request-bound planner. Callers must invoke this
// boundary immediately before physical execution.
func (consumer *SemanticAccessConsumer) ValidatePlan(plan Plan) error {
	if consumer == nil || consumer.planner == nil || consumer.capability == nil {
		return fmt.Errorf("semantic access consumer is required")
	}
	policy := consumer.planner.compiled.semanticAccess
	if policy == nil || !policy.Protected() {
		return fmt.Errorf("protected semantic access policy is required")
	}
	if _, reason := policy.runtimeAttributes(consumer.context); reason != "" {
		return fmt.Errorf("semantic access context is invalid: %s", reason)
	}
	if plan.IR == nil {
		return fmt.Errorf("protected plan has no PlanIR")
	}
	if !planir.CheckSecurityAdmission(plan.IR, consumer.capability) {
		return fmt.Errorf("plan was not admitted by this semantic access consumer")
	}
	if err := plan.IR.Validate(); err != nil {
		return fmt.Errorf("validate protected PlanIR: %w", err)
	}
	if err := consumer.validateProtectedPlan(plan.IR); err != nil {
		return err
	}
	rendered, err := planir.RenderDuckDB(plan.IR)
	if err != nil {
		return fmt.Errorf("render protected PlanIR: %w", err)
	}
	if plan.SQL != rendered.SQL {
		return fmt.Errorf("plan SQL does not match its PlanIR renderer output")
	}
	if !reflect.DeepEqual(plan.Args, rendered.Args) {
		return fmt.Errorf("plan arguments do not match its PlanIR renderer output")
	}
	if !reflect.DeepEqual(plan.Columns, rendered.Columns) {
		return fmt.Errorf("plan columns do not match its PlanIR renderer output")
	}
	return nil
}

func newConsumerPlanner(source *Planner, context SemanticAccessEvaluationContext) (*Planner, error) {
	if source == nil || source.compiled == nil {
		return nil, fmt.Errorf("compiled semantic planner is required")
	}
	options := []PlannerOption{}
	if relation := source.TableRelation(); relation != nil {
		options = append(options, WithTableRelation(relation))
	}
	return NewSemanticAccessPlanner(source.compiled, cloneSemanticAccessEvaluationContext(context), options...)
}

func cloneSemanticAccessEvaluationContext(context SemanticAccessEvaluationContext) SemanticAccessEvaluationContext {
	clone := context
	if context.Attributes == nil {
		return clone
	}
	clone.Attributes = make([]access.EffectiveSemanticAttribute, len(context.Attributes))
	for index, attribute := range context.Attributes {
		clone.Attributes[index] = accessEffectiveSemanticAttribute(attribute)
	}
	return clone
}

func accessEffectiveSemanticAttribute(attribute access.EffectiveSemanticAttribute) access.EffectiveSemanticAttribute {
	attribute.CanonicalValues = append([]string(nil), attribute.CanonicalValues...)
	return attribute
}

func (consumer *SemanticAccessConsumer) validateProtectedPlan(graph *planir.Graph) error {
	if err := consumer.validatePlanSources(graph); err != nil {
		return err
	}
	return nil
}

func (consumer *SemanticAccessConsumer) validatePlanSources(graph *planir.Graph) error {
	scans := map[string]planir.ScanDataset{}
	scanConsumers := map[string]int{}
	barriers := map[string]planir.SecurityBarrier{}
	for id, node := range graph.Nodes {
		for _, input := range node.Inputs() {
			scanConsumers[input]++
		}
		switch value := node.(type) {
		case planir.ScanDataset:
			scans[id] = value
		case *planir.ScanDataset:
			if value != nil {
				scans[id] = *value
			}
		case planir.SecurityBarrier:
			barriers[id] = value
		case *planir.SecurityBarrier:
			if value != nil {
				barriers[id] = *value
			}
		case planir.TraverseRelationship:
			if value.TargetInput == "" {
				return fmt.Errorf("protected relationship %q has no admitted target", id)
			}
		case *planir.TraverseRelationship:
			if value == nil || value.TargetInput == "" {
				return fmt.Errorf("protected relationship %q has no admitted target", id)
			}
		}
	}
	if len(scans) == 0 {
		return fmt.Errorf("protected plan has no dataset scan")
	}
	for id, scan := range scans {
		if scan.Dataset == "" {
			return fmt.Errorf("protected scan %q has no dataset", id)
		}
		if _, ok := consumer.planner.compiled.Dataset(scan.Dataset); !ok {
			return fmt.Errorf("protected scan %q references unknown dataset %q", id, scan.Dataset)
		}
		if scanConsumers[id] != 1 {
			return fmt.Errorf("protected scan %q does not have exactly one barrier consumer", id)
		}
		_, ok := barrierForScan(barriers, id)
		if !ok {
			return fmt.Errorf("protected scan %q has no security barrier", id)
		}
	}
	for id, barrier := range barriers {
		if _, ok := scans[barrier.Input]; !ok {
			return fmt.Errorf("security barrier %q does not protect a dataset scan", id)
		}
	}
	return nil
}

func barrierForScan(barriers map[string]planir.SecurityBarrier, scanID string) (planir.SecurityBarrier, bool) {
	var result planir.SecurityBarrier
	found := false
	for _, barrier := range barriers {
		if barrier.Input != scanID {
			continue
		}
		if found {
			return planir.SecurityBarrier{}, false
		}
		result, found = barrier, true
	}
	return result, found
}
