//go:build fai543experiment

package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	materializeruntime "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	semanticapi "github.com/flidai/leapview/internal/dashboard/semanticapi"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/pkg/arrowresult"
)

const dashboardDirectArrowExperimentQueryID = "fai-543-direct-arrow"

func dashboardDirectArrowExperimentQuery(
	visual visualizationdefinition.Definition,
	model *semanticmodel.Model,
	filters []dataquery.Filter,
	offset, limit int,
	block string,
) (dataquery.Query, semanticapi.DirectArrowExperimentConfig, error) {
	if block != "a" {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment excludes multi-block shaping")
	}
	if visual.Query.Kind != visualizationdefinition.QueryDetail || visual.Query.ResultShape != visualizationdefinition.ResultDetailWindow || visual.Query.Detail == nil {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment only supports ordinary detail queries")
	}
	if _, ok := visual.Spec.Value.(*visualizationir.TableVisualizationSpec); !ok {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment only supports table visualizations")
	}
	base, err := visualizationir.SpecificationBase(visual.Spec)
	if err != nil {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, err
	}
	if base.Calculations != nil && len(*base.Calculations) > 0 {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment excludes calculations")
	}
	if model == nil {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment semantic model is required")
	}
	if offset < 0 || limit <= 0 {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment requires a non-negative offset and positive limit")
	}
	if visual.Query.Detail.Limit <= 0 || int64(limit) > visual.Query.Detail.Limit {
		return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, errors.New("direct Arrow experiment limit exceeds the compiled detail budget")
	}

	fields := make([]dataquery.Field, 0, len(visual.Query.Detail.Fields))
	metrics := make([]dataquery.Field, 0, len(visual.Query.Detail.Fields))
	projection := make([]string, 0, len(visual.Query.Detail.Fields))
	aliases := make(map[string]struct{}, len(visual.Query.Detail.Fields))
	for _, binding := range visual.Query.Detail.Fields {
		field := dataquery.Field{Field: binding.FieldID, Alias: binding.Alias}
		projection = append(projection, binding.Alias)
		aliases[binding.Alias] = struct{}{}
		if _, err := model.ResolveDimension(binding.FieldID); err == nil {
			fields = append(fields, field)
			continue
		}
		if _, err := model.ResolveMetric(binding.FieldID); err != nil {
			return dataquery.Query{}, semanticapi.DirectArrowExperimentConfig{}, fmt.Errorf("direct Arrow experiment field %q: %w", binding.FieldID, err)
		}
		metrics = append(metrics, field)
	}

	sortKey, sortDirection := "", "desc"
	if len(visual.Query.Detail.DefaultSort) > 0 {
		sortKey = visual.Query.Detail.DefaultSort[0].FieldID
		sortDirection = visual.Query.Detail.DefaultSort[0].Direction
	}
	if _, ok := aliases[sortKey]; !ok {
		if _, hasIdentity := aliases["order_id"]; hasIdentity {
			sortKey = "order_id"
		} else if len(visual.Query.Detail.Fields) > 0 {
			sortKey = visual.Query.Detail.Fields[0].Alias
		}
	}
	if sortDirection == "" {
		sortDirection = "desc"
	}
	sorts := []dataquery.Sort{}
	if sortKey != "" {
		sorts = append(sorts, dataquery.Sort{Field: sortKey, Direction: sortDirection})
	}
	if sortKey != "order_id" {
		if _, hasIdentity := aliases["order_id"]; hasIdentity {
			sorts = append(sorts, dataquery.Sort{Field: "order_id", Direction: "asc"})
		}
	}

	logicalTypes := map[string]string{}
	labels := map[string]string{}
	for _, dataset := range base.Datasets {
		if dataset.ID != visual.Query.DatasetID {
			continue
		}
		for _, field := range dataset.Fields {
			logicalTypes[field.ID] = string(field.DataType)
			labels[field.ID] = field.Label
		}
	}
	request := dataquery.Query{
		ProjectID: dashboardBaselineProjectID,
		Surface:   dataquery.SurfaceAPI,
		Operation: dataquery.OperationDashboardRows,
		ModelID:   visual.Query.ModelID,
		Kind:      dataquery.KindSemanticRows,
		Target:    visual.Query.Detail.TableID,
		Fields:    fields,
		Metrics:   metrics,
		Filters:   append([]dataquery.Filter(nil), filters...),
		Sort:      sorts,
		Offset:    offset,
		Limit:     limit + 1,
	}
	config := semanticapi.DirectArrowExperimentConfig{
		QueryID:       dashboardDirectArrowExperimentQueryID,
		Snapshot:      dashboardBaselineSnapshot,
		CursorScope:   "fai-543-prototype-scope",
		SchemaVersion: dashboardNativeArrowSchemaVersion,
		SpecRevision:  visual.SpecRevision,
		DataRevision:  "1",
		LogicalTypes:  logicalTypes,
		Labels:        labels,
		Projection:    projection,
		Limit:         limit,
		Offset:        offset,
	}
	return request, config, nil
}

func dashboardDirectArrowMixedDetailDefinition() (visualizationdefinition.Definition, error) {
	fields := []visualizationir.VisualizationField{
		{ID: "field_00", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeInteger, Nullable: true, Label: "Dimension A"},
		{ID: "field_06", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "Metric A"},
		{ID: "field_01", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeFloat, Nullable: true, Label: "Dimension B"},
		{ID: "field_15", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "Metric B"},
	}
	columns := make([]visualizationir.TableVisualizationColumn, len(fields))
	for index, field := range fields {
		columns[index] = visualizationir.TableVisualizationColumn{
			Field:      visualizationir.VisualizationFieldRef{Dataset: "primary", Field: field.ID},
			Label:      field.Label,
			Formatting: []visualizationir.TableVisualizationFormattingRule{},
		}
	}
	base := dashboardBaselineSpecBase("detail_mixed", fields)
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{
		VisualizationSpecBase: base,
		Kind:                  "table",
		Columns:               columns,
		Presentation:          visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true},
	}}
	return visualizationdefinition.New("detail_mixed", spec, visualizationdefinition.QueryBinding{
		Kind:        visualizationdefinition.QueryDetail,
		ResultShape: visualizationdefinition.ResultDetailWindow,
		ModelID:     dashboardBaselineModelID.String(),
		DatasetID:   "primary",
		Detail: &visualizationdefinition.DetailQueryBinding{
			TableID: dashboardBaselineDatasetID,
			Fields: []visualizationdefinition.FieldBinding{
				{FieldID: "orders.field_00", Alias: "field_00"},
				{FieldID: "value_metric", Alias: "field_06"},
				{FieldID: "orders.field_01", Alias: "field_01"},
				{FieldID: "value_metric_b", Alias: "field_15"},
			},
			Limit: 1_000,
		},
	})
}

