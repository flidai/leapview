//go:build fai543experiment && duckdb_arrow

package http

import (
	"bytes"
	"context"
	"database/sql"
	"database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	stdhttp "net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	runtime "runtime"
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
	duckdb "github.com/duckdb/duckdb-go/v2"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	materializeruntime "github.com/flidai/leapview/internal/analytics/materialize"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
	"github.com/flidai/leapview/internal/analytics/resultidentity"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	semanticapi "github.com/flidai/leapview/internal/dashboard/semanticapi"
	visualizationdefinition "github.com/flidai/leapview/internal/dashboard/visualization/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/internal/workload"
	"github.com/flidai/leapview/pkg/arrowresult"
	"github.com/go-chi/chi/v5"
)

const (
	dashboardDirectArrowQualificationScope       = "fai-544-real-duckdb"
	dashboardDirectArrowQualificationLargeVisual = "qualification_wide"
	dashboardDirectArrowQualificationMaxRows     = 50_000
)

type dashboardDirectArrowQualificationField struct {
	name     string
	typeID   arrow.Type
	nullable bool
	timezone string
}

type dashboardDirectArrowQualificationStats struct {
	queries         int64
	active          int64
	maxActive       int64
	batchSizes      []int64
	leaseDurations  []time.Duration
	waitDurations   []time.Duration
	queryDurations  []time.Duration
	physicalSchemas [][]dashboardDirectArrowQualificationField
}

type dashboardDirectArrowQualificationDatabase struct {
	db        *sql.DB
	queries   atomic.Int64
	active    atomic.Int64
	maxActive atomic.Int64

	mu              sync.Mutex
	batchSizes      []int64
	leaseDurations  []time.Duration
	waitDurations   []time.Duration
	queryDurations  []time.Duration
	physicalSchemas [][]dashboardDirectArrowQualificationField
}

type dashboardDirectArrowQualificationConnectionKey struct{}

type dashboardDirectArrowQualificationLease struct {
	ctx      context.Context
	conn     *sql.Conn
	database *dashboardDirectArrowQualificationDatabase
	started  time.Time
	once     sync.Once
}

func newDashboardDirectArrowQualificationDatabase(tb testing.TB, rows, maxConnections int) *dashboardDirectArrowQualificationDatabase {
	tb.Helper()
	connector, err := duckdb.NewConnector(":memory:", nil)
	if err != nil {
		tb.Fatal(err)
	}
	db := sql.OpenDB(connector)
	db.SetMaxOpenConns(maxConnections)
	db.SetMaxIdleConns(maxConnections)
	fixture := &dashboardDirectArrowQualificationDatabase{db: db}
	if err := fixture.createRealisticDataset(context.Background(), rows); err != nil {
		_ = db.Close()
		tb.Fatal(err)
	}
	return fixture
}

func (d *dashboardDirectArrowQualificationDatabase) createRealisticDataset(ctx context.Context, rows int) error {
	if rows < 0 {
		return fmt.Errorf("qualification row count must be non-negative")
	}
	if _, err := d.db.ExecContext(ctx, `CREATE SCHEMA model`); err != nil {
		return err
	}
	columns := []string{"source_row BIGINT NOT NULL"}
	values := []string{"source_row"}
	for index := 0; index < dashboardBaselineWideFields; index++ {
		name := fmt.Sprintf("field_%02d", index)
		nullable := dashboardDirectArrowQualificationNullable(index)
		nullability := " NOT NULL"
		if nullable {
			nullability = ""
		}
		columns = append(columns, name+" "+dashboardDirectArrowQualificationSQLType(index)+nullability)
		expression := dashboardDirectArrowQualificationSQLValue(index)
		if nullable {
			expression = fmt.Sprintf("CASE WHEN (source_row + %d) %% 13 = 0 THEN NULL ELSE %s END", index, expression)
		}
		values = append(values, expression)
	}
	if _, err := d.db.ExecContext(ctx, `CREATE TABLE model.orders (`+strings.Join(columns, ", ")+")"); err != nil {
		return err
	}
	statement := fmt.Sprintf(
		"INSERT INTO model.orders SELECT %s FROM range(%d) AS source(source_row)",
		strings.Join(values, ", "), rows,
	)
	_, err := d.db.ExecContext(ctx, statement)
	return err
}

func dashboardDirectArrowQualificationNullable(index int) bool {
	return index != 0 && index%2 == 0
}

func dashboardDirectArrowQualificationSQLType(index int) string {
	switch index % 9 {
	case 0:
		return "BIGINT"
	case 1:
		return "DOUBLE"
	case 2:
		return "BOOLEAN"
	case 3, 8:
		return "VARCHAR"
	case 4:
		return "BLOB"
	case 5:
		if (index/9)%2 == 1 {
			return "TIMESTAMPTZ"
		}
		return "TIMESTAMP"
	case 6:
		return "DECIMAL(38, 3)"
	case 7:
		return "DATE"
	default:
		panic("unreachable qualification SQL type")
	}
}

func dashboardDirectArrowQualificationSQLValue(index int) string {
	value := fmt.Sprintf("(source_row * 37 + %d)", index+1)
	switch index % 9 {
	case 0:
		return "CAST(" + value + " AS BIGINT)"
	case 1:
		return "CAST(" + value + " AS DOUBLE) / 7.0 + 0.125"
	case 2:
		return "((source_row + " + strconv.Itoa(index) + ") % 2 = 0)"
	case 3:
		return "CASE WHEN source_row = 1 THEN '' ELSE 'value-' || CAST((source_row * 17 + " + strconv.Itoa(index) + ") % 997 AS VARCHAR) END"
	case 4:
		return "CAST('bytes-' || CAST((source_row * 19 + " + strconv.Itoa(index) + ") % 997 AS VARCHAR) AS BLOB)"
	case 5:
		if (index/9)%2 == 1 {
			return "TIMESTAMPTZ '2023-11-14 22:13:20+00' + source_row * INTERVAL 1 MICROSECOND"
		}
		return "TIMESTAMP '2023-11-14 22:13:20' + source_row * INTERVAL 1 MICROSECOND"
	case 6:
		return "CAST(" + value + " AS DECIMAL(38, 3))"
	case 7:
		return "DATE '2022-01-01' + CAST(source_row % 2000 AS INTEGER)"
	case 8:
		return "'category-' || CAST(source_row % 97 AS VARCHAR)"
	default:
		panic("unreachable qualification SQL value")
	}
}

