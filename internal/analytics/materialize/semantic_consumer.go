package materialize

import (
	"context"
	"fmt"
	"strings"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type semanticProtectionState struct {
	protected bool
	known     bool
}

// semanticProtectionState reports the policy boundary from the activation
// planner's detached source snapshot. A missing or malformed snapshot is not
// treated as a public model: cache and execution callers must fail closed
// rather than allowing an unknown model to enter an unqualified path.
func (r *Runtime) semanticProtectionState() semanticProtectionState {
	if r == nil || r.model == nil || r.planner == nil {
		return semanticProtectionState{}
	}
	compiled := r.planner.CompiledModel()
	if compiled == nil {
		return semanticProtectionState{}
	}
	model := compiled.SourceModel()
	if model == nil {
		return semanticProtectionState{}
	}
	return semanticProtectionState{protected: !model.AccessPolicy.Empty(), known: true}
}

// protectedSemanticModel reports the policy boundary from the activation
// planner's detached source snapshot. It is intentionally independent of the
// request: callers cannot make a protected model public by omitting fields.
// Use semanticProtectionState when an unknown snapshot must be rejected.
func (r *Runtime) protectedSemanticModel() bool {
	return r.semanticProtectionState().protected
}

func (r *Runtime) requireSemanticProtectionState() (semanticProtectionState, error) {
	state := r.semanticProtectionState()
	if !state.known {
		return state, fmt.Errorf("semantic access model snapshot is unavailable")
	}
	return state, nil
}

type semanticConsumerBinder interface {
	BindSemanticConsumer(context.Context, string) (context.Context, error)
}

// bindSemanticConsumerContext lets an execution adapter recover the
// request-bound capability when a governor owns the composition boundary but
// has not explicitly attached it yet. The returned context must replace the
// caller's context for the complete operation; creating a consumer here and
// dropping the returned context would leave the later plan validation without
// the same capability.
func (r *Runtime) bindSemanticConsumerContext(ctx context.Context, request dataquery.Query) (context.Context, error) {
	if !r.protectedSemanticModel() || request.Kind == dataquery.KindModelRows {
		return ctx, nil
	}
	if _, ok := semanticquery.SemanticAccessConsumerContextFromContext(ctx); ok {
		return ctx, nil
	}
	governor, ok := dataquery.GovernorFromContext(ctx)
	if !ok {
		return ctx, fmt.Errorf("protected semantic execution requires a request-bound consumer")
	}
	binder, ok := governor.(semanticConsumerBinder)
	if !ok {
		return ctx, fmt.Errorf("protected semantic execution governor cannot bind a consumer")
	}
	bound, err := binder.BindSemanticConsumer(ctx, request.ModelID)
	if err != nil {
		return ctx, fmt.Errorf("bind protected semantic consumer: %w", err)
	}
	if _, ok := semanticquery.SemanticAccessConsumerContextFromContext(bound); !ok {
		return ctx, fmt.Errorf("protected semantic consumer binding is invalid")
	}
	return bound, nil
}

// semanticPlannerForRequest selects the request-bound consumer planner for a
// protected semantic model. The activation planner is retained for public
// models only; protected execution without an exact consumer capability fails
// closed before planning or cache access.
func (r *Runtime) semanticPlannerForRequest(ctx context.Context, request dataquery.Query) (*semanticquery.Planner, *semanticquery.SemanticAccessConsumer, error) {
	planner, err := r.queryPlanner()
	if err != nil {
		return nil, nil, err
	}
	state, err := r.requireSemanticProtectionState()
	if err != nil {
		return nil, nil, err
	}
	if !state.protected {
		return planner, nil, nil
	}
	if request.Kind == dataquery.KindModelRows {
		return nil, nil, fmt.Errorf("physical authoring preview cannot use a protected semantic consumer")
	}
	bound, ok := semanticquery.SemanticAccessConsumerContextFromContext(ctx)
	if !ok {
		return nil, nil, fmt.Errorf("protected semantic execution requires a request-bound consumer")
	}
	if err := bound.Validate(); err != nil {
		return nil, nil, fmt.Errorf("protected semantic execution context: %w", err)
	}
	binding := bound.Binding
	instanceID := strings.TrimSpace(r.resultPartition.TargetID())
	if instanceID == "" || binding.InstanceID != instanceID {
		return nil, nil, fmt.Errorf("semantic access consumer instance %q does not match runtime instance %q", binding.InstanceID, instanceID)
	}
	if strings.TrimSpace(r.servingStateID) == "" || binding.Generation != r.servingStateID {
		return nil, nil, fmt.Errorf("semantic access consumer generation %q does not match runtime generation %q", binding.Generation, r.servingStateID)
	}
	if request.ModelID != "" && request.ModelID != binding.ModelID {
		return nil, nil, fmt.Errorf("semantic access consumer model %q does not match request model %q", binding.ModelID, request.ModelID)
	}
	if strings.TrimSpace(r.modelID) == "" || binding.ModelID != r.modelID {
		return nil, nil, fmt.Errorf("semantic access consumer model %q does not match runtime model %q", binding.ModelID, r.modelID)
	}
	if request.PrincipalID != "" && request.PrincipalID != binding.PrincipalID {
		return nil, nil, fmt.Errorf("semantic access consumer principal does not match request")
	}
	projectID := strings.TrimSpace(r.resultPartition.ProjectID().String())
	if projectID == "" || binding.ProjectID != projectID {
		return nil, nil, fmt.Errorf("semantic access consumer project %q does not match runtime project %q", binding.ProjectID, projectID)
	}
	environment := strings.TrimSpace(r.resultPartition.Environment())
	if environment == "" || binding.Environment != environment {
		return nil, nil, fmt.Errorf("semantic access consumer environment %q does not match runtime environment %q", binding.Environment, environment)
	}
	consumerPlanner := bound.Consumer.Planner()
	if consumerPlanner == nil || consumerPlanner.CompiledModel() == nil || !consumerPlanner.CompiledModel().MatchesModel(r.model) {
		return nil, nil, fmt.Errorf("semantic access consumer planner is stale")
	}
	return consumerPlanner, bound.Consumer, nil
}

func (r *Runtime) validateSemanticPlan(ctx context.Context, request dataquery.Query, plan semanticquery.Plan) error {
	if !r.protectedSemanticModel() {
		return nil
	}
	_, consumer, err := r.semanticPlannerForRequest(ctx, request)
	if err != nil {
		return err
	}
	return consumer.ValidatePlan(plan)
}

// validateSemanticAuthorizationProjection proves the fields used to
// authorize a count-only table through the same request-bound planner. The
// count SQL intentionally omits those fields, so admitting only PlanCount
// would otherwise make a denied projection invisible to execution.
func validateSemanticAuthorizationProjection(request dataquery.Query, consumer *semanticquery.SemanticAccessConsumer) error {
	if consumer == nil || request.Kind != dataquery.KindSemanticRows || !request.IncludeTotal || len(request.Fields) != 0 || len(request.Metrics) != 0 || len(request.AuthorizationFields) == 0 {
		return nil
	}
	planner := consumer.Planner()
	if planner == nil || planner.CompiledModel() == nil {
		return fmt.Errorf("semantic authorization projection planner is unavailable")
	}
	dimensions, metrics, err := semanticAuthorizationProjectionFields(planner, request)
	if err != nil {
		return err
	}
	projection, err := planner.Plan(semanticquery.Request{
		Dataset: request.Target, Dimensions: dimensions, Metrics: metrics,
		Filters: dataQueryFilters(request.Filters),
	})
	if err != nil {
		return fmt.Errorf("authorize semantic count projection: %w", err)
	}
	if err := consumer.ValidatePlan(projection); err != nil {
		return fmt.Errorf("validate semantic count projection: %w", err)
	}
	return nil
}

// semanticAuthorizationProjectionFields preserves the member kind that was
// present in the original logical projection. Count-only requests carry no
// physical fields, so reconstructing this projection from names alone is
// unsafe when a model has a dimension and metric with the same name.
//
// Empty kinds are accepted for backwards compatibility only when the name is
// unambiguous. Ambiguous legacy references fail closed instead of silently
// selecting the metric namespace.
func semanticAuthorizationProjectionFields(planner *semanticquery.Planner, request dataquery.Query) ([]semanticquery.Field, []semanticquery.Field, error) {
	if planner == nil || planner.CompiledModel() == nil {
		return nil, nil, fmt.Errorf("semantic authorization projection planner is unavailable")
	}
	compiled := planner.CompiledModel()
	dimensions := make([]semanticquery.Field, 0, len(request.AuthorizationFields))
	metrics := make([]semanticquery.Field, 0, len(request.AuthorizationFields))
	for _, field := range request.AuthorizationFields {
		name := strings.TrimSpace(field.Field)
		if name == "" {
			return nil, nil, fmt.Errorf("semantic authorization projection contains an empty field")
		}
		memberKind := strings.ToLower(strings.TrimSpace(field.Kind))
		switch memberKind {
		case dataquery.FieldKindMetric:
			metrics = append(metrics, semanticquery.Field{Field: field.Field, Alias: field.Alias})
			continue
		case dataquery.FieldKindDimension:
			dimensions = append(dimensions, semanticquery.Field{Field: field.Field, Alias: field.Alias})
			continue
		case "":
			_, metricKnown := compiled.Metric(name)
			_, dimensionKnown := compiled.SemanticDimension(name)
			if !dimensionKnown {
				_, dimensionKnown = compiled.PhysicalField(name)
			}
			if metricKnown && dimensionKnown {
				return nil, nil, fmt.Errorf("semantic authorization projection field %q is ambiguous between metric and dimension", name)
			}
			if metricKnown {
				metrics = append(metrics, semanticquery.Field{Field: field.Field, Alias: field.Alias})
				continue
			}
			// Qualified physical fields and legacy semantic dimensions are
			// resolved by the planner's dimension path validation below.
			dimensions = append(dimensions, semanticquery.Field{Field: field.Field, Alias: field.Alias})
			continue
		default:
			return nil, nil, fmt.Errorf("semantic authorization projection field %q has unsupported kind %q", name, field.Kind)
		}
	}
	return dimensions, metrics, nil
}

// semanticConsumerSink rechecks the exact admitted plan before each Arrow
// delivery. Once a schema or batch has been released it cannot be retracted,
// so authority changes during a stream must stop before the next write.
type semanticConsumerSink struct {
	consumer *semanticquery.SemanticAccessConsumer
	plan     semanticquery.Plan
	sink     arrowquery.Sink
}

type semanticConsumerStatsSink struct {
	semanticConsumerSink
	stats arrowquery.SinkStats
}

func (s semanticConsumerStatsSink) RowsWritten() int { return s.stats.RowsWritten() }

func (s semanticConsumerSink) WriteSchema(schema *arrow.Schema) error {
	if err := s.consumer.ValidatePlan(s.plan); err != nil {
		return err
	}
	return s.sink.WriteSchema(schema)
}

func (s semanticConsumerSink) WriteRecord(record arrow.RecordBatch) error {
	if err := s.consumer.ValidatePlan(s.plan); err != nil {
		return err
	}
	return s.sink.WriteRecord(record)
}