func (f *dashboardBaselineFixture) serveDirectArrowExperiment(tb testing.TB, visual string, offset, limit int) ([]byte, dashboardBaselineObservation, dataquery.Result) {
	tb.Helper()
	resolved, err := f.service.Resolver().Resolve(projectgraph.ResourceID(dashboardBaselineDashboardID))
	if err != nil {
		tb.Fatal(err)
	}
	visualDefinition, ok := resolved.Visualization(visual)
	if !ok {
		tb.Fatalf("direct Arrow experiment visualization %q not found", visual)
	}
	request, config, err := dashboardDirectArrowExperimentQuery(visualDefinition, resolved.Model, nil, offset, limit, "a")
	if err != nil {
		tb.Fatal(err)
	}
	observation := dashboardBaselineObservation{}
	ctx := dashboardBaselineObservationContext(context.Background(), &observation)
	ctx = dataquery.WithGovernor(ctx, f.governor)
	if f.decorateContext != nil {
		ctx = f.decorateContext(ctx)
	}
	metadata := dataquery.MetadataFromContext(ctx)
	metadata.ProjectID = dashboardBaselineProjectID
	metadata.Surface = dataquery.SurfaceAPI
	// Match the control handler: dashboard API metadata deliberately leaves the
	// operation empty so the owned dashboard_rows query identity is authoritative.
	metadata.Operation = ""
	metadata.RequestID = "fai-540-baseline"
	metadata.ObjectType = "dashboard_visual"
	metadata.ObjectID = dashboardBaselineDashboardID + ":" + visual
	ctx = dataquery.WithMetadata(ctx, metadata)
	recorder := httptest.NewRecorder()
	result, err := semanticapi.ExecuteDirectArrowExperiment(ctx, recorder, f.core, request, config)
	if err != nil {
		tb.Fatal(err)
	}
	response := recorder.Result()
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		tb.Fatal(err)
	}
	observation.nextCursor = response.Trailer.Get("X-Next-Cursor")
	return body, observation, result
}

func BenchmarkDashboardDirectArrowExperiment(b *testing.B) {
	for _, workload := range []dashboardBaselineWorkload{
		{name: "detail_narrow", visual: "detail_narrow", columns: dashboardBaselineNarrowFields},
		{name: "detail_wide", visual: "detail_wide", columns: dashboardBaselineWideFields},
	} {
		for _, rows := range []int{50, 999} {
			b.Run(workload.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				for _, lane := range []string{"control_api_direct", "candidate_governed_native_v1"} {
					b.Run(lane, func(b *testing.B) {
						fixture := newDashboardBaselineFixture(b, rows)
						arrowBefore := arrowresult.Stats()
						samples := make([]time.Duration, b.N)
						var totalBytes, physicalQueries, cacheOutcomes int64
						b.ReportAllocs()
						b.ResetTimer()
						for index := 0; index < b.N; index++ {
							started := time.Now()
							var body []byte
							var observation dashboardBaselineObservation
							if lane == "control_api_direct" {
								body, observation = fixture.serve(b, workload.visual, rows+1, "arrow")
							} else {
								body, observation, _ = fixture.serveDirectArrowExperiment(b, workload.visual, 0, rows)
							}
							samples[index] = time.Since(started)
							dashboardBaselineBodySink = body
							totalBytes += int64(len(body))
							physicalQueries += int64(observation.physicalQueries)
							cacheOutcomes += int64(len(observation.outcomes))
						}
						b.StopTimer()
						if physicalQueries != int64(b.N) {
							b.Fatalf("%s physical queries = %d, want %d", lane, physicalQueries, b.N)
						}
						if cacheOutcomes != 0 {
							b.Fatalf("%s observed %d retained-cache outcomes", lane, cacheOutcomes)
						}
						arrowAfter := arrowresult.Stats()
						if arrowAfter != arrowBefore {
							b.Fatalf("%s changed retained Arrow ownership: before=%#v after=%#v", lane, arrowBefore, arrowAfter)
						}
						sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
						b.ReportMetric(float64(samples[quantileIndex(len(samples), 0.50)].Nanoseconds()), "p50-ns/op")
						b.ReportMetric(float64(samples[quantileIndex(len(samples), 0.95)].Nanoseconds()), "p95-ns/op")
						b.ReportMetric(float64(samples[quantileIndex(len(samples), 0.99)].Nanoseconds()), "p99-ns/op")
						b.ReportMetric(float64(totalBytes)/float64(b.N), "ipc-bytes/op")
						b.ReportMetric(float64(physicalQueries)/float64(b.N), "physical-queries/op")
						b.ReportMetric(float64(cacheOutcomes)/float64(b.N), "cache-outcomes/op")
						b.ReportMetric(float64(rows), "rows/op")
						b.ReportMetric(float64(workload.columns), "columns/op")
					})
				}
			})
		}
	}
}

func quantileIndex(length int, quantile float64) int {
	if length <= 1 {
		return 0
	}
	index := int(float64(length-1) * quantile)
	if index >= length {
		return length - 1
	}
	return index
}

