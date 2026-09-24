package http

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/analytics/dataquery"
	exploration "github.com/flidai/leapview/internal/analytics/exploration"
	explorationlowering "github.com/flidai/leapview/internal/analytics/exploration/lowering"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

var testDataExplorerQueryLowerer exploration.QueryLowerer = explorationlowering.Service{}

func newDataExplorerLeaseFixture(clientID string, setClientHeader bool) (*BrowserHandler, *http.Request, string, string) {
	h := &BrowserHandler{ResolveProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:test", nil }}
	request := httptest.NewRequest("POST", "/explore/command", nil)
	if setClientHeader {
		request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	}
	key := h.dataExplorerClientKey(request, "project:test", projectsignals.DataExplorerCommand{ClientID: projectsignals.Optional(clientID)})
	return h, request, key, clientID
}

func TestDataExplorerLifecycleStopsOnlyCurrentRun(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	parent := context.Background()
	first, finishFirst, _ := lifecycle.beginRun("client", "run-1", 1, parent)
	second, finishSecond, _ := lifecycle.beginRun("client", "run-2", 2, parent)
	t.Cleanup(finishFirst)
	t.Cleanup(finishSecond)

	if first.Err() == nil {
		t.Fatal("starting a newer run did not cancel the older run")
	}
	if lifecycle.stop("client", "run-1", 1) {
		t.Fatal("stale stop cancelled the newer run")
	}
	if second.Err() != nil {
		t.Fatalf("stale stop cancelled current run: %v", second.Err())
	}
	if lifecycle.stop("client", "wrong-run", 2) {
		t.Fatal("stop with the wrong run ID was accepted")
	}
	if !lifecycle.stop("client", "run-2", 2) {
		t.Fatal("stop for the current run was rejected")
	}
	if !errors.Is(second.Err(), context.Canceled) {
		t.Fatalf("current run error = %v, want context.Canceled", second.Err())
	}
	if lifecycle.acceptSemantic("client", 1) {
		t.Fatal("request sequence regressed after stop")
	}
}

func TestDataExplorerLifecycleMonotonicUnnamedRecoveryStop(t *testing.T) {
	t.Run("newer stop cancels the active older run", func(t *testing.T) {
		var lifecycle dataExplorerLifecycle
		run, finish, _ := lifecycle.beginRun("client", "old-run", 3, context.Background())
		t.Cleanup(finish)
		if !lifecycle.stop("client", "", 4) {
			t.Fatal("newer unnamed recovery Stop was rejected")
		}
		if !errors.Is(run.Err(), context.Canceled) {
			t.Fatalf("run error = %v, want context.Canceled", run.Err())
		}
	})

	t.Run("older stop cannot cancel a newer run", func(t *testing.T) {
		var lifecycle dataExplorerLifecycle
		run, finish, _ := lifecycle.beginRun("client", "new-run", 5, context.Background())
		t.Cleanup(finish)
		if lifecycle.stop("client", "", 4) {
			t.Fatal("older unnamed Stop cancelled the newer run")
		}
		if run.Err() != nil {
			t.Fatalf("older unnamed Stop cancelled current run: %v", run.Err())
		}
	})
}

func TestDataExplorerActionPrefersNestedAuthoredAction(t *testing.T) {
	outer := "run"
	nested := "configure"
	command := projectsignals.DataExplorerCommand{Action: &outer, Explore: &projectsignals.DataExploreCommand{Action: &nested}}
	if got := dataExplorerAction(command); got != "configure" {
		t.Fatalf("resolved action = %q, want nested configure", got)
	}
}

func TestDataExplorerClientIdentityFailsClosed(t *testing.T) {
	h := &BrowserHandler{}
	req := httptest.NewRequest("POST", "/explore/command", nil)
	if got := h.dataExplorerClientKey(req, "project:test", projectsignals.DataExplorerCommand{RunID: projectsignals.Optional("run-1")}); got != "" {
		t.Fatalf("missing client identity resolved to %q", got)
	}
	req.AddCookie(&http.Cookie{Name: "pagestream_client_id", Value: "cookie-client"})
	if got := h.dataExplorerClientKey(req, "project:test", projectsignals.DataExplorerCommand{}); got != "" {
		t.Fatalf("page-stream cookie incorrectly substituted for per-tab identity: %q", got)
	}
}

func TestDataExplorerResponseLeaseSerializesCurrentCheckAndEmission(t *testing.T) {
	h, req, key, clientID := newDataExplorerLeaseFixture("lease-client", false)
	command := projectsignals.DataExplorerCommand{ClientID: projectsignals.Optional(clientID), RequestSeq: 1}
	if key == "" || !h.dataExplorerLifecycle.acceptSemantic(key, 1) {
		t.Fatal("failed to establish lifecycle state")
	}
	unlock, ok := h.dataExplorerResponseLease(req, command)
	if !ok {
		t.Fatal("current response did not acquire emission lease")
	}
	advanced := make(chan struct{})
	go func() {
		h.dataExplorerLifecycle.acceptSemantic(key, 2)
		close(advanced)
	}()
	select {
	case <-advanced:
		t.Fatal("newer lifecycle state advanced before the current response was emitted")
	case <-time.After(10 * time.Millisecond):
	}
	unlock()
	select {
	case <-advanced:
	case <-time.After(time.Second):
		t.Fatal("newer lifecycle state remained blocked after response emission")
	}
}

func TestDataExplorerLifecycleUsesRunIdentityForSameSequence(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	first, finishFirst, firstID := lifecycle.beginRun("client", "", 7, context.Background())
	second, finishSecond, secondID := lifecycle.beginRun("client", "", 7, context.Background())
	t.Cleanup(finishFirst)
	t.Cleanup(finishSecond)
	if firstID == secondID {
		t.Fatalf("same-sequence runs reused identity %q", firstID)
	}
	if lifecycle.currentRun("client", 7, firstID) {
		t.Fatal("older same-sequence run remained current")
	}
	if !lifecycle.currentRun("client", 7, secondID) || second.Err() != nil {
		t.Fatal("newer same-sequence run was not current")
	}
	if first.Err() == nil {
		t.Fatal("starting same-sequence run did not cancel the older run")
	}
}

func TestDataExplorerLifecycleStopCanWinUnacceptedConfigureRace(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	run, finish, runID := lifecycle.beginRun("client", "run-1", 3, context.Background())
	t.Cleanup(finish)
	// The browser has advanced its draft sequence, but the configure request
	// has not reached acceptSemantic yet. A named Stop may still cancel the run.
	if !lifecycle.stop("client", runID, 4) {
		t.Fatal("stop did not cancel the named run during the configure race")
	}
	if !errors.Is(run.Err(), context.Canceled) {
		t.Fatalf("run error = %v, want context.Canceled", run.Err())
	}
}

type cancellationIgnoringSemanticExecutor struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (e *cancellationIgnoringSemanticExecutor) ExecuteDataQuery(ctx context.Context, _ dataquery.Query) (dataquery.Result, error) {
	close(e.started)
	<-ctx.Done()
	close(e.canceled)
	<-e.release
	return dataquery.Result{Rows: []dataquery.Row{{"orders.status": "late"}}}, nil
}

func TestDataExplorerNewerConfigureCancelsRunAndRejectsLateResponse(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	executor := &cancellationIgnoringSemanticExecutor{
		started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{}),
	}
	h.QueryExecutor = executor
	clientID := "configure-cancels-run-client"
	datasetID := "orders"
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID,
		Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
		Metrics:    []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{},
		Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	request := httptest.NewRequest("POST", "/explore/command", nil)
	request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	runCommand := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("run"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 1, RunID: projectsignals.Optional("run-1"),
		Explore: &projectsignals.DataExploreCommand{Action: projectsignals.Optional("run"), RequestSeq: 1, Spec: spec},
	}
	configureCommand := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 2,
		Explore: &projectsignals.DataExploreCommand{Action: projectsignals.Optional("configure"), RequestSeq: 2, Spec: spec},
	}
	firstDone := make(chan struct{})
	var oldRun projectsignals.DataExplorerSignal
	var oldRunOK bool
	go func() {
		_, oldRun, oldRunOK = h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, runCommand)
		close(firstDone)
	}()
	select {
	case <-executor.started:
	case <-time.After(time.Second):
		t.Fatal("semantic run executor did not start")
	}

	var releaseOnce sync.Once
	releaseRun := func() { releaseOnce.Do(func() { close(executor.release) }) }
	defer releaseRun()
	if _, configured, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, configureCommand); !ok {
		t.Fatal("newer configure request failed to project state")
	} else if configured.Explore.Status.State != "stale" {
		t.Fatalf("configure status = %#v, want stale", configured.Explore.Status)
	}
	select {
	case <-executor.canceled:
	case <-time.After(time.Second):
		t.Fatal("newer configure did not cancel the semantic run context")
	}
	releaseRun()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("cancellation-ignoring semantic run did not finish after release")
	}
	if !oldRunOK {
		t.Fatal("old semantic run failed to project its late response")
	}
	if oldRun.Explore.Status.State != "cancelled" {
		t.Fatalf("late run status = %#v, want cancelled", oldRun.Explore.Status)
	}
	if unlock, current := h.dataExplorerResponseLease(request, oldRun.Command); current {
		unlock()
		t.Fatal("late old run acquired the response lease after newer configure")
	}
}

