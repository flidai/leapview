package http

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"slices"
	"strconv"
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
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/arrowresult"
)

const dashboardDirectArrowPrototypeQueryID = "fai-543-direct-arrow"

var errDashboardDirectArrowPrototypeWait = errors.New("prototype did not finish after slow consumer resumed")

type dashboardDirectArrowPrototypeConfig struct {
	queryID       string
	snapshot      string
	cursorScope   string
	schemaVersion string
	specRevision  string
	dataRevision  string
	logicalTypes  map[string]string
	labels        map[string]string
	limit         int
	offset        int
	allocator     memory.Allocator
}

// dashboardDirectArrowPrototypeSink is deliberately test-only. It encodes
// each borrowed batch before returning from WriteRecord and keeps only an
// independently constructed schema. It is an experiment, not a serving path.
type dashboardDirectArrowPrototypeSink struct {
	w       stdhttp.ResponseWriter
	config  dashboardDirectArrowPrototypeConfig
	schema  *arrow.Schema
	writer  *ipc.Writer
	output  *dashboardDirectArrowPrototypeOutput
	written int64
	seen    int64
}

type dashboardDirectArrowPrototypeOutput struct {
	w       io.Writer
	aborted bool
}

func (w *dashboardDirectArrowPrototypeOutput) Write(payload []byte) (int, error) {
	if w.aborted {
		// Close the IPC writer against a discard target so it can release any
		// dictionary memo state without appending a success terminator to the
		// already committed HTTP response.
		return io.Discard.Write(payload)
	}
	return w.w.Write(payload)
}

func (s *dashboardDirectArrowPrototypeSink) WriteSchema(schema *arrow.Schema) error {
	if s == nil || s.w == nil {
		return errors.New("direct Arrow prototype sink is not initialized")
	}
	if schema == nil {
		return errors.New("direct Arrow prototype schema is required")
	}
	if s.schema != nil {
		return errors.New("direct Arrow prototype schema was already written")
	}

	fields := schema.Fields()
	for index := range fields {
		authoritative := map[string]string{}
		if logicalType := s.config.logicalTypes[fields[index].Name]; logicalType != "" {
			authoritative["leapview.logical_type"] = logicalType
		}
		if label := s.config.labels[fields[index].Name]; label != "" {
			authoritative["display.label"] = label
		}
		fields[index].Metadata = publicDashboardNativeArrowMetadata(
			fields[index].Metadata,
			dashboardNativeArrowFieldProducerMetadataAllowlist,
			authoritative,
		)
	}
	metadata := publicDashboardNativeArrowMetadata(schema.Metadata(), nil, map[string]string{
		"leapview.arrow_contract":               dashboardNativeArrowContract,
		"leapview.query_id":                     s.config.queryID,
		"leapview.serving_snapshot":             s.config.snapshot,
		"leapview.visualization_schema_version": s.config.schemaVersion,
		"leapview.visualization_spec_revision":  s.config.specRevision,
		"leapview.visualization_data_revision":  s.config.dataRevision,
	})
	// NewSchema copies the field slice and metadata values. No borrowed schema
	// pointer survives this callback; Arrow data types are immutable descriptors.
	s.schema = arrow.NewSchema(fields, &metadata)
	return nil
}