func TestDashboardDirectArrowExperimentMatchesControlProjection(t *testing.T) {
	for _, workload := range []dashboardBaselineWorkload{
		{name: "detail_narrow", visual: "detail_narrow", columns: dashboardBaselineNarrowFields},
		{name: "detail_wide", visual: "detail_wide", columns: dashboardBaselineWideFields},
	} {
		t.Run(workload.name, func(t *testing.T) {
			const rows = 50
			fixture := newDashboardBaselineFixture(t, rows)
			var plans []semanticquery.Plan
			fixture.database.observePlan = func(plan semanticquery.Plan) { plans = append(plans, plan) }
			var governed atomic.Int64
			fixture.governor.calls = &governed
			fixture.database.evidenceMetadata = true
			control, controlObservation := fixture.serve(t, workload.visual, rows+1, "arrow")
			candidate, candidateObservation, result := fixture.serveDirectArrowExperiment(t, workload.visual, 0, rows)

			if controlObservation.physicalQueries != 1 || candidateObservation.physicalQueries != 1 {
				t.Fatalf("physical queries control/candidate = %d/%d", controlObservation.physicalQueries, candidateObservation.physicalQueries)
			}
			if len(controlObservation.outcomes) != 0 || len(candidateObservation.outcomes) != 0 {
				t.Fatalf("direct comparison touched retained cache: control=%v candidate=%v", controlObservation.outcomes, candidateObservation.outcomes)
			}
			if governed.Load() != 2 {
				t.Fatalf("governance calls for control/candidate = %d, want one per lane", governed.Load())
			}
			if len(plans) != 2 {
				t.Fatalf("physical plans captured = %d, want control and candidate", len(plans))
			}
			if plans[0].SQL != plans[1].SQL || !reflect.DeepEqual(plans[0].Columns, plans[1].Columns) {
				t.Fatalf("control/candidate physical plans differ:\ncontrol=%#v\ncandidate=%#v", plans[0], plans[1])
			}
			if result.RowsReturned != rows {
				t.Fatalf("candidate rows returned = %d, want %d", result.RowsReturned, rows)
			}
			assertDashboardDirectArrowExperimentEquivalent(t, control, candidate, rows, workload.columns)
		})
	}
}

func TestDashboardDirectArrowExperimentPreservesMixedVisualizationProjectionOrder(t *testing.T) {
	visual, err := dashboardDirectArrowMixedDetailDefinition()
	if err != nil {
		t.Fatal(err)
	}
	request, config, err := dashboardDirectArrowExperimentQuery(visual, dashboardBaselineModel(), nil, 0, 1, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := config.Projection, []string{"field_00", "field_06", "field_01", "field_15"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("declared visualization projection = %v, want %v", got, want)
	}
	if got, want := []string{request.Fields[0].Alias, request.Fields[1].Alias, request.Metrics[0].Alias, request.Metrics[1].Alias}, []string{"field_00", "field_01", "field_06", "field_15"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("grouped governed projection = %v, want %v", got, want)
	}

	schema := arrow.NewSchema([]arrow.Field{
		{Name: "field_00", Type: arrow.PrimitiveTypes.Int64},
		{Name: "field_01", Type: arrow.PrimitiveTypes.Float64},
		{Name: "field_06", Type: arrow.PrimitiveTypes.Float64},
		{Name: "field_15", Type: arrow.PrimitiveTypes.Float64},
	}, nil)
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	builders := []array.Builder{
		array.NewInt64Builder(allocator),
		array.NewFloat64Builder(allocator),
		array.NewFloat64Builder(allocator),
		array.NewFloat64Builder(allocator),
	}
	builders[0].(*array.Int64Builder).Append(10)
	builders[1].(*array.Float64Builder).Append(1.5)
	builders[2].(*array.Float64Builder).Append(6.5)
	builders[3].(*array.Float64Builder).Append(15.5)
	columns := make([]arrow.Array, len(builders))
	for index, builder := range builders {
		columns[index] = builder.NewArray()
		builder.Release()
	}
	record := array.NewRecordBatch(schema, columns, 1)
	for _, column := range columns {
		column.Release()
	}
	recorder := httptest.NewRecorder()
	_, err = semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{
		schema: schema, record: record, releaseAfterWrite: true,
	}, request, config)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := ipc.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	gotNames := make([]string, reader.Schema().NumFields())
	for index, field := range reader.Schema().Fields() {
		gotNames[index] = field.Name
	}
	if !reflect.DeepEqual(gotNames, config.Projection) {
		t.Fatalf("native-v1 schema order = %v, want %v", gotNames, config.Projection)
	}
	if !reader.Next() {
		t.Fatalf("read mixed projection record: %v", reader.Err())
	}
	gotValues := []any{
		reader.Record().Column(0).(*array.Int64).Value(0),
		reader.Record().Column(1).(*array.Float64).Value(0),
		reader.Record().Column(2).(*array.Float64).Value(0),
		reader.Record().Column(3).(*array.Float64).Value(0),
	}
	if want := []any{int64(10), 6.5, 1.5, 15.5}; !reflect.DeepEqual(gotValues, want) {
		t.Fatalf("native-v1 mixed values = %v, want %v", gotValues, want)
	}
	reader.Release()
	allocator.AssertSize(t, 0)
}