func (d *dashboardDirectArrowQualificationDatabase) Acquire(ctx context.Context) (analyticsresource.Lease, error) {
	waitStarted := time.Now()
	conn, err := d.db.Conn(ctx)
	wait := time.Since(waitStarted)
	if err != nil {
		return nil, err
	}
	active := d.active.Add(1)
	for {
		current := d.maxActive.Load()
		if active <= current || d.maxActive.CompareAndSwap(current, active) {
			break
		}
	}
	d.mu.Lock()
	d.waitDurations = append(d.waitDurations, wait)
	d.mu.Unlock()
	leased := context.WithValue(ctx, dashboardDirectArrowQualificationConnectionKey{}, conn)
	return &dashboardDirectArrowQualificationLease{ctx: leased, conn: conn, database: d, started: time.Now()}, nil
}

func (l *dashboardDirectArrowQualificationLease) Context() context.Context { return l.ctx }

func (l *dashboardDirectArrowQualificationLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		duration := time.Since(l.started)
		_ = l.conn.Close()
		l.database.active.Add(-1)
		l.database.mu.Lock()
		l.database.leaseDurations = append(l.database.leaseDurations, duration)
		l.database.mu.Unlock()
	})
}

func (d *dashboardDirectArrowQualificationDatabase) QueryArrow(ctx context.Context, plan semanticquery.Plan, sink arrowquery.Sink) error {
	conn, ok := ctx.Value(dashboardDirectArrowQualificationConnectionKey{}).(*sql.Conn)
	if !ok || conn == nil {
		return errors.New("qualification DuckDB query requires an active connection lease")
	}
	d.queries.Add(1)
	started := time.Now()
	err := conn.Raw(func(raw any) error {
		driverConn, ok := raw.(driver.Conn)
		if !ok {
			return errors.New("qualification DuckDB connection does not expose driver.Conn")
		}
		arrowConn, err := duckdb.NewArrowFromConn(driverConn)
		if err != nil {
			return err
		}
		reader, err := arrowConn.QueryContext(ctx, plan.SQL, plan.Args...)
		if err != nil {
			return err
		}
		defer reader.Release()
		if err := arrowquery.ConsumeSchemaBudget(ctx, reader.Schema()); err != nil {
			return err
		}
		fields := make([]dashboardDirectArrowQualificationField, reader.Schema().NumFields())
		for index, field := range reader.Schema().Fields() {
			fields[index] = dashboardDirectArrowQualificationField{name: field.Name, typeID: field.Type.ID(), nullable: field.Nullable}
			if timestamp, ok := field.Type.(*arrow.TimestampType); ok {
				fields[index].timezone = timestamp.TimeZone
			}
		}
		d.mu.Lock()
		d.physicalSchemas = append(d.physicalSchemas, fields)
		d.mu.Unlock()
		if err := sink.WriteSchema(reader.Schema()); err != nil {
			return err
		}
		for reader.Next() {
			record := reader.RecordBatch()
			if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
				return err
			}
			d.mu.Lock()
			d.batchSizes = append(d.batchSizes, record.NumRows())
			d.mu.Unlock()
			// The record is borrowed from DuckDB and is consumed synchronously.
			// No schema, record, column, or buffer is retained by this fixture.
			if err := sink.WriteRecord(record); err != nil {
				return err
			}
		}
		return reader.Err()
	})
	d.mu.Lock()
	d.queryDurations = append(d.queryDurations, time.Since(started))
	d.mu.Unlock()
	return err
}

func (d *dashboardDirectArrowQualificationDatabase) Exec(ctx context.Context, statement string) error {
	_, err := d.db.ExecContext(ctx, statement)
	return err
}

func (d *dashboardDirectArrowQualificationDatabase) Close() error { return d.db.Close() }
func (*dashboardDirectArrowQualificationDatabase) Path() string   { return "fai-544-real-duckdb" }

func (d *dashboardDirectArrowQualificationDatabase) resetStats() {
	d.queries.Store(0)
	d.maxActive.Store(d.active.Load())
	d.mu.Lock()
	d.batchSizes = nil
	d.leaseDurations = nil
	d.waitDurations = nil
	d.queryDurations = nil
	d.physicalSchemas = nil
	d.mu.Unlock()
}

func (d *dashboardDirectArrowQualificationDatabase) stats() dashboardDirectArrowQualificationStats {
	d.mu.Lock()
	defer d.mu.Unlock()
	stats := dashboardDirectArrowQualificationStats{
		queries: d.queries.Load(), active: d.active.Load(), maxActive: d.maxActive.Load(),
		batchSizes:     append([]int64(nil), d.batchSizes...),
		leaseDurations: append([]time.Duration(nil), d.leaseDurations...),
		waitDurations:  append([]time.Duration(nil), d.waitDurations...),
		queryDurations: append([]time.Duration(nil), d.queryDurations...),
	}
	stats.physicalSchemas = make([][]dashboardDirectArrowQualificationField, len(d.physicalSchemas))
	for index := range d.physicalSchemas {
		stats.physicalSchemas[index] = append([]dashboardDirectArrowQualificationField(nil), d.physicalSchemas[index]...)
	}
	return stats
}

type dashboardDirectArrowQualificationFixture struct {
	service  *dashboardruntime.Service
	core     *materializeruntime.Runtime
	database *dashboardDirectArrowQualificationDatabase
	model    *semanticmodel.Model
	handler  Handler
	governor dashboardBaselineGovernor
	admitter *dashboardDirectArrowCapturingAdmitter
	server   *httptest.Server
	client   *stdhttp.Client

	physical     atomic.Int64
	cacheOutcome atomic.Int64
	handlers     atomic.Int64
	errorMu      sync.Mutex
	lastError    error
	largeVisual  visualizationdefinition.Definition
}

