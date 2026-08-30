package http

import (
	"bytes"
	"context"
	"fmt"
	"math"
	stdhttp "net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/flidai/leapview/internal/analytics/arrowdecode"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	"github.com/flidai/leapview/pkg/arrowresult"
)

const dashboardWarmWideChartColumns = 32

var dashboardWarmBundleVisuals = []string{"warm_bundle_chart_0", "warm_bundle_chart_1", "warm_bundle_chart_2", "warm_bundle_chart_3"}

type dashboardWarmWorkload struct {
	name     string
	rows     int
	response string
}

var dashboardWarmWorkloads = []dashboardWarmWorkload{
	{name: "kpi", rows: 1, response: "json"},
	{name: "chart_bundle", rows: 50, response: "json"},
	{name: "wide_chart", rows: 1_000, response: "json"},
	{name: "table_window_json", rows: 1_000, response: "json"},
	{name: "table_window_arrow", rows: 1_000, response: "arrow"},
}

var dashboardWarmPayloadSink []byte

// BenchmarkDashboardWarmCacheConcurrency measures exact batches of identical
// warm requests. One benchmark operation is one simultaneous user batch; the
// request-level latency and throughput metrics normalize that batch by users.
func BenchmarkDashboardWarmCacheConcurrency(b *testing.B) {
	for _, workload := range dashboardWarmWorkloads {
		for _, users := range []int{1, 10, 20, 100} {
			b.Run(workload.name+"/users_"+strconv.Itoa(users), func(b *testing.B) {
				fixture := newDashboardWarmFixture(b, workload.rows)
				assertDashboardWarmFixture(b, fixture, workload)
				fixture.database.queries.Store(0)
				durations := make([]time.Duration, 0, b.N*users)
				var total dashboardWarmSample

				b.ReportAllocs()
				b.ResetTimer()
				b.StopTimer()
				for range b.N {
					start := make(chan struct{})
					results := make(chan dashboardWarmSample, users)
					var batchStarted time.Time
					for range users {
						go func() {
							<-start
							body, observation, err := fixture.executeWarmRequest(workload)
							results <- dashboardWarmSample{duration: time.Since(batchStarted), bytes: int64(len(body)), hits: observation.hits.Load(), misses: observation.misses.Load(), coalesced: observation.coalesced.Load(), physical: observation.physical.Load(), err: err}
						}()
					}
					b.StartTimer()
					batchStarted = time.Now()
					close(start)
					for range users {
						sample := <-results
						if sample.err != nil {
							b.Fatal(sample.err)
						}
						durations = append(durations, sample.duration)
						total.add(sample)
					}
					b.StopTimer()
				}

				requests := int64(b.N * users)
				if total.misses != 0 || total.coalesced != 0 || total.physical != 0 || total.hits < requests || fixture.database.queries.Load() != 0 {
					b.Fatalf("invalid warm samples: requests=%d hits=%d misses=%d coalesced=%d physical=%d database=%d", requests, total.hits, total.misses, total.coalesced, total.physical, fixture.database.queries.Load())
				}
				sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
				elapsed := b.Elapsed()
				b.ReportMetric(float64(total.bytes)/float64(requests), "response-bytes/request")
				b.ReportMetric(float64(total.hits)/float64(requests), "cache-hits/request")
				b.ReportMetric(float64(elapsed.Nanoseconds())/float64(requests), "wall-ns/request")
				b.ReportMetric(float64(requests)/elapsed.Seconds(), "requests/s")
				reportDashboardWarmPercentiles(b, durations)
			})
		}
	}
}