func TestDashboardDirectArrowExperimentUsesEquivalentGovernedQueryAndContext(t *testing.T) {
	const rows = 50
	fixture := newDashboardBaselineFixture(t, rows)
	admitter := &dashboardDirectArrowCapturingAdmitter{}
	observations := []dashboardDirectArrowGovernanceObservation{}
	fixture.governor.observe = func(ctx context.Context, request dataquery.Query) {
		budget, found := dataquery.ResultBudgetFromContext(ctx)
		observations = append(observations, dashboardDirectArrowGovernanceObservation{
			query: request, metadata: dataquery.MetadataFromContext(ctx), budget: budget, hasBudget: found,
		})
	}
	fixture.decorateContext = func(ctx context.Context) context.Context {
		metadata := dataquery.MetadataFromContext(ctx)
		metadata.PrincipalID = "fai-543-principal"
		metadata.CorrelationID = "fai-543-correlation"
		ctx = dataquery.WithMetadata(ctx, metadata)
		ctx = dataquery.WithIndependentResultBudget(ctx, dataquery.ResultLimits{MaxRows: 1_000, MaxBytes: 64 << 20})
		return workload.WithAdmitter(ctx, admitter)
	}

	control, controlObservation := fixture.serve(t, "detail_narrow", rows+1, "arrow")
	candidate, candidateObservation, _ := fixture.serveDirectArrowExperiment(t, "detail_narrow", 0, rows)
	assertDashboardDirectArrowExperimentEquivalent(t, control, candidate, rows, dashboardBaselineNarrowFields)
	if len(observations) != 2 {
		t.Fatalf("control/candidate governance observations = %d, want 2", len(observations))
	}

	controlQuery, candidateQuery := observations[0].query, observations[1].query
	if !reflect.DeepEqual(append([]dataquery.Field{}, controlQuery.Fields...), append([]dataquery.Field{}, candidateQuery.Fields...)) ||
		!reflect.DeepEqual(append([]dataquery.Field{}, controlQuery.Metrics...), append([]dataquery.Field{}, candidateQuery.Metrics...)) ||
		!reflect.DeepEqual(append([]dataquery.Filter{}, controlQuery.Filters...), append([]dataquery.Filter{}, candidateQuery.Filters...)) ||
		!reflect.DeepEqual(append([]dataquery.Sort{}, controlQuery.Sort...), append([]dataquery.Sort{}, candidateQuery.Sort...)) {
		t.Fatalf("control/candidate query shape differs:\ncontrol=%#v\ncandidate=%#v", controlQuery, candidateQuery)
	}
	if controlQuery.ProjectID != candidateQuery.ProjectID ||
		controlQuery.Surface != candidateQuery.Surface ||
		controlQuery.Operation != candidateQuery.Operation ||
		controlQuery.PrincipalID != candidateQuery.PrincipalID ||
		controlQuery.RequestID != candidateQuery.RequestID ||
		controlQuery.ObjectType != candidateQuery.ObjectType ||
		controlQuery.ObjectID != candidateQuery.ObjectID ||
		controlQuery.CorrelationID != candidateQuery.CorrelationID ||
		controlQuery.ModelID != candidateQuery.ModelID ||
		controlQuery.Kind != candidateQuery.Kind ||
		controlQuery.Target != candidateQuery.Target ||
		controlQuery.Offset != candidateQuery.Offset ||
		controlQuery.Limit != candidateQuery.Limit ||
		controlQuery.IncludeTotal != candidateQuery.IncludeTotal {
		t.Fatalf("control/candidate governed query identity differs:\ncontrol=%#v\ncandidate=%#v", controlQuery, candidateQuery)
	}
	if controlQuery.Limit != rows+1 || controlQuery.Offset != 0 {
		t.Fatalf("comparison physical pagination = offset %d limit %d, want 0/%d", controlQuery.Offset, controlQuery.Limit, rows+1)
	}
	if controlObservation.nextCursor != "" || candidateObservation.nextCursor != "" {
		t.Fatalf("short comparison unexpectedly paginated: control=%q candidate=%q", controlObservation.nextCursor, candidateObservation.nextCursor)
	}
	if controlQuery.EffectivePolicyFingerprint == "" || controlQuery.EffectivePolicyFingerprint != candidateQuery.EffectivePolicyFingerprint {
		t.Fatalf("control/candidate policy identity = %q/%q", controlQuery.EffectivePolicyFingerprint, candidateQuery.EffectivePolicyFingerprint)
	}
	if controlQuery.PrincipalID != "fai-543-principal" || observations[0].metadata != observations[1].metadata {
		t.Fatalf("control/candidate authorization metadata = %#v/%#v", observations[0].metadata, observations[1].metadata)
	}
	if !observations[0].hasBudget || !observations[1].hasBudget || observations[0].budget == nil || observations[1].budget == nil {
		t.Fatalf("control/candidate result budgets = %#v/%#v", observations[0].budget, observations[1].budget)
	}
	controlRows, controlBytes := observations[0].budget.Usage()
	candidateRows, candidateBytes := observations[1].budget.Usage()
	if controlRows != candidateRows || controlRows != rows || controlBytes <= 0 || candidateBytes <= controlBytes {
		t.Fatalf("control/candidate budget usage = %d/%d rows, %d/%d bytes", controlRows, candidateRows, controlBytes, candidateBytes)
	}
	requests := admitter.snapshot()
	if len(requests) != 2 || !reflect.DeepEqual(requests[0], requests[1]) {
		t.Fatalf("control/candidate admission requests = %#v", requests)
	}
	if requests[0].Operation != dataquery.OperationDashboardRows || requests[0].PrincipalID != "system:query" || requests[0].EstimatedMemoryBytes != 64<<20 {
		t.Fatalf("comparison admission identity = %#v", requests[0])
	}
}

func TestDashboardDirectArrowExperimentExcludesUnequalFullPageQueryCounts(t *testing.T) {
	const rows = 1_000
	fixture := newDashboardBaselineFixture(t, rows+1)
	_, control := fixture.serve(t, "detail_wide", rows, "arrow")
	_, candidate, _ := fixture.serveDirectArrowExperiment(t, "detail_wide", 0, rows)
	if control.physicalQueries != 2 || candidate.physicalQueries != 1 {
		t.Fatalf("full-page physical queries control/candidate = %d/%d, want documented 2/1 mismatch", control.physicalQueries, candidate.physicalQueries)
	}
	if control.nextCursor == "" || candidate.nextCursor == "" {
		t.Fatalf("full-page cursors control/candidate = %q/%q", control.nextCursor, candidate.nextCursor)
	}
}

func TestDashboardDirectArrowExperimentSatisfiesNativeV1AndBorrowedLifetime(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, record := newDashboardNativeArrowContractFixture(t, allocator)
	recorder := httptest.NewRecorder()
	config := semanticapi.DirectArrowExperimentConfig{
		QueryID: dashboardNativeArrowQueryID, Snapshot: dashboardNativeArrowSnapshot, CursorScope: "scope-a",
		SchemaVersion: dashboardNativeArrowSchemaVersion, SpecRevision: dashboardNativeArrowSpecRevision,
		DataRevision: dashboardNativeArrowDataRevision, LogicalTypes: dashboardNativeArrowLogicalTypes,
		Labels: map[string]string{"customer_alias": "Customer"}, Limit: 3,
	}
	executor := dashboardDirectArrowFixtureExecutor{
		schema: schema, record: record, releaseAfterWrite: true,
		afterRelease: func() {
			if got := allocator.CurrentAlloc(); got != 0 {
				t.Fatalf("borrowed producer memory retained after sink callback: %d bytes", got)
			}
		},
	}
	_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, executor, dashboardDirectArrowFixtureQuery(3, 0), config)
	if err != nil {
		t.Fatal(err)
	}
	allocator.AssertSize(t, 0)

	response := recorder.Result()
	defer response.Body.Close()
	assertDashboardNativeArrowHeaders(t, response)
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	assertDashboardNativeArrowSchema(t, reader.Schema())
	if !reader.Next() {
		t.Fatalf("read experiment Arrow record: %v", reader.Err())
	}
	assertDashboardNativeArrowValues(t, reader.Record())
	if reader.Next() || reader.Err() != nil {
		t.Fatalf("unexpected second experiment record: %v", reader.Err())
	}
}

