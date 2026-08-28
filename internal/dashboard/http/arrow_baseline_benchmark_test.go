package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/decimal128"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	materializeruntime "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	"github.com/flidai/leapview/internal/dashboard"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	reportdef "github.com/flidai/leapview/internal/dashboard/report"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/go-chi/chi/v5"
)

const (
	dashboardBaselineProjectID    = projectgraph.ResourceID("project_1")
	dashboardBaselineModelID      = projectgraph.ResourceID("model_1")
	dashboardBaselineDashboardID  = "dashboard_1"
	dashboardBaselinePageID       = "overview"
	dashboardBaselineSnapshot     = "generation_1"
	dashboardBaselineDatasetID    = "orders"
	dashboardBaselineNarrowFields = 8
	dashboardBaselineWideFields   = 32
)

var (
	dashboardBaselineBodySink     []byte
	dashboardBaselineEnvelopeSink visualizationir.VisualizationEnvelope
	dashboardBaselineRowsetSink   dashboardapi.DashboardTableQueryResponse
)

type dashboardBaselineWorkload struct {
	name    string
	visual  string
	columns int
}

var dashboardBaselineWorkloads = []dashboardBaselineWorkload{
	{name: "detail_narrow", visual: "detail_narrow", columns: dashboardBaselineNarrowFields},
	{name: "detail_wide", visual: "detail_wide", columns: dashboardBaselineWideFields},
	{name: "matrix", visual: "matrix", columns: 2},
	{name: "pivot", visual: "pivot", columns: 3},
}

func BenchmarkDashboardBaselineEndToEnd(b *testing.B) {
	for _, workload := range dashboardBaselineWorkloads {
		for _, rows := range []int{1, 50, 1_000} {
			b.Run(workload.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				for _, execution := range []string{"api_direct", "dashboard_cold", "dashboard_warm"} {
					for _, response := range []string{"json", "arrow"} {
						b.Run(execution+"/"+response, func(b *testing.B) {
							fixture := newDashboardBaselineFixture(b, rows)
							arrowBefore := arrowresult.Stats()
							if execution == "dashboard_warm" {
								fixture.clearCache()
								if _, observation := fixture.serveDashboardRuntime(b, workload.visual, rows, response); observation.cacheMisses() == 0 {
									b.Fatal("warmup did not miss and populate the retained dashboard result cache")
								}
								fixture.database.queries.Store(0)
							}

							var totalBytes, physicalQueries, cacheHits, cacheMisses int64
							var planningMS, connectionWaitMS, databaseMS, executionMS int64
							b.ReportAllocs()
							b.ResetTimer()
							for range b.N {
								if execution == "dashboard_cold" {
									b.StopTimer()
									fixture.clearCache()
									b.StartTimer()
								}
								var body []byte
								var observation dashboardBaselineObservation
								if execution == "api_direct" {
									body, observation = fixture.serve(b, workload.visual, rows, response)
								} else {
									body, observation = fixture.serveDashboardRuntime(b, workload.visual, rows, response)
								}
								dashboardBaselineBodySink = body
								totalBytes += int64(len(body))
								physicalQueries += int64(observation.physicalQueries)
								cacheHits += int64(observation.cacheHits())
								cacheMisses += int64(observation.cacheMisses())
								for _, result := range observation.results {
									planningMS += result.PlanningMS
									connectionWaitMS += result.ConnectionWaitMS
									databaseMS += result.DatabaseMS
									executionMS += result.ExecutionMS
								}
							}
							b.StopTimer()

							operations := float64(b.N)
							if execution == "dashboard_warm" && (physicalQueries != 0 || fixture.database.queries.Load() != 0 || cacheHits == 0) {
								b.Fatalf("warm baseline executed database work: observations=%d database=%d hits=%d", physicalQueries, fixture.database.queries.Load(), cacheHits)
							}
							if execution == "dashboard_cold" && (physicalQueries == 0 || cacheMisses == 0) {
								b.Fatalf("cold baseline did not observe a cache miss and physical query: observations=%d misses=%d", physicalQueries, cacheMisses)
							}
							if execution == "api_direct" && (physicalQueries == 0 || cacheHits != 0 || cacheMisses != 0) {
								b.Fatalf("API direct baseline observations=%d hits=%d misses=%d", physicalQueries, cacheHits, cacheMisses)
							}
							arrowAfter := arrowresult.Stats()
							if arrowAfter.TransientBytes != arrowBefore.TransientBytes {
								b.Fatalf("Arrow transient ownership leaked across request: before=%#v after=%#v", arrowBefore, arrowAfter)
							}
							if execution == "api_direct" && (arrowAfter.Results != arrowBefore.Results || arrowAfter.Leases != arrowBefore.Leases || arrowAfter.Bytes != arrowBefore.Bytes) {
								b.Fatalf("API direct baseline retained Arrow ownership: before=%#v after=%#v", arrowBefore, arrowAfter)
							}
							b.ReportMetric(float64(totalBytes)/operations, "response-bytes/op")
							b.ReportMetric(float64(arrowAfter.Bytes-arrowBefore.Bytes), "retained-arrow-bytes")
							b.ReportMetric(float64(arrowAfter.Leases-arrowBefore.Leases), "retained-arrow-leases")
							b.ReportMetric(float64(arrowAfter.Results-arrowBefore.Results), "retained-arrow-results")
							b.ReportMetric(float64(arrowAfter.TransientBytes-arrowBefore.TransientBytes), "transient-arrow-bytes")
							b.ReportMetric(float64(physicalQueries)/operations, "physical-queries/op")
							b.ReportMetric(float64(cacheHits)/operations, "cache-hits/op")
							b.ReportMetric(float64(cacheMisses)/operations, "cache-misses/op")
							b.ReportMetric(float64(planningMS)/operations, "planning-ms/op")
							b.ReportMetric(float64(connectionWaitMS)/operations, "connection-wait-ms/op")
							b.ReportMetric(float64(databaseMS)/operations, "database-and-capture-ms/op")
							b.ReportMetric(float64(executionMS)/operations, "execution-ms/op")
							b.ReportMetric(float64(rows), "rows/op")
							b.ReportMetric(float64(workload.columns), "input-columns/op")
						})
					}
				}
			})
		}
	}
}

