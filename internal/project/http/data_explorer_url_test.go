package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectview "github.com/flidai/leapview/internal/project"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectmanifest "github.com/flidai/leapview/internal/project/manifest"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
	servingstate "github.com/flidai/leapview/internal/servingstate"
)

type countingDataQueryExecutor struct {
	calls int
}

func (e *countingDataQueryExecutor) ExecuteDataQuery(context.Context, dataquery.Query) (dataquery.Result, error) {
	e.calls++
	return dataquery.Result{Rows: []dataquery.Row{{"status": "paid"}}}, nil
}

func newDataExplorerURLTestHandler(t *testing.T) (*BrowserHandler, *countingDataQueryExecutor) {
	t.Helper()
	const projectID = "project:test"
	const modelID = "semantic:sales"
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName: "orders",
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status":     {Label: "Status", Type: "string"},
					"created_at": {Label: "Created at", Type: "timestamp"},
				},
			},
		},
		Metrics: map[string]semanticmodel.Metric{
			"revenue": {Type: "aggregate", Dataset: "orders", Label: "Revenue", Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.revenue"}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	executor := &countingDataQueryExecutor{}
	h := &BrowserHandler{
		Graph: browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
			{ID: "model:orders", ProjectID: projectID, ServingStateID: "state", Type: "model", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
			{ID: modelID, ProjectID: projectID, ServingStateID: "state", Type: "semantic_model", Key: "sales", Title: "Sales", PayloadJSON: `{}`},
		}}},
		ProjectDefinitionReader: browserProjectDefinitionStub{definition: projectmanifest.Project{
			ID:             projectID,
			Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"]},
			SemanticModels: map[string]*semanticmodel.Model{modelID: model},
			NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders"}},
		}, compiled: map[string]*semanticquery.CompiledModel{modelID: compiled}},
		QueryExecutor:    executor,
		ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return projectID, nil },
		Environment:      "dev",
		CurrentUser:      func(*http.Request) (Principal, bool) { return Principal{DevBypass: true}, true },
	}
	return h, executor
}

func TestDataExplorerDocumentDefersSemanticExecutionToCanonicalUpdates(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	values := url.Values{
		"mode":          {"explore"},
		"semanticModel": {"semantic:sales"},
		"dataset":       {"orders"},
		"dimension":     {"orders.status"},
	}

	document := httptest.NewRecorder()
	h.Explore(document, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil))
	if document.Code != http.StatusOK {
		t.Fatalf("document status = %d, want 200: %s", document.Code, document.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("document executed %d analytical queries, want 0", executor.calls)
	}
	for _, want := range []string{"mode=explore", "semanticModel=semantic%3Asales", "dataset=orders", "dimension=orders.status"} {
		if !strings.Contains(document.Body.String(), want) {
			t.Fatalf("document shell missing normalized updates URL component %q:\n%s", want, document.Body.String())
		}
	}

	streamContext, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequestWithContext(streamContext, http.MethodGet, "/updates?route=data&surface=explore&"+values.Encode(), nil)
	stream := &notifyingResponseRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(stream, request)
	}()
	select {
	case <-stream.wrote:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("updates bootstrap did not write a patch")
	}
	if executor.calls != 1 {
		t.Fatalf("updates executed %d analytical queries, want exactly 1", executor.calls)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("updates stream did not stop after cancellation")
	}
}

func TestDataExplorerRestoredURLCanonicalizesSpacedOperandsBeforeExecution(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	values := url.Values{
		"mode":          {"explore"},
		"semanticModel": {" semantic:sales "},
		"dataset":       {" orders "},
		"dimension":     {" orders.status "},
	}
	document := httptest.NewRecorder()
	h.Explore(document, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil))
	if document.Code != http.StatusOK {
		t.Fatalf("spaced URL status = %d, want 200: %s", document.Code, document.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("spaced document executed %d analytical queries, want 0", executor.calls)
	}
	body := document.Body.String()
	if !strings.Contains(body, "dimension=orders.status") || strings.Contains(body, "dimension=+orders.status+") {
		t.Fatalf("spaced field was not canonicalized in updates URL:\n%s", body)
	}
}

