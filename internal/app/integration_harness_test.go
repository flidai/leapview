package app

// This harness is deliberately project-scoped.  It keeps the active stream
// and command tests runnable without bringing back the retired workspace
// selector/runtime registry fixtures.

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/dashboard/consumer"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/testing/ssetest"
)

type integrationMetrics interface{ QueryMetrics }

type harness struct {
	handler http.Handler
	server  *httptest.Server
	store   *platform.Store
	metrics integrationMetrics
}

type harnessConfig struct {
	fixture     func(*testing.T, string)
	wrapMetrics func(integrationMetrics) integrationMetrics
}

type harnessOption func(*harnessConfig)

func withOlistFixture(fixture func(*testing.T, string)) harnessOption {
	return func(config *harnessConfig) { config.fixture = fixture }
}

func withMetricsWrapper(wrapper func(integrationMetrics) integrationMetrics) harnessOption {
	return func(config *harnessConfig) { config.wrapMetrics = wrapper }
}

func newHarness(t *testing.T, opts ...harnessOption) *harness {
	t.Helper()
	config := harnessConfig{}
	for _, opt := range opts {
		opt(&config)
	}
	metrics := integrationMetrics(fakeMetrics{})
	if config.wrapMetrics != nil {
		metrics = config.wrapMetrics(metrics)
	}
	h := &harness{metrics: metrics}
	h.handler = newAppTestHarness(metrics).Routes()
	h.server = httptest.NewServer(h.handler)
	t.Cleanup(h.server.Close)
	return h
}

func (h *harness) serverURL(t *testing.T) string {
	t.Helper()
	return h.server.URL
}

func (h *harness) updatesPath() string { return "/updates" }

func (h *harness) commandPath(path string) string {
	if strings.HasPrefix(path, "/commands/") {
		return path
	}
	return path
}

func (h *harness) getUpdates(t *testing.T, dashboardID, pageID string, signals map[string]any) string {
	return h.getUpdatesWithQueryTimeout(t, dashboardID, pageID, signals, nil, 250*time.Millisecond)
}

func (h *harness) getUpdatesWithQueryTimeout(t *testing.T, dashboardID, pageID string, signals map[string]any, query url.Values, timeout time.Duration) string {
	t.Helper()
	encoded, err := json.Marshal(signals)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"route": {"dashboard"}, "dashboard": {dashboardID}, "page": {pageID}, "datastar": {string(encoded)}}
	for key, vals := range query {
		for _, value := range vals {
			values.Add(key, value)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, h.updatesPath()+"?"+values.Encode(), nil)
	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /updates status = %d, body:\n%s", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func (h *harness) getUpdatesSignals(t *testing.T, dashboardID, pageID string, signals map[string]any) []map[string]any {
	return patchSignalsFromBody(t, h.getUpdates(t, dashboardID, pageID, signals))
}

func (h *harness) getUpdatesSignalsWithQueryTimeout(t *testing.T, dashboardID, pageID string, signals map[string]any, query url.Values, timeout time.Duration) []map[string]any {
	return patchSignalsFromBody(t, h.getUpdatesWithQueryTimeout(t, dashboardID, pageID, signals, query, timeout))
}

func patchSignalsFromBody(t *testing.T, body string) []map[string]any {
	t.Helper()
	patches := ssetest.PatchSignals(t, body)
	if len(patches) == 0 {
		t.Fatalf("updates did not stream Datastar patch signals:\n%s", body)
	}
	return patches
}

func (h *harness) openUpdatesStream(t *testing.T, dashboardID, pageID string, signals map[string]any) *streamClient {
	t.Helper()
	encoded, err := json.Marshal(signals)
	if err != nil {
		t.Fatal(err)
	}
	values := url.Values{"route": {"dashboard"}, "dashboard": {dashboardID}, "page": {pageID}, "datastar": {string(encoded)}}
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.serverURL(t)+h.updatesPath()+"?"+values.Encode(), nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if clientID := clientIDFromSignals(signals); clientID != "" {
		req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: clientID})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		cancel()
		t.Fatalf("GET /updates status = %d, body:\n%s", res.StatusCode, body)
	}
	client := &streamClient{cancel: cancel, body: res.Body, patches: make(chan map[string]any, 16), errs: make(chan error, 1)}
	go client.read()
	t.Cleanup(client.close)
	return client
}

func (h *harness) postCommand(t *testing.T, path string, signals map[string]any) int {
	t.Helper()
	encoded, err := json.Marshal(signals)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, h.serverURL(t)+h.commandPath(path), bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if clientID := clientIDFromSignals(signals); clientID != "" {
		req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: clientID})
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

func (h *harness) getAuthenticated(t *testing.T, path string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, h.serverURL(t)+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer dev")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return string(body)
}

func (h *harness) getAuthenticatedHydrated(t *testing.T, path string) string {
	return h.getAuthenticated(t, path)
}

func (h *harness) postAuthenticated(t *testing.T, path string) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, h.serverURL(t)+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer dev")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	return res.StatusCode
}

type streamClient struct {
	cancel  context.CancelFunc
	body    io.ReadCloser
	patches chan map[string]any
	errs    chan error
}

func (c *streamClient) nextPatch(t *testing.T) map[string]any {
	t.Helper()
	timer := time.NewTimer(2 * time.Second)
	defer timer.Stop()
	select {
	case patch, ok := <-c.patches:
		if !ok {
			t.Fatal("updates stream closed before next patch")
		}
		return patch
	case err := <-c.errs:
		if err == nil || errors.Is(err, context.Canceled) {
			t.Fatal("updates stream closed before next patch")
		}
		t.Fatal(err)
	case <-timer.C:
		t.Fatal("timed out waiting for next updates patch")
	}
	return nil
}

func (c *streamClient) expectNoPatch(t *testing.T, duration time.Duration) {
	t.Helper()
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case patch := <-c.patches:
		t.Fatalf("unexpected updates patch: %#v", patch)
	case err := <-c.errs:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-timer.C:
	}
}

func (c *streamClient) close() {
	c.cancel()
	_ = c.body.Close()
}

func (c *streamClient) read() {
	defer close(c.patches)
	reader := bufio.NewReader(c.body)
	var event strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			event.WriteString(line)
			if line == "\n" || line == "\r\n" {
				for _, evt := range ssetest.ParseEvents(event.String()) {
					patch, ok, decodeErr := ssetest.DecodePatchSignalEvent(evt)
					if decodeErr != nil {
						c.errs <- decodeErr
						return
					}
					if ok {
						c.patches <- patch
					}
				}
				event.Reset()
			}
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return
		}
		c.errs <- fmt.Errorf("read SSE event: %w", err)
		return
	}
}

// Keep the consumer import anchored in this fixture package: wrappers in the
// active command tests intentionally specialize this canonical interface.
var _ consumer.Executor = fakeMetrics{}
