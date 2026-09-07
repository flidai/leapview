package http

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	"github.com/flidai/leapview/internal/dashboard/resolver"
	"github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
)

type semanticAPIAuditRecorder struct {
	events           []access.CanonicalAuditEvent
	err              error
	semanticAttempts int
	semanticSuccess  int
}

const (
	semanticAPIAuditPrincipal     = "11111111-1111-4111-8111-111111111111"
	semanticAPIAuditRequestID     = "22222222-2222-4222-8222-222222222222"
	semanticAPIAuditCorrelationID = "33333333-3333-4333-8333-333333333333"
)

func (r *semanticAPIAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	if event.Action == access.SemanticDecisionAuditAction {
		r.semanticAttempts++
		if r.err != nil {
			return r.err
		}
		r.semanticSuccess++
	}
	r.events = append(r.events, event)
	return nil
}

type semanticAPIAuditRuntime struct {
	queryruntime.Metrics
	model                   *semanticmodel.Model
	planner                 *semanticquery.Planner
	catalog                 dashboard.Catalog
	executeCall             int
	recorder                *semanticAPIAuditRecorder
	semanticEventsAtExecute int
}

func (m *semanticAPIAuditRuntime) Catalog() dashboard.Catalog { return m.catalog }

func (m *semanticAPIAuditRuntime) SemanticModel(string) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil
}

func (m *semanticAPIAuditRuntime) Planner(string) (consumer.Planner, bool) {
	return m.planner, m.planner != nil
}

func (m *semanticAPIAuditRuntime) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	m.executeCall++
	if m.recorder != nil {
		m.semanticEventsAtExecute = m.recorder.semanticSuccess
	}
	return dataquery.Result{Rows: []dataquery.Row{{"order_count": int64(1)}}}, nil
}

func (m *semanticAPIAuditRuntime) ExecuteConsumersPage(context.Context, consumer.Request, consumer.Publisher) error {
	return errors.New("consumer execution is not part of this qualification fixture")
}

func (m *semanticAPIAuditRuntime) DefaultDashboardID() string { return "" }

func (m *semanticAPIAuditRuntime) ModelIDForDashboard(string) string { return "" }

func (m *semanticAPIAuditRuntime) DefaultFilters(string) dashboard.Filters {
	return dashboard.Filters{}
}

func (m *semanticAPIAuditRuntime) NormalizeVisualizationWindow(string, dashboard.TableRequest) dashboard.TableRequest {
	return dashboard.TableRequest{}
}

func (m *semanticAPIAuditRuntime) QueryDashboard(context.Context, string, dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.Patch{}, nil
}

func (m *semanticAPIAuditRuntime) QueryDashboardPage(context.Context, string, string, dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.Patch{}, nil
}

func (m *semanticAPIAuditRuntime) QueryDashboardVisualizations(context.Context, string, string, dashboard.Filters) (dashboard.Patch, error) {
	return dashboard.Patch{}, nil
}

func (m *semanticAPIAuditRuntime) QueryVisualization(context.Context, string, string, dashboard.Filters, string) (ir.VisualizationEnvelope, error) {
	return ir.VisualizationEnvelope{}, nil
}

func (m *semanticAPIAuditRuntime) QueryVisualizationWindow(context.Context, string, string, dashboard.Filters, ir.VisualizationWindowRequest) (ir.VisualizationEnvelope, error) {
	return ir.VisualizationEnvelope{}, nil
}

func (m *semanticAPIAuditRuntime) QuerySemantic(context.Context, string, reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return nil, nil
}

func (m *semanticAPIAuditRuntime) PreviewSemantic(context.Context, string, reportdef.RowQuery) (reportdef.QueryRows, error) {
	return nil, nil
}

func (m *semanticAPIAuditRuntime) Pages(string) []dashboard.Page { return nil }

func (m *semanticAPIAuditRuntime) Resolver() resolver.Resolver { return nil }