func BenchmarkDashboardWarmSerializationStages(b *testing.B) {
	for _, workload := range dashboardWarmWorkloads[:4] {
		b.Run(workload.name, func(b *testing.B) {
			fixture := newDashboardWarmFixture(b, workload.rows)
			assertDashboardWarmFixture(b, fixture, workload)
			value, envelope, err := fixture.warmSerializationValue(workload)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("json_encoding", func(b *testing.B) {
				var totalBytes int64
				b.ReportAllocs()
				for range b.N {
					payload := encodeDashboardWarmJSON(value)
					dashboardWarmPayloadSink = payload
					totalBytes += int64(len(payload))
				}
				b.ReportMetric(float64(totalBytes)/float64(b.N), "json-bytes/op")
			})
			if workload.name != "table_window_json" {
				return
			}
			b.Run("current_string_projection", func(b *testing.B) {
				b.ReportAllocs()
				for range b.N {
					rowset, err := dashboardVisualizationRowset(envelope, "a", 0, workload.rows, "fai-542-warm", dashboardBaselineSnapshot)
					if err != nil {
						b.Fatal(err)
					}
					dashboardBaselineRowsetSink = rowset
				}
			})
			rowset, err := dashboardVisualizationRowset(envelope, "a", 0, workload.rows, "fai-542-warm", dashboardBaselineSnapshot)
			if err != nil {
				b.Fatal(err)
			}
			b.Run("current_string_ipc", func(b *testing.B) {
				var totalBytes int64
				b.ReportAllocs()
				for range b.N {
					payload, err := encodeDashboardTableArrow(rowset)
					if err != nil {
						b.Fatal(err)
					}
					dashboardWarmPayloadSink = payload
					totalBytes += int64(len(payload))
				}
				b.ReportMetric(float64(totalBytes)/float64(b.N), "ipc-bytes/op")
			})
			lease := newDashboardWarmNativeLease(b, workload.rows, dashboardBaselineWideFields)
			b.Cleanup(lease.Release)
			b.Run("native_ipc_reference", func(b *testing.B) {
				var totalBytes int64
				b.ReportAllocs()
				for range b.N {
					payload, err := encodeDashboardWarmNativeIPC(lease)
					if err != nil {
						b.Fatal(err)
					}
					dashboardWarmPayloadSink = payload
					totalBytes += int64(len(payload))
				}
				b.ReportMetric(float64(totalBytes)/float64(b.N), "ipc-bytes/op")
			})
		})
	}
}

func TestDashboardWarmCacheExperimentUsesRetainedResults(t *testing.T) {
	for _, workload := range dashboardWarmWorkloads {
		t.Run(workload.name, func(t *testing.T) {
			fixture := newDashboardWarmFixture(t, workload.rows)
			assertDashboardWarmFixture(t, fixture, workload)
		})
	}
}