func (s *dashboardDirectArrowPrototypeSink) WriteRecord(record arrow.RecordBatch) error {
	if s == nil {
		return errors.New("direct Arrow prototype sink is not initialized")
	}
	if record == nil || record.NumRows() == 0 {
		return nil
	}
	s.seen += record.NumRows()
	remaining := int64(s.config.limit) - s.written
	if remaining <= 0 {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	// The governed schema differs only in response-safe metadata. Rebind the
	// borrowed columns to that schema for the duration of this callback.
	//
	// ipc.Writer encodes ordinary arrays synchronously, but memoizes dictionary
	// values until Close. DuckDB buffers cannot be retained, so establish owned
	// dictionary values before handing the batch to the writer. Dictionary
	// indices remain borrowed and are consumed synchronously like other columns.
	columns := append([]arrow.Array(nil), record.Columns()...)
	ownedDictionaries := make([]arrow.Array, 0)
	for index, column := range columns {
		dictionary, ok := column.(*array.Dictionary)
		if !ok {
			continue
		}
		values, err := array.Concatenate([]arrow.Array{dictionary.Dictionary()}, s.allocator())
		if err != nil {
			for _, owned := range ownedDictionaries {
				owned.Release()
			}
			return fmt.Errorf("copy direct Arrow prototype dictionary %d: %w", index, err)
		}
		owned := array.NewDictionaryArray(dictionary.DataType(), dictionary.Indices(), values)
		values.Release()
		columns[index] = owned
		ownedDictionaries = append(ownedDictionaries, owned)
	}
	outputRecord := array.NewRecordBatch(s.schema, columns, record.NumRows())
	for _, owned := range ownedDictionaries {
		owned.Release()
	}
	defer outputRecord.Release()
	var emitted arrow.RecordBatch = outputRecord
	if record.NumRows() > remaining {
		emitted = outputRecord.NewSlice(0, remaining)
		defer emitted.Release()
	}
	// ipc.Writer.Write is synchronous. The borrowed record and any slice of it
	// are fully encoded before this callback returns.
	if err := s.writer.Write(emitted); err != nil {
		return err
	}
	s.written += emitted.NumRows()
	return nil
}

func (s *dashboardDirectArrowPrototypeSink) allocator() memory.Allocator {
	if s != nil && s.config.allocator != nil {
		return s.config.allocator
	}
	return memory.DefaultAllocator
}

func (s *dashboardDirectArrowPrototypeSink) RowsWritten() int {
	if s == nil {
		return 0
	}
	return int(s.written)
}

func (s *dashboardDirectArrowPrototypeSink) hasMore() bool {
	return s != nil && s.seen > int64(s.config.limit)
}

func (s *dashboardDirectArrowPrototypeSink) start() error {
	if s.writer != nil {
		return nil
	}
	if s.schema == nil {
		return errors.New("direct Arrow prototype schema must precede records")
	}
	s.w.Header().Set("Content-Type", dashboardArrowMediaType)
	s.w.Header().Set("Cache-Control", "no-store")
	s.w.Header().Set("X-Query-ID", s.config.queryID)
	s.w.Header().Set("X-Serving-Snapshot", s.config.snapshot)
	s.w.Header().Set("X-LeapView-Arrow-Contract", dashboardNativeArrowContract)
	s.w.Header().Set("Trailer", "X-Next-Cursor")
	s.output = &dashboardDirectArrowPrototypeOutput{w: s.w}
	s.writer = ipc.NewWriter(s.output, ipc.WithSchema(s.schema), ipc.WithAllocator(s.allocator()))
	return nil
}

func (s *dashboardDirectArrowPrototypeSink) close() error {
	if s == nil {
		return nil
	}
	if err := s.start(); err != nil {
		return err
	}
	err := s.writer.Close()
	s.writer = nil
	return err
}

func (s *dashboardDirectArrowPrototypeSink) abort() {
	if s == nil || s.writer == nil {
		return
	}
	// Begin, but deliberately do not complete, the next IPC message. A reader
	// that consumed the last complete record must still reject this stream
	// instead of treating the transport EOF as a successful final page.
	_, _ = s.w.Write([]byte{0xff, 0xff, 0xff, 0xff, 0x04, 0x00, 0x00, 0x00, 0x42, 0x42, 0x42, 0x42})
	s.output.aborted = true
	_ = s.writer.Close()
	s.writer = nil
}

var _ arrowquery.Sink = (*dashboardDirectArrowPrototypeSink)(nil)
var _ arrowquery.SinkStats = (*dashboardDirectArrowPrototypeSink)(nil)

func executeDashboardDirectArrowPrototype(
	ctx context.Context,
	w stdhttp.ResponseWriter,
	executor arrowquery.Executor,
	request dataquery.Query,
	config dashboardDirectArrowPrototypeConfig,
) (dataquery.Result, error) {
	if executor == nil {
		return dataquery.Result{}, errors.New("direct Arrow prototype executor is required")
	}
	if config.limit <= 0 {
		return dataquery.Result{}, errors.New("direct Arrow prototype limit must be positive")
	}
	sink := &dashboardDirectArrowPrototypeSink{w: w, config: config}
	result, err := executor.ExecuteDataQueryArrow(ctx, request, sink)
	if err != nil {
		// A committed stream stays incomplete. The experiment never switches to
		// JSON or manufactures a successful cursor after an execution failure.
		sink.abort()
		return result, err
	}
	if err := sink.close(); err != nil {
		return result, err
	}
	if sink.hasMore() {
		w.Header().Set("X-Next-Cursor", encodeIndexCursor(config.offset+config.limit, config.cursorScope, config.snapshot))
	}
	return result, nil
}

func dashboardDirectArrowPrototypeQuery(
	visual definition.Definition,
	model *semanticmodel.Model,
	filters []dataquery.Filter,
	offset, limit int,
	block string,
) (dataquery.Query, dashboardDirectArrowPrototypeConfig, error) {
	if block != "a" {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, fmt.Errorf("direct Arrow prototype only supports block a")
	}
	if visual.Query.Kind != definition.QueryDetail || visual.Query.Detail == nil {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, fmt.Errorf("direct Arrow prototype only supports ordinary detail queries")
	}
	if _, ok := visual.Spec.Value.(*visualizationir.TableVisualizationSpec); !ok {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, fmt.Errorf("direct Arrow prototype only supports table visualizations")
	}
	base, err := visualizationir.SpecificationBase(visual.Spec)
	if err != nil {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, err
	}
	if base.Calculations != nil && len(*base.Calculations) > 0 {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, fmt.Errorf("direct Arrow prototype does not support calculations")
	}
	if model == nil {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, errors.New("direct Arrow prototype semantic model is required")
	}
	if offset < 0 || limit <= 0 {
		return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, errors.New("direct Arrow prototype requires a non-negative offset and positive limit")
	}

	fields := make([]dataquery.Field, 0, len(visual.Query.Detail.Fields))
	metrics := make([]dataquery.Field, 0, len(visual.Query.Detail.Fields))
	aliases := make(map[string]struct{}, len(visual.Query.Detail.Fields))
	for _, binding := range visual.Query.Detail.Fields {
		field := dataquery.Field{Field: binding.FieldID, Alias: binding.Alias}
		aliases[binding.Alias] = struct{}{}
		if _, err := model.ResolveDimension(binding.FieldID); err == nil {
			fields = append(fields, field)
			continue
		}
		if _, err := model.ResolveMetric(binding.FieldID); err != nil {
			return dataquery.Query{}, dashboardDirectArrowPrototypeConfig{}, fmt.Errorf("prototype field %q: %w", binding.FieldID, err)
		}
		metrics = append(metrics, field)
	}

	sortKey, sortDirection := "", "desc"
	if len(visual.Query.Detail.DefaultSort) > 0 {
		sortKey = visual.Query.Detail.DefaultSort[0].FieldID
		sortDirection = visual.Query.Detail.DefaultSort[0].Direction
	}
	if _, ok := aliases[sortKey]; !ok {
		if _, hasOrderID := aliases["order_id"]; hasOrderID {
			sortKey = "order_id"
		} else if len(visual.Query.Detail.Fields) > 0 {
			sortKey = visual.Query.Detail.Fields[0].Alias
		}
	}
	if sortDirection == "" {
		sortDirection = "desc"
	}
	sort := []dataquery.Sort{}
	if sortKey != "" {
		sort = append(sort, dataquery.Sort{Field: sortKey, Direction: sortDirection})
	}
	if sortKey != "order_id" {
		if _, hasOrderID := aliases["order_id"]; hasOrderID {
			sort = append(sort, dataquery.Sort{Field: "order_id", Direction: "asc"})
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
		Surface:   dataquery.SurfaceDashboard,
		Operation: dataquery.OperationDashboardRows,
		ModelID:   visual.Query.ModelID,
		Kind:      dataquery.KindSemanticRows,
		Target:    visual.Query.Detail.TableID,
		Fields:    fields,
		Metrics:   metrics,
		Filters:   append([]dataquery.Filter(nil), filters...),
		Sort:      sort,
		Offset:    offset,
		// The native-v1 contract charges and consumes the limit+1 probe while
		// the sink emits at most limit rows.
		Limit: limit + 1,
	}
	config := dashboardDirectArrowPrototypeConfig{
		queryID:       dashboardDirectArrowPrototypeQueryID,
		snapshot:      dashboardBaselineSnapshot,
		cursorScope:   "fai-543-prototype-scope",
		schemaVersion: dashboardNativeArrowSchemaVersion,
		specRevision:  visual.SpecRevision,
		dataRevision:  "1",
		logicalTypes:  logicalTypes,
		labels:        labels,
		limit:         limit,
		offset:        offset,
	}
	return request, config, nil
}

func (f *dashboardBaselineFixture) serveDirectArrowPrototype(tb testing.TB, visual string, offset, limit int) ([]byte, dashboardBaselineObservation, dataquery.Result) {
	tb.Helper()
	resolved, err := f.service.Resolver().Resolve(projectgraph.ResourceID(dashboardBaselineDashboardID))
	if err != nil {
		tb.Fatal(err)
	}
	visualDefinition, exists := resolved.Visualization(visual)
	if !exists {
		tb.Fatalf("prototype visualization %q not found", visual)
	}
	request, config, err := dashboardDirectArrowPrototypeQuery(visualDefinition, resolved.Model, nil, offset, limit, "a")
	if err != nil {
		tb.Fatal(err)
	}
	observation := dashboardBaselineObservation{}
	ctx := dashboardBaselineObservationContext(context.Background(), &observation)
	ctx = dataquery.WithGovernor(ctx, f.governor)
	ctx = dataquery.WithMetadata(ctx, dataquery.Metadata{
		ProjectID: dashboardBaselineProjectID,
		Surface:   dataquery.SurfaceAPI, Operation: dataquery.OperationAPIQuery,
		RequestID: config.queryID, ObjectType: "dashboard_visual", ObjectID: dashboardBaselineDashboardID + ":" + visual,
	})
	recorder := httptest.NewRecorder()
	result, err := executeDashboardDirectArrowPrototype(ctx, recorder, f.core, request, config)
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

func BenchmarkDashboardDirectArrowPrototype(b *testing.B) {
	for _, workload := range []dashboardBaselineWorkload{
		{name: "detail_narrow", visual: "detail_narrow", columns: dashboardBaselineNarrowFields},
		{name: "detail_wide", visual: "detail_wide", columns: dashboardBaselineWideFields},
	} {
		// A 999-row final page exercises the 1,000-row request ceiling without
		// triggering the control's separate exact-count query.
		for _, rows := range []int{50, 999} {
			b.Run(workload.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				for _, lane := range []string{"current_api_direct", "candidate_governed_native"} {
					b.Run(lane, func(b *testing.B) {
						// Use a short final page so both lanes execute exactly one
						// physical query. Pagination is qualified separately below.
						fixture := newDashboardBaselineFixture(b, rows)
						arrowBefore := arrowresult.Stats()
						var totalBytes, physicalQueries, cacheOutcomes int64
						durations := make([]time.Duration, b.N)
						b.ReportAllocs()
						b.ResetTimer()
						for index := range b.N {
							started := time.Now()
							var body []byte
							var observation dashboardBaselineObservation
							if lane == "current_api_direct" {
								body, observation = fixture.serve(b, workload.visual, rows+1, "arrow")
							} else {
								body, observation, _ = fixture.serveDirectArrowPrototype(b, workload.visual, 0, rows)
							}
							dashboardBaselineBodySink = body
							totalBytes += int64(len(body))
							physicalQueries += int64(observation.physicalQueries)
							cacheOutcomes += int64(len(observation.outcomes))
							durations[index] = time.Since(started)
						}
						b.StopTimer()
						slices.Sort(durations)
						if physicalQueries != int64(b.N) {
							b.Fatalf("%s physical queries = %d, want %d", lane, physicalQueries, b.N)
						}
						if cacheOutcomes != 0 {
							b.Fatalf("%s observed %d retained-cache outcomes", lane, cacheOutcomes)
						}
						arrowAfter := arrowresult.Stats()
						if arrowAfter.Results != arrowBefore.Results || arrowAfter.Leases != arrowBefore.Leases || arrowAfter.Bytes != arrowBefore.Bytes || arrowAfter.TransientBytes != arrowBefore.TransientBytes {
							b.Fatalf("%s changed retained Arrow ownership: before=%#v after=%#v", lane, arrowBefore, arrowAfter)
						}
						b.ReportMetric(float64(totalBytes)/float64(b.N), "response-bytes/op")
						b.ReportMetric(float64(physicalQueries)/float64(b.N), "physical-queries/op")
						b.ReportMetric(float64(cacheOutcomes)/float64(b.N), "cache-outcomes/op")
						b.ReportMetric(float64(rows), "rows/op")
						b.ReportMetric(float64(workload.columns), "columns/op")
						reportDashboardDirectArrowPercentiles(b, durations)
					})
				}
			})
		}
	}
}

func reportDashboardDirectArrowPercentiles(b *testing.B, durations []time.Duration) {
	b.Helper()
	if len(durations) == 0 {
		return
	}
	percentile := func(value int) float64 {
		index := (len(durations)*value - 1) / 100
		return float64(durations[index]) / float64(time.Millisecond)
	}
	b.ReportMetric(percentile(50), "p50-ms/request")
	b.ReportMetric(percentile(95), "p95-ms/request")
	b.ReportMetric(percentile(99), "p99-ms/request")
}

func TestDashboardDirectArrowPrototypeMatchesCurrentDirectValues(t *testing.T) {
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
			current, currentObservation := fixture.serve(t, workload.visual, rows+1, "arrow")
			candidate, candidateObservation, result := fixture.serveDirectArrowPrototype(t, workload.visual, 0, rows)

			if currentObservation.physicalQueries != 1 || candidateObservation.physicalQueries != 1 {
				t.Fatalf("physical queries current/candidate = %d/%d", currentObservation.physicalQueries, candidateObservation.physicalQueries)
			}
			if len(currentObservation.outcomes) != 0 || len(candidateObservation.outcomes) != 0 {
				t.Fatalf("direct comparison touched retained cache: current=%v candidate=%v", currentObservation.outcomes, candidateObservation.outcomes)
			}
			if governed.Load() != 2 {
				t.Fatalf("governance calls for current/candidate = %d, want one per lane", governed.Load())
			}
			if len(plans) != 2 {
				t.Fatalf("physical plans captured = %d, want current and candidate", len(plans))
			}
			if plans[0].SQL != plans[1].SQL || !slices.Equal(plans[0].Columns, plans[1].Columns) {
				t.Fatalf("physical plans differ: current=%#v candidate=%#v", plans[0], plans[1])
			}
			if result.RowsReturned != rows {
				t.Fatalf("candidate rows returned = %d, want %d emitted rows", result.RowsReturned, rows)
			}
			if currentObservation.nextCursor != "" || candidateObservation.nextCursor != "" {
				t.Fatalf("final-page cursors current/candidate = %q/%q, want empty", currentObservation.nextCursor, candidateObservation.nextCursor)
			}
			assertDashboardDirectArrowEquivalent(t, current, candidate, rows, workload.columns)
		})
	}
}