func newSemanticAPIAuditMetrics(t *testing.T, value string, recorder *semanticAPIAuditRecorder) (*queryauthz.Metrics, *semanticAPIAuditRuntime) {
	t.Helper()
	model := semanticConsumerAPIModel()
	definition := access.SemanticAttributeDefinition{
		ID: "definition-region", Name: "region", Type: semanticvalue.TypeString,
		Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1,
		LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State: access.SemanticAttributeRegistryState{
			Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:" + strings.Repeat("a", 64),
		},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatalf("compile semantic model: %v", err)
	}
	planner, err := semanticquery.NewSemanticAccessPlanner(compiled, semanticquery.SemanticAccessEvaluationContext{}, semanticquery.WithTableRelation(func(table string) (string, error) {
		return "model." + table, nil
	}))
	if err != nil {
		t.Fatalf("construct semantic planner: %v", err)
	}
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, value)
	if err != nil {
		t.Fatalf("canonical attribute value: %v", err)
	}
	resolutionSubject, err := access.NewSubjectRef(access.SubjectKindPrincipal, semanticAPIAuditPrincipal)
	if err != nil {
		t.Fatalf("subject: %v", err)
	}
	resolution := access.SemanticAttributeResolution{
		Subject:  resolutionSubject,
		Registry: registry,
		ControlState: access.SemanticAttributeControlState{
			Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:" + strings.Repeat("b", 64),
		},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
	graph, identity := semanticAPIAuditGraph(t)
	semanticResource, err := access.NewResourceRef("semantic_sales", projectgraph.KindSemanticModel)
	if err != nil {
		t.Fatalf("semantic resource: %v", err)
	}
	snapshot, err := accesssnapshot.FromControlState(identity, graph, access.ControlState{
		InstanceID: "instance-api", ProjectID: graph.ProjectID().String(), Revision: 3,
		Grants: []access.ControlGrant{{
			ID: "semantic-use", InstanceID: "instance-api", ProjectID: graph.ProjectID().String(),
			Subject: resolutionSubject, Resource: semanticResource, Capability: access.CapabilityResourceUse,
			ReferenceLifecycle: access.ControlReferenceActive,
		}},
	}, nil)
	if err != nil {
		t.Fatalf("authorization snapshot: %v", err)
	}
	runtime := &semanticAPIAuditRuntime{
		model: model, planner: planner,
		recorder: recorder,
		catalog:  dashboard.Catalog{Project: dashboard.CatalogProject{ID: graph.ProjectID()}, Models: []dashboard.CatalogModel{{ID: "semantic_sales", Title: "Sales", Description: "Sales model"}}},
	}
	metrics := queryauthz.New(runtime, queryauthz.Options{
		ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) { return resolution, nil },
		SnapshotFromContext:       func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
		SubjectsFromContext: func(context.Context, string) ([]access.SubjectRef, error) {
			return []access.SubjectRef{resolutionSubject}, nil
		},
		PrincipalFromContext: func(context.Context) (queryauthz.Principal, bool) {
			return queryauthz.Principal{ID: semanticAPIAuditPrincipal}, true
		},
		AuditRecorder: recorder,
	})
	if got, ok := metrics.SemanticModel("semantic_sales"); !ok || got == nil {
		t.Fatalf("queryauthz metrics did not expose protected model: ok=%v model=%#v", ok, got)
	}
	return &metrics, runtime
}

func semanticAPIAuditGraph(t testing.TB) (projectgraph.ProjectGraph, projectgraph.ServingIdentity) {
	t.Helper()
	projectID := projectgraph.ResourceID("project:sales")
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: projectID, Kind: projectgraph.KindProject, Name: "sales_project"},
		{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatalf("project graph: %v", err)
	}
	identity, err := projectgraph.NewServingIdentity(projectID, "production", "generation-1")
	if err != nil {
		t.Fatalf("serving identity: %v", err)
	}
	return graph, identity
}

func semanticAPIAuditHandler(metrics Metrics) Handler {
	return Handler{
		Metrics:            metrics,
		ResolveProjectID:   func(context.Context) (projectgraph.ResourceID, error) { return "project:sales", nil },
		CurrentPrincipalID: func(*http.Request) string { return semanticAPIAuditPrincipal },
		AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
	}
}

func semanticAPIAuditRequest(t *testing.T, modelID string, body any) *http.Request {
	t.Helper()
	request := semanticConsumerAPIRequest(t, modelID, body)
	request.Header.Set("X-Correlation-ID", semanticAPIAuditCorrelationID)
	request = request.WithContext(dataquery.WithMetadata(request.Context(), dataquery.Metadata{
		RequestID: semanticAPIAuditRequestID, CorrelationID: semanticAPIAuditCorrelationID, PrincipalID: semanticAPIAuditPrincipal,
	}))
	return request
}

func semanticAPIAuditEvents(t testing.TB, recorder *semanticAPIAuditRecorder) []access.CanonicalAuditEvent {
	t.Helper()
	var events []access.CanonicalAuditEvent
	for _, event := range recorder.events {
		if event.Action == access.SemanticDecisionAuditAction {
			events = append(events, event)
		}
	}
	return events
}