func TestDashboardDirectArrowExperimentAccountsForEmittedSchemaMetadata(t *testing.T) {
	schema := arrow.NewSchema([]arrow.Field{{Name: "customer_alias", Type: arrow.BinaryTypes.String}}, nil)
	config := semanticapi.DirectArrowExperimentConfig{
		QueryID: dashboardNativeArrowQueryID, Snapshot: dashboardNativeArrowSnapshot, CursorScope: "scope-a",
		SchemaVersion: dashboardNativeArrowSchemaVersion, SpecRevision: dashboardNativeArrowSpecRevision,
		DataRevision: dashboardNativeArrowDataRevision,
		LogicalTypes: map[string]string{"customer_alias": "string"},
		Labels:       map[string]string{"customer_alias": "Customer"},
		Projection:   []string{"customer_alias"},
		Limit:        1,
	}

	ctx := dataquery.WithResultBudget(context.Background(), dataquery.ResultLimits{MaxRows: 1, MaxBytes: 1 << 20})
	recorder := httptest.NewRecorder()
	_, err := semanticapi.ExecuteDirectArrowExperiment(ctx, recorder, dashboardDirectArrowFixtureExecutor{schema: schema}, dashboardDirectArrowFixtureQuery(1, 0), config)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := ipc.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	emittedBytes := arrowresult.SchemaBytes(reader.Schema())
	reader.Release()
	_, chargedBytes := func() (int, int64) {
		budget, _ := dataquery.ResultBudgetFromContext(ctx)
		return budget.Usage()
	}()
	if chargedBytes != emittedBytes {
		t.Fatalf("emitted schema budget = %d, want final native-v1 schema bytes %d", chargedBytes, emittedBytes)
	}
	if emittedBytes <= arrowresult.SchemaBytes(schema) {
		t.Fatalf("emitted schema bytes = %d, want more than upstream %d", emittedBytes, arrowresult.SchemaBytes(schema))
	}

	ctx = dataquery.WithResultBudget(context.Background(), dataquery.ResultLimits{MaxRows: 1, MaxBytes: emittedBytes - 1})
	recorder = httptest.NewRecorder()
	_, err = semanticapi.ExecuteDirectArrowExperiment(ctx, recorder, dashboardDirectArrowFixtureExecutor{schema: schema}, dashboardDirectArrowFixtureQuery(1, 0), config)
	var limit *dataquery.ResultLimitError
	if !errors.As(err, &limit) || limit.Reason != dataquery.ResultBytes || limit.Observed != emittedBytes {
		t.Fatalf("emitted schema budget error = %v, want observed bytes %d", err, emittedBytes)
	}
	if recorder.Body.Len() != 0 || recorder.Header().Get("X-LeapView-Arrow-Contract") != "" || recorder.Header().Get("X-Next-Cursor") != "" {
		t.Fatalf("metadata budget failure committed Arrow response body=%d headers=%v", recorder.Body.Len(), recorder.Header())
	}
}

func TestDashboardDirectArrowExperimentEmptySchemaAndPagination(t *testing.T) {
	t.Run("empty result", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		record.Release()
		allocator.AssertSize(t, 0)
		recorder := httptest.NewRecorder()
		config := dashboardDirectArrowContractConfig(3, 0)
		_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{schema: schema}, dashboardDirectArrowFixtureQuery(3, 0), config)
		if err != nil {
			t.Fatal(err)
		}
		reader, err := ipc.NewReader(bytes.NewReader(recorder.Body.Bytes()))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Release()
		assertDashboardNativeArrowSchema(t, reader.Schema())
		if reader.Next() || reader.Err() != nil {
			t.Fatalf("empty result next/error = false/%v", reader.Err())
		}
	})

	t.Run("limit plus one probe", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		recorder := httptest.NewRecorder()
		config := dashboardDirectArrowContractConfig(2, 20)
		_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{schema: schema, record: record}, dashboardDirectArrowFixtureQuery(2, 20), config)
		record.Release()
		allocator.AssertSize(t, 0)
		if err != nil {
			t.Fatal(err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		reader, err := ipc.NewReader(response.Body)
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Release()
		var rows int64
		for reader.Next() {
			rows += reader.Record().NumRows()
		}
		if reader.Err() != nil || rows != 2 {
			t.Fatalf("paged experiment rows/error = %d/%v", rows, reader.Err())
		}
		cursor := response.Trailer.Get("X-Next-Cursor")
		if offset, err := semanticapi.DecodeDirectArrowExperimentCursor(cursor, "scope-a", dashboardNativeArrowSnapshot); err != nil || offset != 22 {
			t.Fatalf("experiment cursor = %q (offset=%d err=%v)", cursor, offset, err)
		}
		if _, err := semanticapi.DecodeDirectArrowExperimentCursor(cursor, "scope-b", dashboardNativeArrowSnapshot); err == nil {
			t.Fatal("experiment cursor was accepted for a different scope")
		}
	})
}