func TestDataExploreCommandFromQueryTrimsMetadataButPreservesFilterValues(t *testing.T) {
	command, err := dataExploreCommandFromQuery(url.Values{
		"dimension": {" orders.status "},
		"metric":    {" revenue "},
		"filter":    {`{"field":" orders.status ","operator":" equals ","datasetId":" orders ","values":[" paid "]}`},
		"sort":      {`{"field":" revenue ","direction":" desc "}`},
		"time":      {`{"field":" orders.created_at ","grain":" month ","alias":" order_month "}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Dimensions) != 1 || command.Dimensions[0] != "orders.status" || len(command.Metrics) != 1 || command.Metrics[0] != "revenue" {
		t.Fatalf("trimmed fields = %#v/%#v", command.Dimensions, command.Metrics)
	}
	if len(command.Filters) != 1 || command.Filters[0].Field != "orders.status" || command.Filters[0].Operator != "equals" || projectsignals.ValueOrZero(command.Filters[0].DatasetID) != "orders" || len(command.Filters[0].Values) != 1 || command.Filters[0].Values[0] != " paid " {
		t.Fatalf("trimmed filter metadata or changed value = %#v", command.Filters)
	}
	if len(command.Sort) != 1 || command.Sort[0].Field != "revenue" || command.Sort[0].Direction != "desc" {
		t.Fatalf("trimmed sort = %#v", command.Sort)
	}
	if command.Time == nil || command.Time.Field != "orders.created_at" || command.Time.Grain != "month" || projectsignals.ValueOrZero(command.Time.Alias) != "order_month" {
		t.Fatalf("trimmed time = %#v", command.Time)
	}
}

func TestDataExplorerRestoredURLFailsClosedForStaleField(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	values := url.Values{
		"mode":          {"explore"},
		"semanticModel": {"semantic:sales"},
		"dataset":       {"orders"},
		"dimension":     {"orders.removed_status"},
	}
	recorder := httptest.NewRecorder()
	h.Explore(recorder, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("stale URL status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "orders.removed_status") || !strings.Contains(recorder.Body.String(), "remove it from the URL") {
		t.Fatalf("stale URL feedback = %q, want field and remediation", recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("stale URL executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerRestoredURLFailsClosedWhenBindingsAreUnavailable(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	definition := h.ProjectDefinitionReader.(browserProjectDefinitionStub)
	definition.compiled = nil
	h.ProjectDefinitionReader = definition
	values := url.Values{
		"mode":          {"explore"},
		"semanticModel": {"semantic:sales"},
		"dataset":       {"orders"},
		"dimension":     {"orders.status"},
	}
	recorder := httptest.NewRecorder()
	h.Explore(recorder, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unavailable bindings URL status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no active compiled definition") || !strings.Contains(recorder.Body.String(), "reload the explorer") {
		t.Fatalf("unavailable bindings feedback = %q, want actionable diagnostic", recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("unavailable bindings URL executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerRestoredURLFailsClosedForEmptyCompiledModel(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	definition := h.ProjectDefinitionReader.(browserProjectDefinitionStub)
	definition.compiled = map[string]*semanticquery.CompiledModel{"semantic:sales": &semanticquery.CompiledModel{}}
	h.ProjectDefinitionReader = definition
	values := url.Values{
		"mode":          {"explore"},
		"semanticModel": {"semantic:sales"},
		"dataset":       {"orders"},
		"dimension":     {"orders.status"},
	}
	recorder := httptest.NewRecorder()
	h.Explore(recorder, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("empty compiled model URL status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "no active compiled definition") {
		t.Fatalf("empty compiled model feedback = %q, want unavailable diagnostic", recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("empty compiled model URL executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerBrowseRestoreDoesNotRequireSemanticBindings(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	definition := h.ProjectDefinitionReader.(browserProjectDefinitionStub)
	definition.compiled = nil
	h.ProjectDefinitionReader = definition
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/explore?object=model%3Aorders", nil), false)
	if !ok {
		t.Fatalf("browse restore failed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder.Code != http.StatusOK || projectsignals.ValueOrZero(explorer.Command.Mode) != "browse" || explorer.SelectedObject == nil {
		t.Fatalf("browse restore = status %d, explorer %#v", recorder.Code, explorer)
	}
}

func TestDataExplorerBrowseDocumentDefersPreviewToCanonicalUpdates(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	document := httptest.NewRecorder()
	h.Explore(document, httptest.NewRequest(http.MethodGet, "/explore?object=model%3Aorders", nil))
	if document.Code != http.StatusOK {
		t.Fatalf("browse document status = %d, want 200: %s", document.Code, document.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("browse document executed %d preview queries, want 0", executor.calls)
	}

	streamContext, cancel := context.WithCancel(t.Context())
	request := httptest.NewRequestWithContext(streamContext, http.MethodGet, "/updates?route=data&surface=explore&object=model%3Aorders", nil)
	stream := &notifyingResponseRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(stream, request)
	}()
	select {
	case <-stream.wrote:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("browse updates bootstrap did not write a patch")
	}
	if executor.calls != 1 {
		t.Fatalf("browse updates executed %d preview queries, want exactly 1", executor.calls)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("browse updates stream did not stop after cancellation")
	}
}

func TestValidateRestoredDataExploreStateRejectsWrongKindsAndIncompatibleOperands(t *testing.T) {
	fields := map[string]projectsignals.DataExploreFieldSignal{
		"orders.status":       {ID: "orders.status", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("string")},
		"orders.created_at":   {ID: "orders.created_at", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("timestamp")},
		"revenue":             {ID: "revenue", Kind: "metric", Compatible: true},
		"orders.incompatible": {ID: "orders.incompatible", Kind: "dimension", Compatible: false, CompatibilityReason: projectsignals.Optional("no safe relationship path")},
	}
	projection := DataExplorerProjection{
		SemanticModels: []projectsignals.DataExploreSemanticModelSignal{{ID: "semantic:sales"}},
		Datasets:       []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Fields:         make([]projectsignals.DataExploreFieldSignal, 0, len(fields)),
		Command: projectsignals.DataExploreCommand{
			SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
		},
	}
	for _, field := range fields {
		projection.Fields = append(projection.Fields, field)
	}
	compiled, err := semanticquery.CompileDatasetBindings(&semanticmodel.Model{
		Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	compiledModels := map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}
	for _, test := range []struct {
		name    string
		command projectsignals.DataExploreCommand
		want    string
	}{
		{name: "dimension wrong kind", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Metrics: []string{"orders.status"}}, want: "not a metric"},
		{name: "filter wrong kind", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Filters: []projectsignals.DataExploreFilterSignal{{Field: "revenue", Operator: "equals", Values: []string{"1"}}}}, want: "not a dimension"},
		{name: "filter value cardinality", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Filters: []projectsignals.DataExploreFilterSignal{{Field: "orders.status", Operator: "equals", Values: []string{"paid", "shipped"}}}}, want: "exactly one value"},
		{name: "sort not selected", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Sort: []projectsignals.DataExploreSortSignal{{Field: "orders.status", Direction: "asc"}}}, want: "not selected"},
		{name: "incompatible", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Dimensions: []string{"orders.incompatible"}}, want: "no safe relationship path"},
		{name: "time wrong kind", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Time: &projectsignals.DataExploreTimeSignal{Field: "orders.status", Grain: "month"}}, want: "not a date or timestamp"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRestoredDataExploreState(test.command, projection, nil, compiledModels); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRestoredDataExploreStateRejectsEmptyMembershipFilters(t *testing.T) {
	projection := DataExplorerProjection{
		SemanticModels: []projectsignals.DataExploreSemanticModelSignal{{ID: "semantic:sales"}},
		Datasets:       []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Fields:         []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("string")}},
		Command: projectsignals.DataExploreCommand{
			SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
		},
	}
	compiled, err := semanticquery.CompileDatasetBindings(&semanticmodel.Model{
		Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, operator := range []string{"in", "not_in"} {
		err := validateRestoredDataExploreState(projectsignals.DataExploreCommand{
			SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
			Filters: []projectsignals.DataExploreFilterSignal{{Field: "orders.status", Operator: operator, Values: []string{}}},
		}, projection, nil, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
		if err == nil || !strings.Contains(err.Error(), "at least one value") {
			t.Fatalf("empty %s filter error = %v, want non-empty arity diagnostic", operator, err)
		}
	}
}

func TestValidateRestoredDataExploreStateRejectsUnavailableTargets(t *testing.T) {
	projection := DataExplorerProjection{
		SemanticModels: []projectsignals.DataExploreSemanticModelSignal{{ID: "semantic:sales"}},
		Datasets:       []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Command: projectsignals.DataExploreCommand{
			SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
		},
	}
	compiled, err := semanticquery.CompileDatasetBindings(&semanticmodel.Model{
		Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		command projectsignals.DataExploreCommand
		want    string
	}{
		{name: "semantic model", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:removed")}, want: "semantic model \"semantic:removed\" is no longer available"},
		{name: "dataset", command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("removed")}, want: "dataset \"removed\" is no longer available"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRestoredDataExploreState(test.command, projection, nil, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateRestoredDataExploreStateConstrainsFilterDatasetParticipation(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"status": {Type: "string"}}},
			"customers": {ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"region": {Type: "string"}}},
			"other":     {ModelName: "other", Dimensions: map[string]semanticmodel.MetricDimension{"name": {Type: "string"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"}, "other": {Model: "other"},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count":    {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.status"}},
			"customer_count": {Type: "aggregate", Dataset: "customers", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "customers.region"}},
			"combined":       {Type: "ratio", Numerator: "order_count", Denominator: "customer_count"},
		},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	compiledModels := map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}
	base := DataExplorerProjection{
		SemanticModels: []projectsignals.DataExploreSemanticModelSignal{{ID: "semantic:sales"}},
		Datasets:       []projectsignals.DataExploreDatasetSignal{{ID: "orders"}, {ID: "customers"}, {ID: "other"}},
		Fields: []projectsignals.DataExploreFieldSignal{
			{ID: "orders.status", Kind: "dimension", Compatible: true},
			{ID: "combined", Kind: "metric", Compatible: true},
		},
		Command: projectsignals.DataExploreCommand{SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders")},
	}
	filter := func(dataset string) []projectsignals.DataExploreFilterSignal {
		return []projectsignals.DataExploreFilterSignal{{Field: "orders.status", DatasetID: projectsignals.Optional(dataset), Operator: "equals", Values: []string{"paid"}}}
	}
	singleRoot := projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"), Filters: filter("customers"),
	}
	if err := validateRestoredDataExploreState(singleRoot, base, model, compiledModels); err == nil || !strings.Contains(err.Error(), "does not participate") {
		t.Fatalf("single-root filter dataset error = %v, want participation diagnostic", err)
	}
	multiRoot := singleRoot
	multiRoot.Metrics = []string{"combined"}
	if err := validateRestoredDataExploreState(multiRoot, base, model, compiledModels); err != nil {
		t.Fatalf("participating multi-root filter dataset rejected: %v", err)
	}
	nonParticipating := multiRoot
	nonParticipating.Filters = filter("other")
	if err := validateRestoredDataExploreState(nonParticipating, base, model, compiledModels); err == nil || !strings.Contains(err.Error(), "does not participate") {
		t.Fatalf("nonparticipating multi-root filter dataset error = %v, want participation diagnostic", err)
	}
}

func TestValidateRestoredDataExploreStateChecksDeclaredTimeGrains(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				GrainEntity: "order",
				Entities:    map[string]semanticmodel.EntityDefinition{"order": {Type: "primary", Fields: []string{"order_id"}}},
				Dimensions:  map[string]semanticmodel.MetricDimension{"order_id": {Type: "number", Datatype: semanticmodel.DataTypeInteger}, "created_at": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTime}},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
		Dimensions: map[string]semanticmodel.SemanticDimension{
			"created": {Type: "timestamp", Datatype: semanticmodel.DataTypeDateTime, NativeGrain: "day", Grains: []string{"day"}, Bindings: map[string]semanticmodel.DimensionBinding{"orders": {Field: "orders.created_at"}}},
		},
		Metrics: map[string]semanticmodel.Metric{
			"order_count": {Type: "aggregate", Dataset: "orders", Aggregation: "count", Input: &semanticmodel.MetricInput{Field: "orders.order_id"}},
		},
	}
	projection := DataExplorerProjection{
		SemanticModels: []projectsignals.DataExploreSemanticModelSignal{{ID: "semantic:sales"}},
		Datasets:       []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Fields: []projectsignals.DataExploreFieldSignal{
			{ID: "created", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("timestamp")},
			{ID: "orders.created_at", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("timestamp")},
		},
		Command: projectsignals.DataExploreCommand{
			SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
		},
	}
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	err = validateRestoredDataExploreState(projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
		Time: &projectsignals.DataExploreTimeSignal{Field: "created", Grain: "month"},
	}, projection, model, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
	if err == nil || !strings.Contains(err.Error(), "does not support grain") {
		t.Fatalf("time grain validation error = %v, want declared-grain diagnostic", err)
	}
	if err := validateRestoredDataExploreState(projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
		Time: &projectsignals.DataExploreTimeSignal{Field: "orders.created_at", Grain: "month"},
	}, projection, model, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}); err != nil {
		t.Fatalf("globally valid grain on physical binding rejected: %v", err)
	}
}

func TestValidateRestoredDataExploreStateAcceptsSafeRebase(t *testing.T) {
	model := &semanticmodel.Model{
		Name: "sales",
		Tables: map[string]semanticmodel.Table{
			"orders":    {ModelName: "orders", Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {Field: "orders.customer_id"}, "status": {Field: "orders.status"}}},
			"customers": {ModelName: "customers", Dimensions: map[string]semanticmodel.MetricDimension{"customer_id": {Field: "customers.customer_id"}, "region": {Field: "customers.region"}}},
		},
		Relationships: []semanticmodel.Relationship{{ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"}, ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one"}},
		Datasets:      map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
	}
	project := projectmanifest.Project{
		Models:         map[string]semanticmodel.Table{"model:orders": model.Tables["orders"], "model:customers": model.Tables["customers"]},
		SemanticModels: map[string]*semanticmodel.Model{"semantic:sales": model},
		NameIndex:      projectmanifest.NameIndex{Models: map[string]string{"orders": "model:orders", "customers": "model:customers"}},
	}
	assets := []projectview.DevelopAssetView{
		{ID: "model:orders", Type: string(projectview.AssetTypeModel), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModel), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	command := projectsignals.DataExploreCommand{
		SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("customers"),
		Dimensions: []string{"customers.region", "orders.status"},
	}
	projection := BuildDataExplorerProjection(assets, project, command, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
	if got := projectsignals.ValueOrZero(projection.Command.DatasetID); got != "orders" {
		t.Fatalf("safe rebase dataset = %q, want orders", got)
	}
	if err := validateRestoredDataExploreState(command, projection, model, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}); err != nil {
		t.Fatalf("safe rebase rejected: %v", err)
	}
}