func BenchmarkDashboardBaselineStages(b *testing.B) {
	for _, workload := range dashboardBaselineWorkloads {
		for _, rows := range []int{1, 50, 1_000} {
			b.Run(workload.name+"/rows_"+strconv.Itoa(rows), func(b *testing.B) {
				fixture := newDashboardBaselineFixture(b, rows)
				fixture.clearCache()
				envelope := fixture.queryEnvelope(b, workload.visual, rows)
				fixture.database.queries.Store(0)

				b.Run("warm_query_and_frame", func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						dashboardBaselineEnvelopeSink = fixture.queryEnvelope(b, workload.visual, rows)
					}
					b.StopTimer()
					if fixture.database.queries.Load() != 0 {
						b.Fatalf("warm query/frame stage executed %d physical database queries", fixture.database.queries.Load())
					}
					b.ReportMetric(float64(rows), "rows/op")
					b.ReportMetric(float64(workload.columns), "input-columns/op")
				})

				b.Run("json_serialization", func(b *testing.B) {
					var totalBytes int64
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						recorder := httptest.NewRecorder()
						writeJSON(recorder, stdhttp.StatusOK, envelope)
						dashboardBaselineBodySink = recorder.Body.Bytes()
						totalBytes += int64(recorder.Body.Len())
					}
					b.ReportMetric(float64(totalBytes)/float64(b.N), "response-bytes/op")
				})

				b.Run("string_projection", func(b *testing.B) {
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						rowset, err := dashboardVisualizationRowset(envelope, "a", 0, rows, "baseline-scope", dashboardBaselineSnapshot)
						if err != nil {
							b.Fatal(err)
						}
						dashboardBaselineRowsetSink = rowset
					}
				})

				rowset, err := dashboardVisualizationRowset(envelope, "a", 0, rows, "baseline-scope", dashboardBaselineSnapshot)
				if err != nil {
					b.Fatal(err)
				}
				b.Run("ipc_generation_and_buffering", func(b *testing.B) {
					var totalBytes int64
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						payload, err := encodeDashboardTableArrow(rowset)
						if err != nil {
							b.Fatal(err)
						}
						dashboardBaselineBodySink = payload
						totalBytes += int64(len(payload))
					}
					b.ReportMetric(float64(totalBytes)/float64(b.N), "ipc-bytes/op")
				})
			})
		}
	}
}

func TestDashboardBaselineUsesCurrentCacheWithoutChangingResponses(t *testing.T) {
	fixture := newDashboardBaselineFixture(t, 50)
	fixture.clearCache()
	cold, coldObservation := fixture.serveDashboardRuntime(t, "detail_wide", 50, "arrow")
	if coldObservation.cacheMisses() == 0 || coldObservation.physicalQueries == 0 {
		t.Fatalf("cold request outcomes=%v physical=%d", coldObservation.outcomes, coldObservation.physicalQueries)
	}
	fixture.database.queries.Store(0)
	warm, warmObservation := fixture.serveDashboardRuntime(t, "detail_wide", 50, "arrow")
	if warmObservation.cacheHits() == 0 || warmObservation.physicalQueries != 0 || fixture.database.queries.Load() != 0 {
		t.Fatalf("warm request outcomes=%v physical=%d database=%d", warmObservation.outcomes, warmObservation.physicalQueries, fixture.database.queries.Load())
	}
	if !bytes.Equal(cold, warm) {
		t.Fatal("cold and warm current-path Arrow responses differ")
	}
	fixture.database.queries.Store(0)
	_, firstAPI := fixture.serve(t, "detail_wide", 50, "arrow")
	_, secondAPI := fixture.serve(t, "detail_wide", 50, "arrow")
	if firstAPI.physicalQueries == 0 || secondAPI.physicalQueries == 0 || firstAPI.cacheHits()+firstAPI.cacheMisses()+secondAPI.cacheHits()+secondAPI.cacheMisses() != 0 {
		t.Fatalf("API baseline unexpectedly used retained cache: first=%#v second=%#v", firstAPI, secondAPI)
	}
}

func TestDashboardBaselineFixtureIsDeterministic(t *testing.T) {
	first := newDashboardBaselineFixture(t, 50)
	second := newDashboardBaselineFixture(t, 50)
	for _, response := range []string{"json", "arrow"} {
		firstBody, _ := first.serve(t, "detail_wide", 50, response)
		secondBody, _ := second.serve(t, "detail_wide", 50, response)
		if !bytes.Equal(firstBody, secondBody) {
			t.Fatalf("independent deterministic %s baselines differ", response)
		}
	}
}

