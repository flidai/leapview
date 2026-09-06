package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/semanticvalue"
	"github.com/go-chi/chi/v5"
)

type semanticMetadataAuthority struct {
	snapshot semanticquery.SemanticAccessAttributeSnapshot
	current  semanticquery.SemanticAccessAuthority
}

func (a *semanticMetadataAuthority) get() (semanticquery.SemanticAccessAttributeSnapshot, semanticquery.SemanticAccessAuthority, error) {
	return a.snapshot, a.current, nil
}

type semanticMetadataMetrics struct {
	semanticProjectionMetrics
	consumer *semanticquery.SemanticAccessConsumer
}

func (m *semanticMetadataMetrics) SemanticConsumer(context.Context, string) (*semanticquery.SemanticAccessConsumer, error) {
	return m.consumer, nil
}

func (m *semanticMetadataMetrics) SemanticModel(modelID string) (*semanticmodel.Model, bool) {
	if m.model == nil || modelID != m.model.Name {
		return nil, false
	}
	return m.model, true
}

func TestSemanticMetadataUsesRealConsumerToFilterMembersAndRejectStalePin(t *testing.T) {
	metrics, authority := semanticMetadataConsumerFixture(t)
	handler := Handler{
		Metrics:          metrics,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
	}

	datasetResponse := invokeSemanticMetadata(t, handler, false)
	if datasetResponse.Code != http.StatusOK {
		t.Fatalf("dataset metadata status = %d, body = %s", datasetResponse.Code, datasetResponse.Body.String())
	}
	var datasets api.SemanticDatasetListResponse
	if err := json.Unmarshal(datasetResponse.Body.Bytes(), &datasets); err != nil {
		t.Fatal(err)
	}
	if len(datasets.Items) != 0 {
		t.Fatalf("datasets = %#v, want whole-dataset metadata denied when a member is denied", datasets.Items)
	}
	detail := invokeSemanticDatasetDetail(t, handler)
	if detail.Code != http.StatusForbidden {
		t.Fatalf("dataset detail status = %d, body = %s", detail.Code, detail.Body.String())
	}

	fieldResponse := invokeSemanticMetadata(t, handler, true)
	if fieldResponse.Code != http.StatusOK {
		t.Fatalf("field metadata status = %d, body = %s", fieldResponse.Code, fieldResponse.Body.String())
	}
	var fields api.SemanticFieldListResponse
	if err := json.Unmarshal(fieldResponse.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields.Items) != 1 || fields.Items[0].Name != "allowed" {
		t.Fatalf("fields = %#v, want only the authorized member", fields.Items)
	}

	// The consumer captures the original decision digest. Changing the
	// authority behind that pinned consumer must fail the whole protected
	// metadata response instead of projecting a mixed decision.
	authority.snapshot.EffectiveAttributes = nil
	authority.snapshot.EffectiveAttributeDigest, _ = semanticquery.EffectiveSemanticAttributeDigest(nil)
	authority.snapshot.DirectAssignmentEvidence = access.SemanticAttributeDirectEvidence{}
	staleResponse := invokeSemanticMetadata(t, handler, true)
	if staleResponse.Code != http.StatusForbidden {
		t.Fatalf("stale field metadata status = %d, body = %s", staleResponse.Code, staleResponse.Body.String())
	}
}

func TestSemanticMetadataRejectsProtectedConsumerForAnotherModelID(t *testing.T) {
	metrics, _ := semanticMetadataConsumerFixture(t)
	if _, err := semanticConsumerForRequest(context.Background(), metrics, "other-model"); err == nil {
		t.Fatal("protected consumer composed for sales was accepted for another model ID")
	}
}