func TestDataExplorerResponseLeaseRejectsLateStoppedRun(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("stop-late-response-client", true)

	_, finish, runID := h.dataExplorerLifecycle.beginRun(key, "run-1", 1, context.Background())
	t.Cleanup(finish)
	if !h.dataExplorerLifecycle.stop(key, runID, 1) {
		t.Fatal("stop for the current run was rejected")
	}

	lateRun := projectsignals.DataExplorerCommand{
		Action:     projectsignals.Optional("run"),
		ClientID:   projectsignals.Optional(clientID),
		RequestSeq: 1,
		RunID:      projectsignals.Optional(runID),
	}
	if h.dataExplorerLifecycle.currentRun(key, 1, runID) {
		t.Fatal("stopped run remained current after cancellation")
	}
	if unlock, current := h.dataExplorerResponseLease(request, lateRun); current {
		unlock()
		t.Fatal("late stopped-run response acquired the emission lease")
	}

	stopCommand := projectsignals.DataExplorerCommand{
		Action:     projectsignals.Optional("stop"),
		ClientID:   projectsignals.Optional(clientID),
		RequestSeq: 1,
		RunID:      projectsignals.Optional(runID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, stopCommand); !current {
		t.Fatal("stop response lost its emission lease")
	} else {
		unlock()
	}
}

func TestDataExplorerResponseLeaseNewerRunSupersedesStop(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("stop-newer-run-client", true)

	_, finishOld, oldRunID := h.dataExplorerLifecycle.beginRun(key, "run-1", 1, context.Background())
	t.Cleanup(finishOld)
	if !h.dataExplorerLifecycle.stop(key, oldRunID, 1) {
		t.Fatal("stop for the current run was rejected")
	}
	_, finishNew, newRunID := h.dataExplorerLifecycle.beginRun(key, "run-2", 2, context.Background())
	t.Cleanup(finishNew)

	oldStop := projectsignals.DataExplorerCommand{
		Action:     projectsignals.Optional("stop"),
		ClientID:   projectsignals.Optional(clientID),
		RequestSeq: 1,
		RunID:      projectsignals.Optional(oldRunID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, oldStop); current {
		unlock()
		t.Fatal("stop response for an older run superseded the newer run")
	}
	newRun := projectsignals.DataExplorerCommand{
		Action:     projectsignals.Optional("run"),
		ClientID:   projectsignals.Optional(clientID),
		RequestSeq: 2,
		RunID:      projectsignals.Optional(newRunID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, newRun); !current {
		t.Fatal("newer run lost the emission lease after stop")
	} else {
		unlock()
	}
}

func TestDataExplorerResponseLeaseBindsUnnamedStopToRequestSequence(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("unnamed-stop-sequence-lease-client", true)

	_, finishA, _ := h.dataExplorerLifecycle.beginRun(key, "run-a", 7, context.Background())
	t.Cleanup(finishA)
	stopA := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID), RequestSeq: 7,
	}
	if !h.dataExplorerLifecycle.stop(key, "", 7) {
		t.Fatal("unnamed Stop A at sequence 7 was rejected")
	}

	_, finishB, _ := h.dataExplorerLifecycle.beginRun(key, "run-b", 8, context.Background())
	t.Cleanup(finishB)
	stopB := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID), RequestSeq: 8,
	}
	if !h.dataExplorerLifecycle.stop(key, "", 8) {
		t.Fatal("unnamed Stop B at sequence 8 was rejected")
	}

	if unlock, current := h.dataExplorerResponseLease(request, stopA); current {
		unlock()
		t.Fatal("delayed Stop A response acquired Stop B's tombstone lease")
	}
	if unlock, current := h.dataExplorerResponseLease(request, stopB); !current {
		t.Fatal("current Stop B response lost its tombstone lease")
	} else {
		unlock()
	}
}

