package http

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/query/planir"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticConsumerPlannerContextKey struct{}

func TestSemanticConsumerDiscoveryRejectsRemovedAuthoredPolicy(t *testing.T) {
	fixture := newSemanticConsumerPlannerFixture(t)
	metrics := newSemanticConsumerAPIMetrics(fixture, fixture.allowed)
	fixture.model.AccessGrants = nil
	for name, dataset := range fixture.model.Datasets {
		dataset.RequiredAccessGrants = nil
		dataset.AccessFilters = nil
		fixture.model.Datasets[name] = dataset
	}
	handler := Handler{Metrics: metrics}
	if _, err := handler.semanticTargetAuthorizerForModel("sales", fixture.model); err == nil {
		t.Fatal("compiled protected discovery accepted a stripped authored policy")
	}
}

type semanticConsumerPlannerFixture struct {
	model          *semanticmodel.Model
	registry       access.SemanticAttributeRegistrySnapshot
	allowedContext semanticquery.SemanticAccessEvaluationContext
	deniedContext  semanticquery.SemanticAccessEvaluationContext
	activation     *semanticquery.Planner
	allowed        *semanticquery.SemanticAccessConsumer
	denied         *semanticquery.SemanticAccessConsumer
}

func newSemanticConsumerPlannerFixture(t *testing.T) semanticConsumerPlannerFixture {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "definition-region", Name: "region", Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile,
		DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State: access.SemanticAttributeRegistryState{
			Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:registry",
		},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	model := semanticConsumerAPIModel()
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatalf("compile protected model: %v", err)
	}
	activation, err := semanticquery.NewSemanticAccessPlanner(
		compiled,
		semanticquery.SemanticAccessEvaluationContext{},
		semanticquery.WithTableRelation(func(table string) (string, error) { return "model." + table, nil }),
	)
	if err != nil {
		t.Fatalf("construct activation planner: %v", err)
	}
	allowed := semanticConsumerAPIContext(t, registry, definition, "us")
	denied := semanticConsumerAPIContext(t, registry, definition, "eu")
	allowedConsumer, err := semanticquery.NewSemanticAccessConsumer(activation, allowed, "principal-1", "state-1")
	if err != nil {
		t.Fatalf("construct allowed consumer: %v", err)
	}
	deniedConsumer, err := semanticquery.NewSemanticAccessConsumer(activation, denied, "principal-1", "state-1")
	if err != nil {
		t.Fatalf("construct denied consumer: %v", err)
	}
	return semanticConsumerPlannerFixture{
		model: model, registry: registry, allowedContext: allowed, deniedContext: denied,
		activation: activation, allowed: allowedConsumer, denied: deniedConsumer,
	}
}

func semanticConsumerAPIModel() *semanticmodel.Model {
	return &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"order_id": {Type: "integer", Datatype: semanticmodel.DataTypeInteger},
					"region":   {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
				GrainEntity: "order",
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {
				Model: "orders", RequiredAccessGrants: []string{"region_grant"},
				AccessFilters: []semanticmodel.SemanticAccessFilterSpec{{Field: "region", UserAttribute: "region"}},
			},
		},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"region": {
				Type: "string", Datatype: semanticmodel.DataTypeString,
				Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.region"}},
			},
		},
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}, Empty: "zero"},
		},
	}
}

func semanticConsumerAPIContext(t *testing.T, registry access.SemanticAttributeRegistrySnapshot, definition access.SemanticAttributeDefinition, value string) semanticquery.SemanticAccessEvaluationContext {
	t.Helper()
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, value)
	if err != nil {
		t.Fatalf("canonical semantic attribute %q: %v", value, err)
	}
	return semanticquery.SemanticAccessEvaluationContext{
		RegistryState: registry.State,
		ControlState: access.SemanticAttributeControlState{
			Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:control",
		},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
}

type semanticConsumerAPIMetrics struct {
	semanticTargetMetrics
	consumer       *semanticquery.SemanticAccessConsumer
	plannerErr     error
	plannerCalls   int
	plannerContext context.Context
	executeCalls   int
}

func (m *semanticConsumerAPIMetrics) SemanticPlanner(ctx context.Context, _ string) (*semanticquery.Planner, error) {
	m.plannerCalls++
	m.plannerContext = ctx
	if m.plannerErr != nil {
		return nil, m.plannerErr
	}
	if m.consumer == nil {
		return nil, errors.New("consumer is unavailable")
	}
	return m.consumer.Planner(), nil
}