func newDashboardDirectArrowQualificationFixture(tb testing.TB, rows, maxConnections, socketWriteBuffer int) *dashboardDirectArrowQualificationFixture {
	tb.Helper()
	model := dashboardBaselineModel()
	definition, err := dashboardBaselineDefinition(model)
	if err != nil {
		tb.Fatal(err)
	}
	database := newDashboardDirectArrowQualificationDatabase(tb, rows, maxConnections)
	evidence, err := dashboardBaselineDependencyEvidence(model)
	if err != nil {
		_ = database.Close()
		tb.Fatal(err)
	}
	identity, err := projectgraph.NewServingIdentity(dashboardBaselineProjectID, "qualification", dashboardBaselineSnapshot)
	if err != nil {
		_ = database.Close()
		tb.Fatal(err)
	}
	partition, err := resultidentity.NewPartition(resultidentity.PartitionInput{
		Kind: resultidentity.PartitionProduction, ProjectID: identity.ProjectID, Environment: identity.Environment,
	})
	if err != nil {
		_ = database.Close()
		tb.Fatal(err)
	}
	factory := &dashboardBaselineFactory{
		database: database, dependencyEvidence: evidence, resultPartition: partition,
		resultLimits: dataquery.ResultLimits{MaxRows: dashboardDirectArrowQualificationMaxRows + 1, MaxBytes: 256 << 20},
	}
	service, err := dashboardruntime.NewFromGeneration(context.Background(), "", factory, identity, definition)
	if err != nil {
		_ = database.Close()
		tb.Fatal(err)
	}
	large, err := dashboardBaselineDetailDefinition(dashboardDirectArrowQualificationLargeVisual, dashboardBaselineWideFields)
	if err != nil {
		_ = service.Close()
		_ = database.Close()
		tb.Fatal(err)
	}
	large.Query.Detail.Limit = dashboardDirectArrowQualificationMaxRows
	fixture := &dashboardDirectArrowQualificationFixture{
		service: service, core: factory.core, database: database, model: model,
		handler:  Handler{Metrics: service, ProjectID: dashboardBaselineProjectID},
		governor: dashboardBaselineGovernor{policyFingerprint: dashboardBaselineDigest("fai-544-policy"), calls: &atomic.Int64{}},
		admitter: &dashboardDirectArrowCapturingAdmitter{}, largeVisual: large,
	}
	router := chi.NewRouter()
	router.Post("/api/dashboards/{dashboard}/pages/{page}/visuals/{visual}/data", fixture.handleControl)
	router.Get("/qualification/native/{visual}", fixture.handleCandidate)
	server := httptest.NewUnstartedServer(router)
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	if socketWriteBuffer > 0 {
		server.Listener = &dashboardDirectArrowQualificationListener{Listener: server.Listener, writeBuffer: socketWriteBuffer}
	}
	server.Start()
	transport := &stdhttp.Transport{
		DisableCompression: true, MaxIdleConns: maxConnections * 2,
		MaxIdleConnsPerHost: maxConnections, MaxConnsPerHost: maxConnections,
	}
	fixture.server = server
	fixture.client = &stdhttp.Client{Transport: transport, Timeout: 30 * time.Second}
	tb.Cleanup(func() {
		transport.CloseIdleConnections()
		server.Close()
		if err := service.Close(); err != nil {
			tb.Errorf("close FAI-544 dashboard service: %v", err)
		}
		if err := database.Close(); err != nil {
			tb.Errorf("close FAI-544 DuckDB fixture: %v", err)
		}
	})
	return fixture
}

func (f *dashboardDirectArrowQualificationFixture) qualificationContext(ctx context.Context) context.Context {
	ctx = dataquery.WithGovernor(ctx, f.governor)
	ctx = workload.WithAdmitter(ctx, f.admitter)
	ctx = dataquery.WithCacheOutcomeObserver(ctx, func(string) { f.cacheOutcome.Add(1) })
	return dataquery.WithPhysicalQueryObserver(ctx, func(observation dataquery.PhysicalQueryObservation) {
		f.physical.Add(int64(observation.Count))
	})
}

func (f *dashboardDirectArrowQualificationFixture) handleControl(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	f.handler.QueryDashboardVisualData(w, r.WithContext(f.qualificationContext(r.Context())))
}

func (f *dashboardDirectArrowQualificationFixture) handleCandidate(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	f.handlers.Add(1)
	defer f.handlers.Add(-1)
	limit, err := strconv.Atoi(r.URL.Query().Get("limit"))
	if err != nil || limit <= 0 {
		stdhttp.Error(w, "invalid qualification limit", stdhttp.StatusBadRequest)
		return
	}
	offset := 0
	if token := r.URL.Query().Get("page_token"); token != "" {
		offset, err = semanticapi.DecodeDirectArrowExperimentCursor(token, dashboardDirectArrowQualificationScope, dashboardBaselineSnapshot)
		if err != nil {
			stdhttp.Error(w, "invalid qualification cursor", stdhttp.StatusBadRequest)
			return
		}
	}
	visualID := chi.URLParam(r, "visual")
	visual := f.largeVisual
	if visualID != dashboardDirectArrowQualificationLargeVisual {
		resolved, resolveErr := f.service.Resolver().Resolve(projectgraph.ResourceID(dashboardBaselineDashboardID))
		if resolveErr != nil {
			stdhttp.Error(w, resolveErr.Error(), stdhttp.StatusInternalServerError)
			return
		}
		var ok bool
		visual, ok = resolved.Visualization(visualID)
		if !ok {
			stdhttp.Error(w, "qualification visual not found", stdhttp.StatusNotFound)
			return
		}
	}
	request, config, err := dashboardDirectArrowExperimentQuery(visual, f.model, nil, offset, limit, "a")
	if err != nil {
		stdhttp.Error(w, err.Error(), stdhttp.StatusBadRequest)
		return
	}
	config.QueryID = "fai-544-direct-arrow"
	config.CursorScope = dashboardDirectArrowQualificationScope
	ctx := f.qualificationContext(r.Context())
	metadata := dataquery.MetadataFromContext(ctx)
	metadata.ProjectID = dashboardBaselineProjectID
	metadata.Surface = dataquery.SurfaceAPI
	metadata.RequestID = "fai-544-qualification"
	metadata.ObjectType = "dashboard_visual"
	metadata.ObjectID = dashboardBaselineDashboardID + ":" + visualID
	ctx = dataquery.WithMetadata(ctx, metadata)
	_, err = semanticapi.ExecuteDirectArrowExperiment(ctx, w, f.core, request, config)
	f.errorMu.Lock()
	f.lastError = err
	f.errorMu.Unlock()
}

func (f *dashboardDirectArrowQualificationFixture) resetStats() {
	f.database.resetStats()
	f.physical.Store(0)
	f.cacheOutcome.Store(0)
	if f.governor.calls != nil {
		f.governor.calls.Store(0)
	}
	f.admitter.mu.Lock()
	f.admitter.requests = nil
	f.admitter.mu.Unlock()
	f.errorMu.Lock()
	f.lastError = nil
	f.errorMu.Unlock()
}

func (f *dashboardDirectArrowQualificationFixture) candidateError() error {
	f.errorMu.Lock()
	defer f.errorMu.Unlock()
	return f.lastError
}