func TestDashboardBaselineDocumentsCurrentTypeAndNullBoundaries(t *testing.T) {
	fixture := newDashboardBaselineFixture(t, 51)
	jsonBody, _ := fixture.serve(t, "detail_wide", 50, "json")
	arrowBody, arrowObservation := fixture.serve(t, "detail_wide", 50, "arrow")

	physical := fixture.database.physicalSnapshot()
	wantPhysical := []arrow.Type{arrow.INT64, arrow.FLOAT64, arrow.BOOL, arrow.STRING, arrow.BINARY, arrow.TIMESTAMP, arrow.DECIMAL128, arrow.DATE32, arrow.DICTIONARY}
	for index, want := range wantPhysical {
		if index >= len(physical.types) || physical.types[index] != want {
			t.Fatalf("source physical type %d = %v, want %v; all=%v", index, physical.types[index], want, physical.types)
		}
	}
	if physical.nulls == 0 {
		t.Fatal("deterministic source Arrow fixture did not contain nulls")
	}

	var document map[string]any
	if err := json.Unmarshal(jsonBody, &document); err != nil {
		t.Fatalf("decode JSON baseline: %v", err)
	}
	rows := dashboardBaselineJSONRows(t, document)
	if rows[0][0] != nil {
		t.Fatalf("JSON baseline null = %#v, want nil", rows[0][0])
	}
	if got := rows[1][3]; got != "" {
		t.Fatalf("JSON baseline empty string = %#v", got)
	}
	if got := rows[0][5]; got == nil || strings.Contains(fmt.Sprint(got), "T") {
		t.Fatalf("JSON baseline timestamp boundary = %#v, want current date-only string", got)
	}
	if got := rows[0][6]; got == nil || !strings.HasSuffix(fmt.Sprint(got), ".000") {
		t.Fatalf("JSON baseline decimal boundary = %#v, want exact canonical string", got)
	}

	reader, err := ipc.NewReader(bytes.NewReader(arrowBody))
	if err != nil {
		t.Fatalf("open dashboard Arrow response: %v", err)
	}
	defer reader.Release()
	for index, field := range reader.Schema().Fields() {
		if field.Type.ID() != arrow.STRING {
			t.Fatalf("response field %d physical type = %s, want current UTF-8 baseline", index, field.Type)
		}
	}
	if snapshot, ok := reader.Schema().Metadata().GetValue("leapview.serving_snapshot"); !ok || snapshot != dashboardBaselineSnapshot {
		t.Fatalf("Arrow serving snapshot metadata = %q, %v", snapshot, ok)
	}
	if queryID, ok := reader.Schema().Metadata().GetValue("leapview.query_id"); !ok || queryID != "fai-540-baseline" {
		t.Fatalf("Arrow query ID metadata = %q, %v", queryID, ok)
	}
	if arrowObservation.nextCursor == "" {
		t.Fatal("paginated Arrow baseline omitted X-Next-Cursor")
	}
	if nextCursor, ok := reader.Schema().Metadata().GetValue("leapview.next_cursor"); !ok || nextCursor != arrowObservation.nextCursor {
		t.Fatalf("Arrow next cursor metadata = %q, %v; header=%q", nextCursor, ok, arrowObservation.nextCursor)
	}
	if logicalType, ok := reader.Schema().Field(0).Metadata.GetValue("leapview.logical_type"); !ok || logicalType != "int64" || !reader.Schema().Field(0).Nullable {
		t.Fatalf("Arrow response field metadata = %q, %v nullable=%v", logicalType, ok, reader.Schema().Field(0).Nullable)
	}
	if !reader.Next() {
		t.Fatalf("read dashboard Arrow record: %v", reader.Err())
	}
	record := reader.Record()
	if record.Column(0).NullN() != 0 {
		t.Fatalf("rebuilt dashboard Arrow null count = %d, want current null-to-empty baseline", record.Column(0).NullN())
	}
	if got := record.Column(0).(*array.String).Value(0); got != "" {
		t.Fatalf("rebuilt dashboard Arrow null cell = %q, want current empty string baseline", got)
	}
	if got := record.Column(3).(*array.String).Value(1); got != "" {
		t.Fatalf("rebuilt dashboard Arrow empty-string cell = %q", got)
	}
}

func dashboardBaselineJSONRows(t *testing.T, document map[string]any) [][]any {
	t.Helper()
	dataState, ok := document["dataState"].(map[string]any)
	if !ok {
		t.Fatalf("JSON dataState = %#v", document["dataState"])
	}
	blocks, ok := dataState["blocks"].(map[string]any)
	if !ok {
		t.Fatalf("JSON blocks = %#v", dataState["blocks"])
	}
	block, ok := blocks["a"].(map[string]any)
	if !ok {
		t.Fatalf("JSON block a = %#v", blocks["a"])
	}
	rawRows, ok := block["rows"].([]any)
	if !ok || len(rawRows) < 2 {
		t.Fatalf("JSON rows = %#v", block["rows"])
	}
	rows := make([][]any, len(rawRows))
	for index, raw := range rawRows {
		rows[index], ok = raw.([]any)
		if !ok {
			t.Fatalf("JSON row %d = %#v", index, raw)
		}
	}
	return rows
}

type dashboardBaselineFixture struct {
	service  *dashboardruntime.Service
	core     *materializeruntime.Runtime
	database *dashboardBaselineDatabase
	handler  Handler
}

func newDashboardBaselineFixture(tb testing.TB, rows int) *dashboardBaselineFixture {
	tb.Helper()
	model := dashboardBaselineModel()
	dependencyEvidence, err := dashboardBaselineDependencyEvidence(model)
	if err != nil {
		tb.Fatal(err)
	}
	definition, err := dashboardBaselineDefinition(model)
	if err != nil {
		tb.Fatal(err)
	}
	database := &dashboardBaselineDatabase{rows: rows}
	factory := &dashboardBaselineFactory{database: database, dependencyEvidence: dependencyEvidence}
	identity, err := projectgraph.NewServingIdentity(dashboardBaselineProjectID, "test", dashboardBaselineSnapshot)
	if err != nil {
		tb.Fatal(err)
	}
	service, err := dashboardruntime.NewFromGeneration(context.Background(), "", factory, identity, definition)
	if err != nil {
		tb.Fatal(err)
	}
	tb.Cleanup(func() {
		if err := service.Close(); err != nil {
			tb.Errorf("close dashboard baseline fixture: %v", err)
		}
	})
	return &dashboardBaselineFixture{service: service, core: factory.core, database: database, handler: Handler{Metrics: service, ProjectID: dashboardBaselineProjectID}}
}