func TestDashboardDirectArrowPrototypeFullPageIsNotAComparableLane(t *testing.T) {
	const rows = 1_000
	fixture := newDashboardBaselineFixture(t, rows+1)
	_, current := fixture.serve(t, "detail_wide", rows, "arrow")
	_, candidate, _ := fixture.serveDirectArrowPrototype(t, "detail_wide", 0, rows)
	if current.physicalQueries != 2 || candidate.physicalQueries != 1 {
		t.Fatalf("full-page physical queries current/candidate = %d/%d, want documented 2/1 mismatch", current.physicalQueries, candidate.physicalQueries)
	}
	if current.nextCursor == "" || candidate.nextCursor == "" {
		t.Fatalf("full-page cursors current/candidate = %q/%q", current.nextCursor, candidate.nextCursor)
	}
}

func TestDashboardDirectArrowPrototypeSatisfiesNativeContractAndBorrowedLifetime(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, record := newDashboardNativeArrowContractFixture(t, allocator)
	executor := dashboardDirectArrowFixtureExecutor{schema: schema, record: record}
	recorder := httptest.NewRecorder()
	config := dashboardDirectArrowPrototypeConfig{
		queryID: dashboardNativeArrowQueryID, snapshot: dashboardNativeArrowSnapshot,
		cursorScope: "scope-a", schemaVersion: dashboardNativeArrowSchemaVersion,
		specRevision: dashboardNativeArrowSpecRevision, dataRevision: dashboardNativeArrowDataRevision,
		logicalTypes: dashboardNativeArrowLogicalTypes, limit: 3, allocator: allocator,
	}
	_, err := executeDashboardDirectArrowPrototype(context.Background(), recorder, executor, dataquery.Query{}, config)
	if err != nil {
		record.Release()
		t.Fatal(err)
	}
	// Release the producer-owned batch before consuming the response. Successful
	// decoding afterwards proves the sink did not depend on borrowed buffers.
	// The checked allocator also proves the IPC dictionary memo's owned copy was
	// released when the sink closed.
	record.Release()
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
		t.Fatalf("read prototype Arrow record: %v", reader.Err())
	}
	assertDashboardNativeArrowValues(t, reader.Record())
	if reader.Next() || reader.Err() != nil {
		t.Fatalf("unexpected second prototype record: next/error = false/%v", reader.Err())
	}
	if response.Trailer.Get("X-Next-Cursor") != "" {
		t.Fatalf("final prototype page exposed cursor %q", response.Trailer.Get("X-Next-Cursor"))
	}
}