func TestDashboardWarmNativeReferencePreservesPhysicalFidelity(t *testing.T) {
	fixture := newDashboardWarmFixture(t, 50)
	current, _, err := fixture.executeWarmRequest(dashboardWarmWorkload{name: "table_window_arrow", rows: 50, response: "arrow"})
	if err != nil {
		t.Fatal(err)
	}
	lease := newDashboardWarmNativeLease(t, 50, dashboardBaselineWideFields)
	defer lease.Release()
	decoded, err := arrowdecode.DecodeRows(lease)
	if err != nil {
		t.Fatal(err)
	}
	native, err := encodeDashboardWarmNativeIPC(lease)
	if err != nil {
		t.Fatal(err)
	}
	currentReader, err := ipc.NewReader(bytes.NewReader(current))
	if err != nil {
		t.Fatal(err)
	}
	defer currentReader.Release()
	nativeReader, err := ipc.NewReader(bytes.NewReader(native))
	if err != nil {
		t.Fatal(err)
	}
	defer nativeReader.Release()

	if value, ok := nativeReader.Schema().Metadata().GetValue("fai.fixture"); !ok || value != "dashboard-arrow-evidence" {
		t.Fatalf("native schema fixture metadata = %q, %v", value, ok)
	}
	if value, ok := currentReader.Schema().Metadata().GetValue("fai.fixture"); ok {
		t.Fatalf("current string projection unexpectedly retained schema metadata %q", value)
	}
	wantTypes := []arrow.Type{arrow.INT64, arrow.FLOAT64, arrow.BOOL, arrow.STRING, arrow.BINARY, arrow.TIMESTAMP, arrow.DECIMAL128, arrow.DATE32, arrow.DICTIONARY}
	for index, field := range nativeReader.Schema().Fields() {
		if got, want := field.Type.ID(), wantTypes[index%len(wantTypes)]; got != want {
			t.Fatalf("native field %d type = %s, want %s", index, got, want)
		}
		if value, ok := field.Metadata.GetValue("fai.fixture.column"); !ok || value != field.Name {
			t.Fatalf("native field %d metadata = %q, %v", index, value, ok)
		}
		if value, ok := currentReader.Schema().Field(index).Metadata.GetValue("fai.fixture.column"); ok {
			t.Fatalf("current field %d unexpectedly retained metadata %q", index, value)
		}
		if got := currentReader.Schema().Field(index).Type.ID(); got != arrow.STRING {
			t.Fatalf("current field %d type = %s, want all-string baseline", index, got)
		}
	}
	if !nativeReader.Next() || !currentReader.Next() {
		t.Fatalf("read current/native records: current=%v native=%v", currentReader.Err(), nativeReader.Err())
	}
	nativeRecord := nativeReader.Record()
	currentRecord := currentReader.Record()
	if got, want := int(nativeRecord.NumRows()), len(decoded); got != want || got != 50 {
		t.Fatalf("native/decoded rows = %d/%d, want 50", got, want)
	}
	for column, field := range nativeReader.Schema().Fields() {
		currentColumn := currentRecord.Column(column).(*array.String)
		for row := 0; row < int(nativeRecord.NumRows()); row++ {
			wantNull := (row+column)%13 == 0
			if got := nativeRecord.Column(column).IsNull(row); got != wantNull {
				t.Fatalf("native field %d row %d null = %v, want %v", column, row, got, wantNull)
			}
			if wantNull {
				if got := decoded[row][field.Name]; got != nil {
					t.Fatalf("decoded field %d row %d null = %#v", column, row, got)
				}
				if currentColumn.IsNull(row) || currentColumn.Value(row) != "" {
					t.Fatalf("current field %d row %d null projection = null:%v value:%q, want non-null empty string", column, row, currentColumn.IsNull(row), currentColumn.Value(row))
				}
				continue
			}

			want := dashboardWarmExpectedDecodedValue(row, column)
			assertDashboardWarmValue(t, fmt.Sprintf("native IPC field %d row %d", column, row), dashboardWarmNativeIPCValue(t, nativeRecord.Column(column), row), want)
			assertDashboardWarmValue(t, fmt.Sprintf("decoded field %d row %d", column, row), decoded[row][field.Name], want)
			if got, wantString := currentColumn.Value(row), dashboardWarmCurrentProjection(want); currentColumn.IsNull(row) || got != wantString {
				t.Fatalf("current field %d row %d projection = null:%v value:%q, want %q", column, row, currentColumn.IsNull(row), got, wantString)
			}
		}
		if dictionary, ok := nativeRecord.Column(column).(*array.Dictionary); ok {
			assertDashboardWarmDictionary(t, dictionary, column, int(nativeRecord.NumRows()))
		}
	}
	if nativeRecord.Column(3).IsNull(1) || nativeRecord.Column(3).(*array.String).Value(1) != "" {
		t.Fatal("native UTF-8 empty string became null or changed")
	}
	if current := currentRecord.Column(3).(*array.String); current.IsNull(1) || current.Value(1) != "" {
		t.Fatal("current UTF-8 empty string projection changed")
	}
}

func assertDashboardWarmDictionary(tb testing.TB, values *array.Dictionary, column, rows int) {
	tb.Helper()
	dictionary, ok := values.Dictionary().(*array.String)
	if !ok {
		tb.Fatalf("dictionary field %d values = %T, want utf8", column, values.Dictionary())
	}
	wantIndex := 0
	for row := 0; row < rows; row++ {
		if (row+column)%13 == 0 {
			continue
		}
		if got := values.GetValueIndex(row); got != wantIndex {
			tb.Fatalf("dictionary field %d row %d index = %d, want %d", column, row, got, wantIndex)
		}
		if got, want := dictionary.Value(wantIndex), "category-"+strconv.Itoa(row%97); got != want {
			tb.Fatalf("dictionary field %d value %d = %q, want %q", column, wantIndex, got, want)
		}
		wantIndex++
	}
	if got := dictionary.Len(); got != wantIndex {
		tb.Fatalf("dictionary field %d values = %d, want %d", column, got, wantIndex)
	}
}

func dashboardWarmExpectedDecodedValue(row, column int) any {
	value := int64(row*37 + column + 1)
	switch column % 9 {
	case 0:
		return value
	case 1:
		return float64(value)/7.0 + 0.125
	case 2:
		return (row+column)%2 == 0
	case 3:
		if row == 1 {
			return ""
		}
		return "value-" + strconv.Itoa((row*17+column)%997)
	case 4:
		return []byte("bytes-" + strconv.Itoa((row*19+column)%997))
	case 5:
		return arrow.Timestamp(1_700_000_000_000_000 + value*1_000).ToTime(arrow.Microsecond).In(time.UTC)
	case 6:
		return decimal128.FromI64(value * 1_000).ToString(3)
	case 7:
		return arrow.Date32(19_000 + value%2_000).ToTime().In(time.UTC)
	default:
		return "category-" + strconv.Itoa(row%97)
	}
}

