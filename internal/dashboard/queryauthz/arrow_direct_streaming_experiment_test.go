//go:build fai543experiment

package authz_test

import (
	"bytes"
	"context"
	"net/http/httptest"
	"testing"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/flidai/leapview/internal/access"
	accesspolicy "github.com/flidai/leapview/internal/access/policy"
	accesssnapshot "github.com/flidai/leapview/internal/access/snapshot"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/catalog"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	queryauthz "github.com/flidai/leapview/internal/dashboard/queryauthz"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
	semanticapi "github.com/flidai/leapview/internal/dashboard/semanticapi"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const directArrowExperimentProject = projectgraph.ResourceID("project:sales")

func TestDirectArrowExperimentUsesOwnerAuthorizationPoliciesMasksAndAudit(t *testing.T) {
	graph, identity, physical := directArrowExperimentGraph(t)
	row, err := accesspolicy.Compile("rls", "row_filter", `{"field":"orders.region","operator":"equals","values":["EU"]}`)
	if err != nil {
		t.Fatal(err)
	}
	mask, err := accesspolicy.Compile("mask", "column_mask", `{"field":"orders.email","mask":"null"}`)
	if err != nil {
		t.Fatal(err)
	}
	policies := []accesssnapshot.DataPolicy{
		{ID: "rls", Resource: physical, PolicyType: "row_filter", ExpressionJSON: `{"field":"orders.region","operator":"equals","values":["EU"]}`, Compiled: row},
		{ID: "mask", Resource: physical, PolicyType: "column_mask", ExpressionJSON: `{"field":"orders.email","mask":"null"}`, Compiled: mask},
	}
	snapshot := directArrowExperimentSnapshot(t, graph, identity, physical, true, policies)
	capture := &directArrowExperimentCapture{}
	audit := &directArrowExperimentAudit{}
	metrics := directArrowExperimentMetrics(snapshot, capture, audit)
	request := directArrowExperimentRequest()
	config := directArrowExperimentConfig()
	recorder := httptest.NewRecorder()
	if _, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, metrics, request, config); err != nil {
		t.Fatal(err)
	}
	if capture.calls != 1 || len(capture.request.Filters) != 1 || len(capture.request.ColumnMasks) != 1 || capture.request.EffectivePolicyFingerprint == "" {
		t.Fatalf("experiment executor received ungoverned request: calls=%d request=%#v", capture.calls, capture.request)
	}
	filter := capture.request.Filters[0]
	if filter.Field != "orders.region" || filter.Operator != "equals" || len(filter.Values) != 1 || filter.Values[0] != "EU" {
		t.Fatalf("experiment row policy = %#v", filter)
	}
	if capture.request.ColumnMasks[0].Field != "orders.email" || capture.request.ColumnMasks[0].Mask != "null" {
		t.Fatalf("experiment column masks = %#v", capture.request.ColumnMasks)
	}
	if len(audit.events) != 1 || audit.events[0].Status != "success" || audit.events[0].PrincipalID != "alice" || audit.events[0].Identity != identity {
		t.Fatalf("experiment audit events = %#v", audit.events)
	}
	reader, err := ipc.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	if reader.Schema().NumFields() != 1 || reader.Schema().Field(0).Name != "customer_email" || reader.Schema().Field(0).Type.ID() != arrow.STRING {
		t.Fatalf("experiment governed schema = %s", reader.Schema())
	}
	if _, ok := reader.Schema().Metadata().GetValue("source.connection"); ok {
		t.Fatal("experiment exposed unsafe schema metadata")
	}
	if _, ok := reader.Schema().Field(0).Metadata.GetValue("source.connection"); ok {
		t.Fatal("experiment exposed unsafe field metadata")
	}

	deniedCapture := &directArrowExperimentCapture{}
	denied := directArrowExperimentMetrics(directArrowExperimentSnapshot(t, graph, identity, physical, false, nil), deniedCapture, nil)
	deniedResponse := httptest.NewRecorder()
	if _, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), deniedResponse, denied, request, config); err == nil {
		t.Fatal("unauthorized direct Arrow experiment was accepted")
	}
	if deniedCapture.calls != 0 || deniedResponse.Body.Len() != 0 {
		t.Fatalf("unauthorized experiment reached executor/response: calls=%d body=%d", deniedCapture.calls, deniedResponse.Body.Len())
	}
}

type directArrowExperimentUnderlying struct {
	queryruntime.Metrics
	model   *semanticmodel.Model
	capture *directArrowExperimentCapture
}

func (m directArrowExperimentUnderlying) Catalog() catalog.Catalog {
	return catalog.Catalog{Project: catalog.Project{ID: directArrowExperimentProject}}
}

func (m directArrowExperimentUnderlying) SemanticModel(id string) (*semanticmodel.Model, bool) {
	return m.model, m.model != nil && (id == string(m.model.Name) || id == "semantic_sales")
}

func (m directArrowExperimentUnderlying) Planner(id string) (consumer.Planner, bool) {
	if m.model == nil || (id != string(m.model.Name) && id != "semantic_sales") {
		return nil, false
	}
	planner, err := semanticquery.NewCompiledPlanner(m.model)
	return planner, err == nil
}