func dashboardBaselineDependencyEvidence(model *semanticmodel.Model) (resultidentity.Evidence, error) {
	semanticDigest, err := semanticquery.SemanticModelDigest(model)
	if err != nil {
		return resultidentity.Evidence{}, err
	}
	return resultidentity.NewEvidence(resultidentity.EvidenceInput{
		SemanticModelID:     dashboardBaselineModelID,
		SemanticModelDigest: semanticDigest,
		DatasetRelations: []resultidentity.DatasetRelation{{
			Dataset: dashboardBaselineDatasetID,
			Relation: resultidentity.RelationRevision{
				RelationID:     "model:orders",
				RevisionDigest: dashboardBaselineDigest("orders-revision"),
			},
		}},
		BindingFingerprint: dashboardBaselineDigest("binding"),
		RuntimeDigest:      dashboardBaselineDigest("runtime"),
		CapabilityDigest:   dashboardBaselineDigest("capability"),
	})
}

func dashboardBaselineDigest(value string) string {
	digest := sha256.Sum256([]byte("fai-540-dashboard-baseline:" + value))
	return fmt.Sprintf("sha256:%x", digest)
}

func (f *dashboardBaselineFixture) clearCache() {
	f.core.ClearQueryCache()
}

func (f *dashboardBaselineFixture) serve(tb testing.TB, visual string, rows int, response string) ([]byte, dashboardBaselineObservation) {
	tb.Helper()
	requestBody, err := json.Marshal(dashboardapi.DashboardVisualQueryRequest{Limit: rows})
	if err != nil {
		tb.Fatal(err)
	}
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/dashboards/"+dashboardBaselineDashboardID+"/pages/"+dashboardBaselinePageID+"/visuals/"+visual+"/data", bytes.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Serving-Snapshot", dashboardBaselineSnapshot)
	request.Header.Set("X-Request-ID", "fai-540-baseline")
	if response == "arrow" {
		request.Header.Set("Accept", dashboardArrowMediaType)
	} else {
		request.Header.Set("Accept", "application/json")
	}
	route := chi.NewRouteContext()
	route.URLParams.Add("dashboard", dashboardBaselineDashboardID)
	route.URLParams.Add("page", dashboardBaselinePageID)
	route.URLParams.Add("visual", visual)
	ctx := context.WithValue(request.Context(), chi.RouteCtxKey, route)
	observation := dashboardBaselineObservation{}
	ctx = dataquery.WithCacheOutcomeObserver(ctx, func(outcome string) { observation.outcomes = append(observation.outcomes, outcome) })
	ctx = dataquery.WithPhysicalQueryObserver(ctx, func(value dataquery.PhysicalQueryObservation) {
		observation.physicalQueries += value.Count
		observation.results = append(observation.results, value.Result)
	})
	request = request.WithContext(ctx)
	recorder := httptest.NewRecorder()
	f.handler.QueryDashboardVisualData(recorder, request)
	if recorder.Code != stdhttp.StatusOK {
		tb.Fatalf("dashboard baseline %s/%s status=%d body=%s", visual, response, recorder.Code, recorder.Body.String())
	}
	if response == "arrow" {
		if got := recorder.Header().Get("Content-Type"); got != dashboardArrowMediaType {
			tb.Fatalf("dashboard baseline Arrow content type = %q", got)
		}
		if got := recorder.Header().Get("X-Serving-Snapshot"); got != dashboardBaselineSnapshot {
			tb.Fatalf("dashboard baseline serving snapshot = %q", got)
		}
		if got := recorder.Header().Get("X-Query-ID"); got != "fai-540-baseline" {
			tb.Fatalf("dashboard baseline query ID = %q", got)
		}
		observation.nextCursor = recorder.Header().Get("X-Next-Cursor")
	}
	return recorder.Body.Bytes(), observation
}

func (f *dashboardBaselineFixture) serveDashboardRuntime(tb testing.TB, visual string, rows int, response string) ([]byte, dashboardBaselineObservation) {
	tb.Helper()
	observation := dashboardBaselineObservation{}
	ctx := dashboardBaselineObservationContext(context.Background(), &observation)
	envelope := f.queryEnvelopeWithContext(tb, ctx, visual, rows)
	recorder := httptest.NewRecorder()
	if response == "json" {
		writeJSON(recorder, stdhttp.StatusOK, envelope)
	} else {
		rowset, err := dashboardVisualizationRowset(envelope, "a", 0, rows, "baseline-scope", dashboardBaselineSnapshot)
		if err != nil {
			tb.Fatal(err)
		}
		request := httptest.NewRequest(stdhttp.MethodPost, "/dashboard-baseline", nil)
		request.Header.Set("Accept", dashboardArrowMediaType)
		request.Header.Set("X-Request-ID", "fai-540-baseline")
		writeDashboardTableRowset(recorder, request, rowset, envelope)
	}
	if recorder.Code != stdhttp.StatusOK {
		tb.Fatalf("dashboard runtime baseline %s/%s status=%d body=%s", visual, response, recorder.Code, recorder.Body.String())
	}
	return recorder.Body.Bytes(), observation
}

func (f *dashboardBaselineFixture) queryEnvelope(tb testing.TB, visual string, rows int) visualizationir.VisualizationEnvelope {
	return f.queryEnvelopeWithContext(tb, context.Background(), visual, rows)
}