func TestDashboardDirectArrowPrototypeEmptyResultPreservesSchema(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, record := newDashboardNativeArrowContractFixture(t, allocator)
	record.Release()
	allocator.AssertSize(t, 0)
	recorder := httptest.NewRecorder()
	config := dashboardDirectArrowPrototypeConfig{
		queryID: dashboardNativeArrowQueryID, snapshot: dashboardNativeArrowSnapshot,
		cursorScope: "scope-a", schemaVersion: dashboardNativeArrowSchemaVersion,
		specRevision: dashboardNativeArrowSpecRevision, dataRevision: dashboardNativeArrowDataRevision,
		logicalTypes: dashboardNativeArrowLogicalTypes, limit: 3, allocator: allocator,
	}
	result, err := executeDashboardDirectArrowPrototype(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{schema: schema}, dataquery.Query{}, config)
	if err != nil {
		t.Fatal(err)
	}
	allocator.AssertSize(t, 0)
	if result.RowsReturned != 0 {
		t.Fatalf("empty prototype rows returned = %d", result.RowsReturned)
	}
	response := recorder.Result()
	defer response.Body.Close()
	assertDashboardNativeArrowHeaders(t, response)
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	assertDashboardNativeArrowSchema(t, reader.Schema())
	if reader.Next() || reader.Err() != nil {
		t.Fatalf("empty prototype emitted a record: next/error = false/%v", reader.Err())
	}
}

