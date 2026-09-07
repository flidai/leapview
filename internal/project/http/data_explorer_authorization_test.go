package http

import (
	"context"
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	"github.com/flidai/leapview/internal/semanticvalue"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type dataExplorerAuditRecorder struct {
	events []access.CanonicalAuditEvent
	err    error
}

func (r *dataExplorerAuditRecorder) RecordCanonicalAuditEvent(_ context.Context, event access.CanonicalAuditEvent) error {
	r.events = append(r.events, event)
	return r.err
}

type dataExplorerBoundDefinitionStub struct {
	definition projectmanifest.Project
	compiled   map[string]*semanticquery.CompiledModel
	identity   projectgraph.ServingIdentity
	control    access.AuthorizationControlRevision
	boundErr   error
}

func (s dataExplorerBoundDefinitionStub) ProjectDefinitionSnapshot(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, error) {
	return s.definition, s.compiled, nil
}

func (s dataExplorerBoundDefinitionStub) ProjectDefinitionSnapshotBound(context.Context) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, projectgraph.ServingIdentity, access.AuthorizationControlRevision, error) {
	return s.definition, s.compiled, s.identity, s.control, s.boundErr
}

func TestDataExplorerAccessPredicateUsesCompiledPolicyAndCanonicalResolution(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	state := dataExplorerAuditStateForTest(t, project)
	recorder := &dataExplorerAuditRecorder{}
	handler := &BrowserHandler{SemanticAuditRecorder: recorder, SemanticAuditActorFromContext: func(context.Context) (string, error) {
		return "principal-1", nil
	}, ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, registry, definition, "us"), nil
	}}

	predicate, err := handler.dataExplorerAccessPredicateWithAudit(context.Background(), project, compiled, state)
	if err != nil {
		t.Fatalf("data explorer access predicate: %v", err)
	}
	if predicate == nil || !predicate("semantic:sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("canonical matching resolution was denied for protected orders dataset")
	}

	handler.ResolveSemanticAttributes = func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, registry, definition, "eu"), nil
	}
	predicate, err = handler.dataExplorerAccessPredicateWithAudit(context.Background(), project, compiled, state)
	if err != nil {
		t.Fatalf("denied resolution unexpectedly failed to construct predicate: %v", err)
	}
	if predicate == nil || predicate("semantic:sales", semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatal("non-matching canonical resolution was allowed for protected orders dataset")
	}
	if len(recorder.events) != 2 || recorder.events[0].Status != "success" || recorder.events[1].Status != "denied" {
		t.Fatalf("protected decision events = %#v, want one success and one denied", recorder.events)
	}
	if strings.Contains(string(recorder.events[0].MetadataJSON), `"us"`) || strings.Contains(string(recorder.events[0].MetadataJSON), "orders.status =") {
		t.Fatalf("semantic audit metadata exposed raw values or predicates: %s", recorder.events[0].MetadataJSON)
	}
}

func TestDataExplorerAccessPredicateRequiresAuditForProtectedModel(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	handler := &BrowserHandler{ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, registry, definition, "us"), nil
	}}
	predicate, err := handler.dataExplorerAccessPredicateWithAudit(context.Background(), project, compiled, dataExplorerAuditStateForTest(t, project))
	if !errors.Is(err, errDataExplorerSemanticAccessUnavailable) || predicate != nil {
		t.Fatalf("protected predicate without audit = %v, %v; want unavailable and nil", predicate, err)
	}
}