func TestDashboardDirectArrowExperimentFailureBoundaries(t *testing.T) {
	t.Run("cancellation before commit", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		recorder := httptest.NewRecorder()
		_, err := semanticapi.ExecuteDirectArrowExperiment(ctx, recorder, dashboardDirectArrowFixtureExecutor{}, dashboardDirectArrowFixtureQuery(1, 0), dashboardDirectArrowContractConfig(1, 0))
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
		if recorder.Body.Len() != 0 || recorder.Header().Get("X-LeapView-Arrow-Contract") != "" {
			t.Fatalf("pre-commit cancellation wrote body=%d headers=%v", recorder.Body.Len(), recorder.Header())
		}
	})

	t.Run("execution failure after commit", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		recorder := httptest.NewRecorder()
		wantErr := errors.New("experiment stream failed")
		_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{schema: schema, record: record, afterRecord: wantErr}, dashboardDirectArrowFixtureQuery(2, 0), dashboardDirectArrowContractConfig(2, 0))
		record.Release()
		allocator.AssertSize(t, 0)
		if !errors.Is(err, wantErr) {
			t.Fatalf("post-commit error = %v", err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		if response.Trailer.Get("X-Next-Cursor") != "" {
			t.Fatalf("failed stream exposed cursor %q", response.Trailer.Get("X-Next-Cursor"))
		}
		if err := consumeDashboardNativeArrow(response.Body); err == nil {
			t.Fatal("failed experiment stream remained readable as complete Arrow IPC")
		}
	})

	t.Run("execution failure after schema but before commit", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		record.Release()
		allocator.AssertSize(t, 0)
		recorder := httptest.NewRecorder()
		wantErr := errors.New("experiment failed after schema")
		_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{
			schema: schema, afterRecord: wantErr,
		}, dashboardDirectArrowFixtureQuery(2, 0), dashboardDirectArrowContractConfig(2, 0))
		if !errors.Is(err, wantErr) {
			t.Fatalf("pre-commit schema error = %v", err)
		}
		if recorder.Body.Len() != 0 || recorder.Header().Get("X-LeapView-Arrow-Contract") != "" {
			t.Fatalf("schema-only failure committed body=%d headers=%v", recorder.Body.Len(), recorder.Header())
		}
	})

	t.Run("cancellation during stream", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		ctx, cancel := context.WithCancel(context.Background())
		recorder := httptest.NewRecorder()
		writer := &dashboardDirectArrowCancelingWriter{ResponseWriter: recorder, cancel: cancel}
		_, err := semanticapi.ExecuteDirectArrowExperiment(ctx, writer, dashboardDirectArrowFixtureExecutor{
			schema: schema, record: record, checkContextAfterRecord: true,
		}, dashboardDirectArrowFixtureQuery(3, 0), dashboardDirectArrowContractConfig(3, 0))
		record.Release()
		allocator.AssertSize(t, 0)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-stream cancellation error = %v", err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		if response.Trailer.Get("X-Next-Cursor") != "" {
			t.Fatalf("canceled stream exposed cursor %q", response.Trailer.Get("X-Next-Cursor"))
		}
		if err := consumeDashboardNativeArrow(response.Body); err == nil {
			t.Fatal("canceled experiment stream remained readable as complete Arrow IPC")
		}
	})

	t.Run("partial response write", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		writer := &dashboardDirectArrowFailingWriter{header: make(stdhttp.Header), remaining: 32, err: errors.New("slow client disconnected")}
		_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), writer, dashboardDirectArrowFixtureExecutor{schema: schema, record: record}, dashboardDirectArrowFixtureQuery(3, 0), dashboardDirectArrowContractConfig(3, 0))
		record.Release()
		allocator.AssertSize(t, 0)
		if !errors.Is(err, writer.err) {
			t.Fatalf("partial-write error = %v", err)
		}
		if writer.header.Get("X-Next-Cursor") != "" {
			t.Fatalf("partial write exposed cursor %q", writer.header.Get("X-Next-Cursor"))
		}
	})
}

func TestDashboardDirectArrowExperimentPinsExecutionForSlowConsumer(t *testing.T) {
	model := dashboardBaselineModel()
	evidence, err := dashboardBaselineDependencyEvidence(model)
	if err != nil {
		t.Fatal(err)
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: resultidentity.PartitionProduction, ProjectID: dashboardBaselineProjectID, Environment: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	database := &dashboardDirectArrowLeasedDatabase{dashboardBaselineDatabase: &dashboardBaselineDatabase{rows: 3}}
	runtime, err := materializeruntime.NewRuntimeView(context.Background(), materializeruntime.RuntimeConfig{
		ModelID: dashboardBaselineModelID.String(), Model: model, Database: database,
		Sources: dashboardBaselineSources{}, SnapshotOnly: true, ResultPartition: partition, DependencyEvidence: evidence,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("close slow-consumer runtime: %v", err)
		}
	})
	detail, err := dashboardBaselineDetailDefinition("detail", dashboardBaselineNarrowFields)
	if err != nil {
		t.Fatal(err)
	}
	request, config, err := dashboardDirectArrowExperimentQuery(detail, model, nil, 0, 3, "a")
	if err != nil {
		t.Fatal(err)
	}
	writer := newDashboardDirectArrowBlockingWriter()
	done := make(chan error, 1)
	go func() {
		_, err := semanticapi.ExecuteDirectArrowExperiment(context.Background(), writer, runtime, request, config)
		done <- err
	}()
	select {
	case <-writer.blocked:
	case <-time.After(time.Second):
		t.Fatal("experiment did not reach slow consumer")
	}
	if database.active.Load() != 1 {
		t.Fatalf("database leases while response writer blocked = %d, want 1", database.active.Load())
	}
	close(writer.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if database.active.Load() != 0 {
		t.Fatalf("database leases after stream completion = %d, want 0", database.active.Load())
	}
}

func TestDashboardDirectArrowExperimentPreservesAdmissionAndCacheBypass(t *testing.T) {
	fixture := newDashboardBaselineFixture(t, 10)
	resolved, err := fixture.service.Resolver().Resolve(projectgraph.ResourceID(dashboardBaselineDashboardID))
	if err != nil {
		t.Fatal(err)
	}
	visual, ok := resolved.Visualization("detail_narrow")
	if !ok {
		t.Fatal("detail fixture is missing")
	}
	request, config, err := dashboardDirectArrowExperimentQuery(visual, resolved.Model, nil, 0, 10, "a")
	if err != nil {
		t.Fatal(err)
	}
	admitter := &dashboardDirectArrowRejectingAdmitter{}
	observation := dashboardBaselineObservation{}
	ctx := dashboardBaselineObservationContext(workload.WithAdmitter(context.Background(), admitter), &observation)
	ctx = dataquery.WithGovernor(ctx, fixture.governor)
	recorder := httptest.NewRecorder()
	result, err := semanticapi.ExecuteDirectArrowExperiment(ctx, recorder, fixture.core, request, config)
	if err == nil || result.ExecutionState != dataquery.ExecutionRejected {
		t.Fatalf("admission result/error = %#v/%v", result, err)
	}
	if admitter.calls.Load() != 1 || fixture.database.queries.Load() != 0 {
		t.Fatalf("admission/database calls = %d/%d", admitter.calls.Load(), fixture.database.queries.Load())
	}
	if len(observation.outcomes) != 0 || recorder.Body.Len() != 0 {
		t.Fatalf("rejected candidate cache outcomes/body = %v/%d", observation.outcomes, recorder.Body.Len())
	}
}

func TestDashboardDirectArrowExperimentEligibilityAndQuerySemantics(t *testing.T) {
	model := dashboardBaselineModel()
	detail, err := dashboardBaselineDetailDefinition("detail", dashboardBaselineNarrowFields)
	if err != nil {
		t.Fatal(err)
	}
	filters := []dataquery.Filter{{Field: "orders.field_00", Operator: "eq", Values: []any{int64(7)}}}
	request, config, err := dashboardDirectArrowExperimentQuery(detail, model, filters, 7, 50, "a")
	if err != nil {
		t.Fatalf("ordinary detail table rejected: %v", err)
	}
	if request.Offset != 7 || request.Limit != 51 || len(request.Filters) != 1 || request.Filters[0].Field != filters[0].Field {
		t.Fatalf("experiment pagination/filter request = %#v", request)
	}
	if len(request.Sort) != 1 || request.Sort[0] != (dataquery.Sort{Field: "field_00", Direction: "desc"}) {
		t.Fatalf("experiment stable sort = %#v", request.Sort)
	}
	if len(request.Fields) != dashboardBaselineNarrowFields || len(request.Metrics) != 0 {
		t.Fatalf("experiment projection fields/metrics = %d/%d", len(request.Fields), len(request.Metrics))
	}
	if config.LogicalTypes["field_00"] != "integer" || config.Labels["field_00"] != "field_00" {
		t.Fatalf("experiment field metadata = logical %q label %q", config.LogicalTypes["field_00"], config.Labels["field_00"])
	}
	if _, _, err := dashboardDirectArrowExperimentQuery(detail, model, nil, 0, 50, "all"); err == nil {
		t.Fatal("multi-block detail query was accepted")
	}
	if _, _, err := dashboardDirectArrowExperimentQuery(detail, model, nil, 0, 1_001, "a"); err == nil {
		t.Fatal("detail query above its compiled row budget was accepted")
	}
	calculated := detail
	calculations := []visualizationir.VisualizationCalculation{{}}
	calculated.Spec.Value.(*visualizationir.TableVisualizationSpec).Calculations = &calculations
	if _, _, err := dashboardDirectArrowExperimentQuery(calculated, model, nil, 0, 50, "a"); err == nil {
		t.Fatal("calculated detail query was accepted")
	}
	for _, kind := range []struct {
		name  string
		pivot bool
	}{{name: "matrix"}, {name: "pivot", pivot: true}} {
		definition, err := dashboardBaselineAggregateDefinition(kind.name, kind.pivot)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := dashboardDirectArrowExperimentQuery(definition, model, nil, 0, 50, "a"); err == nil {
			t.Fatalf("%s query was accepted", kind.name)
		}
	}
	recorder := httptest.NewRecorder()
	mismatched := dashboardDirectArrowFixtureQuery(49, 0)
	_, err = semanticapi.ExecuteDirectArrowExperiment(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{}, mismatched, dashboardDirectArrowContractConfig(50, 0))
	if err == nil || !strings.Contains(err.Error(), "pagination do not match") || recorder.Body.Len() != 0 {
		t.Fatalf("mismatched query/response pagination error/body = %v/%d", err, recorder.Body.Len())
	}
}

type dashboardDirectArrowFixtureExecutor struct {
	schema                  *arrow.Schema
	record                  arrow.RecordBatch
	afterRecord             error
	releaseAfterWrite       bool
	afterRelease            func()
	checkContextAfterRecord bool
}

type dashboardDirectArrowGovernanceObservation struct {
	query     dataquery.Query
	metadata  dataquery.Metadata
	budget    *dataquery.ResultBudget
	hasBudget bool
}

type dashboardDirectArrowCapturingAdmitter struct {
	mu       sync.Mutex
	requests []workload.Request
}

func (a *dashboardDirectArrowCapturingAdmitter) Acquire(ctx context.Context, request workload.Request) (workload.Lease, error) {
	a.mu.Lock()
	a.requests = append(a.requests, request)
	a.mu.Unlock()
	return dashboardDirectArrowAdmissionLease{ctx: ctx}, nil
}

func (a *dashboardDirectArrowCapturingAdmitter) snapshot() []workload.Request {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]workload.Request(nil), a.requests...)
}