func TestDataExplorerResponseLeaseAllowsLegacyNamedStopWithoutSequence(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("legacy-named-stop-lease-client", true)
	_, finish, runID := h.dataExplorerLifecycle.beginRun(key, "legacy-run", 0, context.Background())
	t.Cleanup(finish)
	if !h.dataExplorerLifecycle.stop(key, runID, 0) {
		t.Fatal("legacy named Stop without a sequence was rejected")
	}

	namedStop := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID), RunID: projectsignals.Optional(runID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, namedStop); !current {
		t.Fatal("matching legacy named Stop response lost its lease")
	} else {
		unlock()
	}
	unnamedStop := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, unnamedStop); current {
		unlock()
		t.Fatal("sequence-less unnamed Stop response resolved to a tombstone")
	}
}

func TestDataExplorerResponseLeaseRejectsStopSupersededByConfigure(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("stop-configure-lease-client", true)
	_, finish, runID := h.dataExplorerLifecycle.beginRun(key, "run-7", 7, context.Background())
	t.Cleanup(finish)
	stop := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
		RequestSeq: 7, RunID: projectsignals.Optional(runID),
	}
	if !h.dataExplorerLifecycle.stop(key, runID, 7) {
		t.Fatal("named Stop at sequence 7 was rejected")
	}
	if !h.dataExplorerLifecycle.acceptSemantic(key, 8) {
		t.Fatal("Configure at sequence 8 was rejected")
	}
	configure := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID), RequestSeq: 8,
	}
	if unlock, current := h.dataExplorerResponseLease(request, configure); !current {
		t.Fatal("current Configure response lost its emission lease")
	} else {
		unlock()
	}
	if unlock, current := h.dataExplorerResponseLease(request, stop); current {
		unlock()
		t.Fatal("delayed named Stop at sequence 7 passed after Configure 8")
	}
}

func TestDataExplorerResponseLeaseAllowsOnlyCurrentStopWithoutActiveRun(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("no-active-stop-lease-client", true)
	if !h.dataExplorerLifecycle.acceptSemantic(key, 8) {
		t.Fatal("current semantic sequence 8 was rejected")
	}
	if h.dataExplorerLifecycle.stop(key, "", 8) {
		t.Fatal("Stop without an active run was accepted as a cancellation")
	}
	currentStop := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID), RequestSeq: 8,
	}
	if unlock, current := h.dataExplorerResponseLease(request, currentStop); !current {
		t.Fatal("current no-active Stop response could not emit its no-running status")
	} else {
		unlock()
	}
	staleStop := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID), RequestSeq: 7,
	}
	if unlock, current := h.dataExplorerResponseLease(request, staleStop); current {
		unlock()
		t.Fatal("stale no-active Stop response acquired an emission lease")
	}
	sequenceLess := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, sequenceLess); current {
		unlock()
		t.Fatal("sequence-less unnamed no-active Stop was not fail-closed")
	}
}

func TestDataExplorerNoActiveStopAdvancesSequenceBeforeResponseLease(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	clientID := "no-active-stop-advances-sequence-client"
	request := httptest.NewRequest("POST", "/explore/command", nil)
	request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	key := h.dataExplorerClientKey(request, "project:test", projectsignals.DataExplorerCommand{ClientID: projectsignals.Optional(clientID)})
	if !h.dataExplorerLifecycle.acceptSemantic(key, 7) {
		t.Fatal("semantic sequence 7 was rejected")
	}
	datasetID := "orders"
	command := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 8,
		Explore: &projectsignals.DataExploreCommand{
			Action: projectsignals.Optional("stop"), RequestSeq: 8,
			Spec: exploration.ExplorationSpec{
				SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID,
				Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
				Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
			},
		},
	}
	_, explorer, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, command)
	if !ok {
		t.Fatal("no-active Stop failed to project its current status")
	}
	if explorer.Explore.Status.State != "cancelled" || projectsignals.ValueOrZero(explorer.Explore.Status.Message) != "no exploration is running" {
		t.Fatalf("no-active Stop status = %#v, want current no-running acknowledgement", explorer.Explore.Status)
	}
	h.dataExplorerLifecycle.mu.Lock()
	latest := h.dataExplorerLifecycle.latest[key]
	h.dataExplorerLifecycle.mu.Unlock()
	if latest != 8 {
		t.Fatalf("latest semantic sequence = %d, want Stop sequence 8", latest)
	}
	if unlock, current := h.dataExplorerResponseLease(request, explorer.Command); !current {
		t.Fatal("current no-active Stop lost its response lease after advancing the semantic sequence")
	} else {
		unlock()
	}
}

func TestDataExplorerResponseLeaseAllowsCurrentNoActiveStopAfterOlderTombstone(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("older-stop-tombstone-no-active-client", true)
	_, finish, runID := h.dataExplorerLifecycle.beginRun(key, "old-run", 7, context.Background())
	if !h.dataExplorerLifecycle.stop(key, runID, 7) {
		t.Fatal("Stop A at sequence 7 was rejected")
	}
	finish()
	if !h.dataExplorerLifecycle.acceptSemantic(key, 8) {
		t.Fatal("current semantic sequence 8 was rejected")
	}
	if h.dataExplorerLifecycle.stop(key, "", 8) {
		t.Fatal("Stop B was accepted as a cancellation without an active run")
	}
	stopB := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID), RequestSeq: 8,
	}
	if unlock, current := h.dataExplorerResponseLease(request, stopB); !current {
		t.Fatal("current no-active Stop B could not lease its no-running response after an older tombstone")
	} else {
		unlock()
	}
	stopA := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
		RequestSeq: 7, RunID: projectsignals.Optional(runID),
	}
	if unlock, current := h.dataExplorerResponseLease(request, stopA); current {
		unlock()
		t.Fatal("older tombstoned Stop A passed after semantic sequence 8")
	}
}

