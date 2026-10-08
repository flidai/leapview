package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/apache/arrow-go/v18/arrow"
	"github.com/apache/arrow-go/v18/arrow/array"
	"github.com/apache/arrow-go/v18/arrow/ipc"
	"github.com/apache/arrow-go/v18/arrow/memory"
	"github.com/flidai/leapview/internal/analytics/arrowquery"
	"github.com/flidai/leapview/internal/analytics/dataquery"
)

func TestSemanticArrowHTTPClientCancellationReleasesExecutor(t *testing.T) {
	metrics := &cancelableSemanticArrowMetrics{
		allocator: memory.NewCheckedAllocator(memory.DefaultAllocator),
		blocked:   make(chan struct{}),
		stopped:   make(chan struct{}),
		abort:     make(chan struct{}),
		canceled:  make(chan error, 1),
	}
	application := assembleRuntime(metrics, testStoreOptions(testStore(t), assemblyConfig{}))
	handlerDone := make(chan struct{}, 3)
	routes := application.Routes()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() { handlerDone <- struct{}{} }()
		routes.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	// Release even a deliberately detached executor before closing the server.
	// This bounds cleanup when the cancellation assertion fails.
	t.Cleanup(func() { close(metrics.abort) })
	client := server.Client()
	request := func(ctx context.Context, authenticated bool) *http.Request {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/v1/semantic-models/test/query", strings.NewReader(`{"metrics":[{"field":"order_count"}],"limit":1000}`))
		if err != nil {
			t.Fatal(err)
		}
		if authenticated {
			req.Header.Set("Authorization", "Bearer dev")
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/vnd.apache.arrow.stream")
		return servingSnapshotRequest(t, application, req)
	}
	wait := func(ch <-chan struct{}, description string) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", description)
		}
	}

	deniedCtx, cancelDenied := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelDenied()
	denied, err := client.Do(request(deniedCtx, false))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, denied.Body)
	denied.Body.Close()
	wait(handlerDone, "unauthenticated handler")
	if denied.StatusCode != http.StatusUnauthorized || metrics.calls.Load() != 0 {
		t.Fatalf("unauthenticated status=%d executor calls=%d", denied.StatusCode, metrics.calls.Load())
	}

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	// Bound a stalled first read without letting a deadline masquerade as the
	// explicit client cancellation that this test is meant to exercise.
	watchdog := time.AfterFunc(10*time.Second, cancel)
	defer watchdog.Stop()
	response, err := client.Do(request(ctx, true))
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(response.Body)
		t.Fatalf("Arrow query status=%d body=%s", response.StatusCode, body)
	}
	reader, err := ipc.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Release()
	if !reader.Next() || reader.Record().NumRows() != 500 {
		t.Fatal("client did not decode the first bounded Arrow batch")
	}
	wait(metrics.blocked, "producer barrier after bounded batches")
	if !watchdog.Stop() {
		t.Fatal("first Arrow batch exceeded the cancellation watchdog")
	}
	if metrics.calls.Load() != 1 || metrics.active.Load() != 1 {
		t.Fatalf("before cancellation executor calls=%d active=%d", metrics.calls.Load(), metrics.active.Load())
	}
	select {
	case err := <-metrics.canceled:
		t.Fatalf("executor canceled before explicit client cancellation: %v", err)
	default:
	}
	cancel()
	response.Body.Close()
	select {
	case err := <-metrics.canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("executor cancellation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("HTTP client cancellation did not reach the Arrow executor")
	}
	wait(metrics.stopped, "Arrow executor unwind")
	wait(handlerDone, "canceled HTTP handler")
	if metrics.active.Load() != 0 {
		t.Fatal("canceled Arrow executor retained fixture activity")
	}
	metrics.allocator.AssertSize(t, 0)

	followupCtx, cancelFollowup := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancelFollowup()
	followup, err := client.Do(request(followupCtx, true))
	if err != nil {
		t.Fatal(err)
	}
	defer followup.Body.Close()
	if followup.StatusCode != http.StatusOK {
		t.Fatalf("follow-up status = %d", followup.StatusCode)
	}
	complete, err := ipc.NewReader(followup.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer complete.Release()
	var rows int64
	for complete.Next() {
		rows += complete.Record().NumRows()
	}
	if err := complete.Err(); err != nil || rows != 1000 {
		t.Fatalf("follow-up Arrow stream rows=%d err=%v", rows, err)
	}
	wait(handlerDone, "follow-up HTTP handler")
	if metrics.calls.Load() != 2 || metrics.active.Load() != 0 {
		t.Fatalf("final executor calls=%d active=%d", metrics.calls.Load(), metrics.active.Load())
	}
	metrics.allocator.AssertSize(t, 0)
}

type cancelableSemanticArrowMetrics struct {
	fakeMetrics
	allocator *memory.CheckedAllocator
	calls     atomic.Int32
	active    atomic.Int32
	blocked   chan struct{}
	stopped   chan struct{}
	abort     chan struct{}
	canceled  chan error
}

func (m *cancelableSemanticArrowMetrics) ExecuteDataQueryArrow(ctx context.Context, request dataquery.Query, sink arrowquery.Sink) (dataquery.Result, error) {
	if request.ProjectID != testProjectID {
		return dataquery.Result{}, errors.New("Arrow request lost the serving project identity")
	}
	call := m.calls.Add(1)
	if m.active.Add(1) != 1 {
		m.active.Add(-1)
		return dataquery.Result{}, errors.New("previous Arrow executor still owns fixture capacity")
	}
	defer func() {
		m.active.Add(-1)
		if call == 1 {
			close(m.stopped)
		}
	}()
	builder := array.NewInt64Builder(m.allocator)
	for index := range 500 {
		builder.Append(int64(index))
	}
	values := builder.NewArray()
	builder.Release()
	defer values.Release()
	schema := arrow.NewSchema([]arrow.Field{{Name: "order_count", Type: arrow.PrimitiveTypes.Int64}}, nil)
	record := array.NewRecordBatch(schema, []arrow.Array{values}, 500)
	defer record.Release()
	if err := sink.WriteSchema(schema); err != nil {
		return dataquery.Result{}, err
	}
	// Deliver bounded batches before waiting for cancellation; the client must
	// decode a complete batch while the executor is still active.
	for range 2 {
		if err := sink.WriteRecord(record); err != nil {
			return dataquery.Result{}, err
		}
	}
	if call == 1 {
		close(m.blocked)
		select {
		case <-ctx.Done():
			m.canceled <- ctx.Err()
			return dataquery.Result{}, ctx.Err()
		case <-m.abort:
			return dataquery.Result{}, errors.New("fixture cleanup released detached executor")
		}
	}
	return dataquery.Result{RowsReturned: 1000}, nil
}