type dashboardDirectArrowAdmissionLease struct{ ctx context.Context }

func (l dashboardDirectArrowAdmissionLease) Context() context.Context { return l.ctx }
func (dashboardDirectArrowAdmissionLease) QueueWait() time.Duration   { return 0 }
func (dashboardDirectArrowAdmissionLease) Release()                   {}

func (e dashboardDirectArrowFixtureExecutor) ExecuteDataQueryArrow(ctx context.Context, _ dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if err := ctx.Err(); err != nil {
		return dataquery.Result{}, err
	}
	if e.schema == nil {
		return dataquery.Result{}, errors.New("fixture schema is required")
	}
	if err := arrowquery.ConsumeSchemaBudget(ctx, e.schema); err != nil {
		return dataquery.Result{}, err
	}
	if err := sink.WriteSchema(e.schema); err != nil {
		return dataquery.Result{}, err
	}
	if e.record != nil {
		if err := arrowquery.ConsumeResultBudget(ctx, e.record); err != nil {
			return dataquery.Result{}, err
		}
		err := sink.WriteRecord(e.record)
		if e.releaseAfterWrite {
			e.record.Release()
			if e.afterRelease != nil {
				e.afterRelease()
			}
		}
		if err != nil {
			return dataquery.Result{}, err
		}
		if e.checkContextAfterRecord {
			if err := ctx.Err(); err != nil {
				return dataquery.Result{}, err
			}
		}
	}
	result := dataquery.Result{}
	if stats, ok := sink.(arrowquery.SinkStats); ok {
		result.RowsReturned = stats.RowsWritten()
	}
	return result, e.afterRecord
}

type dashboardDirectArrowRejectingAdmitter struct{ calls atomic.Int64 }

func (a *dashboardDirectArrowRejectingAdmitter) Acquire(_ context.Context, request workload.Request) (workload.Lease, error) {
	a.calls.Add(1)
	return nil, &workload.Rejection{Reason: workload.InstanceMemoryLimit, Class: request.Class, PrincipalID: request.PrincipalID, Operation: request.Operation}
}

type dashboardDirectArrowFailingWriter struct {
	header    stdhttp.Header
	buffer    bytes.Buffer
	remaining int
	err       error
}

type dashboardDirectArrowCancelingWriter struct {
	stdhttp.ResponseWriter
	cancel context.CancelFunc
	once   sync.Once
}

func (w *dashboardDirectArrowCancelingWriter) Write(payload []byte) (int, error) {
	count, err := w.ResponseWriter.Write(payload)
	w.once.Do(w.cancel)
	return count, err
}

func (w *dashboardDirectArrowFailingWriter) Header() stdhttp.Header { return w.header }
func (*dashboardDirectArrowFailingWriter) WriteHeader(int)          {}
func (w *dashboardDirectArrowFailingWriter) Write(payload []byte) (int, error) {
	if w.remaining <= 0 {
		return 0, w.err
	}
	count := min(len(payload), w.remaining)
	_, _ = w.buffer.Write(payload[:count])
	w.remaining -= count
	if count != len(payload) {
		return count, w.err
	}
	return count, nil
}