func (f *dashboardBaselineFixture) queryEnvelopeWithContext(tb testing.TB, ctx context.Context, visual string, rows int) visualizationir.VisualizationEnvelope {
	tb.Helper()
	resolved, err := f.service.Resolver().Resolve(projectgraph.ResourceID(dashboardBaselineDashboardID))
	if err != nil {
		tb.Fatal(err)
	}
	definition := resolved.Definition.Visualizations[visual]
	envelope, err := f.service.QueryVisualizationWindow(ctx, dashboardBaselineDashboardID, dashboardBaselinePageID, dashboard.Filters{}, visualizationir.VisualizationWindowRequest{
		VisualID: visual, SpecRevision: definition.SpecRevision, DataRevision: 1, BlockID: "a", Limit: int64(rows),
	})
	if err != nil {
		tb.Fatal(err)
	}
	return envelope
}

func dashboardBaselineObservationContext(ctx context.Context, observation *dashboardBaselineObservation) context.Context {
	ctx = dataquery.WithCacheOutcomeObserver(ctx, func(outcome string) { observation.outcomes = append(observation.outcomes, outcome) })
	return dataquery.WithPhysicalQueryObserver(ctx, func(value dataquery.PhysicalQueryObservation) {
		observation.physicalQueries += value.Count
		observation.results = append(observation.results, value.Result)
	})
}

type dashboardBaselineObservation struct {
	outcomes        []string
	physicalQueries int
	results         []dataquery.Result
	nextCursor      string
}

func (o dashboardBaselineObservation) cacheHits() int {
	count := 0
	for _, outcome := range o.outcomes {
		if outcome == dataquery.CacheHit {
			count++
		}
	}
	return count
}

func (o dashboardBaselineObservation) cacheMisses() int {
	count := 0
	for _, outcome := range o.outcomes {
		if outcome == dataquery.CacheMiss {
			count++
		}
	}
	return count
}

type dashboardBaselineFactory struct {
	database           *dashboardBaselineDatabase
	dependencyEvidence resultidentity.Evidence
	core               *materializeruntime.Runtime
}

func (f *dashboardBaselineFactory) OpenDashboardProjectDataRuntimes(ctx context.Context, config dashboardruntime.ProjectDataRuntimeConfig) (map[projectgraph.ResourceID]dashboardruntime.DataRuntime, error) {
	models := config.Definition.Models()
	model := models[dashboardBaselineModelID]
	core, err := materializeruntime.NewRuntimeView(ctx, materializeruntime.RuntimeConfig{
		ModelID: dashboardBaselineModelID.String(), Model: model, Database: f.database,
		Sources: dashboardBaselineSources{}, SnapshotOnly: true, QueryCacheNamespace: dashboardBaselineSnapshot,
		DependencyEvidence: f.dependencyEvidence,
	})
	if err != nil {
		return nil, err
	}
	f.core = core
	adapter := &dashboardBaselineDataRuntime{core: core, data: reportdef.NewDataQueryService(dashboardBaselineProjectID, dashboardBaselineModelID.String(), core)}
	return map[projectgraph.ResourceID]dashboardruntime.DataRuntime{dashboardBaselineModelID: adapter}, nil
}

type dashboardBaselineDataRuntime struct {
	core *materializeruntime.Runtime
	data reportdef.DataService
}

func (r *dashboardBaselineDataRuntime) Query(ctx context.Context, request reportdef.AggregateQuery) (reportdef.QueryRows, error) {
	return r.data.Query(ctx, request)
}
func (r *dashboardBaselineDataRuntime) Rows(ctx context.Context, request reportdef.RowQuery) (reportdef.QueryRows, error) {
	return r.data.Rows(ctx, request)
}
func (r *dashboardBaselineDataRuntime) Count(ctx context.Context, request reportdef.CountQuery) (int, error) {
	return r.data.Count(ctx, request)
}
func (r *dashboardBaselineDataRuntime) Histogram(ctx context.Context, request reportdef.RawValueQuery, bins int) ([]reportdef.HistogramBin, error) {
	return r.data.Histogram(ctx, request, bins)
}
func (r *dashboardBaselineDataRuntime) Distribution(ctx context.Context, request reportdef.RawValueQuery, sort []reportdef.QuerySort, limit int) (reportdef.QueryRows, error) {
	return r.data.Distribution(ctx, request, sort, limit)
}
func (r *dashboardBaselineDataRuntime) ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error) {
	return r.core.ExecuteDataQuery(ctx, request)
}
func (r *dashboardBaselineDataRuntime) Refresh(ctx context.Context) error { return r.core.Refresh(ctx) }
func (r *dashboardBaselineDataRuntime) Close() error                      { return r.core.Close() }
func (r *dashboardBaselineDataRuntime) LastRefresh() time.Time            { return r.core.LastRefresh() }
func (r *dashboardBaselineDataRuntime) Planner() consumer.Planner         { return r.core.Planner() }

type dashboardBaselineSources struct{}

func (dashboardBaselineSources) Prepare(context.Context, *semanticmodel.Model) (materializeruntime.PreparedSources, error) {
	return dashboardBaselinePreparedSources{}, nil
}

type dashboardBaselinePreparedSources struct{}

func (dashboardBaselinePreparedSources) PlanModelTable(context.Context, *semanticmodel.Model, string, semanticmodel.Table) (materializeruntime.ModelTablePlan, error) {
	return materializeruntime.ModelTablePlan{Mode: materializeruntime.PlanModeModelSQL, SQL: "SELECT 1"}, nil
}
func (dashboardBaselinePreparedSources) Close() error { return nil }

type dashboardBaselinePhysicalSnapshot struct {
	types []arrow.Type
	nulls int
}

type dashboardBaselineDatabase struct {
	rows     int
	queries  atomic.Int64
	mu       sync.Mutex
	physical dashboardBaselinePhysicalSnapshot
}