func TestDataExplorerResponseLeaseAllowsConfigureWithoutRunIDAfterStop(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("stop-configure-client", true)

	_, finish, runID := h.dataExplorerLifecycle.beginRun(key, "run-1", 1, context.Background())
	t.Cleanup(finish)
	if !h.dataExplorerLifecycle.stop(key, runID, 1) {
		t.Fatal("stop for the current run was rejected")
	}
	if !h.dataExplorerLifecycle.acceptSemantic(key, 2) {
		t.Fatal("higher-sequence configure request was rejected")
	}

	configure := projectsignals.DataExplorerCommand{
		Action:     projectsignals.Optional("configure"),
		ClientID:   projectsignals.Optional(clientID),
		RequestSeq: 2,
	}
	if unlock, current := h.dataExplorerResponseLease(request, configure); !current {
		t.Fatal("configure response without a run ID lost the emission lease after stop")
	} else {
		unlock()
	}
}

func TestDataExplorerLifecycleSuggestionLaneDoesNotCancelSemanticRun(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	semantic, finishSemantic, semanticID := lifecycle.beginRun("client", "run-1", 10, context.Background())
	suggestion, finishSuggestion, suggestionID := lifecycle.beginSuggestions("client", "", 11, context.Background())
	t.Cleanup(finishSemantic)
	t.Cleanup(finishSuggestion)
	if semantic.Err() != nil {
		t.Fatalf("suggestion start cancelled semantic run: %v", semantic.Err())
	}
	if !lifecycle.currentRun("client", 10, semanticID) {
		t.Fatal("semantic run became stale while suggestions were loading")
	}
	if !lifecycle.currentSuggestions("client", 11, suggestionID) || suggestion.Err() != nil {
		t.Fatal("suggestion lane did not remain current")
	}
}

func TestDataExplorerLifecycleSuppressesOlderSuggestionWithSameQuerySequence(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	// Both suggestions belong to the same authored exploration request. Their
	// lane sequence is independent, so an executor that ignores cancellation
	// still cannot publish the older response as current.
	first, finishFirst, firstID := lifecycle.beginSuggestions("client", "suggestion-1", 1, context.Background())
	second, finishSecond, secondID := lifecycle.beginSuggestions("client", "suggestion-2", 2, context.Background())
	t.Cleanup(finishFirst)
	t.Cleanup(finishSecond)
	if first.Err() == nil {
		t.Fatal("starting a newer suggestion did not request cancellation of the older one")
	}
	if lifecycle.currentSuggestions("client", 1, firstID) {
		t.Fatal("older same-query suggestion remained current")
	}
	if !lifecycle.currentSuggestions("client", 2, secondID) || second.Err() != nil {
		t.Fatal("newer same-query suggestion was not current")
	}
}

type cancellationIgnoringSuggestionExecutor struct {
	firstStarted chan struct{}
	releaseFirst chan struct{}
	mu           sync.Mutex
	calls        int
}

func (e *cancellationIgnoringSuggestionExecutor) ExecuteDataQuery(_ context.Context, _ dataquery.Query) (dataquery.Result, error) {
	e.mu.Lock()
	e.calls++
	call := e.calls
	e.mu.Unlock()
	if call == 1 {
		close(e.firstStarted)
		<-e.releaseFirst
	}
	return dataquery.Result{Rows: []dataquery.Row{{"orders.status": "paid"}}}, nil
}

func TestDataExplorerSuppressesOlderSuggestionExecutorResponse(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	executor := &cancellationIgnoringSuggestionExecutor{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	h.QueryExecutor = executor
	clientID := "suggestion-overlap-client"
	makeCommand := func(suggestionSeq int64) projectsignals.DataExplorerCommand {
		datasetID := "orders"
		search := ""
		return projectsignals.DataExplorerCommand{
			Action:     projectsignals.Optional("configure"),
			ClientID:   projectsignals.Optional(clientID),
			Mode:       projectsignals.Optional("explore"),
			RequestSeq: 42,
			Explore: &projectsignals.DataExploreCommand{
				Action:     projectsignals.Optional("configure"),
				Spec:       exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID, Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
				RequestSeq: 42,
				FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{
					Field: "orders.status", Search: &search, SuggestionRequestSeq: suggestionSeq,
				},
			},
		}
	}
	request := httptest.NewRequest("POST", "/explore/command", nil)
	request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	firstDone := make(chan struct{})
	var first projectsignals.DataExplorerSignal
	var firstOK bool
	go func() {
		_, first, firstOK = h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, makeCommand(1))
		close(firstDone)
	}()
	select {
	case <-executor.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first suggestion executor did not start")
	}
	_, second, secondOK := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, makeCommand(2))
	close(executor.releaseFirst)
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("cancellation-ignoring first suggestion did not finish")
	}
	if !firstOK || !secondOK {
		t.Fatal("overlapping suggestion requests failed to project state")
	}
	if first.Explore.FilterSuggestions == nil || !first.Explore.FilterSuggestions.Stale {
		t.Fatalf("older suggestion response = %#v, want stale", first.Explore.FilterSuggestions)
	}
	if second.Explore.FilterSuggestions == nil || second.Explore.FilterSuggestions.Stale {
		t.Fatalf("newer suggestion response = %#v, want current", second.Explore.FilterSuggestions)
	}
	if unlock, current := h.dataExplorerResponseLease(request, first.Command); current {
		unlock()
		t.Fatal("older suggestion response acquired the emission lease")
	}
}