func TestDashboardDirectArrowPrototypePaginationAndFailureBoundaries(t *testing.T) {
	t.Run("limit plus one probe", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		recorder := httptest.NewRecorder()
		config := dashboardDirectArrowPrototypeConfig{
			queryID: dashboardNativeArrowQueryID, snapshot: dashboardNativeArrowSnapshot,
			cursorScope: "scope-a", schemaVersion: dashboardNativeArrowSchemaVersion,
			specRevision: dashboardNativeArrowSpecRevision, dataRevision: dashboardNativeArrowDataRevision,
			logicalTypes: dashboardNativeArrowLogicalTypes, limit: 2, offset: 20, allocator: allocator,
		}
		_, err := executeDashboardDirectArrowPrototype(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{schema: schema, record: record}, dataquery.Query{}, config)
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
			t.Fatalf("paged prototype rows/error = %d/%v", rows, reader.Err())
		}
		cursor := response.Trailer.Get("X-Next-Cursor")
		if offset, err := decodeIndexCursor(cursor, "scope-a", dashboardNativeArrowSnapshot); err != nil || offset != 22 {
			t.Fatalf("prototype cursor = %q (offset=%d err=%v)", cursor, offset, err)
		}
	})

	t.Run("cancellation before commit", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := executeDashboardDirectArrowPrototype(ctx, recorder, dashboardDirectArrowFixtureExecutor{}, dataquery.Query{}, dashboardDirectArrowPrototypeConfig{limit: 1})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation error = %v", err)
		}
		if recorder.Body.Len() != 0 || recorder.Header().Get("X-LeapView-Arrow-Contract") != "" {
			t.Fatalf("pre-commit cancellation wrote response body=%d headers=%v", recorder.Body.Len(), recorder.Header())
		}
	})

	t.Run("execution failure after commit", func(t *testing.T) {
		allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
		schema, record := newDashboardNativeArrowContractFixture(t, allocator)
		recorder := httptest.NewRecorder()
		config := dashboardDirectArrowPrototypeConfig{
			queryID: dashboardNativeArrowQueryID, snapshot: dashboardNativeArrowSnapshot,
			cursorScope: "scope-a", schemaVersion: dashboardNativeArrowSchemaVersion,
			specRevision: dashboardNativeArrowSpecRevision, dataRevision: dashboardNativeArrowDataRevision,
			logicalTypes: dashboardNativeArrowLogicalTypes, limit: 2, allocator: allocator,
		}
		wantErr := errors.New("prototype stream failed")
		_, err := executeDashboardDirectArrowPrototype(context.Background(), recorder, dashboardDirectArrowFixtureExecutor{schema: schema, record: record, afterRecord: wantErr}, dataquery.Query{}, config)
		record.Release()
		allocator.AssertSize(t, 0)
		if !errors.Is(err, wantErr) {
			t.Fatalf("post-commit error = %v", err)
		}
		response := recorder.Result()
		defer response.Body.Close()
		if got := response.Trailer.Get("X-Next-Cursor"); got != "" {
			t.Fatalf("failed prototype stream exposed cursor %q", got)
		}
		if got := response.Header.Get("X-LeapView-Arrow-Contract"); got != dashboardNativeArrowContract {
			t.Fatalf("committed prototype contract = %q", got)
		}
		if err := consumeDashboardNativeArrow(response.Body); err == nil {
			t.Fatal("failed prototype stream remained readable as a complete Arrow response")
		}
	})
}