func dashboardWarmNativeIPCValue(tb testing.TB, values arrow.Array, row int) any {
	tb.Helper()
	switch values := values.(type) {
	case *array.Int64:
		return values.Value(row)
	case *array.Float64:
		return values.Value(row)
	case *array.Boolean:
		return values.Value(row)
	case *array.String:
		return values.Value(row)
	case *array.Binary:
		return values.Value(row)
	case *array.Timestamp:
		typeInfo := values.DataType().(*arrow.TimestampType)
		if typeInfo.Unit != arrow.Microsecond {
			tb.Fatalf("timestamp unit = %s, want microsecond", typeInfo.Unit)
		}
		return values.Value(row).ToTime(typeInfo.Unit).In(time.UTC)
	case *array.Decimal128:
		typeInfo := values.DataType().(*arrow.Decimal128Type)
		if typeInfo.Precision != 38 || typeInfo.Scale != 3 {
			tb.Fatalf("decimal type = precision %d scale %d, want 38/3", typeInfo.Precision, typeInfo.Scale)
		}
		return values.Value(row).ToString(typeInfo.Scale)
	case *array.Date32:
		return values.Value(row).ToTime().In(time.UTC)
	case *array.Dictionary:
		typeInfo := values.DataType().(*arrow.DictionaryType)
		if typeInfo.IndexType.ID() != arrow.INT16 || typeInfo.ValueType.ID() != arrow.STRING {
			tb.Fatalf("dictionary type = %s => %s, want int16 => utf8", typeInfo.IndexType, typeInfo.ValueType)
		}
		index := values.GetValueIndex(row)
		dictionary, ok := values.Dictionary().(*array.String)
		if !ok || index < 0 || index >= dictionary.Len() {
			tb.Fatalf("dictionary row %d index = %d, values=%T len=%d", row, index, values.Dictionary(), values.Dictionary().Len())
		}
		return dictionary.Value(index)
	default:
		tb.Fatalf("unsupported native IPC fidelity array %T", values)
		return nil
	}
}

func dashboardWarmCurrentProjection(value any) string {
	switch value := value.(type) {
	case []byte:
		return string(value)
	case time.Time:
		return value.Format(time.DateOnly)
	case float64:
		return dashboardCellString(math.Round(value*100) / 100)
	default:
		return dashboardCellString(value)
	}
}

func assertDashboardWarmValue(tb testing.TB, label string, got, want any) {
	tb.Helper()
	switch want := want.(type) {
	case []byte:
		gotBytes, ok := got.([]byte)
		if !ok || !bytes.Equal(gotBytes, want) {
			tb.Fatalf("%s = %#v (%T), want exact bytes %q", label, got, got, want)
		}
	case time.Time:
		gotTime, ok := got.(time.Time)
		if !ok || !gotTime.Equal(want) || gotTime.Location() != time.UTC {
			tb.Fatalf("%s = %#v (%T), want %s in UTC", label, got, got, want.Format(time.RFC3339Nano))
		}
	default:
		if got != want {
			tb.Fatalf("%s = %#v (%T), want %#v (%T)", label, got, got, want, want)
		}
	}
}

type dashboardWarmObservation struct {
	hits      atomic.Int64
	misses    atomic.Int64
	coalesced atomic.Int64
	physical  atomic.Int64
}

type dashboardWarmSample struct {
	duration  time.Duration
	bytes     int64
	hits      int64
	misses    int64
	coalesced int64
	physical  int64
	err       error
}

func newDashboardWarmFixture(tb testing.TB, rows int) *dashboardBaselineFixture {
	tb.Helper()
	fixture := newDashboardBaselineFixture(tb, rows)
	fixture.database.evidenceMetadata = true
	return fixture
}

func (s *dashboardWarmSample) add(value dashboardWarmSample) {
	s.bytes += value.bytes
	s.hits += value.hits
	s.misses += value.misses
	s.coalesced += value.coalesced
	s.physical += value.physical
}