func TestDataExplorerResponseLeaseRejectsSuggestionAfterNewerConfigure(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	executor := &cancellationIgnoringSuggestionExecutor{firstStarted: make(chan struct{}), releaseFirst: make(chan struct{})}
	h.QueryExecutor = executor
	clientID := "suggestion-configure-race-client"
	datasetID := "orders"
	search := ""
	spec := exploration.ExplorationSpec{
		SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID,
		Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
		Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
	}
	suggestionCommand := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 41,
		Explore: &projectsignals.DataExploreCommand{
			Action: projectsignals.Optional("configure"), RequestSeq: 41, Spec: spec,
			FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{
				Field: "orders.status", Search: &search, SuggestionRequestSeq: 1,
			},
		},
	}
	configureCommand := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 42,
		Explore: &projectsignals.DataExploreCommand{Action: projectsignals.Optional("configure"), RequestSeq: 42, Spec: spec},
	}
	request := httptest.NewRequest("POST", "/explore/command", nil)
	request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	firstDone := make(chan struct{})
	var first projectsignals.DataExplorerSignal
	var firstOK bool
	go func() {
		_, first, firstOK = h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, suggestionCommand)
		close(firstDone)
	}()
	select {
	case <-executor.firstStarted:
	case <-time.After(time.Second):
		t.Fatal("suggestion executor did not start")
	}

	var releaseOnce sync.Once
	releaseSuggestion := func() { releaseOnce.Do(func() { close(executor.releaseFirst) }) }
	defer releaseSuggestion()
	if _, _, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, configureCommand); !ok {
		t.Fatal("newer configure request failed to project state")
	}
	releaseSuggestion()
	select {
	case <-firstDone:
	case <-time.After(time.Second):
		t.Fatal("cancellation-ignoring suggestion did not finish")
	}
	if !firstOK {
		t.Fatal("suggestion request failed to project state")
	}
	if first.Command.RequestSeq != 41 || first.Command.Explore == nil || first.Command.Explore.FilterSuggestions == nil || first.Command.Explore.FilterSuggestions.SuggestionRequestSeq != 1 {
		t.Fatalf("late suggestion command = %#v, want semantic sequence 41 and current token 1", first.Command)
	}
	key := h.dataExplorerClientKey(request, "project:test", suggestionCommand)
	if !h.dataExplorerLifecycle.currentSuggestions(key, 1, "") {
		t.Fatal("suggestion token stopped being current after configure")
	}
	if unlock, current := h.dataExplorerResponseLease(request, first.Command); current {
		unlock()
		t.Fatal("older-semantic-sequence suggestion acquired the emission lease")
	}
}

func TestDataExplorerResponseLeaseAllowsCurrentSuggestionDuringSemanticRun(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("current-suggestion-active-run-client", true)
	command := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID),
		Mode: projectsignals.Optional("explore"), RequestSeq: 42,
		Explore: &projectsignals.DataExploreCommand{
			Action: projectsignals.Optional("configure"), RequestSeq: 42,
			FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status", SuggestionRequestSeq: 1},
		},
	}
	semantic, finishSemantic, _ := h.dataExplorerLifecycle.beginRun(key, "semantic-run", 42, context.Background())
	t.Cleanup(finishSemantic)
	if accepted, _ := h.dataExplorerLifecycle.acceptSuggestions(key, 1); !accepted {
		t.Fatal("current suggestion token was rejected")
	}
	suggestion, finishSuggestion, _ := h.dataExplorerLifecycle.beginSuggestions(key, "suggestion-run", 1, context.Background())
	t.Cleanup(finishSuggestion)
	if semantic.Err() != nil {
		t.Fatalf("starting the suggestion lane cancelled semantic run: %v", semantic.Err())
	}
	if suggestion.Err() != nil {
		t.Fatalf("current suggestion lane was cancelled: %v", suggestion.Err())
	}
	unlock, current := h.dataExplorerResponseLease(request, command)
	if !current {
		t.Fatal("current-sequence suggestion lost its emission lease during semantic run")
	}
	unlock()
	if semantic.Err() != nil {
		t.Fatalf("leasing the suggestion response cancelled semantic run: %v", semantic.Err())
	}
	if suggestion.Err() != nil {
		t.Fatalf("leasing the suggestion response cancelled its lane: %v", suggestion.Err())
	}
}

func TestDataExplorerSuggestionSequenceIsStrictlyPositiveAndMonotonic(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	if accepted, _ := lifecycle.acceptSuggestions("client", 1); !accepted {
		t.Fatal("first suggestion sequence was rejected")
	}
	// Equal tokens can be reused by a caller for a different search value, so
	// they must not be allowed to race as if they were distinct requests.
	if accepted, _ := lifecycle.acceptSuggestions("client", 1); accepted {
		t.Fatal("duplicate suggestion sequence was accepted")
	}
	if accepted, _ := lifecycle.acceptSuggestions("client", 0); accepted {
		t.Fatal("non-positive suggestion sequence was accepted")
	}
	if accepted, _ := lifecycle.acceptSuggestions("client", dataExplorerMaxSuggestionRequestSeq+1); accepted {
		t.Fatal("unsafe suggestion sequence was accepted")
	}
	if accepted, _ := lifecycle.acceptSuggestions("client", 2); !accepted {
		t.Fatal("newer suggestion sequence was rejected")
	}
}

func TestDataExplorerDuplicateSuggestionTokenCannotEmitPatch(t *testing.T) {
	h, _ := newDataExplorerURLTestHandler(t)
	clientID := "duplicate-suggestion-client"
	makeCommand := func(search string) projectsignals.DataExplorerCommand {
		datasetID := "orders"
		return projectsignals.DataExplorerCommand{
			Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID), Mode: projectsignals.Optional("explore"), RequestSeq: 7,
			Explore: &projectsignals.DataExploreCommand{
				Action: projectsignals.Optional("configure"), RequestSeq: 7,
				Spec:              exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID, Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
				FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status", Search: &search, SuggestionRequestSeq: 1},
			},
		}
	}
	request := httptest.NewRequest("POST", "/explore/command", nil)
	request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	if _, _, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, makeCommand("paid")); !ok {
		t.Fatal("initial suggestion request failed")
	}
	_, duplicate, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, makeCommand("pending"))
	if !ok {
		t.Fatal("duplicate suggestion request failed to project state")
	}
	if duplicate.Command.Explore == nil || duplicate.Command.Explore.FilterSuggestions == nil || duplicate.Command.Explore.FilterSuggestions.SuggestionRequestSeq != 0 {
		t.Fatalf("duplicate suggestion command = %#v, want invalidated token", duplicate.Command.Explore)
	}
	if unlock, current := h.dataExplorerResponseLease(request, duplicate.Command); current {
		unlock()
		t.Fatal("duplicate suggestion acquired an emission lease")
	}
}