func TestDashboardDirectArrowPrototypeSlowConsumerKeepsExecutorPinned(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, record := newDashboardNativeArrowContractFixture(t, allocator)
	active := &atomic.Bool{}
	executor := dashboardDirectArrowFixtureExecutor{schema: schema, record: record, active: active}
	recorder := &dashboardDirectArrowBlockingWriter{
		ResponseRecorder: httptest.NewRecorder(),
		entered:          make(chan struct{}),
		release:          make(chan struct{}),
	}
	config := dashboardDirectArrowPrototypeConfig{
		queryID: dashboardNativeArrowQueryID, snapshot: dashboardNativeArrowSnapshot,
		cursorScope: "scope-a", schemaVersion: dashboardNativeArrowSchemaVersion,
		specRevision: dashboardNativeArrowSpecRevision, dataRevision: dashboardNativeArrowDataRevision,
		logicalTypes: dashboardNativeArrowLogicalTypes, limit: 3, allocator: allocator,
	}
	done := make(chan error, 1)
	go func() {
		_, err := executeDashboardDirectArrowPrototype(context.Background(), recorder, executor, dataquery.Query{}, config)
		done <- err
	}()
	finish := func() error {
		recorder.resume()
		select {
		case err := <-done:
			return err
		case <-time.After(2 * time.Second):
			return errDashboardDirectArrowPrototypeWait
		}
	}

	select {
	case <-recorder.entered:
	case <-time.After(2 * time.Second):
		err := finish()
		if errors.Is(err, errDashboardDirectArrowPrototypeWait) {
			t.Fatalf("prototype did not reach the blocked response writer (completion error: %v)", err)
		}
		record.Release()
		allocator.AssertSize(t, 0)
		t.Fatalf("prototype did not reach the blocked response writer (completion error: %v)", err)
	}
	if !active.Load() {
		err := finish()
		if errors.Is(err, errDashboardDirectArrowPrototypeWait) {
			t.Fatalf("executor was released while the synchronous sink was blocked (completion error: %v)", err)
		}
		record.Release()
		allocator.AssertSize(t, 0)
		t.Fatalf("executor was released while the synchronous sink was blocked (completion error: %v)", err)
	}
	select {
	case err := <-done:
		recorder.resume()
		record.Release()
		allocator.AssertSize(t, 0)
		t.Fatalf("prototype returned before slow consumer released it: %v", err)
	default:
	}
	if err := finish(); err != nil {
		if errors.Is(err, errDashboardDirectArrowPrototypeWait) {
			t.Fatal(err)
		}
		record.Release()
		allocator.AssertSize(t, 0)
		t.Fatal(err)
	}
	if active.Load() {
		record.Release()
		t.Fatal("executor remained pinned after the response completed")
	}
	record.Release()
	allocator.AssertSize(t, 0)
}