func (d *dashboardBaselineDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	d.queries.Add(1)
	countOnly := strings.Contains(strings.ToUpper(plan.SQL), "COUNT(")
	rowCount := d.rows
	if countOnly {
		rowCount = 1
	}
	fields := make([]arrow.Field, len(plan.Columns))
	arrays := make([]arrow.Array, len(plan.Columns))
	totalNulls := 0
	for index, column := range plan.Columns {
		fields[index] = dashboardBaselineArrowField(column, index, countOnly)
		builder := array.NewBuilder(memory.DefaultAllocator, fields[index].Type)
		for row := 0; row < rowCount; row++ {
			valid := countOnly || column == "row_group" || column == "column_group" || column == "value" || (row+index)%13 != 0
			if !valid {
				builder.AppendNull()
				totalNulls++
				continue
			}
			if err := appendDashboardBaselineArrowValue(builder, column, row, index, countOnly, d.rows); err != nil {
				builder.Release()
				for _, values := range arrays[:index] {
					values.Release()
				}
				return err
			}
		}
		arrays[index] = builder.NewArray()
		builder.Release()
	}
	defer func() {
		for _, values := range arrays {
			values.Release()
		}
	}()
	schema := arrow.NewSchema(fields, nil)
	if !countOnly && len(fields) >= dashboardBaselineNarrowFields {
		types := make([]arrow.Type, len(fields))
		for index, field := range fields {
			types[index] = field.Type.ID()
		}
		d.mu.Lock()
		d.physical = dashboardBaselinePhysicalSnapshot{types: types, nulls: totalNulls}
		d.mu.Unlock()
	}
	if err := sink.WriteSchema(schema); err != nil {
		return err
	}
	record := array.NewRecordBatch(schema, arrays, int64(rowCount))
	defer record.Release()
	if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
		return err
	}
	return sink.WriteRecord(record)
}

func (d *dashboardBaselineDatabase) physicalSnapshot() dashboardBaselinePhysicalSnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return dashboardBaselinePhysicalSnapshot{types: append([]arrow.Type(nil), d.physical.types...), nulls: d.physical.nulls}
}

func (d *dashboardBaselineDatabase) Exec(context.Context, string) error { return nil }
func (d *dashboardBaselineDatabase) Close() error                       { return nil }
func (d *dashboardBaselineDatabase) Path() string                       { return "fai-540-deterministic" }

func dashboardBaselineArrowField(column string, fallback int, countOnly bool) arrow.Field {
	field := arrow.Field{Name: column, Nullable: true}
	if countOnly {
		field.Type = arrow.PrimitiveTypes.Int64
		return field
	}
	switch column {
	case "row_group", "column_group":
		field.Type = arrow.BinaryTypes.String
		return field
	case "value":
		field.Type = &arrow.Decimal128Type{Precision: 38, Scale: 3}
		return field
	}
	index := dashboardBaselineFieldIndex(column, fallback)
	switch index % 9 {
	case 0:
		field.Type = arrow.PrimitiveTypes.Int64
	case 1:
		field.Type = arrow.PrimitiveTypes.Float64
	case 2:
		field.Type = arrow.FixedWidthTypes.Boolean
	case 3:
		field.Type = arrow.BinaryTypes.String
	case 4:
		field.Type = arrow.BinaryTypes.Binary
	case 5:
		field.Type = arrow.FixedWidthTypes.Timestamp_us
	case 6:
		field.Type = &arrow.Decimal128Type{Precision: 38, Scale: 3}
	case 7:
		field.Type = arrow.FixedWidthTypes.Date32
	case 8:
		field.Type = &arrow.DictionaryType{IndexType: arrow.PrimitiveTypes.Int16, ValueType: arrow.BinaryTypes.String}
	}
	return field
}

func dashboardBaselineFieldIndex(column string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimPrefix(column, "field_"))
	if err != nil {
		return fallback
	}
	return value
}

func appendDashboardBaselineArrowValue(builder array.Builder, column string, row, fallback int, countOnly bool, totalRows int) error {
	if countOnly {
		builder.(*array.Int64Builder).Append(int64(totalRows))
		return nil
	}
	if column == "row_group" {
		builder.(*array.StringBuilder).Append("row-" + strconv.Itoa(row))
		return nil
	}
	if column == "column_group" {
		builder.(*array.StringBuilder).Append("column-" + strconv.Itoa(row%4))
		return nil
	}
	if column == "value" {
		builder.(*array.Decimal128Builder).Append(decimal128.FromI64(int64(row+1) * 1_000))
		return nil
	}
	index := dashboardBaselineFieldIndex(column, fallback)
	value := int64(row*37 + index + 1)
	switch typed := builder.(type) {
	case *array.Int64Builder:
		typed.Append(value)
	case *array.Float64Builder:
		typed.Append(float64(value)/7.0 + 0.125)
	case *array.BooleanBuilder:
		typed.Append((row+index)%2 == 0)
	case *array.StringBuilder:
		if row == 1 {
			typed.Append("")
		} else {
			typed.Append("value-" + strconv.Itoa((row*17+index)%997))
		}
	case *array.BinaryBuilder:
		typed.Append([]byte("bytes-" + strconv.Itoa((row*19+index)%997)))
	case *array.TimestampBuilder:
		typed.Append(arrow.Timestamp(1_700_000_000_000_000 + value*1_000))
	case *array.Decimal128Builder:
		typed.Append(decimal128.FromI64(value * 1_000))
	case *array.Date32Builder:
		typed.Append(arrow.Date32(19_000 + value%2_000))
	case *array.BinaryDictionaryBuilder:
		return typed.AppendString("category-" + strconv.Itoa(row%97))
	default:
		return fmt.Errorf("unsupported FAI-540 Arrow builder %T", builder)
	}
	return nil
}