func (directArrowExperimentUnderlying) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, nil
}

func (m directArrowExperimentUnderlying) ExecuteDataQueryArrow(_ context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	m.capture.calls++
	m.capture.request = request
	unsafe := arrow.MetadataFrom(map[string]string{"source.connection": "must-not-survive"})
	schema := arrow.NewSchema([]arrow.Field{{Name: request.Fields[0].Alias, Type: arrow.BinaryTypes.String, Nullable: true, Metadata: unsafe}}, &unsafe)
	if err := sink.WriteSchema(schema); err != nil {
		return dataquery.Result{}, err
	}
	return dataquery.Result{}, nil
}

type directArrowExperimentCapture struct {
	calls   int
	request dataquery.Query
}

type directArrowExperimentAudit struct{ events []access.CanonicalAuditEvent }

func (a *directArrowExperimentAudit) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	a.events = append(a.events, event)
	return nil
}

func directArrowExperimentMetrics(snapshot accesssnapshot.AuthorizationSnapshot, capture *directArrowExperimentCapture, audit access.CanonicalAuditRecorder) queryauthz.Metrics {
	model := &semanticmodel.Model{
		Name: "sales", Sources: map[string]semanticmodel.Source{"orders": {}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Tables: map[string]semanticmodel.Table{"orders": {
			Execution: semanticmodel.ExecutionDefinition{Source: "orders"}, ModelName: "orders", GrainEntity: "region",
			Entities: map[string]semanticmodel.EntityDefinition{"region": {Type: "primary", Fields: []string{"region"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{
				"region": {Field: "orders.region", Table: "orders", Name: "region", Type: "string", Datatype: semanticmodel.DataTypeString},
			},
		}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"region": {Name: "region", Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.region"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.region"}, Empty: "zero"},
		},
	}
	underlying := directArrowExperimentUnderlying{model: model, capture: capture}
	return queryauthz.New(underlying, queryauthz.Options{
		SnapshotFromContext: func(context.Context) (accesssnapshot.AuthorizationSnapshot, error) { return snapshot, nil },
		SubjectsFromContext: func(context.Context, string) ([]access.SubjectRef, error) {
			subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
			return []access.SubjectRef{subject}, err
		},
		PrincipalFromContext: func(context.Context) (queryauthz.Principal, bool) { return queryauthz.Principal{ID: "alice"}, true },
		CredentialFromContext: func(context.Context) (access.APICredential, bool) {
			return access.APICredential{Token: access.APIToken{ID: "credential-a"}}, true
		},
		AuditRecorder: audit,
	})
}

func directArrowExperimentGraph(t testing.TB) (projectgraph.ProjectGraph, projectgraph.ServingIdentity, access.ResourceRef) {
	t.Helper()
	graph, err := projectgraph.NewProjectGraph([]projectgraph.Resource{
		{ID: directArrowExperimentProject, Kind: projectgraph.KindProject, Name: "sales_project"},
		{ID: "semantic_sales", Kind: projectgraph.KindSemanticModel, Name: "sales"},
		{ID: "model_orders", Kind: projectgraph.KindModel, Name: "orders"},
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(directArrowExperimentProject, "production", "generation-1")
	if err != nil {
		t.Fatal(err)
	}
	physical, err := access.NewResourceRef("model_orders", projectgraph.KindModel)
	if err != nil {
		t.Fatal(err)
	}
	return graph, identity, physical
}

func directArrowExperimentSnapshot(
	t testing.TB,
	graph projectgraph.ProjectGraph,
	identity projectgraph.ServingIdentity,
	physical access.ResourceRef,
	granted bool,
	policies []accesssnapshot.DataPolicy,
) accesssnapshot.AuthorizationSnapshot {
	t.Helper()
	var grants []accesssnapshot.Grant
	if granted {
		subject, err := access.NewSubjectRef(access.SubjectKindPrincipal, "alice")
		if err != nil {
			t.Fatal(err)
		}
		grant, err := access.NewCanonicalGrant(graph, subject, physical, access.CapabilityResourceUse)
		if err != nil {
			t.Fatal(err)
		}
		grants = []accesssnapshot.Grant{{ID: "physical", Canonical: grant}}
	}
	snapshot, err := accesssnapshot.NewAuthorizationSnapshot(identity, graph, grants, policies)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func directArrowExperimentRequest() dataquery.Query {
	return dataquery.Query{
		ProjectID: directArrowExperimentProject, Surface: dataquery.SurfaceAPI, Operation: dataquery.OperationDashboardRows,
		ModelID: "semantic_sales", Target: "orders", Kind: dataquery.KindSemanticRows,
		Fields: []dataquery.Field{{Field: "orders.email", Alias: "customer_email"}}, Limit: 51,
	}
}

func directArrowExperimentConfig() semanticapi.DirectArrowExperimentConfig {
	return semanticapi.DirectArrowExperimentConfig{
		QueryID: "fai-543-authz", Snapshot: "generation-1", CursorScope: "scope-authz",
		SchemaVersion: "1", SpecRevision: "spec-authz", DataRevision: "1",
		LogicalTypes: map[string]string{"customer_email": "string"}, Labels: map[string]string{"customer_email": "Customer"}, Limit: 50,
	}
}