func (m *semanticConsumerAPIMetrics) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	m.executeCalls++
	return dataquery.Result{Rows: []dataquery.Row{{"order_count": int64(1)}}}, nil
}

func newSemanticConsumerAPIMetrics(fixture semanticConsumerPlannerFixture, consumer *semanticquery.SemanticAccessConsumer) *semanticConsumerAPIMetrics {
	return &semanticConsumerAPIMetrics{
		semanticTargetMetrics: semanticTargetMetrics{
			semanticProjectionMetrics: semanticProjectionMetrics{
				model:   fixture.model,
				catalog: dashboard.Catalog{Models: []dashboard.CatalogModel{{ID: "sales"}}},
				planner: fixture.activation, plannerOkay: true,
			},
			// Keep the API query authorization stage permissive; planner
			// evaluation is the behavior under test here.
			authorizeTarget: func(context.Context, string, semanticquery.SemanticAccessTarget) error { return nil },
		},
		consumer: consumer,
	}
}

func semanticConsumerAPIRequest(t *testing.T, modelID string, body any) *http.Request {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	request := semanticModelRequest(modelID)
	request.Method = http.MethodPost
	request.Body = io.NopCloser(bytes.NewReader(payload))
	request.ContentLength = int64(len(payload))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "request-1")
	return request
}

func semanticConsumerAPIQueryInput() api.SemanticQueryRequest {
	return api.SemanticQueryRequest{Metrics: []api.SemanticFieldRef{{Field: "order_count"}}, Limit: 10}
}

func semanticConsumerAPIRowInput() api.SemanticPreviewRequest {
	return api.SemanticPreviewRequest{Dimensions: []api.SemanticFieldRef{{Field: "region"}}, Limit: 10}
}

func TestSemanticExplainUsesRequestBoundProtectedConsumerPlanner(t *testing.T) {
	fixture := newSemanticConsumerPlannerFixture(t)
	metrics := newSemanticConsumerAPIMetrics(fixture, fixture.allowed)
	ctx := context.WithValue(context.Background(), semanticConsumerPlannerContextKey{}, "request-context")

	aggregate, err := semanticExplainAggregate(ctx, metrics, "sales", reportAggregateQueryForConsumerTest())
	if err != nil {
		t.Fatalf("protected aggregate explain: %v", err)
	}
	if metrics.plannerCalls != 1 || metrics.plannerContext.Value(semanticConsumerPlannerContextKey{}) != "request-context" {
		t.Fatalf("semantic planner calls/context = %d/%v", metrics.plannerCalls, metrics.plannerContext)
	}
	if _, err := fixture.activation.Plan(semanticquery.Request{Metrics: []semanticquery.Field{{Field: "order_count"}}}); err == nil {
		t.Fatal("neutral activation planner unexpectedly planned a protected query")
	}
	if !planHasSecurityBarrier(aggregate) {
		t.Fatal("protected explain plan has no security barrier")
	}

	rows, err := semanticExplainRows(ctx, metrics, "sales", reportRowQueryForConsumerTest())
	if err != nil {
		t.Fatalf("protected rows explain: %v", err)
	}
	if metrics.plannerCalls != 2 || !planHasSecurityBarrier(rows) {
		t.Fatalf("protected rows planner calls/barrier = %d/%v", metrics.plannerCalls, planHasSecurityBarrier(rows))
	}
}

func TestSemanticExplainProtectedModelFailsWhenConsumerPlannerPortMissing(t *testing.T) {
	fixture := newSemanticConsumerPlannerFixture(t)
	metrics := fixture.semanticTargetMetrics()
	if _, err := semanticExplainAggregate(context.Background(), metrics, "sales", reportAggregateQueryForConsumerTest()); !errors.Is(err, errSemanticAuthorizationUnavailable) {
		t.Fatalf("missing protected consumer planner error = %v, want authorization unavailable", err)
	}
}