func dashboardBaselineModel() *semanticmodel.Model {
	tableDimensions := map[string]semanticmodel.MetricDimension{}
	semanticDimensions := map[string]semanticmodel.SemanticDimension{}
	columns := map[string]semanticmodel.ModelColumn{}
	for index := 0; index < dashboardBaselineWideFields; index++ {
		name := fmt.Sprintf("field_%02d", index)
		datatype := dashboardBaselineLogicalDataType(index)
		typeName := dashboardBaselineSemanticType(datatype)
		tableDimensions[name] = semanticmodel.MetricDimension{Field: "orders." + name, Name: name, Type: typeName, Datatype: datatype}
		semanticDimensions[name] = semanticmodel.SemanticDimension{Name: name, Type: typeName, Datatype: datatype, Bindings: map[string]semanticmodel.DimensionBinding{dashboardBaselineDatasetID: {Field: "orders." + name}}}
		columns[name] = semanticmodel.ModelColumn{Name: name, Field: name, Type: typeName, Datatype: datatype}
	}
	for _, name := range []string{"row_group", "column_group"} {
		tableDimensions[name] = semanticmodel.MetricDimension{Field: "orders." + name, Name: name, Type: "string", Datatype: semanticmodel.DataTypeString}
		semanticDimensions[name] = semanticmodel.SemanticDimension{Name: name, Type: "string", Datatype: semanticmodel.DataTypeString, Bindings: map[string]semanticmodel.DimensionBinding{dashboardBaselineDatasetID: {Field: "orders." + name}}}
		columns[name] = semanticmodel.ModelColumn{Name: name, Field: name, Type: "string", Datatype: semanticmodel.DataTypeString}
	}
	return &semanticmodel.Model{
		Name: dashboardBaselineModelID.String(),
		Tables: map[string]semanticmodel.Table{dashboardBaselineDatasetID: {
			ModelName: dashboardBaselineDatasetID, Columns: columns, Dimensions: tableDimensions,
			Entities: map[string]semanticmodel.EntityDefinition{"row": {Type: "primary", Fields: []string{"field_00"}}}, GrainEntity: "row",
		}},
		Datasets:   map[string]semanticmodel.SemanticDatasetSpec{dashboardBaselineDatasetID: {Model: dashboardBaselineDatasetID}},
		Dimensions: semanticDimensions,
		Metrics: map[string]semanticmodel.Metric{"value_metric": {
			Name: "value_metric", Type: "aggregate", Dataset: dashboardBaselineDatasetID, Aggregation: "sum",
			Input: &semanticmodel.MetricInput{Field: "orders.field_06"}, Empty: "zero",
		}},
	}
}

func dashboardBaselineLogicalDataType(index int) semanticmodel.LogicalDataType {
	switch index % 9 {
	case 0:
		return semanticmodel.DataTypeInteger
	case 1:
		return semanticmodel.DataTypeFloat
	case 2:
		return semanticmodel.DataTypeBoolean
	case 5:
		return semanticmodel.DataTypeDateTime
	case 6:
		return semanticmodel.DataTypeDecimal
	case 7:
		return semanticmodel.DataTypeDate
	default:
		return semanticmodel.DataTypeString
	}
}

func dashboardBaselineSemanticType(datatype semanticmodel.LogicalDataType) string {
	switch datatype {
	case semanticmodel.DataTypeInteger, semanticmodel.DataTypeFloat, semanticmodel.DataTypeDecimal:
		return "number"
	case semanticmodel.DataTypeBoolean:
		return "boolean"
	case semanticmodel.DataTypeDate:
		return "date"
	case semanticmodel.DataTypeDateTime:
		return "timestamp"
	default:
		return "string"
	}
}

func dashboardBaselineDefinition(model *semanticmodel.Model) (*dashboardruntime.ProjectDefinition, error) {
	visualizations := map[string]visualizationdefinition.Definition{}
	for _, shape := range []struct {
		id      string
		columns int
	}{{"detail_narrow", dashboardBaselineNarrowFields}, {"detail_wide", dashboardBaselineWideFields}} {
		definition, err := dashboardBaselineDetailDefinition(shape.id, shape.columns)
		if err != nil {
			return nil, err
		}
		visualizations[shape.id] = definition
	}
	matrix, err := dashboardBaselineAggregateDefinition("matrix", false)
	if err != nil {
		return nil, err
	}
	visualizations["matrix"] = matrix
	pivot, err := dashboardBaselineAggregateDefinition("pivot", true)
	if err != nil {
		return nil, err
	}
	visualizations["pivot"] = pivot
	pageVisuals := make([]dashboard.PageVisual, 0, len(visualizations))
	for _, id := range []string{"detail_narrow", "detail_wide", "matrix", "pivot"} {
		pageVisuals = append(pageVisuals, dashboard.PageVisual{ID: id, Kind: "visual", Visual: id})
	}
	compiled, err := dashboarddefinition.New(dashboardBaselineDashboardID, "FAI-540 Dashboard Baseline", "", dashboardBaselineModelID.String(), []dashboard.Page{{ID: dashboardBaselinePageID, Title: "Overview", Visuals: pageVisuals}}, visualizations)
	if err != nil {
		return nil, err
	}
	return dashboardruntime.NewProjectDefinition(dashboardBaselineProjectID, "FAI-540", "", map[projectgraph.ResourceID]*semanticmodel.Model{dashboardBaselineModelID: model}, map[projectgraph.ResourceID]dashboarddefinition.Definition{projectgraph.ResourceID(dashboardBaselineDashboardID): compiled})
}