func (f *dashboardBaselineFixture) executeWarmRequest(workload dashboardWarmWorkload) ([]byte, *dashboardWarmObservation, error) {
	observation := &dashboardWarmObservation{}
	ctx := dataquery.WithGovernor(context.Background(), f.governor)
	ctx = dataquery.WithCacheOutcomeObserver(ctx, func(outcome string) {
		switch outcome {
		case dataquery.CacheHit:
			observation.hits.Add(1)
		case dataquery.CacheMiss:
			observation.misses.Add(1)
		case dataquery.CacheCoalesced:
			observation.coalesced.Add(1)
		}
	})
	ctx = dataquery.WithPhysicalQueryObserver(ctx, func(value dataquery.PhysicalQueryObservation) { observation.physical.Add(int64(value.Count)) })

	switch workload.name {
	case "kpi":
		envelope, err := f.service.QueryVisualization(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, "warm_kpi")
		return marshalDashboardWarmValue(envelope, observation, err)
	case "wide_chart":
		envelope, err := f.service.QueryVisualization(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, "warm_wide_chart")
		return marshalDashboardWarmValue(envelope, observation, err)
	case "chart_bundle":
		visuals, err := f.queryDashboardWarmBundle(ctx, observation)
		return marshalDashboardWarmValue(visuals, observation, err)
	case "table_window_json", "table_window_arrow":
		envelope, err := f.service.QueryVisualizationWindow(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, visualizationir.VisualizationWindowRequest{VisualID: "detail_wide", SpecRevision: "", DataRevision: 1, BlockID: "a", Limit: int64(workload.rows)})
		if err != nil {
			return nil, observation, err
		}
		if workload.response == "json" {
			return marshalDashboardWarmValue(envelope, observation, nil)
		}
		rowset, err := dashboardVisualizationRowset(envelope, "a", 0, workload.rows, "fai-542-warm", dashboardBaselineSnapshot)
		if err != nil {
			return nil, observation, err
		}
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(stdhttp.MethodPost, "/fai-542-warm", nil)
		request.Header.Set("Accept", dashboardArrowMediaType)
		writeDashboardTableRowset(recorder, request, rowset, envelope)
		if recorder.Code != stdhttp.StatusOK {
			return nil, observation, fmt.Errorf("current Arrow response status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return recorder.Body.Bytes(), observation, nil
	default:
		return nil, observation, fmt.Errorf("unknown FAI-542 workload %q", workload.name)
	}
}

func (f *dashboardBaselineFixture) queryDashboardWarmBundle(ctx context.Context, observation *dashboardWarmObservation) (map[string]visualizationir.VisualizationEnvelope, error) {
	targets := make([]consumer.Target, len(dashboardWarmBundleVisuals))
	for index, id := range dashboardWarmBundleVisuals {
		targets[index] = consumer.Target{Kind: consumer.KindVisual, ID: id}
	}
	visuals := make(map[string]visualizationir.VisualizationEnvelope, len(targets))
	var publishMu sync.Mutex
	var publishErr error
	err := f.service.ExecuteConsumersPage(ctx, consumer.Request{DashboardID: dashboardBaselineDashboardID, PageID: dashboardBaselinePageID, ModelID: dashboardBaselineModelID.String(), Command: "fai_542_warm_bundle", Targets: targets, Concurrency: 1}, func(result consumer.Result) bool {
		if observation != nil && result.Queries > 0 {
			observation.physical.Add(int64(result.Queries))
		}
		publishMu.Lock()
		defer publishMu.Unlock()
		if result.Err != nil {
			publishErr = result.Err
			return false
		}
		visuals[result.Target.ID] = result.Envelope
		return true
	})
	if err != nil {
		return nil, err
	}
	if publishErr != nil {
		return nil, publishErr
	}
	if len(visuals) != len(targets) {
		return nil, fmt.Errorf("chart bundle returned %d visuals, want %d", len(visuals), len(targets))
	}
	return visuals, nil
}

func (f *dashboardBaselineFixture) warmSerializationValue(workload dashboardWarmWorkload) (any, visualizationir.VisualizationEnvelope, error) {
	ctx := dataquery.WithGovernor(context.Background(), f.governor)
	switch workload.name {
	case "kpi":
		envelope, err := f.service.QueryVisualization(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, "warm_kpi")
		return envelope, envelope, err
	case "wide_chart":
		envelope, err := f.service.QueryVisualization(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, "warm_wide_chart")
		return envelope, envelope, err
	case "chart_bundle":
		visuals, err := f.queryDashboardWarmBundle(ctx, nil)
		return visuals, visualizationir.VisualizationEnvelope{}, err
	case "table_window_json":
		envelope, err := f.service.QueryVisualizationWindow(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, visualizationir.VisualizationWindowRequest{VisualID: "detail_wide", DataRevision: 1, BlockID: "a", Limit: int64(workload.rows)})
		return envelope, envelope, err
	default:
		return nil, visualizationir.VisualizationEnvelope{}, fmt.Errorf("unsupported serialization workload %q", workload.name)
	}
}

func assertDashboardWarmFixture(tb testing.TB, fixture *dashboardBaselineFixture, workload dashboardWarmWorkload) {
	tb.Helper()
	fixture.clearCache()
	cold, coldObservation, err := fixture.executeWarmRequest(workload)
	if err != nil {
		tb.Fatal(err)
	}
	if coldObservation.misses.Load() == 0 || coldObservation.physical.Load() == 0 {
		tb.Fatalf("cold %s outcomes hits=%d misses=%d coalesced=%d physical=%d", workload.name, coldObservation.hits.Load(), coldObservation.misses.Load(), coldObservation.coalesced.Load(), coldObservation.physical.Load())
	}
	fixture.database.queries.Store(0)
	warm, warmObservation, err := fixture.executeWarmRequest(workload)
	if err != nil {
		tb.Fatal(err)
	}
	if warmObservation.hits.Load() == 0 || warmObservation.misses.Load() != 0 || warmObservation.coalesced.Load() != 0 || warmObservation.physical.Load() != 0 || fixture.database.queries.Load() != 0 {
		tb.Fatalf("warm %s outcomes hits=%d misses=%d coalesced=%d physical=%d database=%d", workload.name, warmObservation.hits.Load(), warmObservation.misses.Load(), warmObservation.coalesced.Load(), warmObservation.physical.Load(), fixture.database.queries.Load())
	}
	if !bytes.Equal(cold, warm) {
		tb.Fatalf("cold and warm %s responses differ", workload.name)
	}
}

func marshalDashboardWarmValue(value any, observation *dashboardWarmObservation, err error) ([]byte, *dashboardWarmObservation, error) {
	if err != nil {
		return nil, observation, err
	}
	return encodeDashboardWarmJSON(value), observation, nil
}

func encodeDashboardWarmJSON(value any) []byte {
	recorder := httptest.NewRecorder()
	writeJSON(recorder, stdhttp.StatusOK, value)
	return recorder.Body.Bytes()
}

func reportDashboardWarmPercentiles(b *testing.B, durations []time.Duration) {
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

func dashboardWarmCacheExtendModel(model *semanticmodel.Model) {
	if model == nil {
		return
	}
	for index := 0; index < dashboardWarmWideChartColumns-1; index++ {
		name := fmt.Sprintf("warm_metric_%02d", index)
		model.Metrics[name] = semanticmodel.Metric{Name: name, Type: "aggregate", Dataset: dashboardBaselineDatasetID, Aggregation: "sum", Input: &semanticmodel.MetricInput{Field: "orders.field_06"}, Empty: "zero"}
	}
}

func dashboardWarmCacheVisualDefinitions() (map[string]visualizationdefinition.Definition, error) {
	definitions := map[string]visualizationdefinition.Definition{}
	kpiBase := dashboardBaselineSpecBase("warm_kpi", []visualizationir.VisualizationField{{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: "Value"}})
	kpiBase.Kind = "kpi"
	kpiBase.DataBudget.MaxRows = 1
	kpiSpec := visualizationir.VisualizationSpec{Value: &visualizationir.KPIVisualizationSpec{VisualizationSpecBase: kpiBase, Kind: "kpi", Value: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"}, Presentation: visualizationir.KPIVisualizationPresentation{Mode: visualizationir.VisualizationKPIModeCompact, Delta: visualizationir.VisualizationKPIDeltaModeAbsolute, FavorableDirection: visualizationir.VisualizationKPIDirectionNeutral, MissingComparison: visualizationir.VisualizationKPIMissingComparisonShowUnavailable, Ranges: []visualizationir.VisualizationKPIQualitativeRange{}}}}
	kpi, err := visualizationdefinition.New("warm_kpi", kpiSpec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: visualizationdefinition.ResultScalar, ModelID: dashboardBaselineModelID.String(), DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: dashboardBaselineDatasetID, Metrics: []visualizationdefinition.FieldBinding{{FieldID: "warm_metric_00", Alias: "value"}}, Limit: 1}})
	if err != nil {
		return nil, err
	}
	definitions["warm_kpi"] = kpi

	wide, err := dashboardWarmCartesianDefinition("warm_wide_chart", dashboardWarmWideChartColumns-1, 0)
	if err != nil {
		return nil, err
	}
	definitions["warm_wide_chart"] = wide
	for index, id := range dashboardWarmBundleVisuals {
		definition, err := dashboardWarmCartesianDefinition(id, 1, index)
		if err != nil {
			return nil, err
		}
		definitions[id] = definition
	}
	return definitions, nil
}

func dashboardWarmCartesianDefinition(id string, metricCount, metricOffset int) (visualizationdefinition.Definition, error) {
	fields := make([]visualizationir.VisualizationField, 1, metricCount+1)
	fields[0] = visualizationir.VisualizationField{ID: "label", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Nullable: true, Label: "Label"}
	y := make([]visualizationir.VisualizationFieldRef, metricCount)
	metrics := make([]visualizationdefinition.FieldBinding, metricCount)
	for index := range metricCount {
		alias := fmt.Sprintf("value_%02d", index)
		if metricCount == 1 {
			alias = "value"
		}
		fields = append(fields, visualizationir.VisualizationField{ID: alias, Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Nullable: true, Label: alias})
		y[index] = visualizationir.VisualizationFieldRef{Dataset: "primary", Field: alias}
		metrics[index] = visualizationdefinition.FieldBinding{FieldID: fmt.Sprintf("warm_metric_%02d", index+metricOffset), Alias: alias}
	}
	base := dashboardBaselineSpecBase(id, fields)
	base.Kind = "cartesian"
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.CartesianVisualizationSpec{VisualizationSpecBase: base, Kind: "cartesian", Mark: visualizationir.VisualizationCartesianMarkLine, X: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "label"}, Y: y, Presentation: visualizationir.CartesianVisualizationPresentation{VisualizationPresentation: visualizationir.VisualizationPresentation{Legend: visualizationir.VisualizationLegendPositionHidden, LabelPolicy: visualizationir.VisualizationLabelPolicy{Density: visualizationir.VisualizationLabelDensityHidden, Priority: []visualizationir.VisualizationLabelPriority{}, MaxCharacters: 24, TooltipFallback: true}}}}}
	shape := visualizationdefinition.ResultCategoryMultiMeasure
	if metricCount == 1 {
		shape = visualizationdefinition.ResultCategoryValue
	}
	return visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{Kind: visualizationdefinition.QueryAggregate, ResultShape: shape, ModelID: dashboardBaselineModelID.String(), DatasetID: "primary", Aggregate: &visualizationdefinition.AggregateQueryBinding{TableID: dashboardBaselineDatasetID, Dimensions: []visualizationdefinition.FieldBinding{{FieldID: dashboardBaselineDatasetID + ".field_03", Alias: "label"}}, Metrics: metrics, Limit: 2_000}})
}