func TestDataExplorerSuggestionOnlyPreservesSemanticStatus(t *testing.T) {
	h, executor := newDataExplorerURLTestHandler(t)
	clientID := "suggestion-only-status-client"
	datasetID := "orders"
	search := ""
	command := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("configure"), ClientID: projectsignals.Optional(clientID), Mode: projectsignals.Optional("explore"), RequestSeq: 1,
		Explore: &projectsignals.DataExploreCommand{
			Action: projectsignals.Optional("configure"), RequestSeq: 1,
			Spec: exploration.ExplorationSpec{
				SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &datasetID,
				Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{},
				Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100,
			},
			FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status", Search: &search, SuggestionRequestSeq: 1},
		},
	}
	request := httptest.NewRequest("POST", "/explore/command", nil)
	request.Header.Set("X-LeapView-Data-Explorer-Client", clientID)
	_, explorer, ok := h.dataExplorerSignalsForCommand(httptest.NewRecorder(), request, command)
	if !ok {
		t.Fatal("suggestion-only command failed to project state")
	}
	if executor.calls != 1 {
		t.Fatalf("suggestion-only command executed %d queries, want one suggestion query; error=%q signal=%#v command=%#v", executor.calls, projectsignals.ValueOrZero(explorer.Explore.FilterSuggestions.Error), explorer.Explore.FilterSuggestions, explorer.Explore.Command)
	}
	status := explorer.Explore.Status
	if status.Loading || status.State == "loading" {
		t.Fatalf("suggestion-only semantic status = %#v, must not be loading", status)
	}
	if status.State != "stale" || !status.Stale {
		t.Fatalf("suggestion-only semantic status = %#v, want stale configured state", status)
	}
	if explorer.Explore.FilterSuggestions == nil || explorer.Explore.FilterSuggestions.Loading {
		t.Fatalf("suggestion-only filter suggestion signal = %#v, want terminal signal", explorer.Explore.FilterSuggestions)
	}
}

func TestLowerSuggestionFiltersIncludesTimeRangeExactlyOnce(t *testing.T) {
	timeField := "orders.created_at"
	fields := map[string]projectsignals.DataExploreFieldSignal{
		"orders.status": {ID: "orders.status", Kind: "dimension", Compatible: true},
		"orders.region": {ID: "orders.region", Kind: "dimension", Compatible: true},
	}
	timeRange := &exploration.ExplorationTimeRange{Value: &exploration.AbsoluteExplorationTimeRange{
		Kind: "absolute",
		Lower: &exploration.ExplorationTimeBound{
			Inclusive: true,
			Value:     exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: "2026-01-01T00:00:00Z"}},
		},
		Upper: &exploration.ExplorationTimeBound{
			Inclusive: false,
			Value:     exploration.ExplorationTemporalValue{Value: &exploration.TimestampExplorationTemporalValue{Kind: "timestamp", Value: "2026-02-01T00:00:00Z"}},
		},
	}}
	tests := []struct {
		name    string
		filters []exploration.ExplorationFilter
	}{
		{name: "no retained filters"},
		{name: "multiple retained filters", filters: []exploration.ExplorationFilter{
			lifecycleTestStringFilter("orders.region", "equals", "CA"),
			lifecycleTestStringFilter("orders.region", "equals", "NY"),
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			filters, err := lowerSuggestionFilters(exploration.ExplorationSpec{
				Filters: test.filters,
				Time:    &exploration.ExplorationTimeSelection{Field: timeField, Range: timeRange},
			}, fields, "orders.status", "", testDataExplorerQueryLowerer)
			if err != nil {
				t.Fatalf("lower suggestion filters: %v", err)
			}
			if len(filters) != len(test.filters)+2 {
				t.Fatalf("lowered filters = %#v, want %d predicates", filters, len(test.filters)+2)
			}
			timePredicates := 0
			for _, filter := range filters {
				if filter.Field == timeField {
					timePredicates++
				}
			}
			if timePredicates != 2 {
				t.Fatalf("time predicates = %d in %#v, want exactly one lower/upper range", timePredicates, filters)
			}
		})
	}
}

func TestDataExplorerLifecyclePruningRetainsCurrentIdentity(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	for index := 0; index < dataExplorerLifecycleMaxIdentities; index++ {
		if !lifecycle.acceptSemantic(fmt.Sprintf("old-%d", index), 1) {
			t.Fatalf("old identity %d was rejected", index)
		}
	}
	old, finishOld, oldID := lifecycle.beginRun("current", "old-run", 1, context.Background())
	newer, finishNew, newerID := lifecycle.beginRun("current", "new-run", 2, context.Background())
	if old.Err() == nil || newer.Err() != nil {
		t.Fatalf("run cancellation = old:%v newer:%v", old.Err(), newer.Err())
	}
	finishNew()
	finishOld()
	if lifecycle.latest["current"] != 2 || lifecycle.latestRun["current"] != newerID {
		t.Fatalf("current lifecycle state = seq:%d run:%q, want seq 2/new run %q", lifecycle.latest["current"], lifecycle.latestRun["current"], newerID)
	}
	if lifecycle.currentRun("current", 1, oldID) {
		t.Fatal("delayed old response became current after pruning")
	}
	if len(lifecycle.identityAccess) > dataExplorerLifecycleMaxIdentities {
		t.Fatalf("identity map size = %d, want at most %d", len(lifecycle.identityAccess), dataExplorerLifecycleMaxIdentities)
	}
}

func TestDataExplorerLifecycleIdentityMapsAreBounded(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	for index := 0; index < dataExplorerLifecycleMaxIdentities*4; index++ {
		key := string(rune('a'+index%26)) + string(rune(index/26))
		if !lifecycle.acceptSemantic(key, 1) {
			t.Fatalf("semantic request %q was rejected", key)
		}
		if accepted, _ := lifecycle.acceptSuggestions(key, 1); !accepted {
			t.Fatalf("suggestion request %q was rejected", key)
		}
		_, finishRun, _ := lifecycle.beginRun(key, "run-"+key, 1, context.Background())
		if !lifecycle.stop(key, "run-"+key, 1) {
			t.Fatalf("stop request %q was rejected", key)
		}
		finishRun()
		_, finishSuggestion, _ := lifecycle.beginSuggestions(key, "suggestion-"+key, 1, context.Background())
		finishSuggestion()
	}
	for name, size := range map[string]int{
		"latest":            len(lifecycle.latest),
		"suggestionsLatest": len(lifecycle.suggestionsLatest),
		"latestRun":         len(lifecycle.latestRun),
		"suggestionsRun":    len(lifecycle.suggestionsRun),
		"stoppedRun":        len(lifecycle.stoppedRun),
		"stoppedRequestSeq": len(lifecycle.stoppedRequestSeq),
		"identityAccess":    len(lifecycle.identityAccess),
	} {
		if size > dataExplorerLifecycleMaxIdentities {
			t.Fatalf("%s map size = %d, want at most %d", name, size, dataExplorerLifecycleMaxIdentities)
		}
	}
}