func dashboardBaselineDetailDefinition(id string, columns int) (visualizationdefinition.Definition, error) {
	fields := make([]visualizationir.VisualizationField, columns)
	tableColumns := make([]visualizationir.TableVisualizationColumn, columns)
	bindings := make([]visualizationdefinition.FieldBinding, columns)
	for index := range fields {
		name := fmt.Sprintf("field_%02d", index)
		fields[index] = visualizationir.VisualizationField{ID: name, Role: visualizationir.VisualizationFieldRoleDimension, DataType: dashboardBaselineVisualizationDataType(index), Nullable: true, Label: name}
		tableColumns[index] = visualizationir.TableVisualizationColumn{Field: visualizationir.VisualizationFieldRef{Dataset: "primary", Field: name}, Label: name, Formatting: []visualizationir.TableVisualizationFormattingRule{}}
		bindings[index] = visualizationdefinition.FieldBinding{FieldID: dashboardBaselineDatasetID + "." + name, Alias: name}
	}
	base := dashboardBaselineSpecBase(id, fields)
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.TableVisualizationSpec{
		VisualizationSpecBase: base, Kind: "table", Columns: tableColumns,
		Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true},
	}}
	return visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryDetail, ResultShape: visualizationdefinition.ResultDetailWindow,
		ModelID: dashboardBaselineModelID.String(), DatasetID: "primary",
		Detail: &visualizationdefinition.DetailQueryBinding{TableID: dashboardBaselineDatasetID, Fields: bindings, Limit: 1_000},
	})
}

func dashboardBaselineAggregateDefinition(id string, pivot bool) (visualizationdefinition.Definition, error) {
	fields := []visualizationir.VisualizationField{
		{ID: "row_group", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Row"},
		{ID: "value", Role: visualizationir.VisualizationFieldRoleMetric, DataType: visualizationir.VisualizationDataTypeDecimal, Label: "Value"},
	}
	base := dashboardBaselineSpecBase(id, fields)
	rowRef := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "row_group"}
	valueRef := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "value"}
	rowBinding := visualizationdefinition.FieldBinding{FieldID: dashboardBaselineDatasetID + ".row_group", Alias: "row_group"}
	valueBinding := visualizationdefinition.FieldBinding{FieldID: "value_metric", Alias: "value"}
	if !pivot {
		spec := visualizationir.VisualizationSpec{Value: &visualizationir.MatrixVisualizationSpec{
			VisualizationSpecBase: base, Kind: "matrix", Rows: []visualizationir.VisualizationFieldRef{rowRef}, Metrics: []visualizationir.VisualizationFieldRef{valueRef},
			MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true},
		}}
		return visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{
			Kind: visualizationdefinition.QueryMatrix, ResultShape: visualizationdefinition.ResultMatrixWindow,
			ModelID: dashboardBaselineModelID.String(), DatasetID: "primary",
			Matrix: &visualizationdefinition.MatrixQueryBinding{TableID: dashboardBaselineDatasetID, Rows: []visualizationdefinition.FieldBinding{rowBinding}, Metrics: []visualizationdefinition.FieldBinding{valueBinding}, Limit: 1_000},
		})
	}
	fields = append(fields, visualizationir.VisualizationField{ID: "column_group", Role: visualizationir.VisualizationFieldRoleDimension, DataType: visualizationir.VisualizationDataTypeString, Label: "Column"})
	base = dashboardBaselineSpecBase(id, fields)
	columnRef := visualizationir.VisualizationFieldRef{Dataset: "primary", Field: "column_group"}
	spec := visualizationir.VisualizationSpec{Value: &visualizationir.PivotVisualizationSpec{
		VisualizationSpecBase: base, Kind: "pivot", Rows: []visualizationir.VisualizationFieldRef{rowRef}, Columns: []visualizationir.VisualizationFieldRef{columnRef}, Metrics: []visualizationir.VisualizationFieldRef{valueRef},
		MetricFormatting: map[string][]visualizationir.TableVisualizationFormattingRule{}, Presentation: visualizationir.GridVisualizationPresentation{RowHeight: 28, ShowHeader: true},
	}}
	return visualizationdefinition.New(id, spec, visualizationdefinition.QueryBinding{
		Kind: visualizationdefinition.QueryPivot, ResultShape: visualizationdefinition.ResultPivotWindow,
		ModelID: dashboardBaselineModelID.String(), DatasetID: "primary",
		Pivot: &visualizationdefinition.PivotQueryBinding{TableID: dashboardBaselineDatasetID, Rows: []visualizationdefinition.FieldBinding{rowBinding}, Columns: []visualizationdefinition.FieldBinding{{FieldID: dashboardBaselineDatasetID + ".column_group", Alias: "column_group"}}, Metrics: []visualizationdefinition.FieldBinding{valueBinding}, Limit: 1_000},
	})
}

func dashboardBaselineSpecBase(title string, fields []visualizationir.VisualizationField) visualizationir.VisualizationSpecBase {
	return visualizationir.VisualizationSpecBase{
		Title: title, Accessibility: visualizationir.VisualizationAccessibility{Title: title, Description: title},
		Datasets:   []visualizationir.VisualizationDatasetSchema{{ID: "primary", Fields: fields}},
		DataBudget: visualizationir.VisualizationDataBudget{MaxRows: 2_000, RequiredCompleteness: visualizationir.VisualizationCompletenessComplete},
	}
}

func dashboardBaselineVisualizationDataType(index int) visualizationir.VisualizationDataType {
	switch index % 9 {
	case 0:
		return visualizationir.VisualizationDataTypeInteger
	case 1:
		return visualizationir.VisualizationDataTypeFloat
	case 2:
		return visualizationir.VisualizationDataTypeBoolean
	case 5:
		return visualizationir.VisualizationDataTypeTemporal
	case 6:
		return visualizationir.VisualizationDataTypeDecimal
	case 7:
		return visualizationir.VisualizationDataTypeDate
	default:
		return visualizationir.VisualizationDataTypeString
	}
}