type dashboardDirectArrowQualificationResponse struct {
	body     []byte
	header   stdhttp.Header
	trailer  stdhttp.Header
	status   int
	duration time.Duration
}

func (f *dashboardDirectArrowQualificationFixture) requestControl(ctx context.Context, visual string, limit int, pageToken string) (dashboardDirectArrowQualificationResponse, error) {
	payload, err := json.Marshal(dashboardapi.DashboardVisualQueryRequest{Limit: limit, PageToken: pageToken})
	if err != nil {
		return dashboardDirectArrowQualificationResponse{}, err
	}
	endpoint := f.server.URL + "/api/dashboards/" + dashboardBaselineDashboardID + "/pages/" + dashboardBaselinePageID + "/visuals/" + visual + "/data"
	request, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return dashboardDirectArrowQualificationResponse{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", dashboardArrowMediaType)
	request.Header.Set("X-Serving-Snapshot", dashboardBaselineSnapshot)
	request.Header.Set("X-Request-ID", "fai-544-control")
	return f.do(request)
}

func (f *dashboardDirectArrowQualificationFixture) requestCandidate(ctx context.Context, visual string, limit int, pageToken string) (dashboardDirectArrowQualificationResponse, error) {
	query := url.Values{"limit": []string{strconv.Itoa(limit)}}
	if pageToken != "" {
		query.Set("page_token", pageToken)
	}
	request, err := stdhttp.NewRequestWithContext(ctx, stdhttp.MethodGet, f.server.URL+"/qualification/native/"+visual+"?"+query.Encode(), nil)
	if err != nil {
		return dashboardDirectArrowQualificationResponse{}, err
	}
	request.Header.Set("X-Serving-Snapshot", dashboardBaselineSnapshot)
	request.Header.Set("X-Request-ID", "fai-544-candidate")
	return f.do(request)
}

func (f *dashboardDirectArrowQualificationFixture) do(request *stdhttp.Request) (dashboardDirectArrowQualificationResponse, error) {
	started := time.Now()
	response, err := f.client.Do(request)
	if err != nil {
		return dashboardDirectArrowQualificationResponse{}, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	return dashboardDirectArrowQualificationResponse{
		body: body, header: response.Header.Clone(), trailer: response.Trailer.Clone(),
		status: response.StatusCode, duration: time.Since(started),
	}, err
}

type dashboardDirectArrowQualificationListener struct {
	net.Listener
	writeBuffer int
}

func (l *dashboardDirectArrowQualificationListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	if tcp, ok := conn.(*net.TCPConn); ok {
		_ = tcp.SetWriteBuffer(l.writeBuffer)
	}
	return conn, nil
}

func TestDashboardDirectArrowQualificationRealDuckDBHTTPParity(t *testing.T) {
	for _, workload := range []struct {
		name    string
		visual  string
		columns int
	}{
		{name: "narrow", visual: "detail_narrow", columns: dashboardBaselineNarrowFields},
		{name: "wide", visual: "detail_wide", columns: dashboardBaselineWideFields},
	} {
		t.Run(workload.name, func(t *testing.T) {
			const rows = 999
			fixture := newDashboardDirectArrowQualificationFixture(t, rows, 2, 64<<10)
			fixture.resetStats()
			ownershipBefore := arrowresult.Stats()
			control, err := fixture.requestControl(t.Context(), workload.visual, rows+1, "")
			if err != nil {
				t.Fatal(err)
			}
			candidate, err := fixture.requestCandidate(t.Context(), workload.visual, rows, "")
			if err != nil {
				t.Fatal(err)
			}
			if control.status != stdhttp.StatusOK || candidate.status != stdhttp.StatusOK {
				t.Fatalf("control/candidate status = %d/%d", control.status, candidate.status)
			}
			assertDashboardDirectArrowQualificationEquivalent(t, control.body, candidate.body, workload.columns)
			if control.header.Get("X-Next-Cursor") != "" || candidate.trailer.Get("X-Next-Cursor") != "" {
				t.Fatalf("short result cursors control/candidate = %q/%q", control.header.Get("X-Next-Cursor"), candidate.trailer.Get("X-Next-Cursor"))
			}
			stats := fixture.database.stats()
			if stats.queries != 2 || fixture.physical.Load() != 2 || fixture.cacheOutcome.Load() != 0 {
				t.Fatalf("control/candidate physical/cache observations = database %d physical %d cache %d", stats.queries, fixture.physical.Load(), fixture.cacheOutcome.Load())
			}
			if fixture.governor.calls.Load() != 2 || len(fixture.admitter.snapshot()) != 2 {
				t.Fatalf("control/candidate governance/admission calls = %d/%d", fixture.governor.calls.Load(), len(fixture.admitter.snapshot()))
			}
			if stats.active != 0 || len(stats.leaseDurations) != 2 || len(stats.queryDurations) != 2 {
				t.Fatalf("connection lifecycle after parity request = %#v", stats)
			}
			if ownershipAfter := arrowresult.Stats(); ownershipAfter != ownershipBefore {
				t.Fatalf("direct HTTP comparison changed retained Arrow ownership: before=%#v after=%#v", ownershipBefore, ownershipAfter)
			}
		})
	}
}

func TestDashboardDirectArrowQualificationRealDuckDBMultiBatchAndEmpty(t *testing.T) {
	t.Run("multiple borrowed batches", func(t *testing.T) {
		const rows = 10_000
		fixture := newDashboardDirectArrowQualificationFixture(t, rows, 2, 64<<10)
		fixture.resetStats()
		response, err := fixture.requestCandidate(t.Context(), dashboardDirectArrowQualificationLargeVisual, rows, "")
		if err != nil {
			t.Fatal(err)
		}
		if response.status != stdhttp.StatusOK {
			t.Fatalf("candidate status = %d body=%s", response.status, response.body)
		}
		reader, err := ipc.NewReader(bytes.NewReader(response.body))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Release()
		assertDashboardDirectArrowQualificationSchema(t, reader.Schema())
		var batches int
		var gotRows int64
		var nullableNulls int
		for reader.Next() {
			batches++
			record := reader.Record()
			if batches == 1 {
				assertDashboardDirectArrowQualificationValues(t, record)
			}
			gotRows += record.NumRows()
			nullableNulls += record.Column(2).NullN()
		}
		if reader.Err() != nil || gotRows != rows || batches < 2 || nullableNulls == 0 {
			t.Fatalf("real DuckDB stream batches/rows/nulls/error = %d/%d/%d/%v", batches, gotRows, nullableNulls, reader.Err())
		}
		stats := fixture.database.stats()
		if len(stats.batchSizes) < 2 || stats.queries != 1 || stats.active != 0 {
			t.Fatalf("real DuckDB batch/lifecycle stats = %#v", stats)
		}
		if response.trailer.Get("X-Next-Cursor") != "" {
			t.Fatalf("exact final result exposed cursor %q", response.trailer.Get("X-Next-Cursor"))
		}
	})

	t.Run("empty result preserves physical schema", func(t *testing.T) {
		fixture := newDashboardDirectArrowQualificationFixture(t, 0, 1, 64<<10)
		response, err := fixture.requestCandidate(t.Context(), "detail_wide", 100, "")
		if err != nil {
			t.Fatal(err)
		}
		reader, err := ipc.NewReader(bytes.NewReader(response.body))
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Release()
		assertDashboardDirectArrowQualificationSchema(t, reader.Schema())
		if reader.Next() || reader.Err() != nil {
			t.Fatalf("empty real DuckDB stream produced records: %v", reader.Err())
		}
	})
}

func TestDashboardDirectArrowQualificationDictionaryAcrossBatches(t *testing.T) {
	allocator := memory.NewCheckedAllocator(memory.DefaultAllocator)
	schema, first := newDashboardNativeArrowContractFixture(t, allocator)
	_, second := newDashboardNativeArrowContractFixture(t, allocator)
	executor := &dashboardDirectArrowQualificationBorrowedExecutor{schema: schema, records: []arrow.RecordBatch{first, second}}
	recorder := httptest.NewRecorder()
	config := dashboardDirectArrowContractConfig(6, 0)
	_, err := semanticapi.ExecuteDirectArrowExperiment(t.Context(), recorder, executor, dashboardDirectArrowFixtureQuery(6, 0), config)
	if err != nil {
		t.Fatal(err)
	}
	allocator.AssertSize(t, 0)
	reader, err := ipc.NewReader(bytes.NewReader(recorder.Body.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	assertDashboardNativeArrowSchema(t, reader.Schema())
	var batches int
	for reader.Next() {
		batches++
		assertDashboardNativeArrowValues(t, reader.Record())
	}
	if reader.Err() != nil || batches != 2 {
		t.Fatalf("dictionary batch count/error = %d/%v", batches, reader.Err())
	}
}

type dashboardDirectArrowQualificationBorrowedExecutor struct {
	schema  *arrow.Schema
	records []arrow.RecordBatch
}

func (e *dashboardDirectArrowQualificationBorrowedExecutor) ExecuteDataQueryArrow(ctx context.Context, _ dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if err := arrowquery.ConsumeSchemaBudget(ctx, e.schema); err != nil {
		return dataquery.Result{}, err
	}
	if err := sink.WriteSchema(e.schema); err != nil {
		return dataquery.Result{}, err
	}
	for index, record := range e.records {
		if err := arrowquery.ConsumeResultBudget(ctx, record); err != nil {
			return dataquery.Result{}, err
		}
		if err := sink.WriteRecord(record); err != nil {
			return dataquery.Result{}, err
		}
		// The producer releases each batch as soon as the synchronous callback
		// returns. The native sink must not retain its dictionary or buffers.
		record.Release()
		e.records[index] = nil
	}
	return dataquery.Result{RowsReturned: 6}, nil
}

func TestDashboardDirectArrowQualificationPaginationContract(t *testing.T) {
	const totalRows = 2_501
	const pageSize = 1_000
	fixture := newDashboardDirectArrowQualificationFixture(t, totalRows, 2, 64<<10)

	fixture.resetStats()
	control, err := fixture.requestControl(t.Context(), "detail_wide", pageSize, "")
	if err != nil {
		t.Fatal(err)
	}
	controlCursor := control.header.Get("X-Next-Cursor")
	if controlCursor == "" || fixture.database.stats().queries != 2 {
		t.Fatalf("current api_direct full page cursor/queries = %q/%d, want cursor and rows+exact-count queries", controlCursor, fixture.database.stats().queries)
	}

	fixture.resetStats()
	first, err := fixture.requestCandidate(t.Context(), "detail_wide", pageSize, "")
	if err != nil {
		t.Fatal(err)
	}
	firstCursor := first.trailer.Get("X-Next-Cursor")
	if rows := dashboardDirectArrowQualificationRows(t, first.body); rows != pageSize {
		t.Fatalf("candidate first page rows = %d", rows)
	}
	if offset, err := semanticapi.DecodeDirectArrowExperimentCursor(firstCursor, dashboardDirectArrowQualificationScope, dashboardBaselineSnapshot); err != nil || offset != pageSize {
		t.Fatalf("candidate first cursor = %q offset=%d error=%v", firstCursor, offset, err)
	}
	if fixture.database.stats().queries != 1 {
		t.Fatalf("candidate limit+1 first page queries = %d, want 1", fixture.database.stats().queries)
	}
	if firstCursor == controlCursor {
		t.Fatal("candidate and current opaque cursors unexpectedly share identity")
	}
	if _, err := semanticapi.DecodeDirectArrowExperimentCursor(firstCursor, "wrong-scope", dashboardBaselineSnapshot); err == nil {
		t.Fatal("candidate cursor was accepted in a different scope")
	}

	second, err := fixture.requestCandidate(t.Context(), "detail_wide", pageSize, firstCursor)
	if err != nil {
		t.Fatal(err)
	}
	secondCursor := second.trailer.Get("X-Next-Cursor")
	if rows := dashboardDirectArrowQualificationRows(t, second.body); rows != pageSize || secondCursor == "" {
		t.Fatalf("candidate second page rows/cursor = %d/%q", rows, secondCursor)
	}
	final, err := fixture.requestCandidate(t.Context(), "detail_wide", pageSize, secondCursor)
	if err != nil {
		t.Fatal(err)
	}
	if rows := dashboardDirectArrowQualificationRows(t, final.body); rows != 501 || final.trailer.Get("X-Next-Cursor") != "" {
		t.Fatalf("candidate final page rows/cursor = %d/%q", rows, final.trailer.Get("X-Next-Cursor"))
	}
}

func TestDashboardDirectArrowQualificationSlowAndDisconnectedClientsReleaseConnections(t *testing.T) {
	t.Run("delayed reader pins one bounded connection", func(t *testing.T) {
		fixture := newDashboardDirectArrowQualificationFixture(t, 20_000, 2, 1<<10)
		fixture.resetStats()
		request, err := stdhttp.NewRequestWithContext(t.Context(), stdhttp.MethodGet, fixture.server.URL+"/qualification/native/"+dashboardDirectArrowQualificationLargeVisual+"?limit=20000", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		beforeRSS := dashboardDirectArrowQualificationRSS()
		time.Sleep(100 * time.Millisecond)
		blocked := fixture.database.stats()
		if blocked.active != 1 || fixture.handlers.Load() != 1 {
			response.Body.Close()
			t.Fatalf("delayed reader active connections/handlers = %d/%d", blocked.active, fixture.handlers.Load())
		}
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			response.Body.Close()
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		waitDashboardDirectArrowQualification(t, time.Second, func() bool { return fixture.database.active.Load() == 0 && fixture.handlers.Load() == 0 })
		after := fixture.database.stats()
		if len(after.leaseDurations) != 1 || after.leaseDurations[0] < 100*time.Millisecond {
			t.Fatalf("delayed reader connection hold = %v", after.leaseDurations)
		}
		t.Logf("delayed reader RSS before/blocked=%d/%d connection_hold=%s", beforeRSS, dashboardDirectArrowQualificationRSS(), after.leaseDurations[0])
	})

	t.Run("disconnect cancels and releases", func(t *testing.T) {
		fixture := newDashboardDirectArrowQualificationFixture(t, 20_000, 1, 1<<10)
		fixture.resetStats()
		request, err := stdhttp.NewRequestWithContext(t.Context(), stdhttp.MethodGet, fixture.server.URL+"/qualification/native/"+dashboardDirectArrowQualificationLargeVisual+"?limit=20000", nil)
		if err != nil {
			t.Fatal(err)
		}
		response, err := fixture.client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		buffer := make([]byte, 1)
		_, _ = response.Body.Read(buffer)
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
		waitDashboardDirectArrowQualification(t, 2*time.Second, func() bool { return fixture.database.active.Load() == 0 && fixture.handlers.Load() == 0 })
		if err := fixture.candidateError(); err == nil {
			t.Fatal("disconnected HTTP client did not terminate the synchronous stream")
		}
		if fixture.database.stats().queries != 1 {
			t.Fatalf("disconnected client physical queries = %d", fixture.database.stats().queries)
		}
	})
}

func TestDashboardDirectArrowQualificationSlowStreamDoesNotBlockIndependentRequest(t *testing.T) {
	fixture := newDashboardDirectArrowQualificationFixture(t, 20_000, 2, 1<<10)
	fixture.resetStats()
	request, err := stdhttp.NewRequestWithContext(t.Context(), stdhttp.MethodGet, fixture.server.URL+"/qualification/native/"+dashboardDirectArrowQualificationLargeVisual+"?limit=20000", nil)
	if err != nil {
		t.Fatal(err)
	}
	slow, err := fixture.client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	waitDashboardDirectArrowQualification(t, time.Second, func() bool { return fixture.database.active.Load() == 1 })
	normal, err := fixture.requestCandidate(t.Context(), "detail_narrow", 50, "")
	if err != nil {
		slow.Body.Close()
		t.Fatal(err)
	}
	if normal.status != stdhttp.StatusOK || dashboardDirectArrowQualificationRows(t, normal.body) != 50 {
		slow.Body.Close()
		t.Fatalf("independent candidate status/rows = %d/%d", normal.status, dashboardDirectArrowQualificationRows(t, normal.body))
	}
	if fixture.database.stats().maxActive < 2 {
		slow.Body.Close()
		t.Fatalf("slow and independent streams did not use isolated connections: %#v", fixture.database.stats())
	}
	_ = slow.Body.Close()
	waitDashboardDirectArrowQualification(t, 2*time.Second, func() bool { return fixture.database.active.Load() == 0 && fixture.handlers.Load() == 0 })
}

func waitDashboardDirectArrowQualification(t testing.TB, timeout time.Duration, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for FAI-544 qualification cleanup")
		}
		time.Sleep(time.Millisecond)
	}
}

func BenchmarkDashboardDirectArrowQualification(b *testing.B) {
	for _, workload := range []struct {
		name    string
		visual  string
		columns int
	}{
		{name: "real_narrow_rows_999", visual: "detail_narrow", columns: dashboardBaselineNarrowFields},
		{name: "real_wide_rows_999", visual: "detail_wide", columns: dashboardBaselineWideFields},
	} {
		b.Run(workload.name, func(b *testing.B) {
			for _, lane := range []string{"control_api_direct_http", "candidate_native_v1_http"} {
				b.Run(lane, func(b *testing.B) {
					fixture := newDashboardDirectArrowQualificationFixture(b, 999, 2, 64<<10)
					fixture.resetStats()
					ownershipBefore := arrowresult.Stats()
					var memoryBefore runtime.MemStats
					runtime.ReadMemStats(&memoryBefore)
					rssBefore := dashboardDirectArrowQualificationRSS()
					durations := make([]time.Duration, 0, b.N)
					var totalBytes int64
					b.ReportAllocs()
					b.ResetTimer()
					for range b.N {
						var response dashboardDirectArrowQualificationResponse
						var err error
						if lane == "control_api_direct_http" {
							response, err = fixture.requestControl(context.Background(), workload.visual, 1_000, "")
						} else {
							response, err = fixture.requestCandidate(context.Background(), workload.visual, 999, "")
						}
						if err != nil || response.status != stdhttp.StatusOK {
							b.Fatalf("%s response status/error = %d/%v", lane, response.status, err)
						}
						durations = append(durations, response.duration)
						totalBytes += int64(len(response.body))
						dashboardBaselineBodySink = response.body
					}
					b.StopTimer()
					var memoryAfter runtime.MemStats
					runtime.ReadMemStats(&memoryAfter)
					stats := fixture.database.stats()
					if stats.queries != int64(b.N) || fixture.physical.Load() != int64(b.N) || fixture.cacheOutcome.Load() != 0 || stats.active != 0 {
						b.Fatalf("%s invalid query/cache/lifecycle stats: database=%d physical=%d cache=%d active=%d", lane, stats.queries, fixture.physical.Load(), fixture.cacheOutcome.Load(), stats.active)
					}
					if ownershipAfter := arrowresult.Stats(); ownershipAfter != ownershipBefore {
						b.Fatalf("%s changed retained Arrow ownership: before=%#v after=%#v", lane, ownershipBefore, ownershipAfter)
					}
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					operations := float64(b.N)
					b.ReportMetric(float64(totalBytes)/operations, "ipc-bytes/op")
					b.ReportMetric(float64(sumDashboardDirectArrowQualificationDurations(stats.leaseDurations).Nanoseconds())/operations, "connection-hold-ns/op")
					b.ReportMetric(float64(sumDashboardDirectArrowQualificationDurations(stats.waitDurations).Nanoseconds())/operations, "connection-wait-ns/op")
					b.ReportMetric(float64(len(stats.batchSizes))/operations, "duckdb-batches/op")
					b.ReportMetric(float64(memoryAfter.NumGC-memoryBefore.NumGC)/operations, "gc-cycles/op")
					b.ReportMetric(float64(max(rssBefore, dashboardDirectArrowQualificationRSS())), "process-rss-bytes")
					b.ReportMetric(float64(runtime.GOMAXPROCS(0)), "gomaxprocs")
					b.ReportMetric(float64(durations[quantileIndex(len(durations), .50)].Nanoseconds()), "p50-ns/op")
					b.ReportMetric(float64(durations[quantileIndex(len(durations), .95)].Nanoseconds()), "p95-ns/op")
					b.ReportMetric(float64(durations[quantileIndex(len(durations), .99)].Nanoseconds()), "p99-ns/op")
					b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "requests/s")
					b.ReportMetric(float64(workload.columns), "columns/op")
				})
			}
		})
	}
}

func BenchmarkDashboardDirectArrowQualificationConcurrency(b *testing.B) {
	for _, users := range []int{1, 4, 8} {
		b.Run("real_wide_rows_999/users_"+strconv.Itoa(users), func(b *testing.B) {
			for _, lane := range []string{"control_api_direct_http", "candidate_native_v1_http"} {
				b.Run(lane, func(b *testing.B) {
					fixture := newDashboardDirectArrowQualificationFixture(b, 999, users, 64<<10)
					fixture.resetStats()
					durations := make([]time.Duration, 0, b.N*users)
					var totalBytes int64
					b.ReportAllocs()
					b.ResetTimer()
					b.StopTimer()
					for range b.N {
						start := make(chan struct{})
						results := make(chan dashboardDirectArrowQualificationResponse, users)
						errorsOut := make(chan error, users)
						batchStarted := time.Now()
						for range users {
							go func() {
								<-start
								var response dashboardDirectArrowQualificationResponse
								var err error
								if lane == "control_api_direct_http" {
									response, err = fixture.requestControl(context.Background(), "detail_wide", 1_000, "")
								} else {
									response, err = fixture.requestCandidate(context.Background(), "detail_wide", 999, "")
								}
								if err == nil {
									response.duration = time.Since(batchStarted)
								}
								results <- response
								errorsOut <- err
							}()
						}
						b.StartTimer()
						batchStarted = time.Now()
						close(start)
						for range users {
							response := <-results
							if err := <-errorsOut; err != nil || response.status != stdhttp.StatusOK {
								b.Fatalf("%s concurrent response status/error = %d/%v", lane, response.status, err)
							}
							durations = append(durations, response.duration)
							totalBytes += int64(len(response.body))
						}
						b.StopTimer()
					}
					requests := int64(b.N * users)
					stats := fixture.database.stats()
					if stats.queries != requests || fixture.physical.Load() != requests || fixture.cacheOutcome.Load() != 0 || stats.active != 0 {
						b.Fatalf("%s concurrent query/cache/lifecycle stats = database %d physical %d cache %d active %d, want %d requests", lane, stats.queries, fixture.physical.Load(), fixture.cacheOutcome.Load(), stats.active, requests)
					}
					sort.Slice(durations, func(i, j int) bool { return durations[i] < durations[j] })
					elapsed := b.Elapsed()
					b.ReportMetric(float64(totalBytes)/float64(requests), "ipc-bytes/request")
					b.ReportMetric(float64(elapsed.Nanoseconds())/float64(requests), "wall-ns/request")
					b.ReportMetric(float64(requests)/elapsed.Seconds(), "requests/s")
					b.ReportMetric(float64(stats.maxActive), "max-connections")
					b.ReportMetric(float64(durations[quantileIndex(len(durations), .50)].Nanoseconds()), "p50-ns/request")
					b.ReportMetric(float64(durations[quantileIndex(len(durations), .95)].Nanoseconds()), "p95-ns/request")
					b.ReportMetric(float64(durations[quantileIndex(len(durations), .99)].Nanoseconds()), "p99-ns/request")
				})
			}
		})
	}
}

func assertDashboardDirectArrowQualificationSchema(t testing.TB, schema *arrow.Schema) {
	t.Helper()
	if schema.NumFields() != dashboardBaselineWideFields {
		t.Fatalf("qualification schema fields = %d, want %d", schema.NumFields(), dashboardBaselineWideFields)
	}
	if err := validateDashboardNativeArrowMetadata(schema.Metadata(), dashboardNativeArrowSchemaMetadataAllowlist); err != nil {
		t.Fatal(err)
	}
	want := []arrow.Type{arrow.INT64, arrow.FLOAT64, arrow.BOOL, arrow.STRING, arrow.BINARY, arrow.TIMESTAMP, arrow.DECIMAL128, arrow.DATE32, arrow.STRING}
	lostNonNullDeclarations := 0
	for index, typeID := range want {
		field := schema.Field(index)
		if field.Name != fmt.Sprintf("field_%02d", index) || field.Type.ID() != typeID {
			t.Fatalf("qualification field %d = %s %s, want field_%02d %s", index, field.Name, field.Type, index, typeID)
		}
		if dashboardDirectArrowQualificationNullable(index) && !field.Nullable {
			t.Fatalf("qualification nullable field %d became non-nullable", index)
		}
		if !dashboardDirectArrowQualificationNullable(index) && field.Nullable {
			lostNonNullDeclarations++
		}
		if err := validateDashboardNativeArrowMetadata(field.Metadata, dashboardNativeArrowFieldMetadataAllowlist); err != nil {
			t.Fatalf("qualification field %d metadata: %v", index, err)
		}
	}
	// DuckDB's Arrow driver currently marks every query result field nullable,
	// including columns declared NOT NULL by this fixture. The candidate
	// preserves that borrowed schema exactly, so the qualification records this
	// as a blocking fidelity gap instead of silently treating it as native-v1
	// coverage for mixed nullability.
	if lostNonNullDeclarations == 0 {
		t.Fatal("real DuckDB fixture no longer exposes the known NOT NULL declaration gap; update the qualification decision")
	}
	decimalType, ok := schema.Field(6).Type.(*arrow.Decimal128Type)
	if !ok || decimalType.Precision != 38 || decimalType.Scale != 3 {
		t.Fatalf("qualification decimal type = %v", schema.Field(6).Type)
	}
	neutral, ok := schema.Field(5).Type.(*arrow.TimestampType)
	if !ok || neutral.TimeZone != "" || neutral.Unit != arrow.Microsecond {
		t.Fatalf("qualification timezone-neutral timestamp = %v", schema.Field(5).Type)
	}
	aware, ok := schema.Field(14).Type.(*arrow.TimestampType)
	if !ok || aware.TimeZone == "" || aware.Unit != arrow.Microsecond {
		t.Fatalf("qualification timezone-aware timestamp = %v", schema.Field(14).Type)
	}
}

func assertDashboardDirectArrowQualificationValues(t testing.TB, record arrow.RecordBatch) {
	t.Helper()
	if record.NumRows() < 12 || record.NumCols() != dashboardBaselineWideFields {
		t.Fatalf("qualification first batch shape = %dx%d", record.NumRows(), record.NumCols())
	}
	// The existing detail query's stable default order is descending by the
	// first projected dimension, so the 10,000-row fixture starts at source row
	// 9,999. Assert source-derived values rather than only comparing encoders.
	const sourceRow int64 = 9_999
	if values := record.Column(0).(*array.Int64); values.NullN() != 0 || values.Value(0) != sourceRow*37+1 {
		t.Fatalf("qualification non-null int64 values/nulls = %d/%d", values.Value(0), values.NullN())
	}
	if values := record.Column(1).(*array.Float64); values.Value(0) != float64(sourceRow*37+2)/7.0+0.125 {
		t.Fatalf("qualification float64 value = %v", values.Value(0))
	}
	if values := record.Column(2).(*array.Boolean); values.Value(0) {
		t.Fatal("qualification boolean value was not preserved")
	}
	if values := record.Column(3).(*array.String); values.Value(0) != "value-496" {
		t.Fatalf("qualification string value = %q", values.Value(0))
	}
	if values := record.Column(4).(*array.Binary); !bytes.Equal(values.Value(0), []byte("bytes-555")) {
		t.Fatalf("qualification binary value = %q", values.Value(0))
	}
	if values := record.Column(5).(*array.Timestamp); values.Value(0) != arrow.Timestamp(time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC).UnixMicro()+sourceRow) {
		t.Fatalf("qualification timestamp value = %d", values.Value(0))
	}
	if values := record.Column(6).(*array.Decimal128); values.Value(0).ToString(3) != "369970.000" {
		t.Fatalf("qualification decimal value = %s", values.Value(0).ToString(3))
	}
	if values := record.Column(7).(*array.Date32); values.Value(0) != arrow.Date32FromTime(time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC).AddDate(0, 0, int(sourceRow%2_000))) {
		t.Fatalf("qualification date value = %d", values.Value(0))
	}
	if values := record.Column(8).(*array.String); values.Value(0) != "category-8" {
		t.Fatalf("qualification low-cardinality value = %q", values.Value(0))
	}
	for _, column := range []int{2, 4, 6, 8} {
		position := int((sourceRow + int64(column)) % 13)
		values := record.Column(column)
		if !values.IsNull(position) || values.IsNull(position-1) || values.IsNull(position+1) {
			t.Fatalf("qualification column %d null position around row %d = %v/%v/%v", column, position, values.IsNull(position-1), values.IsNull(position), values.IsNull(position+1))
		}
	}
}