func newDashboardWarmNativeLease(tb testing.TB, rows, columns int) *arrowresult.Lease {
	tb.Helper()
	names := make([]string, columns)
	for index := range names {
		names[index] = fmt.Sprintf("field_%02d", index)
	}
	collector := arrowresult.NewBuilder()
	database := &dashboardBaselineDatabase{rows: rows, evidenceMetadata: true}
	if err := database.QueryArrow(context.Background(), semanticquery.Plan{Columns: names}, collector); err != nil {
		collector.Abort()
		tb.Fatal(err)
	}
	result, err := collector.Finish()
	if err != nil {
		tb.Fatal(err)
	}
	lease, err := result.Acquire()
	result.Release()
	if err != nil {
		tb.Fatal(err)
	}
	return lease
}

func encodeDashboardWarmNativeIPC(lease *arrowresult.Lease) ([]byte, error) {
	if lease == nil || lease.Schema() == nil {
		return nil, arrowresult.ErrResultReleased
	}
	var output bytes.Buffer
	writer := ipc.NewWriter(&output, ipc.WithSchema(lease.Schema()))
	if err := lease.VisitRecords(func(record arrow.RecordBatch) error { return writer.Write(record) }); err != nil {
		_ = writer.Close()
		return nil, err
	}
	if err := writer.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}
