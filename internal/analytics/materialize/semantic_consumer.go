package materialize

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func (r *Runtime) protectedSemanticModel() bool {
	if r == nil {
		return false
	}
	if r.planner != nil && r.planner.CompiledModel() != nil && r.planner.CompiledModel().SemanticAccessPolicy().Protected() {
		return true
	}
	return semanticquery.ModelRequiresSemanticAccess(r.model)
}

func (r *Runtime) admitSemanticConsumer(ctx context.Context, request dataquery.Query) (*Runtime, error) {
	if !r.protectedSemanticModel() {
		return r, nil
	}
	if r.closed.Load() || r.servingStateID == "" || r.semanticAccessAuthority == nil || r.planner == nil {
		return nil, fmt.Errorf("semantic consumer authority is unavailable")
	}
	if r.semanticAudit == nil || r.semanticAudit.Recorder == nil || r.semanticAudit.ActorFromContext == nil {
		return nil, fmt.Errorf("semantic audit authority is unavailable")
	}
	if strings.TrimSpace(r.semanticAudit.InstanceID) == "" {
		return nil, fmt.Errorf("semantic audit instance identity is unavailable")
	}
	if err := r.semanticAudit.Identity.Validate(); err != nil ||
		r.semanticAudit.Identity.GenerationID != r.servingStateID ||
		r.semanticAudit.Identity.ProjectID != r.resultPartition.ProjectID() ||
		r.semanticAudit.Identity.Environment != r.resultPartition.Environment() {
		return nil, fmt.Errorf("semantic audit serving identity is unavailable")
	}
	if r.semanticCache != nil && r.semanticAudit.InstanceID != strings.TrimSpace(r.semanticCache.Binding.InstanceID) {
		return nil, fmt.Errorf("semantic audit instance identity is unavailable")
	}
	if request.Kind == dataquery.KindModelTableRows {
		return nil, fmt.Errorf("physical authoring preview cannot use a protected semantic consumer")
	}
	if r.planner.CompiledModel() == nil || !r.planner.CompiledModel().MatchesModel(r.model) {
		return nil, fmt.Errorf("semantic consumer execution snapshot is stale")
	}
	resolved, err := r.semanticAccessAuthority.ResolveSemanticAttributes(ctx)
	if err != nil {
		return nil, fmt.Errorf("semantic consumer access denied")
	}
	if resolved.Subject.Kind != access.SubjectKindPrincipal || resolved.Subject.ID == "" ||
		(request.PrincipalID != "" && request.PrincipalID != resolved.Subject.ID) {
		return nil, fmt.Errorf("semantic consumer caller does not match authenticated authority")
	}
	actorPrincipalID, err := r.semanticAudit.ActorFromContext(ctx)
	if err != nil || strings.TrimSpace(actorPrincipalID) == "" || actorPrincipalID != resolved.Subject.ID {
		return nil, fmt.Errorf("semantic audit actor does not match authenticated authority")
	}
	resource, err := access.NewResourceRef(projectgraph.ResourceID(r.modelID), projectgraph.KindSemanticModel)
	if err != nil {
		return nil, fmt.Errorf("semantic audit model identity is unavailable")
	}
	modelDigest, err := semanticquery.SemanticModelDigest(r.model)
	if err != nil {
		return nil, fmt.Errorf("semantic audit model digest is unavailable")
	}
	auditObserver, err := semanticquery.NewSemanticAuditObserver(ctx, r.semanticAudit.Recorder, semanticquery.SemanticAuditBinding{
		Event: access.CanonicalAuditEvent{
			Identity: r.semanticAudit.Identity, PrincipalID: resolved.Subject.ID,
			Action: access.SemanticDecisionAuditAction, Resource: resource,
			Capability: access.CapabilityResourceUse, RequestID: request.RequestID,
			CorrelationID: request.CorrelationID,
		},
		InstanceID: r.semanticAudit.InstanceID, ActorPrincipalID: actorPrincipalID,
		SemanticModelDigest: modelDigest,
	}, resolved)
	if err != nil {
		return nil, fmt.Errorf("semantic audit authority is unavailable")
	}
	consumer, err := semanticquery.NewSemanticAccessConsumer(r.planner, semanticquery.SemanticAccessEvaluationContext{
		RegistryState: resolved.Registry.State, ControlState: resolved.ControlState, Attributes: resolved.Attributes,
	}, resolved.Subject.ID, r.servingStateID, auditObserver)
	if err != nil {
		return nil, fmt.Errorf("semantic consumer access denied")
	}
	if err := validateSemanticAuthorizationProjection(request, consumer); err != nil {
		return nil, fmt.Errorf("semantic consumer access denied: %w", err)
	}
	resolved.Attributes = append([]access.EffectiveSemanticAttribute(nil), resolved.Attributes...)
	for i := range resolved.Attributes {
		resolved.Attributes[i].CanonicalValues = append([]string(nil), resolved.Attributes[i].CanonicalValues...)
	}
	// This is a borrowed, request-local view, not another runtime or cache
	// authority. Do not copy closeOnce/atomic ownership from the activation.
	return &Runtime{
		modelID: r.modelID, model: r.model, planner: consumer.Planner(), db: r.db,
		sources: r.sources, queryCache: r.queryCache, resultPartition: r.resultPartition,
		resultLimits: r.resultLimits, dependencyEvidence: r.dependencyEvidence,
		requiredExtensions: r.requiredExtensions, snapshotOnly: r.snapshotOnly,
		semanticAccessAuthority: r.semanticAccessAuthority, servingStateID: r.servingStateID,
		semanticAudit:    cloneSemanticAuditConfig(r.semanticAudit),
		semanticConsumer: consumer, semanticResolution: resolved, activation: r,
		semanticCache: cloneSemanticCacheConfig(r.semanticCache),
	}, nil
}