func TestSemanticAPIProtectedQueryAndExplainUseConsumerAndRedactAttributeValues(t *testing.T) {
	fixture := newSemanticConsumerPlannerFixture(t)
	metrics := newSemanticConsumerAPIMetrics(fixture, fixture.allowed)
	handler := semanticModelHandler(metrics)

	explainRecorder := httptest.NewRecorder()
	handler.ExplainSemanticModelQuery(explainRecorder, semanticConsumerAPIRequest(t, "sales", semanticConsumerAPIQueryInput()))
	if explainRecorder.Code != http.StatusOK {
		t.Fatalf("protected explain status = %d, body = %s", explainRecorder.Code, explainRecorder.Body.String())
	}
	var explain api.SemanticExplainResponse
	if err := json.Unmarshal(explainRecorder.Body.Bytes(), &explain); err != nil {
		t.Fatal(err)
	}
	if len(explain.Args) == 0 {
		t.Fatal("protected explain omitted the policy argument entirely")
	}
	for _, arg := range explain.Args {
		if _, present := arg["value"]; present {
			t.Fatalf("protected explain exposed effective attribute value: %#v", arg)
		}
		if arg["redacted"] != true {
			t.Fatalf("protected explain arg is not marked redacted: %#v", arg)
		}
	}

	queryRecorder := httptest.NewRecorder()
	handler.QuerySemanticModel(queryRecorder, semanticConsumerAPIRequest(t, "sales", semanticConsumerAPIQueryInput()))
	if queryRecorder.Code != http.StatusOK {
		t.Fatalf("protected query status = %d, body = %s", queryRecorder.Code, queryRecorder.Body.String())
	}
	if metrics.executeCalls != 1 {
		t.Fatalf("protected query execution calls = %d, want 1", metrics.executeCalls)
	}
}

func TestSemanticAPIProtectedQueryAndExplainDenyConsumerPlanner(t *testing.T) {
	fixture := newSemanticConsumerPlannerFixture(t)
	metrics := newSemanticConsumerAPIMetrics(fixture, fixture.denied)
	handler := semanticModelHandler(metrics)

	explainRecorder := httptest.NewRecorder()
	handler.ExplainSemanticModelQuery(explainRecorder, semanticConsumerAPIRequest(t, "sales", semanticConsumerAPIQueryInput()))
	if explainRecorder.Code != http.StatusBadRequest {
		t.Fatalf("denied protected explain status = %d, body = %s", explainRecorder.Code, explainRecorder.Body.String())
	}
	queryRecorder := httptest.NewRecorder()
	handler.QuerySemanticModel(queryRecorder, semanticConsumerAPIRequest(t, "sales", semanticConsumerAPIQueryInput()))
	if queryRecorder.Code != http.StatusBadRequest {
		t.Fatalf("denied protected query status = %d, body = %s", queryRecorder.Code, queryRecorder.Body.String())
	}
	if metrics.executeCalls != 0 {
		t.Fatalf("denied protected query execution calls = %d, want 0", metrics.executeCalls)
	}
}

func TestSemanticExplainResponseKeepsUnprotectedArguments(t *testing.T) {
	plan := semanticquery.Plan{SQL: "SELECT ?", Args: []any{"ordinary-value"}, Columns: []string{"value"}}
	response := semanticExplainResponse("query", plan, nil)
	if len(response.Args) != 1 || response.Args[0]["value"] != "ordinary-value" {
		t.Fatalf("unprotected explain args = %#v, want visible value", response.Args)
	}
}

func reportAggregateQueryForConsumerTest() reportdef.AggregateQuery {
	return reportdef.AggregateQuery{Dataset: "orders", Metrics: []reportdef.QueryField{{Field: "order_count"}}, Limit: 10}
}

func reportRowQueryForConsumerTest() reportdef.RowQuery {
	return reportdef.RowQuery{Dataset: "orders", Dimensions: []reportdef.QueryField{{Field: "region"}}, Limit: 10}
}

func planHasSecurityBarrier(plan semanticquery.Plan) bool {
	if plan.IR == nil {
		return false
	}
	for _, node := range plan.IR.Nodes {
		if node.Kind() == planir.KindSecurityBarrier {
			return true
		}
	}
	return false
}

func (m *semanticConsumerPlannerFixture) semanticTargetMetrics() Metrics {
	return semanticTargetMetrics{
		semanticProjectionMetrics: semanticProjectionMetrics{
			model: m.model, planner: m.activation, plannerOkay: true,
		},
		authorizeTarget: func(context.Context, string, semanticquery.SemanticAccessTarget) error { return nil },
	}
}