func TestDataExplorerSignalsReturns503WhenSemanticAuditWriteFails(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	state := dataExplorerAuditStateForTest(t, project)
	recorder := &dataExplorerAuditRecorder{err: errors.New("audit store unavailable")}
	handler := &BrowserHandler{
		Graph:                   browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{ID: "semantic:sales", ProjectID: projectgraph.ResourceID(project.ID), ServingStateID: servingstate.ID(state.identity.GenerationID), Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`}}}},
		ProjectDefinitionReader: dataExplorerBoundDefinitionStub{definition: project, compiled: compiled, identity: state.identity, control: state.control},
		ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
			return dataExplorerResolution(t, registry, definition, "us"), nil
		},
		SemanticAuditRecorder: recorder,
		SemanticAuditActorFromContext: func(context.Context) (string, error) {
			return "principal-1", nil
		},
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return projectgraph.ResourceID(project.ID), nil
		},
		Environment: "development",
		CurrentUser: func(*stdhttp.Request) (Principal, bool) { return Principal{ID: "principal-1", DevBypass: true}, true },
	}
	request := httptest.NewRequest(stdhttp.MethodGet, "/explore?model=semantic:sales&dataset=orders", nil)
	request.Header.Set("X-Request-ID", "data-explorer-request-1")
	request.Header.Set("X-Correlation-ID", "data-explorer-correlation-1")
	recorderHTTP := httptest.NewRecorder()
	if _, _, ok := handler.dataExplorerSignals(recorderHTTP, request); ok {
		t.Fatal("data explorer returned a projection after audit persistence failed")
	}
	if recorderHTTP.Code != stdhttp.StatusServiceUnavailable {
		t.Fatalf("data explorer status = %d, want %d", recorderHTTP.Code, stdhttp.StatusServiceUnavailable)
	}
	if len(recorder.events) == 0 || recorder.events[0].RequestID != "data-explorer-request-1" || recorder.events[0].CorrelationID != "data-explorer-correlation-1" {
		t.Fatalf("data explorer audit metadata = %#v (status=%d body=%s)", recorder.events, recorderHTTP.Code, recorderHTTP.Body.String())
	}
}

func TestDataExplorerSignalsKeepsOrdinaryCompatibilityWithoutAuthorizationSnapshot(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{"orders": {
			ModelName: "orders", GrainEntity: "order",
			Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
			Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}},
		}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	const projectID = "project:ordinary"
	handler := &BrowserHandler{
		Graph:                   browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{{ID: "semantic:sales", ProjectID: projectgraph.ResourceID(projectID), ServingStateID: servingstate.ID("generation_1"), Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`}}}},
		ProjectDefinitionReader: dataExplorerBoundDefinitionStub{definition: projectmanifest.Project{ID: projectID, SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}, compiled: map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}, boundErr: errors.New("authorization snapshot unavailable")},
		ResolveProjectID:        func(context.Context) (projectgraph.ResourceID, error) { return projectgraph.ResourceID(projectID), nil },
		Environment:             "development",
		CurrentUser:             func(*stdhttp.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	recorder := httptest.NewRecorder()
	if _, _, ok := handler.dataExplorerSignals(recorder, httptest.NewRequest(stdhttp.MethodGet, "/explore", nil)); !ok {
		t.Fatalf("ordinary data explorer rejected legacy definition reader: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestDataExplorerAccessPredicateFailsClosedForMissingAuthorityOrSubject(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	for name, resolve := range map[string]func(context.Context) (access.SemanticAttributeResolution, error){
		"missing authority": nil,
		"resolver error": func(context.Context) (access.SemanticAttributeResolution, error) {
			return access.SemanticAttributeResolution{}, errors.New("authority unavailable")
		},
		"missing subject": func(context.Context) (access.SemanticAttributeResolution, error) {
			resolution := dataExplorerResolution(t, registry, definition, "us")
			resolution.Subject = access.SubjectRef{}
			return resolution, nil
		},
		"group subject": func(context.Context) (access.SemanticAttributeResolution, error) {
			resolution := dataExplorerResolution(t, registry, definition, "us")
			resolution.Subject = access.SubjectRef{Kind: access.SubjectKindGroup, ID: "group-1"}
			return resolution, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := &BrowserHandler{ResolveSemanticAttributes: resolve}
			predicate, err := handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
			if !errors.Is(err, errDataExplorerSemanticAccessUnavailable) {
				t.Fatalf("predicate error = %v, want %v", err, errDataExplorerSemanticAccessUnavailable)
			}
			if predicate != nil {
				t.Fatal("predicate was returned for unavailable semantic authority")
			}
		})
	}
}

func TestDataExplorerAccessPredicateFailsClosedForStaleRegistry(t *testing.T) {
	project, compiled, registry, definition := dataExplorerAuthorizationFixture(t, "us")
	stale := registry
	stale.State.Revision++
	state := dataExplorerAuditStateForTest(t, project)
	handler := &BrowserHandler{SemanticAuditRecorder: &dataExplorerAuditRecorder{}, SemanticAuditActorFromContext: func(context.Context) (string, error) {
		return "principal-1", nil
	}, ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		return dataExplorerResolution(t, stale, definition, "us"), nil
	}}
	predicate, err := handler.dataExplorerAccessPredicateWithAudit(context.Background(), project, compiled, state)
	if !errors.Is(err, errDataExplorerSemanticAccessUnavailable) || predicate != nil {
		t.Fatalf("stale registry predicate = %v, %v; want unavailable and nil", predicate, err)
	}
}