func TestSemanticMetadataDoesNotAuthorizeDimensionAsSameNamedMetric(t *testing.T) {
	metrics, authority := semanticMetadataConsumerFixture(t)
	metrics.model.Metrics = map[string]semanticmodel.Metric{"denied": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.allowed"}}}
	planner, err := semanticquery.NewCompiledPlanner(metrics.model)
	if err != nil {
		t.Fatal(err)
	}
	metrics.planner = planner
	metrics.consumer, err = semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "sales", Generation: "generation-1", PrincipalID: "alice", Authority: authority.get,
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := Handler{Metrics: metrics,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
	}
	response := invokeSemanticMetadata(t, handler, true)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var fields api.SemanticFieldListResponse
	if err := json.Unmarshal(response.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	foundMetric := false
	for _, field := range fields.Items {
		if field.Name != "denied" {
			continue
		}
		if field.Kind != "metric" {
			t.Fatalf("denied dimension inherited metric authorization: %#v", field)
		}
		foundMetric = true
	}
	if !foundMetric {
		t.Fatal("authorized same-named metric was omitted")
	}
}

func TestSemanticModelListHidesProtectedModelWithoutAuthorizedDataset(t *testing.T) {
	metrics, _ := semanticMetadataConsumerFixtureWithRole(t, false)
	metrics.catalog = dashboard.Catalog{Models: []dashboard.CatalogModel{{ID: "sales"}}}
	handler := Handler{
		Metrics:          metrics,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/semantic-models", nil)
	request.Header.Set("X-Serving-Snapshot", "generation-1")
	recorder := httptest.NewRecorder()
	handler.ListSemanticModels(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("model list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response api.SemanticModelListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 0 {
		t.Fatalf("protected model list = %#v, want denied model omitted", response.Items)
	}
}

func TestSemanticModelListHidesUnknownCatalogEntry(t *testing.T) {
	metrics, _ := semanticMetadataConsumerFixture(t)
	metrics.catalog = dashboard.Catalog{Models: []dashboard.CatalogModel{{ID: "unknown"}}}
	handler := Handler{
		Metrics:          metrics,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil },
		AuthorizeListResource: func(context.Context, string, projectgraph.ResourceID, access.ResourceRef, access.Capability) (bool, error) {
			return true, nil
		},
	}
	request := httptest.NewRequest(http.MethodGet, "/semantic-models", nil)
	request.Header.Set("X-Serving-Snapshot", "generation-1")
	recorder := httptest.NewRecorder()
	handler.ListSemanticModels(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unknown model list status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	var response api.SemanticModelListResponse
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Items) != 0 {
		t.Fatalf("unknown model list = %#v, want entry omitted", response.Items)
	}
}

func invokeSemanticMetadata(t *testing.T, handler Handler, fields bool) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("model", "sales")
	if fields {
		route.URLParams.Add("dataset", "orders")
	}
	request := httptest.NewRequest(http.MethodGet, "/semantic-models/sales", nil)
	request.Header.Set("X-Serving-Snapshot", "generation-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	if fields {
		handler.ListSemanticFields(recorder, request)
	} else {
		handler.ListSemanticDatasets(recorder, request)
	}
	return recorder
}

func invokeSemanticDatasetDetail(t *testing.T, handler Handler) *httptest.ResponseRecorder {
	t.Helper()
	route := chi.NewRouteContext()
	route.URLParams.Add("model", "sales")
	route.URLParams.Add("dataset", "orders")
	request := httptest.NewRequest(http.MethodGet, "/semantic-models/sales/datasets/orders", nil)
	request.Header.Set("X-Serving-Snapshot", "generation-1")
	request = request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, route))
	recorder := httptest.NewRecorder()
	handler.GetSemanticDataset(recorder, request)
	return recorder
}

func semanticMetadataConsumerFixture(t *testing.T) (*semanticMetadataMetrics, *semanticMetadataAuthority) {
	return semanticMetadataConsumerFixtureWithRole(t, true)
}