type dashboardDirectArrowBlockingWriter struct {
	*httptest.ResponseRecorder
	entered    chan struct{}
	release    chan struct{}
	once       sync.Once
	resumeOnce sync.Once
}

func (w *dashboardDirectArrowBlockingWriter) Write(payload []byte) (int, error) {
	w.once.Do(func() {
		close(w.entered)
		<-w.release
	})
	return w.ResponseRecorder.Write(payload)
}

func (w *dashboardDirectArrowBlockingWriter) resume() {
	w.resumeOnce.Do(func() { close(w.release) })
}

func TestDashboardDirectArrowPrototypeEligibilityIsNarrow(t *testing.T) {
	model := dashboardBaselineModel()
	detail, err := dashboardBaselineDetailDefinition("detail", dashboardBaselineNarrowFields)
	if err != nil {
		t.Fatal(err)
	}
	filters := []dataquery.Filter{{Field: "orders.field_00", Operator: "eq", Values: []any{int64(7)}}}
	request, config, err := dashboardDirectArrowPrototypeQuery(detail, model, filters, 7, 50, "a")
	if err != nil {
		t.Fatalf("ordinary detail table rejected: %v", err)
	}
	if request.Offset != 7 || request.Limit != 51 || len(request.Filters) != 1 || request.Filters[0].Field != filters[0].Field {
		t.Fatalf("prototype pagination/filter request = %#v", request)
	}
	if request.Surface != dataquery.SurfaceDashboard || request.Operation != dataquery.OperationDashboardRows {
		t.Fatalf("prototype admission metadata = %q/%q, want dashboard/dashboard_rows", request.Surface, request.Operation)
	}
	if len(request.Sort) != 1 || request.Sort[0] != (dataquery.Sort{Field: "field_00", Direction: "desc"}) {
		t.Fatalf("prototype stable sort = %#v", request.Sort)
	}
	if len(request.Fields) != dashboardBaselineNarrowFields || len(request.Metrics) != 0 || request.Fields[0] != (dataquery.Field{Field: "orders.field_00", Alias: "field_00"}) {
		t.Fatalf("prototype governed projection = %#v", request.Fields)
	}
	if config.logicalTypes["field_00"] != "integer" || config.labels["field_00"] != "field_00" {
		t.Fatalf("prototype public field metadata = logical %q label %q", config.logicalTypes["field_00"], config.labels["field_00"])
	}
	if _, _, err := dashboardDirectArrowPrototypeQuery(detail, model, nil, 0, 50, "all"); err == nil {
		t.Fatal("multi-block detail query was accepted")
	}
	calculated := detail
	calculations := []visualizationir.VisualizationCalculation{{}}
	calculated.Spec.Value.(*visualizationir.TableVisualizationSpec).Calculations = &calculations
	if _, _, err := dashboardDirectArrowPrototypeQuery(calculated, model, nil, 0, 50, "a"); err == nil {
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
		if _, _, err := dashboardDirectArrowPrototypeQuery(definition, model, nil, 0, 50, "a"); err == nil {
			t.Fatalf("%s query was accepted", kind.name)
		}
	}
}