type dataExplorerSuggestionExecutor struct {
	query  dataquery.Query
	result dataquery.Result
	calls  int
}

func lifecycleTestStringFilter(field, operator string, values ...string) exploration.ExplorationFilter {
	items := make([]exploration.ExplorationFilterValue, 0, len(values))
	for _, value := range values {
		items = append(items, exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: value}})
	}
	var expression exploration.ExplorationFilterExpressionVariant
	switch operator {
	case "is_null", "is_not_null":
		expression = &exploration.NullCheckExplorationFilterExpression{Kind: "null_check", Operator: operator}
	case "in", "not_in":
		expression = &exploration.SetExplorationFilterExpression{Kind: "set", Operator: operator, Values: items}
	default:
		expression = &exploration.ComparisonExplorationFilterExpression{Kind: "comparison", Operator: operator, Value: items[0]}
	}
	return exploration.ExplorationFilter{Field: field, Expression: exploration.ExplorationFilterExpression{Value: expression}}
}

func (e *dataExplorerSuggestionExecutor) ExecuteDataQuery(_ context.Context, query dataquery.Query) (dataquery.Result, error) {
	e.calls++
	e.query = query
	return e.result, nil
}

func TestDataExplorerFilterSuggestionsAreBoundedTypedAndDeterministic(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{
				"order": {Type: "primary", Fields: []string{"status"}},
			}, Dimensions: map[string]semanticmodel.MetricDimension{
				"status": {Type: "string"},
			}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	limit := int64(2)
	dataset := "orders"
	search := "pa"
	suggestionSeq := int64(4)
	executor := &dataExplorerSuggestionExecutor{result: dataquery.Result{Rows: []dataquery.Row{
		{"orders.status": "z"}, {"orders.status": "a"}, {"orders.status": "z"},
	}}}
	signal := dataExplorerFilterSuggestions(t.Context(), executor, testDataExplorerQueryLowerer, projectgraph.ResourceID("project:test"), projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
			Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
		RequestSeq:        9,
		FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status", Limit: &limit, Search: &search, SuggestionRequestSeq: suggestionSeq},
	}, []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", DatasetID: "orders", Compatible: true, Type: projectsignals.Optional("string")}}, mustCompileDataExplorerModel(t, model))
	if signal.Error != nil || signal.Loading || signal.RequestSeq != 9 || signal.SuggestionRequestSeq != suggestionSeq {
		t.Fatalf("suggestion signal = %#v", signal)
	}
	if !signal.Truncated || len(signal.Values) != 2 || signal.Values[0].Label != "a" || signal.Values[1].Label != "z" {
		t.Fatalf("suggestion values = %#v, want sorted bounded values", signal.Values)
	}
	if executor.query.Limit != 3 || executor.query.Operation != dataquery.OperationDataExploreFilterSuggestions || executor.query.ProjectID != "project:test" {
		t.Fatalf("suggestion query = %#v", executor.query)
	}
	if len(executor.query.Filters) != 1 || executor.query.Filters[0].Operator != "contains" || executor.query.Filters[0].Values[0] != "pa" {
		t.Fatalf("suggestion search filter = %#v", executor.query.Filters)
	}
}

func TestDataExplorerFilterSuggestionsRejectInvalidTypedSearchWithoutExecution(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{
			"order": {Type: "primary", Fields: []string{"order_date"}},
		}, Dimensions: map[string]semanticmodel.MetricDimension{"order_date": {Type: "date"}}}},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}},
	}
	executor := &dataExplorerSuggestionExecutor{}
	dataset := "orders"
	search := "2026-02-30"
	signal := dataExplorerFilterSuggestions(t.Context(), executor, testDataExplorerQueryLowerer, projectgraph.ResourceID("project:test"), projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
			Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
		FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.order_date", Search: &search},
	}, []projectsignals.DataExploreFieldSignal{{ID: "orders.order_date", Kind: "dimension", DatasetID: "orders", Compatible: true, Type: projectsignals.Optional("date")}}, mustCompileDataExplorerModel(t, model))
	if signal.Error == nil || executor.calls != 0 {
		t.Fatalf("invalid date suggestion = %#v, calls=%d; want validation failure before execution", signal, executor.calls)
	}
}

func TestDataExplorerSemanticRejectsNonParticipatingFilterDataset(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{
				"order": {Type: "primary", Fields: []string{"status"}},
			}, Dimensions: map[string]semanticmodel.MetricDimension{"status": {Type: "string"}}},
			"customers": {ModelName: "customers", GrainEntity: "customer", Entities: map[string]semanticmodel.EntityDefinition{
				"customer": {Type: "primary", Fields: []string{"region"}},
			}, Dimensions: map[string]semanticmodel.MetricDimension{"region": {Type: "string"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
	}
	dataset := "orders"
	filterDataset := "customers"
	filter := exploration.ExplorationFilter{
		Field: "orders.status", DatasetID: &filterDataset,
		Expression: exploration.ExplorationFilterExpression{Value: &exploration.ComparisonExplorationFilterExpression{
			Kind: "comparison", Operator: "equals",
			Value: exploration.ExplorationFilterValue{Value: &exploration.StringExplorationFilterValue{Kind: "string", Value: "paid"}},
		}},
	}
	executor := &dataExplorerSuggestionExecutor{}
	_, result := dataExplorerSemanticResult(t.Context(), executor, testDataExplorerQueryLowerer, "project:test", projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
			Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}}, Filters: []exploration.ExplorationFilter{filter},
			Metrics: []exploration.ExplorationMetricRef{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
	}, []projectsignals.DataExploreFieldSignal{
		{ID: "orders.status", Kind: "dimension", DatasetID: "orders", Compatible: true},
	}, model, mustCompileDataExplorerModel(t, model))
	if result.Error == nil {
		t.Fatal("non-participating explicit filter dataset was accepted")
	}
	if executor.calls != 0 {
		t.Fatalf("unauthorized filter dataset executed %d queries", executor.calls)
	}
}

