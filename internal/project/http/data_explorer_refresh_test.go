package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerRefreshBootstrapCannotReplaceNewerCommand(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	executor := &cancellationIgnoringSemanticExecutor{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	h.QueryExecutor = executor
	clientID := "explorer-refresh-race"
	values := url.Values{
		"route":         {"data"},
		"surface":       {"explore"},
		"clientId":      {clientID},
		"mode":          {"explore"},
		"semanticModel": {"semantic:sales"},
		"dataset":       {"orders"},
		"dimension":     {"orders.status"},
	}
	streamContext, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := httptest.NewRequestWithContext(streamContext, http.MethodGet, "/updates?"+values.Encode(), nil)
	stream := &notifyingResponseRecorder{ResponseRecorder: httptest.NewRecorder(), wrote: make(chan struct{})}
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.Updates(stream, request)
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("refresh bootstrap did not start its semantic query")
	}

	datasetID := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	newer := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 1,
		Explore: &projectsignals.DataExploreCommand{Action: projectsignals.Optional("configure"), RequestSeq: 1, Spec: spec},
	}
	if _, _, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/explore/command", nil), newer); !ok {
		t.Fatal("newer configure command failed")
	}
	select {
	case <-executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("newer command did not cancel the refresh query")
	}
	close(executor.release)

	select {
	case <-stream.wrote:
		t.Fatalf("late refresh bootstrap emitted a stale full-state patch: %s", stream.Body.String())
	case <-time.After(100 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("refresh update stream did not stop after cancellation")
	}
}