type dashboardDirectArrowFixtureExecutor struct {
	schema      *arrow.Schema
	record      arrow.RecordBatch
	afterRecord error
	active      *atomic.Bool
}

func (e dashboardDirectArrowFixtureExecutor) ExecuteDataQueryArrow(ctx context.Context, _ dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if e.active != nil {
		e.active.Store(true)
		defer e.active.Store(false)
	}
	if err := ctx.Err(); err != nil {
		return dataquery.Result{}, err
	}
	if e.schema == nil {
		return dataquery.Result{}, errors.New("fixture schema is required")
	}
	if err := sink.WriteSchema(e.schema); err != nil {
		return dataquery.Result{}, err
	}
	if e.record != nil {
		if err := sink.WriteRecord(e.record); err != nil {
			return dataquery.Result{}, err
		}
	}
	result := dataquery.Result{}
	if stats, ok := sink.(arrowquery.SinkStats); ok {
		result.RowsReturned = stats.RowsWritten()
	}
	return result, e.afterRecord
}

func assertDashboardDirectArrowEquivalent(t testing.TB, currentPayload, candidatePayload []byte, rows, columns int) {
	t.Helper()
	currentReader, err := ipc.NewReader(bytes.NewReader(currentPayload))
	if err != nil {
		t.Fatalf("open current Arrow response: %v", err)
	}
	defer currentReader.Release()
	candidateReader, err := ipc.NewReader(bytes.NewReader(candidatePayload))
	if err != nil {
		t.Fatalf("open candidate Arrow response: %v", err)
	}
	defer candidateReader.Release()
	if currentReader.Schema().NumFields() != columns || candidateReader.Schema().NumFields() != columns {
		t.Fatalf("current/candidate columns = %d/%d, want %d", currentReader.Schema().NumFields(), candidateReader.Schema().NumFields(), columns)
	}
	if err := validateDashboardNativeArrowMetadata(candidateReader.Schema().Metadata(), dashboardNativeArrowSchemaMetadataAllowlist); err != nil {
		t.Fatal(err)
	}
	for column := 0; column < columns; column++ {
		currentField := currentReader.Schema().Field(column)
		candidateField := candidateReader.Schema().Field(column)
		if currentField.Name != candidateField.Name || candidateField.Name != fmt.Sprintf("field_%02d", column) {
			t.Fatalf("field %d aliases current/candidate = %q/%q", column, currentField.Name, candidateField.Name)
		}
		if currentField.Type.ID() != arrow.STRING {
			t.Fatalf("current field %d type = %s, want legacy string", column, currentField.Type)
		}
		if candidateField.Type.ID() != dashboardBaselineArrowField(candidateField.Name, column, false).Type.ID() {
			t.Fatalf("candidate field %d type = %s", column, candidateField.Type)
		}
		if err := validateDashboardNativeArrowMetadata(candidateField.Metadata, dashboardNativeArrowFieldMetadataAllowlist); err != nil {
			t.Fatalf("candidate field %d metadata: %v", column, err)
		}
	}
	if !currentReader.Next() || !candidateReader.Next() {
		t.Fatalf("read current/candidate record: current=%v candidate=%v", currentReader.Err(), candidateReader.Err())
	}
	currentRecord, candidateRecord := currentReader.Record(), candidateReader.Record()
	if currentRecord.NumRows() != int64(rows) || candidateRecord.NumRows() != int64(rows) {
		t.Fatalf("current/candidate rows = %d/%d, want %d", currentRecord.NumRows(), candidateRecord.NumRows(), rows)
	}
	for column := 0; column < columns; column++ {
		legacy := currentRecord.Column(column).(*array.String)
		for row := 0; row < rows; row++ {
			native := candidateRecord.Column(column)
			wantNull := (row+column)%13 == 0
			if native.IsNull(row) != wantNull {
				t.Fatalf("candidate field %d row %d null = %v, want %v", column, row, native.IsNull(row), wantNull)
			}
			if legacy.IsNull(row) {
				t.Fatalf("legacy field %d row %d unexpectedly null", column, row)
			}
			wantProjection := ""
			if !wantNull {
				wantProjection = dashboardWarmCurrentProjection(dashboardWarmNativeIPCValue(t, native, row))
			}
			if got := legacy.Value(row); got != wantProjection {
				t.Fatalf("field %d row %d current projection = %q, want %q", column, row, got, wantProjection)
			}
		}
		if dictionary, ok := candidateRecord.Column(column).(*array.Dictionary); ok {
			assertDashboardWarmDictionary(t, dictionary, column, rows)
		}
	}
	if currentReader.Next() || currentReader.Err() != nil || candidateReader.Next() || candidateReader.Err() != nil {
		t.Fatalf("unexpected additional records current=%v candidate=%v", currentReader.Err(), candidateReader.Err())
	}
}