type dashboardDirectArrowBlockingWriter struct {
	recorder *httptest.ResponseRecorder
	blocked  chan struct{}
	release  chan struct{}
	once     sync.Once
}

type dashboardDirectArrowLeasedDatabase struct {
	*dashboardBaselineDatabase
	active atomic.Int64
}

func (d *dashboardDirectArrowLeasedDatabase) Acquire(ctx context.Context) (analyticsresource.Lease, error) {
	d.active.Add(1)
	return &dashboardDirectArrowDatabaseLease{ctx: ctx, active: &d.active}, nil
}

type dashboardDirectArrowDatabaseLease struct {
	ctx    context.Context
	active *atomic.Int64
	once   sync.Once
}

func (l *dashboardDirectArrowDatabaseLease) Context() context.Context { return l.ctx }
func (l *dashboardDirectArrowDatabaseLease) Release() {
	l.once.Do(func() { l.active.Add(-1) })
}

func newDashboardDirectArrowBlockingWriter() *dashboardDirectArrowBlockingWriter {
	return &dashboardDirectArrowBlockingWriter{recorder: httptest.NewRecorder(), blocked: make(chan struct{}), release: make(chan struct{})}
}

func (w *dashboardDirectArrowBlockingWriter) Header() stdhttp.Header { return w.recorder.Header() }
func (w *dashboardDirectArrowBlockingWriter) WriteHeader(status int) { w.recorder.WriteHeader(status) }
func (w *dashboardDirectArrowBlockingWriter) Write(payload []byte) (int, error) {
	w.once.Do(func() {
		close(w.blocked)
		<-w.release
	})
	return w.recorder.Write(payload)
}

func dashboardDirectArrowFixtureQuery(limit, offset int) dataquery.Query {
	return dataquery.Query{
		Surface: dataquery.SurfaceAPI, Operation: dataquery.OperationDashboardRows, Kind: dataquery.KindSemanticRows,
		Limit: limit + 1, Offset: offset,
	}
}

func dashboardDirectArrowContractConfig(limit, offset int) semanticapi.DirectArrowExperimentConfig {
	return semanticapi.DirectArrowExperimentConfig{
		QueryID: dashboardNativeArrowQueryID, Snapshot: dashboardNativeArrowSnapshot, CursorScope: "scope-a",
		SchemaVersion: dashboardNativeArrowSchemaVersion, SpecRevision: dashboardNativeArrowSpecRevision,
		DataRevision: dashboardNativeArrowDataRevision, LogicalTypes: dashboardNativeArrowLogicalTypes,
		Labels: map[string]string{"customer_alias": "Customer"}, Limit: limit, Offset: offset,
	}
}

func assertDashboardDirectArrowExperimentEquivalent(t testing.TB, controlPayload, candidatePayload []byte, rows, columns int) {
	t.Helper()
	controlReader, err := ipc.NewReader(bytes.NewReader(controlPayload))
	if err != nil {
		t.Fatalf("open control Arrow response: %v", err)
	}
	defer controlReader.Release()
	candidateReader, err := ipc.NewReader(bytes.NewReader(candidatePayload))
	if err != nil {
		t.Fatalf("open candidate Arrow response: %v", err)
	}
	defer candidateReader.Release()
	if controlReader.Schema().NumFields() != columns || candidateReader.Schema().NumFields() != columns {
		t.Fatalf("control/candidate columns = %d/%d, want %d", controlReader.Schema().NumFields(), candidateReader.Schema().NumFields(), columns)
	}
	if err := validateDashboardNativeArrowMetadata(candidateReader.Schema().Metadata(), dashboardNativeArrowSchemaMetadataAllowlist); err != nil {
		t.Fatal(err)
	}
	for column := 0; column < columns; column++ {
		controlField := controlReader.Schema().Field(column)
		candidateField := candidateReader.Schema().Field(column)
		if controlField.Name != candidateField.Name || candidateField.Name != fmt.Sprintf("field_%02d", column) {
			t.Fatalf("field %d aliases control/candidate = %q/%q", column, controlField.Name, candidateField.Name)
		}
		if controlField.Type.ID() != arrow.STRING {
			t.Fatalf("control field %d type = %s, want all-string IPC", column, controlField.Type)
		}
		if candidateField.Type.ID() != dashboardBaselineArrowField(candidateField.Name, column, false).Type.ID() {
			t.Fatalf("candidate field %d type = %s", column, candidateField.Type)
		}
		if err := validateDashboardNativeArrowMetadata(candidateField.Metadata, dashboardNativeArrowFieldMetadataAllowlist); err != nil {
			t.Fatalf("candidate field %d metadata: %v", column, err)
		}
	}
	if !controlReader.Next() || !candidateReader.Next() {
		t.Fatalf("read control/candidate record: control=%v candidate=%v", controlReader.Err(), candidateReader.Err())
	}
	controlRecord, candidateRecord := controlReader.Record(), candidateReader.Record()
	if controlRecord.NumRows() != int64(rows) || candidateRecord.NumRows() != int64(rows) {
		t.Fatalf("control/candidate rows = %d/%d, want %d", controlRecord.NumRows(), candidateRecord.NumRows(), rows)
	}
	for column := 0; column < columns; column++ {
		legacy := controlRecord.Column(column).(*array.String)
		for row := 0; row < rows; row++ {
			native := candidateRecord.Column(column)
			wantNull := (row+column)%13 == 0
			if native.IsNull(row) != wantNull {
				t.Fatalf("candidate field %d row %d null = %v, want %v", column, row, native.IsNull(row), wantNull)
			}
			wantProjection := ""
			if !wantNull {
				wantProjection = dashboardWarmCurrentProjection(dashboardWarmNativeIPCValue(t, native, row))
			}
			if got := legacy.Value(row); got != wantProjection {
				t.Fatalf("field %d row %d control projection = %q, want %q", column, row, got, wantProjection)
			}
		}
		if dictionary, ok := candidateRecord.Column(column).(*array.Dictionary); ok {
			assertDashboardWarmDictionary(t, dictionary, column, rows)
		}
	}
	if controlReader.Next() || controlReader.Err() != nil || candidateReader.Next() || candidateReader.Err() != nil {
		t.Fatalf("unexpected additional records control=%v candidate=%v", controlReader.Err(), candidateReader.Err())
	}
}