func TestDataExplorerAccessPredicateRejectsAuthoredPolicyMutation(t *testing.T) {
	project, compiled, _, _ := dataExplorerAuthorizationFixture(t, "us")
	project.SemanticModels["semantic:sales"].AccessGrants["region_grant"] = semanticmodel.SemanticAccessGrantSpec{
		UserAttribute: "region", AllowedValues: []any{"eu"},
	}
	called := false
	handler := &BrowserHandler{ResolveSemanticAttributes: func(context.Context) (access.SemanticAttributeResolution, error) {
		called = true
		return access.SemanticAttributeResolution{}, nil
	}}
	predicate, err := handler.dataExplorerAccessPredicate(context.Background(), project, compiled)
	if !errors.Is(err, errDataExplorerSemanticAccessUnavailable) {
		t.Fatalf("mutated policy error = %v, want %v", err, errDataExplorerSemanticAccessUnavailable)
	}
	if predicate != nil {
		t.Fatal("predicate was returned for mutated authored policy")
	}
	if called {
		t.Fatal("authority was resolved before rejecting stale compiled policy")
	}
}

func TestDataExplorerAccessPredicateRejectsStrippedPolicy(t *testing.T) {
	project, compiled, _, _ := dataExplorerAuthorizationFixture(t, "us")
	model := project.SemanticModels["semantic:sales"]
	model.AccessGrants = nil
	for name, dataset := range model.Datasets {
		dataset.RequiredAccessGrants = nil
		dataset.AccessFilters = nil
		model.Datasets[name] = dataset
	}
	predicate, err := (&BrowserHandler{}).dataExplorerAccessPredicate(context.Background(), project, compiled)
	if err == nil || predicate != nil {
		t.Fatal("stripped authored policy bypassed compiled protection")
	}
}

func dataExplorerAuthorizationFixture(t testing.TB, region string) (projectmanifest.Project, map[string]*semanticquery.CompiledModel, access.SemanticAttributeRegistrySnapshot, access.SemanticAttributeDefinition) {
	t.Helper()
	definition := access.SemanticAttributeDefinition{
		ID: "def-region", Name: "region", Type: semanticvalue.TypeString, Shape: access.SemanticAttributeScalar,
		Profile: semanticvalue.Profile, DefinitionVersion: 1, LifecycleState: access.SemanticAttributeActive, Enabled: true,
	}
	registry := access.SemanticAttributeRegistrySnapshot{
		State:       access.SemanticAttributeRegistryState{Profile: semanticvalue.Profile, Revision: 7, Digest: "sha256:" + strings.Repeat("a", 64)},
		Definitions: []access.SemanticAttributeDefinition{definition},
	}
	model := &semanticmodel.Model{
		Name: "sales",
		AccessGrants: map[string]semanticmodel.SemanticAccessGrantSpec{
			"region_grant": {UserAttribute: "region", AllowedValues: []any{"us"}},
		},
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders", GrainEntity: "order",
				Entities:   map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
				Dimensions: map[string]semanticmodel.MetricDimension{"order_id": {Datatype: semanticmodel.DataTypeInteger}},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders", RequiredAccessGrants: []string{"region_grant"}},
		},
	}
	compiled, err := semanticquery.CompileModelWithSemanticAccess(model, semanticquery.SemanticAccessCompileContext{Registry: registry})
	if err != nil {
		t.Fatalf("compile protected semantic model: %v", err)
	}
	return projectmanifest.Project{ID: "project:demo", SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model}}, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}, registry, definition
}

func dataExplorerResolution(t testing.TB, registry access.SemanticAttributeRegistrySnapshot, definition access.SemanticAttributeDefinition, region string) access.SemanticAttributeResolution {
	t.Helper()
	values, digest, err := access.CanonicalSemanticAttributeValues(definition, region)
	if err != nil {
		t.Fatalf("canonical semantic attribute value: %v", err)
	}
	return access.SemanticAttributeResolution{
		Subject:      access.SubjectRef{Kind: access.SubjectKindPrincipal, ID: "principal-1"},
		Registry:     registry,
		ControlState: access.SemanticAttributeControlState{Profile: semanticvalue.Profile, Revision: 11, Digest: "sha256:" + strings.Repeat("b", 64)},
		Attributes: []access.EffectiveSemanticAttribute{{
			DefinitionID: definition.ID, DefinitionName: definition.Name, DefinitionVersion: definition.DefinitionVersion,
			Type: definition.Type, Shape: definition.Shape, CanonicalValues: values, ValueDigest: digest, Source: "direct",
		}},
	}
}

func dataExplorerAuditStateForTest(t testing.TB, project projectmanifest.Project) *dataExplorerAuditState {
	t.Helper()
	identity, err := projectgraph.NewServingIdentity(projectgraph.ResourceID(project.ID), "development", "generation_1")
	if err != nil {
		t.Fatal(err)
	}
	return &dataExplorerAuditState{
		identity: identity,
		control:  access.AuthorizationControlRevision{InstanceID: "instance_demo", ProjectID: project.ID, Revision: 1},
		bound:    true,
	}
}