func semanticMetadataConsumerFixtureWithRole(t *testing.T, includeRole bool) (*semanticMetadataMetrics, *semanticMetadataAuthority) {
	t.Helper()
	literal, err := semanticmodel.NewSemanticAccessLiteral("member")
	if err != nil {
		t.Fatal(err)
	}
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				GrainEntity: "order",
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"allowed"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"allowed": {Field: "orders.allowed", Table: "orders", Name: "allowed", Type: "string", Datatype: semanticmodel.DataTypeString},
					"denied":  {Field: "orders.denied", Table: "orders", Name: "denied", Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"allowed": {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.allowed"}}},
			"denied":  {Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.denied"}}},
		},
		AccessPolicy: semanticmodel.SemanticAccessPolicy{
			AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
				"view_allowed": {UserAttribute: "role", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
				"view_denied":  {UserAttribute: "denied_role", AllowedValues: []semanticmodel.SemanticAccessLiteral{literal}},
			},
			Datasets: map[string]semanticmodel.SemanticDatasetAccessSpec{"orders": {RequiredAccessGrants: []string{"view_allowed"}}},
			Dimensions: map[string][]string{
				"allowed": {"view_allowed"},
				"denied":  {"view_denied"},
			},
		},
	}
	planner, err := semanticquery.NewCompiledPlanner(model, semanticquery.WithTableRelation(func(table string) (string, error) { return table, nil }))
	if err != nil {
		t.Fatal(err)
	}
	role := semanticMetadataDefinition("def-role", "role")
	deniedRole := semanticMetadataDefinition("def-denied-role", "denied_role")
	definitions := []access.SemanticAttributeDefinition{role, deniedRole}
	registryDigest, err := access.SemanticAttributeRegistryDigest(semanticvalue.Profile, definitions)
	if err != nil {
		t.Fatal(err)
	}
	registry := access.SemanticAttributeRegistrySnapshot{State: access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 1, Digest: registryDigest}, Definitions: definitions}
	values, valueDigest, err := access.CanonicalSemanticAttributeValues(role, "member")
	if err != nil {
		t.Fatal(err)
	}
	subject := access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "alice"}
	assignment := access.SemanticAttributeAssignment{ID: "assignment-role", DefinitionID: role.ID, DefinitionName: role.Name, DefinitionVersion: role.DefinitionVersion, Type: role.Type, Shape: role.Shape, Subject: subject, CanonicalValues: values, ValueDigest: valueDigest, AssignmentVersion: 1}
	attributes := []access.EffectiveSemanticAttribute(nil)
	assignments := []access.SemanticAttributeAssignment(nil)
	if includeRole {
		attributes = []access.EffectiveSemanticAttribute{{DefinitionID: role.ID, DefinitionName: role.Name, DefinitionVersion: role.DefinitionVersion, Type: role.Type, Shape: role.Shape, CanonicalValues: values, ValueDigest: valueDigest, Source: "direct"}}
		assignments = []access.SemanticAttributeAssignment{assignment}
	}
	controlDigest, err := access.SemanticAttributeControlDigest(assignments, nil)
	if err != nil {
		t.Fatal(err)
	}
	control := access.SemanticAttributeControlSnapshot{State: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 1, Digest: controlDigest}, Assignments: assignments}
	resolution := access.SemanticAttributeResolution{Subject: subject, Subjects: []access.SubjectRef{subject}, Registry: registry, Control: control, Attributes: attributes, ObservedAt: time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)}
	snapshot, authority, err := semanticquery.SemanticAccessResolutionSnapshot("instance-1", "alice", resolution)
	if err != nil {
		t.Fatal(err)
	}
	authorityState := &semanticMetadataAuthority{snapshot: snapshot, current: authority}
	consumer, err := semanticquery.NewSemanticAccessConsumer(planner, semanticquery.SemanticAccessConsumerConfig{
		InstanceID: "instance-1", ProjectID: "project:test", Environment: "prod", ModelID: "sales", Generation: "generation-1", PrincipalID: "alice",
		Authority: authorityState.get,
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics := &semanticMetadataMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: model, planner: planner, plannerOkay: true}, consumer: consumer}
	return metrics, authorityState
}

func semanticMetadataDefinition(id, name string) access.SemanticAttributeDefinition {
	return access.SemanticAttributeDefinition{ID: id, Name: name, Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar, Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true, Metadata: access.SemanticAttributeMetadata{Owner: access.SemanticAttributeOwner{Kind: access.SemanticAttributeOwnerInstance}}}
}
