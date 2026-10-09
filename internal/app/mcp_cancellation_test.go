package app

import (
	"context"
	"errors"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPClientCancellationAllowsFreshGovernedCall(t *testing.T) {
	metrics := &mcpCancelableMetrics{started: make(chan struct{}), stopped: make(chan struct{}), abort: make(chan struct{})}
	store := testStore(t)
	application := assembleRuntime(metrics, testStoreOptions(store, assemblyConfig{
		Auth: testAuth(store, accessmodule.AuthConfig{DevBypass: true, DevAPIToken: "mcp-secret"}),
	}))
	live := httptest.NewServer(application.Routes())
	t.Cleanup(live.Close)
	var abortOnce sync.Once
	t.Cleanup(func() { abortOnce.Do(func() { close(metrics.abort) }) })
	httpClient := *live.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: "mcp-secret"}
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "leapview-cancellation-test", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &mcpsdk.StreamableClientTransport{
		Endpoint: live.URL + "/mcp", HTTPClient: &httpClient, MaxRetries: -1, DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	params := &mcpsdk.CallToolParams{Name: "query_semantic_model", Arguments: map[string]any{
		"model": "test", "metrics": []map[string]string{{"field": "order_count"}}, "limit": 1,
	}}
	callCtx, cancelCall := context.WithCancel(t.Context())
	defer cancelCall()
	callDone := make(chan error, 1)
	go func() { _, err := session.CallTool(callCtx, params); callDone <- err }()
	select {
	case <-metrics.started:
	case err := <-callDone:
		t.Fatalf("call ended before active execution: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not reach active query execution")
	}
	complete := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
		defer cancel()
		result, err := session.CallTool(ctx, params)
		if err != nil || result == nil || result.IsError || result.StructuredContent == nil {
			t.Fatalf("fresh governed query result=%#v error=%v", result, err)
		}
	}
	cancelCall()
	select {
	case err := <-callDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled client call error=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("SDK client call did not finish on cancellation")
	}
	// Stateless MCP permits a cancellation notification to refer to an unknown
	// temporary session. This guard qualifies client cancellation and a fresh
	// governed call after controlled drain, not server-side disconnect cancellation.
	abortOnce.Do(func() { close(metrics.abort) })
	select {
	case <-metrics.stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("controlled query did not drain")
	}
	t.Logf("active tool observed server context cancellation=%v", metrics.canceled.Load())
	complete()
	if metrics.calls.Load() != 2 {
		t.Fatalf("query executions=%d, want canceled+fresh", metrics.calls.Load())
	}
}

type mcpCancelableMetrics struct {
	fakeMetrics
	calls                   atomic.Int32
	canceled                atomic.Bool
	started, stopped, abort chan struct{}
}

func (m *mcpCancelableMetrics) ExecuteDataQuery(ctx context.Context, query dataquery.Query) (dataquery.Result, error) {
	if m.calls.Add(1) == 1 {
		close(m.started)
		defer close(m.stopped)
		select {
		case <-ctx.Done():
			m.canceled.Store(true)
			return dataquery.Result{}, ctx.Err()
		case <-m.abort:
			return dataquery.Result{}, errors.New("controlled probe released")
		}
	}
	return m.fakeMetrics.ExecuteDataQuery(ctx, query)
}