func TestDataExplorerMultiRootValidationFailsClosedWithoutCompiledModel(t *testing.T) {
	spec := exploration.ExplorationSpec{Metrics: []exploration.ExplorationMetricRef{{Field: "order_share"}}}
	fields := map[string]projectsignals.DataExploreFieldSignal{
		"order_share": {ID: "order_share", Kind: "metric", Compatible: true},
	}
	if err := validateExplorerFilterDatasets(spec, fields, nil); err == nil {
		t.Fatal("multi-root metric validation accepted missing compiled model")
	}
}

func TestDataExplorerSemanticAllowsRelatedPhysicalFilterScopedToRoot(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {
				ModelName:   "orders",
				GrainEntity: "order",
				Entities: map[string]semanticmodel.EntityDefinition{
					"order": {Type: "primary", Fields: []string{"status", "customer_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"status":      {Type: "string"},
					"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
				},
			},
			"customers": {
				ModelName:   "customers",
				GrainEntity: "customer",
				Entities: map[string]semanticmodel.EntityDefinition{
					"customer": {Type: "primary", Fields: []string{"customer_id"}},
				},
				Dimensions: map[string]semanticmodel.MetricDimension{
					"customer_id": {Type: "string", Datatype: semanticmodel.DataTypeString},
					"state":       {Type: "string"},
				},
			},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{
			"orders": {Model: "orders"}, "customers": {Model: "customers"},
		},
		Relationships: []semanticmodel.Relationship{{
			ID: "orders_customers", FromDataset: "orders", FromFields: []string{"customer_id"},
			ToDataset: "customers", ToFields: []string{"customer_id"}, Cardinality: "many_to_one",
		}},
	}
	dataset := "orders"
	filter := lifecycleTestStringFilter("customers.state", "equals", "CA")
	filter.DatasetID = &dataset
	executor := &dataExplorerSuggestionExecutor{result: dataquery.Result{Rows: []dataquery.Row{{"customers.state": "CA"}}}}
	_, result := dataExplorerSemanticResult(t.Context(), executor, testDataExplorerQueryLowerer, "project:test", projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{
			SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
			Dimensions: []exploration.ExplorationDimensionRef{{Field: "orders.status"}},
			Metrics:    []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{filter},
			Sort: []exploration.ExplorationSort{}, Limit: 100,
		},
	}, []projectsignals.DataExploreFieldSignal{
		{ID: "orders.status", Kind: "dimension", DatasetID: "orders", Compatible: true},
		{ID: "customers.state", Kind: "dimension", DatasetID: "customers", Compatible: true},
	}, model, mustCompileDataExplorerModel(t, model))
	if result.Error != nil {
		t.Fatalf("related physical filter rejected during semantic preflight: %v", projectsignals.ValueOrZero(result.Error))
	}
	if executor.calls != 1 {
		t.Fatalf("related physical filter executed %d queries, want exactly 1", executor.calls)
	}
	if len(executor.query.Filters) != 1 || executor.query.Filters[0].Field != "customers.state" || executor.query.Filters[0].Dataset != "orders" {
		t.Fatalf("executor filter = %#v, want customers.state scoped to orders", executor.query.Filters)
	}
}

func TestDataExplorerSuggestionsRejectUnreachableDataset(t *testing.T) {
	model := &semanticmodel.Model{
		Tables: map[string]semanticmodel.Table{
			"orders": {ModelName: "orders", GrainEntity: "order", Entities: map[string]semanticmodel.EntityDefinition{
				"order": {Type: "primary", Fields: []string{"status"}},
			}, Dimensions: map[string]semanticmodel.MetricDimension{"status": {Type: "string"}}},
			"customers": {ModelName: "customers", GrainEntity: "customer", Entities: map[string]semanticmodel.EntityDefinition{
				"customer": {Type: "primary", Fields: []string{"region"}},
			}, Dimensions: map[string]semanticmodel.MetricDimension{"region": {Type: "string"}}},
		},
		Datasets: map[string]semanticmodel.SemanticDatasetSpec{"orders": {Model: "orders"}, "customers": {Model: "customers"}},
	}
	dataset := "customers"
	executor := &dataExplorerSuggestionExecutor{}
	search := "a"
	signal := dataExplorerFilterSuggestions(t.Context(), executor, testDataExplorerQueryLowerer, "project:test", projectsignals.DataExploreCommand{
		Spec: exploration.ExplorationSpec{SchemaVersion: 1, ModelID: "semantic:sales", DatasetID: &dataset,
			Dimensions: []exploration.ExplorationDimensionRef{}, Metrics: []exploration.ExplorationMetricRef{}, Filters: []exploration.ExplorationFilter{}, Sort: []exploration.ExplorationSort{}, Limit: 100},
		FilterSuggestions: &projectsignals.DataExploreFilterSuggestionsCommand{Field: "orders.status", Search: &search},
	}, []projectsignals.DataExploreFieldSignal{{ID: "orders.status", Kind: "dimension", DatasetID: "orders", Compatible: true}}, mustCompileDataExplorerModel(t, model))
	if signal.Error == nil || executor.calls != 0 {
		t.Fatalf("unreachable suggestion dataset signal = %#v, calls=%d", signal, executor.calls)
	}
}

func mustCompileDataExplorerModel(t *testing.T, model *semanticmodel.Model) *semanticquery.CompiledModel {
	t.Helper()
	compiled, err := semanticquery.CompileModel(model)
	if err != nil {
		t.Fatalf("compile semantic model: %v", err)
	}
	return compiled
}

type cancelledDataExplorerExecutor struct{}

func (cancelledDataExplorerExecutor) ExecuteDataQuery(ctx context.Context, _ dataquery.Query) (dataquery.Result, error) {
	return dataquery.Result{}, ctx.Err()
}

func TestDataExplorerPreviewCancellationIsStaleNotError(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	preview := dataExplorerPreview(ctx, cancelledDataExplorerExecutor{}, "project:test", projectsignals.DataExplorerObjectSignal{
		Layer: "model", ResourceID: "model:orders", SemanticModelID: projectsignals.Optional("semantic:sales"), DatasetID: projectsignals.Optional("orders"),
	}, projectsignals.DataExplorerCommand{Count: 10, Limit: 10, Block: projectsignals.Pointer("a")})
	if preview.Loading || !preview.Stale || preview.Error != nil || preview.ProgressPercent != nil {
		t.Fatalf("cancelled preview = %#v; want terminal stale state without error/progress", preview)
	}
}