func assertSemanticAPIAuditEvidence(t testing.TB, event access.CanonicalAuditEvent, allowed bool, target func(access.SemanticAuditTarget) bool) {
	t.Helper()
	evidence, err := access.DecodeSemanticDecisionEvidence(event.MetadataJSON)
	if err != nil {
		t.Fatalf("decode semantic decision evidence: %v", err)
	}
	if event.Action != access.SemanticDecisionAuditAction || event.PrincipalID != semanticAPIAuditPrincipal || event.Resource.ID().String() != "semantic_sales" ||
		event.Identity.GenerationID != "generation-1" || event.RequestID != semanticAPIAuditRequestID || event.CorrelationID != semanticAPIAuditCorrelationID || evidence.Allowed != allowed || !target(evidence.Target) {
		t.Fatalf("semantic audit event binding = %#v / %#v", event, evidence)
	}
	if evidence.InstanceID == "" || evidence.ActorPrincipalID != semanticAPIAuditPrincipal || evidence.Registry.Revision != 7 || evidence.Control.Revision != 11 {
		t.Fatalf("semantic audit authority binding = %#v", evidence)
	}
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"canonicalValues", "allowedValues", "predicates", "Predicates", `"us"`, `"eu"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("semantic audit evidence leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestSemanticAPIAuditRecordsBoundMemberDiscoveryAndDeniedTargets(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		allowed bool
		items   int
	}{
		{name: "allowed", value: "us", allowed: true, items: 2},
		{name: "denied", value: "eu", allowed: false, items: 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &semanticAPIAuditRecorder{}
			metrics, _ := newSemanticAPIAuditMetrics(t, test.value, recorder)
			recorderResponse := httptest.NewRecorder()
			semanticAPIAuditHandler(metrics).ListSemanticModelFields(recorderResponse, semanticAPIAuditRequest(t, "semantic_sales", nil))
			if recorderResponse.Code != http.StatusOK {
				t.Fatalf("member discovery status = %d, body = %s", recorderResponse.Code, recorderResponse.Body.String())
			}
			var response api.SemanticFieldListResponse
			if err := json.Unmarshal(recorderResponse.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if len(response.Items) != test.items {
				t.Fatalf("member discovery items = %d, want %d", len(response.Items), test.items)
			}
			events := semanticAPIAuditEvents(t, recorder)
			if len(events) != 2 {
				t.Fatalf("semantic decision events = %d, want 2", len(events))
			}
			for _, event := range events {
				assertSemanticAPIAuditEvidence(t, event, test.allowed, func(target access.SemanticAuditTarget) bool {
					return target.Dataset == "orders" || target.Metric == "order_count"
				})
			}
		})
	}
}

func TestSemanticAPIAuditRecordsWholeModelProjection(t *testing.T) {
	recorder := &semanticAPIAuditRecorder{}
	metrics, _ := newSemanticAPIAuditMetrics(t, "us", recorder)
	response := httptest.NewRecorder()
	semanticAPIAuditHandler(metrics).GetSemanticModel(response, semanticAPIAuditRequest(t, "semantic_sales", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("whole-model status = %d, body = %s", response.Code, response.Body.String())
	}
	events := semanticAPIAuditEvents(t, recorder)
	if len(events) != 3 {
		t.Fatalf("whole-model semantic decision events = %d, want 3", len(events))
	}
	for _, event := range events {
		assertSemanticAPIAuditEvidence(t, event, true, func(target access.SemanticAuditTarget) bool {
			return target.Dataset == "orders" || target.Dimension == "region" || target.Metric == "order_count"
		})
	}
}

func TestSemanticAPIAuditRecordsExplainAndQueryBeforeOutput(t *testing.T) {
	for _, test := range []struct {
		name    string
		value   string
		status  int
		allowed bool
	}{
		{name: "allowed", value: "us", status: http.StatusOK, allowed: true},
		{name: "denied", value: "eu", status: http.StatusNotFound, allowed: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			explainRecorder := &semanticAPIAuditRecorder{}
			explainMetrics, _ := newSemanticAPIAuditMetrics(t, test.value, explainRecorder)
			explainHandler := semanticAPIAuditHandler(explainMetrics)
			explain := httptest.NewRecorder()
			explainHandler.ExplainSemanticModelQuery(explain, semanticAPIAuditRequest(t, "semantic_sales", semanticConsumerAPIQueryInput()))
			if explain.Code != test.status {
				t.Fatalf("explain status = %d, want %d, body = %s", explain.Code, test.status, explain.Body.String())
			}
			events := semanticAPIAuditEvents(t, explainRecorder)
			if len(events) == 0 {
				t.Fatal("explain produced no semantic decision event")
			}
			for _, event := range events {
				assertSemanticAPIAuditEvidence(t, event, test.allowed, func(target access.SemanticAuditTarget) bool {
					return target.Metric == "order_count" || target.Dataset == "orders"
				})
			}

			queryRecorder := &semanticAPIAuditRecorder{}
			queryMetrics, runtime := newSemanticAPIAuditMetrics(t, test.value, queryRecorder)
			queryHandler := semanticAPIAuditHandler(queryMetrics)
			query := httptest.NewRecorder()
			queryHandler.QuerySemanticModel(query, semanticAPIAuditRequest(t, "semantic_sales", semanticConsumerAPIQueryInput()))
			if query.Code != test.status {
				t.Fatalf("query status = %d, want %d, body = %s", query.Code, test.status, query.Body.String())
			}
			queryEvents := semanticAPIAuditEvents(t, queryRecorder)
			if len(queryEvents) == 0 {
				t.Fatal("query produced no semantic decision event")
			}
			for _, event := range queryEvents {
				assertSemanticAPIAuditEvidence(t, event, test.allowed, func(target access.SemanticAuditTarget) bool {
					return target.Metric == "order_count" || target.Dataset == "orders"
				})
			}
			if test.allowed {
				if runtime.executeCall != 1 || runtime.semanticEventsAtExecute == 0 || !strings.Contains(query.Body.String(), "order_count") {
					t.Fatalf("allowed query execution/output = %d/%s", runtime.executeCall, query.Body.String())
				}
			} else if runtime.executeCall != 0 {
				t.Fatalf("denied query execution calls = %d", runtime.executeCall)
			}
		})
	}
}

func TestSemanticAPIAuditWriteFailurePreventsGovernedOutputs(t *testing.T) {
	for _, test := range []struct {
		name       string
		status     int
		call       func(Handler, http.ResponseWriter, *http.Request)
		assertBody func(testing.TB, []byte)
	}{
		{name: "member discovery", status: http.StatusOK, call: func(handler Handler, writer http.ResponseWriter, request *http.Request) {
			handler.ListSemanticModelFields(writer, request)
		}, assertBody: func(t testing.TB, body []byte) {
			var response api.SemanticFieldListResponse
			if err := json.Unmarshal(body, &response); err != nil {
				t.Fatalf("decode empty member discovery response: %v", err)
			}
			if len(response.Items) != 0 || response.Page.NextCursor != "" {
				t.Fatalf("member discovery exposed fields after audit failure: %#v", response)
			}
		}},
		{name: "whole model", status: http.StatusNotFound, call: func(handler Handler, writer http.ResponseWriter, request *http.Request) {
			handler.GetSemanticModel(writer, request)
		}},
		{name: "explain", status: http.StatusNotFound, call: func(handler Handler, writer http.ResponseWriter, request *http.Request) {
			handler.ExplainSemanticModelQuery(writer, request)
		}},
		{name: "query", status: http.StatusNotFound, call: func(handler Handler, writer http.ResponseWriter, request *http.Request) {
			handler.QuerySemanticModel(writer, request)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &semanticAPIAuditRecorder{err: errors.New("private audit storage detail")}
			metrics, runtime := newSemanticAPIAuditMetrics(t, "us", recorder)
			response := httptest.NewRecorder()
			requestBody := any(nil)
			if test.name == "explain" || test.name == "query" {
				requestBody = semanticConsumerAPIQueryInput()
			}
			test.call(semanticAPIAuditHandler(metrics), response, semanticAPIAuditRequest(t, "semantic_sales", requestBody))
			if response.Code != test.status {
				t.Fatalf("write failure status = %d, want %d, body = %s", response.Code, test.status, response.Body.String())
			}
			if test.assertBody != nil {
				test.assertBody(t, response.Body.Bytes())
			}
			if recorder.semanticAttempts == 0 {
				t.Fatal("write failure did not reach semantic decision recorder")
			}
			if len(semanticAPIAuditEvents(t, recorder)) != 0 {
				t.Fatal("write failure retained a semantic decision event")
			}
			if runtime.executeCall != 0 || strings.Contains(response.Body.String(), "order_count") {
				t.Fatalf("write failure disclosed query output: calls=%d body=%s", runtime.executeCall, response.Body.String())
			}
			if strings.Contains(response.Body.String(), "private audit storage detail") {
				t.Fatal("write failure leaked recorder error")
			}
		})
	}
}

func TestSemanticAPIModelSummaryIsResourceMetadataOnly(t *testing.T) {
	recorder := &semanticAPIAuditRecorder{}
	metrics, _ := newSemanticAPIAuditMetrics(t, "eu", recorder)
	response := httptest.NewRecorder()
	semanticAPIAuditHandler(metrics).ListSemanticModels(response, semanticAPIAuditRequest(t, "semantic_sales", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("summary list status = %d, body = %s", response.Code, response.Body.String())
	}
	var list api.SemanticModelListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != "semantic_sales" || list.Items[0].Title != "Sales" || list.Items[0].Description != "Sales model" {
		t.Fatalf("summary list = %#v", list.Items)
	}
	if len(semanticAPIAuditEvents(t, recorder)) != 0 {
		t.Fatal("summary-only resource metadata unexpectedly emitted semantic member decisions")
	}
}

// Keep the compile-time contract visible in this qualification file. The
// queryauthz wrapper must remain assignable to the API's narrow surface.
var _ Metrics = (*queryauthz.Metrics)(nil)
