package http

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
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

func testExplorationCommand(spec exploration.ExplorationSpec) projectsignals.DataExploreCommand {
	if spec.SchemaVersion == 0 {
		spec.SchemaVersion = 1
	}
	if spec.Limit == 0 {
		spec.Limit = 100
	}
	if spec.Dimensions == nil {
		spec.Dimensions = []exploration.ExplorationDimensionRef{}
	}
	if spec.Metrics == nil {
		spec.Metrics = []exploration.ExplorationMetricRef{}
	}
	if spec.Filters == nil {
		spec.Filters = []exploration.ExplorationFilter{}
	}
	if spec.Sort == nil {
		spec.Sort = []exploration.ExplorationSort{}
	}
	return projectsignals.DataExploreCommand{Spec: spec}
}

func testStringFilter(field, operator string, values ...string) exploration.ExplorationFilter {
	items := make([]exploration.ExplorationFilterValue, 0, len(values))
	for _, value := range values {
		items = append(items, exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: value}})
	}
	op := operator
	var expression exploration.ExplorationFilterExpressionVariant
	switch operator {
	case "is_null", "is_not_null":
		expression = &exploration.NullCheckExplorationFilterExpression{Kind: "null_check", Operator: op}
	case "in", "not_in":
		expression = &exploration.SetExplorationFilterExpression{Kind: "set", Operator: op, Values: items}
	default:
		expression = &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: op, Value: items[0]}
	}
	return exploration.ExplorationFilter{Field: field, Expression: exploration.ExplorationFilterExpression{Value: expression}}
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
					"quantity":   {Label: "Quantity", Type: "number", Datatype: semanticmodel.DataTypeInteger},
					"amount":     {Label: "Amount", Type: "number", Datatype: semanticmodel.DataTypeDecimal},
					"order_date": {Label: "Order date", Type: "date", Datatype: semanticmodel.DataTypeDate},
					"active":     {Label: "Active", Type: "boolean", Datatype: semanticmodel.DataTypeBoolean},
					"event_at":   {Label: "Event at", Type: "timestamp", Datatype: semanticmodel.DataTypeDateTimeTZ},
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
			{ID: "model:orders", ProjectID: projectID, ServingStateID: "state", Type: "model_table", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
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
		"mode":      {"explore"},
		"model":     {"semantic:sales"},
		"dataset":   {"orders"},
		"dimension": {"orders.status"},
	}

	document := httptest.NewRecorder()
	h.Explore(document, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil))
	if document.Code != http.StatusOK {
		t.Fatalf("document status = %d, want 200: %s", document.Code, document.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("document executed %d analytical queries, want 0", executor.calls)
	}
	for _, want := range []string{"mode=explore", "v=2", "state="} {
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
		"mode":      {"explore"},
		"model":     {" semantic:sales "},
		"dataset":   {" orders "},
		"dimension": {" orders.status "},
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
	if !strings.Contains(body, "v=2") || !strings.Contains(body, "state=") || strings.Contains(body, "+orders.status+") {
		t.Fatalf("spaced field was not canonicalized in updates URL:\n%s", body)
	}
}

func TestDataExploreCommandFromQueryTrimsMetadataButPreservesFilterValues(t *testing.T) {
	command, err := dataExploreCommandFromQuery(url.Values{
		"dimension": {" orders.status "},
		"metric":    {" revenue "},
		"filter":    {`{"field":" orders.status ","operator":" equals ","dataset":" orders ","values":[" paid "]}`},
		"sort":      {`{"field":" revenue ","direction":" desc "}`},
		"time":      {`{"field":" orders.created_at ","grain":" month ","alias":" order_month "}`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(command.Spec.Dimensions) != 1 || command.Spec.Dimensions[0].Field != "orders.status" || len(command.Spec.Metrics) != 1 || command.Spec.Metrics[0].Field != "revenue" {
		t.Fatalf("trimmed fields = %#v/%#v", command.Spec.Dimensions, command.Spec.Metrics)
	}
	if len(command.Spec.Filters) != 1 || command.Spec.Filters[0].Field != "orders.status" || command.Spec.Filters[0].DatasetID == nil || *command.Spec.Filters[0].DatasetID != "orders" {
		t.Fatalf("trimmed filter metadata = %#v", command.Spec.Filters)
	}
	if len(command.Spec.Sort) != 1 || command.Spec.Sort[0].Field != "revenue" || command.Spec.Sort[0].Direction != "desc" {
		t.Fatalf("trimmed sort = %#v", command.Spec.Sort)
	}
	if command.Spec.Time == nil || command.Spec.Time.Field != "orders.created_at" || command.Spec.Time.Grain != "month" || projectsignals.ValueOrZero(command.Spec.Time.Alias) != "order_month" {
		t.Fatalf("trimmed time = %#v", command.Spec.Time)
	}
}

func TestDataExplorerLegacyURLAdaptsTypedFilterValues(t *testing.T) {
	tests := []struct {
		name     string
		field    string
		value    string
		wantKind string
		version  string
	}{
		{name: "integer", field: "orders.quantity", value: "42", wantKind: "integer", version: "1"},
		{name: "date", field: "orders.order_date", value: "2026-09-03", wantKind: "date", version: ""},
		{name: "boolean", field: "orders.active", value: "true", wantKind: "boolean", version: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, executor := newDataExplorerURLTestHandler(t)
			filter, err := json.Marshal(map[string]any{
				"field": test.field, "operator": "equals", "values": []string{test.value},
			})
			if err != nil {
				t.Fatal(err)
			}
			values := url.Values{
				"mode":      {"explore"},
				"model":     {"semantic:sales"},
				"dataset":   {"orders"},
				"dimension": {test.field},
				"filter":    {string(filter)},
			}
			if test.version != "" {
				values.Set("v", test.version)
			}
			recorder := httptest.NewRecorder()
			_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil), true)
			if !ok {
				t.Fatalf("legacy URL rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			if executor.calls != 1 {
				t.Fatalf("legacy URL executed %d analytical queries, want 1", executor.calls)
			}
			if len(explorer.Explore.Command.Spec.Filters) != 1 {
				t.Fatalf("filters = %#v", explorer.Explore.Command.Spec.Filters)
			}
			kind, err := explorer.Explore.Command.Spec.Filters[0].Expression.Value.(*exploration.ComparisonExplorationFilterExpression).Value.Kind()
			if err != nil || kind != test.wantKind {
				t.Fatalf("adapted filter kind = %q, %v; want %q", kind, err, test.wantKind)
			}
		})
	}
}

func TestDataExplorerLegacyURLAdaptsDecimalAndTimestampSetValues(t *testing.T) {
	decimalFilter, err := json.Marshal(map[string]any{
		"field": "orders.amount", "operator": "in", "values": []string{"1.25", "2.50"},
	})
	if err != nil {
		t.Fatal(err)
	}
	timestampFilter, err := json.Marshal(map[string]any{
		"field": "orders.event_at", "operator": "in", "values": []string{"2026-09-03T00:00:00Z", "2026-09-04T00:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	h, executor := newDataExplorerURLTestHandler(t)
	values := url.Values{
		"v":         {"1"},
		"mode":      {"explore"},
		"model":     {"semantic:sales"},
		"dataset":   {"orders"},
		"dimension": {"orders.amount", "orders.event_at"},
		"filter":    {string(decimalFilter), string(timestampFilter)},
	}
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil), true)
	if !ok {
		t.Fatalf("legacy URL rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("legacy URL executed %d analytical queries, want 1", executor.calls)
	}
	if len(explorer.Explore.Command.Spec.Filters) != 2 {
		t.Fatalf("filters = %#v, want decimal and timestamp set filters", explorer.Explore.Command.Spec.Filters)
	}
	wantKinds := []string{"decimal", "timestamp"}
	for index, wantKind := range wantKinds {
		expression, ok := explorer.Explore.Command.Spec.Filters[index].Expression.Value.(*exploration.SetExplorationFilterExpression)
		if !ok {
			t.Fatalf("filter %d expression = %T, want set", index, explorer.Explore.Command.Spec.Filters[index].Expression.Value)
		}
		if len(expression.Values) != 2 {
			t.Fatalf("filter %d values = %#v, want both legacy values adapted", index, expression.Values)
		}
		for valueIndex := range expression.Values {
			kind, err := expression.Values[valueIndex].Kind()
			if err != nil || kind != wantKind {
				t.Fatalf("filter %d value %d kind = %q, %v; want %q", index, valueIndex, kind, err, wantKind)
			}
		}
	}
}

func TestDataExplorerLegacyURLWithoutModelUsesProjectedDefaultModel(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	values := url.Values{
		"v":         {"1"},
		"mode":      {"explore"},
		"dataset":   {"orders"},
		"dimension": {"orders.status"},
	}
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil), true)
	if !ok {
		t.Fatalf("legacy URL without model rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 1 {
		t.Fatalf("legacy URL without model executed %d analytical queries, want 1", executor.calls)
	}
	if got := explorer.Explore.Command.Spec.ModelID; got != "semantic:sales" {
		t.Fatalf("restored default model = %q, want semantic:sales", got)
	}
}

func TestDataExplorerLegacyURLRejectsInvalidTypedFilterLiteralWithoutExecution(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	filter, err := json.Marshal(map[string]any{
		"field": "orders.quantity", "operator": "equals", "values": []string{"not-an-integer"},
	})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"v": {"1"}, "mode": {"explore"}, "model": {"semantic:sales"}, "dataset": {"orders"}, "dimension": {"orders.quantity"}, "filter": {string(filter)}}
	recorder := httptest.NewRecorder()
	_, _, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil), true)
	if ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid legacy URL accepted: ok=%v status=%d body=%s", ok, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "integer") {
		t.Fatalf("invalid legacy URL feedback = %q, want integer diagnostic", recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("invalid legacy URL executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerLegacyURLRejectsMissingProjectedLogicalType(t *testing.T) {
	command := testExplorationCommand(exploration.ExplorationSpec{
		ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Filters: []exploration.ExplorationFilter{testStringFilter("orders.status", "equals", "paid")},
	})
	err := adaptLegacyExplorationFilterValues(&command.Spec, []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", Compatible: true}})
	if err == nil || !strings.Contains(err.Error(), "no logical type") {
		t.Fatalf("missing logical type conversion error = %v, want fail-closed type diagnostic", err)
	}
}

func TestDataExplorerV2URLDoesNotInferLegacyFilterValueKind(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	dataset := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.quantity"}},
		Metrics:    []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{{Field: "orders.quantity", Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{
			Kind: "comparison", Operator: "equals", Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "42"}},
		}}}},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	state, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(state)}}
	recorder := httptest.NewRecorder()
	_, _, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil), true)
	if ok || recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong-kind v2 URL accepted: ok=%v status=%d body=%s", ok, recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "value kind") {
		t.Fatalf("wrong-kind v2 feedback = %q, want value-kind diagnostic", recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("wrong-kind v2 URL executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExploreV2URLRoundTripsFullSpec(t *testing.T) {
	alias := "order_month"
	grain := exploration.ExplorationTimeGrainMonth
	lower := "2025-01-01T00:00:00Z"
	upper := "2025-02-01T00:00:00Z"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1,
		ModelID:       "semantic:sales",
		DatasetID:     projectsignals.Optional("orders"),
		Dimensions:    []exploration.ExplorationDimensionRef{{Field: "orders.created_at", Alias: &alias, Grain: &grain}},
		Metrics:       []exploration.ExplorationMetricRef{{Field: "revenue", Alias: projectsignals.Optional("total_revenue")}},
		Filters: []exploration.ExplorationFilter{{Field: "orders.status", Expression: exploration.ExplorationFilterExpression{Value: &exploration.SetExplorationFilterExpression{
			Kind: "set", Operator: "in",
			Values: []exploration.ExplorationFilterValue{{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}}},
		}}}},
		Time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainMonth, Range: &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{
			Kind: "absolute", Lower: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: lower}}, Inclusive: true}, Upper: &exploration.ExplorationTimeBound{Value: exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: upper}}, Inclusive: false},
		}}},
		Sort: []exploration.ExplorationSort{{Field: "revenue", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 250,
		Pivot:         &exploration.ExplorationPivotConfig{Rows: []exploration.ExplorationDimensionRef{{Field: "orders.created_at"}}, Columns: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{{Field: "revenue"}}, Window: &exploration.ExplorationPivotWindow{Limit: 50}},
		Table:         &exploration.ExplorationTableDisplayConfig{Columns: &[]exploration.ExplorationTableColumn{{Field: "revenue", Width: projectsignals.Pointer(int32(120))}}},
		Visualization: &exploration.ExplorationVisualizationConfig{Value: &exploration.TableExplorationVisualization{Kind: "table", Columns: []exploration.ExplorationVisualizationFieldRef{{Field: "revenue"}}}},
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	command, err := dataExploreCommandFromQuery(url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(raw)}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(command.Spec, spec) {
		t.Fatalf("full spec changed across v2 URL decode:\nwant %#v\n got %#v", spec, command.Spec)
	}
}

func TestDataExplorerURLRejectsAmbiguousOrIncompatibleShape(t *testing.T) {
	tests := []struct {
		name   string
		values url.Values
	}{
		{name: "duplicate version", values: url.Values{"v": {"1", "1"}, "object": {"model:orders"}}},
		{name: "duplicate mode", values: url.Values{"mode": {"browse", "browse"}, "object": {"model:orders"}}},
		{name: "duplicate object", values: url.Values{"object": {"model:orders", "model:orders"}}},
		{name: "duplicate model", values: url.Values{"mode": {"explore"}, "model": {"semantic:sales", "semantic:sales"}}},
		{name: "duplicate dataset", values: url.Values{"mode": {"explore"}, "dataset": {"orders", "orders"}}},
		{name: "duplicate time", values: url.Values{"mode": {"explore"}, "time": {`{"field":"orders.created_at","grain":"day"}`, `{"field":"orders.created_at","grain":"day"}`}}},
		{name: "duplicate limit", values: url.Values{"mode": {"explore"}, "limit": {"100", "100"}}},
		{name: "blank browse object", values: url.Values{"object": {"  "}}},
		{name: "blank model", values: url.Values{"mode": {"explore"}, "model": {""}}},
		{name: "blank dataset", values: url.Values{"mode": {"explore"}, "dataset": {""}}},
		{name: "blank time", values: url.Values{"mode": {"explore"}, "time": {""}}},
		{name: "blank limit", values: url.Values{"mode": {"explore"}, "limit": {""}}},
		{name: "unsupported version in browse", values: url.Values{"v": {"2"}, "object": {"model:orders"}}},
		{name: "unsupported mode", values: url.Values{"mode": {"preview"}, "object": {"model:orders"}}},
		{name: "missing mode with explore operands", values: url.Values{"dimension": {"orders.status"}}},
		{name: "browse with explore operands", values: url.Values{"mode": {"browse"}, "metric": {"revenue"}}},
		{name: "explore with object", values: url.Values{"mode": {"explore"}, "object": {"model:orders"}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			h, _ := newDataExplorerURLTestHandler(t)
			recorder := httptest.NewRecorder()
			_, _, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/explore?"+test.values.Encode(), nil), false)
			if ok {
				t.Fatalf("URL was accepted: %s", recorder.Body.String())
			}
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}

func TestDataExploreCommandFromQueryRejectsDuplicateSortFields(t *testing.T) {
	_, err := dataExploreCommandFromQuery(url.Values{"sort": {
		`{"field":"revenue","direction":"asc"}`,
		`{"field":"revenue","direction":"desc"}`,
	}})
	if err == nil || !strings.Contains(err.Error(), "specified more than once") {
		t.Fatalf("duplicate sort fields error = %v, want duplicate diagnostic", err)
	}
}

func TestDataExploreCommandFromQueryRejectsDuplicateFieldIdentifiers(t *testing.T) {
	tests := []url.Values{
		{"dimension": {"orders.status", "orders.status"}},
		{"metric": {"revenue", "revenue"}},
		{"dimension": {"orders.status"}, "metric": {"orders.status"}},
		{"dimension": {" orders.status ", "orders.status"}},
	}
	for _, values := range tests {
		if command, err := dataExploreCommandFromQuery(values); err == nil {
			t.Fatalf("duplicate field URL accepted as %#v", command)
		}
	}
}

func TestDataExploreCommandFromQueryNormalizesBlankTimeAliasToAbsent(t *testing.T) {
	command, err := dataExploreCommandFromQuery(url.Values{"time": {`{"field":"orders.created_at","grain":"day","alias":"  "}`}})
	if err != nil {
		t.Fatal(err)
	}
	if command.Spec.Time == nil {
		t.Fatal("time selection was dropped")
	}
	if command.Spec.Time.Alias != nil {
		t.Fatalf("blank alias = %q, want absent", *command.Spec.Time.Alias)
	}
}

func TestDataExplorerRestoredURLFailsClosedForStaleField(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	values := url.Values{
		"mode":      {"explore"},
		"model":     {"semantic:sales"},
		"dataset":   {"orders"},
		"dimension": {"orders.removed_status"},
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

func TestDataExplorerRestoredURLFailsClosedForUnauthorizedModel(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	h.Graph = browserGraphStub{graph: servingstate.AssetGraph{Assets: []servingstate.Asset{
		{ID: "model:orders", ProjectID: "project:test", ServingStateID: "state", Type: "model_table", Key: "orders", Title: "Orders", PayloadJSON: `{}`},
	}}}
	recorder := httptest.NewRecorder()
	state, err := json.Marshal(exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(state)}}
	_, _, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/explore?"+values.Encode(), nil), true)
	if ok {
		t.Fatal("exploration restored against an unauthorized model")
	}
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("unauthorized model status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "semantic:sales") || !strings.Contains(recorder.Body.String(), "no longer available") {
		t.Fatalf("unauthorized model feedback = %q, want actionable model diagnostic", recorder.Body.String())
	}
	if executor.calls != 0 {
		t.Fatalf("unauthorized model executed %d analytical queries, want 0", executor.calls)
	}
}

func TestDataExplorerRestoredV2URLAcceptsSortByExplicitMetricAlias(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	state, err := json.Marshal(exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []exploration.ExplorationMetricRef{{Field: "revenue", Alias: projectsignals.Optional("total_revenue")}},
		Filters:    []exploration.ExplorationFilter{},
		Sort:       []exploration.ExplorationSort{{Field: "total_revenue", Direction: exploration.ExplorationSortDirectionDesc}}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"v": {"2"}, "mode": {"explore"}, "state": {string(state)}}
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?"+values.Encode(), nil), true)
	if !ok {
		t.Fatalf("sort alias URL rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if explorer.Explore.Command.Spec.Sort[0].Field != "total_revenue" || executor.calls != 1 {
		t.Fatalf("restored sort/execution = %#v/%d", explorer.Explore.Command.Spec.Sort, executor.calls)
	}
}

func TestDataExplorerRestoredV2URLAcceptsTimeOnlySortAlias(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	state, err := json.Marshal(exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: exploration.ExplorationTimeGrainDay, Alias: projectsignals.Optional("order_day")},
		Sort: []exploration.ExplorationSort{{Field: "order_day", Direction: exploration.ExplorationSortDirectionAsc}}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?mode=explore&v=2&state="+url.QueryEscape(string(state)), nil), true)
	if !ok {
		t.Fatalf("time-only sort URL rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 1 || len(explorer.Explore.Command.Spec.Sort) != 1 || explorer.Explore.Command.Spec.Sort[0].Field != "order_day" {
		t.Fatalf("restored time sort/execution = %#v/%d, want alias sort and one query", explorer.Explore.Command.Spec.Sort, executor.calls)
	}
}

func TestDataExplorerRestoredV2URLAcceptsDateTimeTzTimeSelection(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	state, err := json.Marshal(exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Time: &exploration.ExplorationTimeSelection{Field: "orders.event_at", Grain: exploration.ExplorationTimeGrainDay},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?mode=explore&v=2&state="+url.QueryEscape(string(state)), nil), true)
	if !ok {
		t.Fatalf("DateTimeTz time URL rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 1 || explorer.Explore.Command.Spec.Time == nil || explorer.Explore.Command.Spec.Time.Field != "orders.event_at" {
		t.Fatalf("restored DateTimeTz time/execution = %#v/%d, want event_at and one query", explorer.Explore.Command.Spec.Time, executor.calls)
	}
}

func TestDataExplorerRestoredV2URLAcceptsDateTimeTzDimensionGrain(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	grain := exploration.ExplorationTimeGrainDay
	state, err := json.Marshal(exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.event_at", Grain: &grain}}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	_, explorer, ok := h.dataExplorerSignalsForURL(recorder, httptest.NewRequest(http.MethodGet, "/updates?mode=explore&v=2&state="+url.QueryEscape(string(state)), nil), true)
	if !ok {
		t.Fatalf("DateTimeTz dimension URL rejected: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if executor.calls != 1 || len(explorer.Explore.Command.Spec.Dimensions) != 1 || explorer.Explore.Command.Spec.Dimensions[0].Field != "orders.event_at" {
		t.Fatalf("restored DateTimeTz dimension/execution = %#v/%d, want event_at and one query", explorer.Explore.Command.Spec.Dimensions, executor.calls)
	}
}

func TestDataExplorerRestoredURLFailsClosedWhenBindingsAreUnavailable(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	definition := h.ProjectDefinitionReader.(browserProjectDefinitionStub)
	definition.compiled = nil
	h.ProjectDefinitionReader = definition
	values := url.Values{
		"mode":      {"explore"},
		"model":     {"semantic:sales"},
		"dataset":   {"orders"},
		"dimension": {"orders.status"},
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
		"mode":      {"explore"},
		"model":     {"semantic:sales"},
		"dataset":   {"orders"},
		"dimension": {"orders.status"},
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
		Models:   []projectsignals.DataExploreModelSignal{{ID: "semantic:sales"}},
		Datasets: []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Fields:   make([]projectsignals.DataExploreFieldSignal, 0, len(fields)),
		Command:  testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders")}),
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
		{name: "dimension wrong kind", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Metrics: []exploration.ExplorationMetricRef{{Field: "orders.status"}}}), want: "not a metric"},
		{name: "filter wrong kind", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Filters: []exploration.ExplorationFilter{testStringFilter("revenue", "equals", "1")}}), want: "not a dimension"},
		{name: "sort not selected", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Sort: []exploration.ExplorationSort{{Field: "orders.status", Direction: "asc"}}}), want: "not selected"},
		{name: "incompatible", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.incompatible"}}}), want: "no safe relationship path"},
		{name: "time wrong kind", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Time: &exploration.ExplorationTimeSelection{Field: "orders.status", Grain: "month"}}), want: "not a date or timestamp"},
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
		Models:   []projectsignals.DataExploreModelSignal{{ID: "semantic:sales"}},
		Datasets: []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Fields:   []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("string")}},
		Command:  testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders")}),
	}
	compiled, err := semanticquery.CompileDatasetBindings(&semanticmodel.Model{
		Name: "sales", Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders"}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, operator := range []string{"in", "not_in"} {
		err := validateRestoredDataExploreState(testExplorationCommand(exploration.ExplorationSpec{
			ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
			Filters: []exploration.ExplorationFilter{testStringFilter("orders.status", operator)},
		}), projection, nil, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
		if err == nil || !strings.Contains(err.Error(), "at least one value") {
			t.Fatalf("empty %s filter error = %v, want non-empty arity diagnostic", operator, err)
		}
	}
}

func TestValidateRestoredDataExploreStateRejectsUnavailableTargets(t *testing.T) {
	projection := DataExplorerProjection{
		Models:   []projectsignals.DataExploreModelSignal{{ID: "semantic:sales"}},
		Datasets: []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Command:  testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders")}),
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
		{name: "model", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:removed"}), want: "model \"semantic:removed\" is no longer available"},
		{name: "dataset", command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("removed")}), want: "dataset \"removed\" is no longer available"},
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
		Models:   []projectsignals.DataExploreModelSignal{{ID: "semantic:sales"}},
		Datasets: []projectsignals.DataExploreDatasetSignal{{ID: "orders"}, {ID: "customers"}, {ID: "other"}},
		Fields: []projectsignals.DataExploreFieldSignal{
			{ID: "orders.status", Kind: "dimension", Compatible: true},
			{ID: "combined", Kind: "metric", Compatible: true},
		},
		Command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders")}),
	}
	filter := func(dataset string) []exploration.ExplorationFilter {
		f := testStringFilter("orders.status", "equals", "paid")
		f.DatasetID = projectsignals.Optional(dataset)
		return []exploration.ExplorationFilter{f}
	}
	singleRoot := testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"), Filters: filter("customers")})
	if err := validateRestoredDataExploreState(singleRoot, base, model, compiledModels); err == nil || !strings.Contains(err.Error(), "does not participate") {
		t.Fatalf("single-root filter dataset error = %v, want participation diagnostic", err)
	}
	multiRoot := singleRoot
	multiRoot.Spec.Metrics = []exploration.ExplorationMetricRef{{Field: "combined"}}
	if err := validateRestoredDataExploreState(multiRoot, base, model, compiledModels); err != nil {
		t.Fatalf("participating multi-root filter dataset rejected: %v", err)
	}
	nonParticipating := multiRoot
	nonParticipating.Spec.Filters = filter("other")
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
		Models:   []projectsignals.DataExploreModelSignal{{ID: "semantic:sales"}},
		Datasets: []projectsignals.DataExploreDatasetSignal{{ID: "orders"}},
		Fields: []projectsignals.DataExploreFieldSignal{
			{ID: "created", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("timestamp")},
			{ID: "orders.created_at", Kind: "dimension", Compatible: true, Type: projectsignals.Optional("timestamp")},
		},
		Command: testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders")}),
	}
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatal(err)
	}
	err = validateRestoredDataExploreState(testExplorationCommand(exploration.ExplorationSpec{
		ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Time: &exploration.ExplorationTimeSelection{Field: "created", Grain: "month"},
	}), projection, model, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
	if err == nil || !strings.Contains(err.Error(), "grain") {
		t.Fatalf("time grain validation error = %v, want declared-grain diagnostic", err)
	}
	if err := validateRestoredDataExploreState(testExplorationCommand(exploration.ExplorationSpec{
		ModelID: "semantic:sales", DatasetID: projectsignals.Optional("orders"),
		Time: &exploration.ExplorationTimeSelection{Field: "orders.created_at", Grain: "month"},
	}), projection, model, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}); err != nil {
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
		{ID: "model:orders", Type: string(projectview.AssetTypeModelTable), Key: "orders", Title: "Orders"},
		{ID: "model:customers", Type: string(projectview.AssetTypeModelTable), Key: "customers", Title: "Customers"},
		{ID: "semantic:sales", Type: string(projectview.AssetTypeSemanticModel), Key: "sales", Title: "Sales"},
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		t.Fatal(err)
	}
	command := testExplorationCommand(exploration.ExplorationSpec{ModelID: "semantic:sales", DatasetID: projectsignals.Optional("customers"), Dimensions: []exploration.ExplorationDimensionRef{{Field: "customers.region"}, {Field: "orders.status"}}})
	projection := BuildDataExplorerProjection(assets, project, command, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled})
	if got := projectsignals.ValueOrZero(projection.Command.Spec.DatasetID); got != "orders" {
		t.Fatalf("safe rebase dataset = %q, want orders", got)
	}
	if err := validateRestoredDataExploreState(command, projection, model, map[string]*semanticquery.CompiledModel{"semantic:sales": compiled}); err != nil {
		t.Fatalf("safe rebase rejected: %v", err)
	}
}