func (r *Runtime) validateSemanticResolution(ctx context.Context) error {
	if !r.protectedSemanticModel() {
		return nil
	}
	if r.semanticConsumer == nil || r.semanticAccessAuthority == nil || r.activation == nil || r.closed.Load() || r.activation.closed.Load() {
		return fmt.Errorf("semantic consumer execution snapshot is unavailable")
	}
	if r.planner == nil || r.planner.CompiledModel() == nil || !r.planner.CompiledModel().MatchesModel(r.model) {
		return fmt.Errorf("semantic consumer execution snapshot is stale")
	}
	current, err := r.semanticAccessAuthority.ResolveSemanticAttributes(ctx)
	if err != nil || current.Subject != r.semanticResolution.Subject ||
		current.Registry.State != r.semanticResolution.Registry.State || current.ControlState != r.semanticResolution.ControlState ||
		!reflect.DeepEqual(current.Attributes, r.semanticResolution.Attributes) {
		return fmt.Errorf("semantic consumer control snapshot is stale")
	}
	if err := r.validateSemanticCacheLifecycle(ctx); err != nil {
		return err
	}
	return nil
}

func (r *Runtime) validateSemanticPlan(ctx context.Context, plan semanticquery.Plan) error {
	if !r.protectedSemanticModel() {
		return nil
	}
	if err := r.validateSemanticResolution(ctx); err != nil {
		return err
	}
	return r.semanticConsumer.ValidatePlan(plan)
}

// Guard each externally delivered batch, not only completion: an Arrow
// transport cannot retract records after an authorization failure.
type semanticConsumerSink struct {
	ctx     context.Context
	runtime *Runtime
	sink    arrowquery.Sink
}

type semanticConsumerStatsSink struct {
	semanticConsumerSink
	stats arrowquery.SinkStats
}

func (s semanticConsumerStatsSink) RowsWritten() int { return s.stats.RowsWritten() }

func (s semanticConsumerSink) WriteSchema(schema *arrow.Schema) error {
	if err := s.runtime.validateSemanticResolution(s.ctx); err != nil {
		return err
	}
	return s.sink.WriteSchema(schema)
}

func (s semanticConsumerSink) WriteRecord(record arrow.RecordBatch) error {
	if err := s.runtime.validateSemanticResolution(s.ctx); err != nil {
		return err
	}
	return s.sink.WriteRecord(record)
}