func assertDashboardDirectArrowQualificationEquivalent(t testing.TB, controlPayload, candidatePayload []byte, columns int) {
	t.Helper()
	controlReader, err := ipc.NewReader(bytes.NewReader(controlPayload))
	if err != nil {
		t.Fatal(err)
	}
	defer controlReader.Release()
	candidateReader, err := ipc.NewReader(bytes.NewReader(candidatePayload))
	if err != nil {
		t.Fatal(err)
	}
	defer candidateReader.Release()
	if controlReader.Schema().NumFields() != columns || candidateReader.Schema().NumFields() != columns {
		t.Fatalf("qualification control/candidate fields = %d/%d, want %d", controlReader.Schema().NumFields(), candidateReader.Schema().NumFields(), columns)
	}
	controlValues := make([][]string, columns)
	for controlReader.Next() {
		record := controlReader.Record()
		for column := 0; column < columns; column++ {
			values := record.Column(column).(*array.String)
			for row := 0; row < int(record.NumRows()); row++ {
				controlValues[column] = append(controlValues[column], values.Value(row))
			}
		}
	}
	if controlReader.Err() != nil {
		t.Fatal(controlReader.Err())
	}
	candidateValues := make([][]string, columns)
	var nulls int
	for candidateReader.Next() {
		record := candidateReader.Record()
		for column := 0; column < columns; column++ {
			values := record.Column(column)
			for row := 0; row < int(record.NumRows()); row++ {
				if values.IsNull(row) {
					nulls++
					candidateValues[column] = append(candidateValues[column], "")
					continue
				}
				candidateValues[column] = append(candidateValues[column], dashboardWarmCurrentProjection(dashboardWarmNativeIPCValue(t, values, row)))
			}
		}
	}
	if candidateReader.Err() != nil {
		t.Fatal(candidateReader.Err())
	}
	if !reflect.DeepEqual(controlValues, candidateValues) {
		t.Fatal("real DuckDB current string projection and native-v1 values differ")
	}
	if columns > 2 && nulls == 0 {
		t.Fatal("realistic qualification fixture did not exercise null-to-empty legacy projection")
	}
}

func dashboardDirectArrowQualificationRows(t testing.TB, payload []byte) int {
	t.Helper()
	reader, err := ipc.NewReader(bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	rows := 0
	for reader.Next() {
		rows += int(reader.Record().NumRows())
	}
	if reader.Err() != nil {
		t.Fatal(reader.Err())
	}
	return rows
}

func sumDashboardDirectArrowQualificationDurations(values []time.Duration) time.Duration {
	var total time.Duration
	for _, value := range values {
		total += value
	}
	return total
}

func dashboardDirectArrowQualificationRSS() int64 {
	payload, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	fields := strings.Fields(string(payload))
	if len(fields) < 2 {
		return 0
	}
	pages, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil {
		return 0
	}
	return pages * int64(os.Getpagesize())
}
