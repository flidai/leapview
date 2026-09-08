package http

import (
	"context"
	"fmt"
	stdhttp "net/http"
	"strings"
	"sync"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	projectnavigation "github.com/flidai/leapview/internal/project/navigation"
	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

// dataExplorerLifecycle coordinates the synchronous HTTP command endpoint.
// Datastar may have several command requests in flight at once, so query
// cancellation and sequence checks must live outside an individual request.
// The state is intentionally process-local and ephemeral; it is not a
// substitute for saved exploration persistence or a query job store.
type dataExplorerLifecycle struct {
	mu                sync.Mutex
	active            map[string]*dataExplorerExecution
	suggestionsActive map[string]*dataExplorerExecution
	latest            map[string]int64
	suggestionsLatest map[string]int64
	latestRun         map[string]string
	suggestionsRun    map[string]string
	stoppedRun        map[string]string
	identityAccess    map[string]time.Time
	nextRunID         uint64
}

const dataExplorerLifecycleMaxIdentities = 128

type dataExplorerExecution struct {
	cancel     context.CancelFunc
	requestSeq int64
	runID      string
}

func dataExplorerAction(command projectsignals.DataExplorerCommand) string {
	if command.Explore != nil {
		if action := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(command.Explore.Action))); action != "" {
			// The nested command is the authored exploration action. Prefer it
			// when an outer command carries a stale action from a previous run.
			return action
		}
	}
	if action := strings.ToLower(strings.TrimSpace(projectsignals.ValueOrZero(command.Action))); action != "" {
		return action
	}
	return ""
}

func dataExplorerRequestSeq(command projectsignals.DataExplorerCommand) int64 {
	seq := command.RequestSeq
	if command.Explore != nil && command.Explore.RequestSeq > seq {
		seq = command.Explore.RequestSeq
	}
	return seq
}

func dataExplorerRunID(command projectsignals.DataExplorerCommand) string {
	if runID := strings.TrimSpace(projectsignals.ValueOrZero(command.RunID)); runID != "" {
		return runID
	}
	return ""
}

func dataExplorerSuggestionRequestSeq(command projectsignals.DataExplorerCommand) int64 {
	if command.Explore == nil || command.Explore.FilterSuggestions == nil {
		return 0
	}
	return command.Explore.FilterSuggestions.SuggestionRequestSeq
}

func dataExploreStatus(command projectsignals.DataExploreCommand, result projectsignals.DataExploreResultSignal, action string, executed bool) projectsignals.DataExploreStatusSignal {
	status := projectsignals.DataExploreStatusSignal{RequestSeq: command.RequestSeq, State: "idle"}
	if result.Error != nil && strings.TrimSpace(*result.Error) != "" {
		status.Error = result.Error
		status.State = "error"
		return status
	}
	if action == "stop" {
		status.State = "cancelled"
		status.Message = projectsignals.Pointer("exploration stopped")
		return status
	}
	if action == "configure" {
		status.State = "stale"
		status.Stale = true
		status.Message = projectsignals.Pointer("configuration changed; run the exploration to refresh results")
		return status
	}
	if executed {
		status.State = "success"
		progress := float64(100)
		status.ProgressPercent = &progress
		return status
	}
	if !explorationSpecIsEmpty(command.Spec) {
		status.State = "stale"
		status.Stale = true
	}
	return status
}

func dataExploreLoadingStatus(command projectsignals.DataExploreCommand) projectsignals.DataExploreStatusSignal {
	return projectsignals.DataExploreStatusSignal{Loading: true, RequestSeq: command.RequestSeq, State: "loading"}
}

func (h *BrowserHandler) dataExplorerClientKey(r *stdhttp.Request, projectID projectgraph.ResourceID, command projectsignals.DataExplorerCommand) string {
	client := ""
	if r != nil {
		client = strings.TrimSpace(r.Header.Get("X-LeapView-Data-Explorer-Client"))
		if client == "" {
			client = strings.TrimSpace(r.Header.Get("X-Data-Explorer-Client"))
		}
	}
	if client == "" {
		client = strings.TrimSpace(projectsignals.ValueOrZero(command.ClientID))
	}
	// A request/run ID is not a client identity: using it here would make a
	// run impossible to address after the request that started it. Missing or
	// malformed identities fail closed so two tabs can never share state.
	if client == "" || len(client) > 128 {
		return ""
	}
	principal := "anonymous"
	if h != nil && h.CurrentUser != nil && r != nil {
		if current, ok := h.CurrentUser(r); ok && strings.TrimSpace(current.ID) != "" {
			principal = strings.TrimSpace(current.ID)
		}
	}
	return fmt.Sprintf("%s\x00%s\x00%s", projectID, principal, client)
}

func (h *BrowserHandler) dataExplorerProjectID(r *stdhttp.Request, catalog projectnavigation.Catalog) projectgraph.ResourceID {
	if projectID, err := projectgraph.NewResourceID(strings.TrimSpace(catalog.Project.ID)); err == nil {
		return projectID
	}
	if h == nil || h.ResolveProjectID == nil {
		return ""
	}
	ctx := context.Background()
	if r != nil {
		ctx = r.Context()
	}
	projectID, err := h.boundProject(ctx)
	if err != nil {
		return ""
	}
	return projectID
}

func (h *BrowserHandler) hasDataExplorerClientIdentity(r *stdhttp.Request, command projectsignals.DataExplorerCommand) bool {
	return h != nil && h.dataExplorerClientKey(r, "project:identity-check", command) != ""
}

func (l *dataExplorerLifecycle) accept(key string, requestSeq int64) bool {
	return l.acceptSemantic(key, requestSeq)
}

func (l *dataExplorerLifecycle) acceptSemantic(key string, requestSeq int64) bool {
	if requestSeq <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.latest == nil {
		l.latest = make(map[string]int64)
	}
	l.touchLocked(key)
	if previous := l.latest[key]; requestSeq < previous {
		l.pruneLocked()
		return false
	}
	if requestSeq > l.latest[key] {
		l.latest[key] = requestSeq
	}
	l.pruneLocked()
	return true
}

func (l *dataExplorerLifecycle) acceptSuggestions(key string, requestSeq int64) (bool, int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.suggestionsLatest == nil {
		l.suggestionsLatest = make(map[string]int64)
	}
	l.touchLocked(key)
	if requestSeq <= 0 || requestSeq > dataExplorerMaxSuggestionRequestSeq || requestSeq <= l.suggestionsLatest[key] {
		l.pruneLocked()
		return false, requestSeq
	}
	l.suggestionsLatest[key] = requestSeq
	l.pruneLocked()
	return true, requestSeq
}

func (l *dataExplorerLifecycle) current(key string, requestSeq int64) bool {
	return l.currentRun(key, requestSeq, "")
}

func (l *dataExplorerLifecycle) currentRun(key string, requestSeq int64, runID string) bool {
	if requestSeq <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.touchLocked(key)
	current := l.currentRunLocked(key, requestSeq, runID)
	l.pruneLocked()
	return current
}

func (l *dataExplorerLifecycle) currentRunLocked(key string, requestSeq int64, runID string) bool {
	// Stop records a tombstone instead of advancing the request sequence so
	// that the stop response can still acquire the emission lease. A semantic
	// response from that stopped run must nevertheless remain stale, including
	// when its executor ignores context cancellation and returns later.
	runID = strings.TrimSpace(runID)
	if stoppedRunID := strings.TrimSpace(l.stoppedRun[key]); stoppedRunID != "" && runID != "" && runID == stoppedRunID {
		return false
	}
	if requestSeq < l.latest[key] {
		return false
	}
	if runID != "" && l.latestRun[key] != "" {
		return l.latestRun[key] == runID
	}
	return true
}

func (l *dataExplorerLifecycle) begin(key, runID string, requestSeq int64, parent context.Context) (context.Context, func()) {
	ctx, finish, _ := l.beginLane(key, runID, requestSeq, parent, false)
	return ctx, finish
}

func (l *dataExplorerLifecycle) beginRun(key, runID string, requestSeq int64, parent context.Context) (context.Context, func(), string) {
	return l.beginLane(key, runID, requestSeq, parent, false)
}

func (l *dataExplorerLifecycle) beginSuggestions(key, runID string, requestSeq int64, parent context.Context) (context.Context, func(), string) {
	return l.beginLane(key, runID, requestSeq, parent, true)
}

func (l *dataExplorerLifecycle) beginLane(key, runID string, requestSeq int64, parent context.Context, suggestions bool) (context.Context, func(), string) {
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	l.mu.Lock()
	if runID = strings.TrimSpace(runID); runID == "" {
		l.nextRunID++
		runID = fmt.Sprintf("explore-%d", l.nextRunID)
	}
	activeMap := &l.active
	latestMap := &l.latest
	runMap := &l.latestRun
	if suggestions {
		activeMap = &l.suggestionsActive
		latestMap = &l.suggestionsLatest
		runMap = &l.suggestionsRun
	}
	l.touchLocked(key)
	if *activeMap == nil {
		*activeMap = make(map[string]*dataExplorerExecution)
	}
	if !suggestions && l.stoppedRun != nil {
		delete(l.stoppedRun, key)
	}
	if *latestMap == nil {
		*latestMap = make(map[string]int64)
	}
	if *runMap == nil {
		*runMap = make(map[string]string)
	}
	if previous := (*activeMap)[key]; previous != nil {
		previous.cancel()
	}
	entry := &dataExplorerExecution{cancel: cancel, requestSeq: requestSeq, runID: runID}
	(*activeMap)[key] = entry
	if requestSeq >= (*latestMap)[key] {
		(*latestMap)[key] = requestSeq
		(*runMap)[key] = runID
	}
	l.pruneLocked()
	l.mu.Unlock()
	return ctx, func() {
		cancel()
		l.mu.Lock()
		if current := (*activeMap)[key]; current == entry {
			delete(*activeMap, key)
		}
		l.pruneLocked()
		l.mu.Unlock()
	}, runID
}

func (l *dataExplorerLifecycle) stop(key, runID string, requestSeq int64) bool {
	l.mu.Lock()
	l.touchLocked(key)
	active := l.active[key]
	if active == nil {
		l.mu.Unlock()
		return false
	}
	runID = strings.TrimSpace(runID)
	if runID != "" {
		if active.runID != runID {
			l.mu.Unlock()
			return false
		}
	} else if requestSeq > 0 && active.requestSeq > 0 && active.requestSeq != requestSeq {
		l.mu.Unlock()
		return false
	}
	// A stop command belongs to the request/run it names. An older command
	// must not be able to cancel a newer execution that has already advanced
	// the monotonic sequence for this client.
	if requestSeq > 0 && requestSeq < l.latest[key] {
		// A matching run ID is authoritative even when a newer configure draft
		// has advanced the browser sequence. It is precisely what lets Stop
		// cancel the active Run after a draft edit without killing another run.
		if runID == "" {
			l.mu.Unlock()
			return false
		}
	}
	if requestSeq > l.latest[key] {
		if l.latest == nil {
			l.latest = make(map[string]int64)
		}
		l.latest[key] = requestSeq
	}
	if l.stoppedRun == nil {
		l.stoppedRun = make(map[string]string)
	}
	l.stoppedRun[key] = active.runID
	l.pruneLocked()
	l.mu.Unlock()
	active.cancel()
	return true
}

func (l *dataExplorerLifecycle) currentSuggestions(key string, requestSeq int64, runID string) bool {
	if requestSeq <= 0 {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.touchLocked(key)
	current := l.currentSuggestionsLocked(key, requestSeq, runID)
	l.pruneLocked()
	return current
}

func (l *dataExplorerLifecycle) currentSuggestionsLocked(key string, requestSeq int64, runID string) bool {
	if requestSeq < l.suggestionsLatest[key] {
		return false
	}
	if runID != "" && l.suggestionsRun[key] != "" {
		return l.suggestionsRun[key] == runID
	}
	return true
}

func (l *dataExplorerLifecycle) stopped(key, runID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.touchLocked(key)
	current := l.stoppedLocked(key, runID)
	l.pruneLocked()
	return current
}

func (l *dataExplorerLifecycle) stoppedLocked(key, runID string) bool {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		// A legacy Stop may omit runId. The tombstone recorded by stop still
		// identifies the run that was cancelled and lets emission remain safe.
		runID = l.stoppedRun[key]
	}
	if runID == "" || l.stoppedRun[key] != runID {
		return false
	}
	// A newer semantic run may start before the stop response is emitted. Its
	// latest run identity supersedes the tombstone, so the old stop must not
	// pass the emission lease and replace that newer state.
	return l.latestRun[key] == "" || l.latestRun[key] == runID
}

func (l *dataExplorerLifecycle) touchLocked(key string) {
	if strings.TrimSpace(key) == "" {
		return
	}
	if l.identityAccess == nil {
		l.identityAccess = make(map[string]time.Time)
	}
	l.identityAccess[key] = time.Now()
}

const dataExplorerMaxSuggestionRequestSeq int64 = 9_007_199_254_740_991

// pruneLocked bounds process-local identity state. Active executions remain
// protected until they finish; an idle identity can be discarded because a
// subsequent request establishes a fresh monotonic baseline. Stopped
// tombstones remain while they fit the same bound, allowing the stop response
// to pass the emission guard without making the map permanent.
func (l *dataExplorerLifecycle) pruneLocked() {
	for len(l.identityAccess) > dataExplorerLifecycleMaxIdentities {
		oldestKey := ""
		var oldest time.Time
		for key, accessed := range l.identityAccess {
			if l.active[key] != nil || l.suggestionsActive[key] != nil {
				continue
			}
			if oldestKey == "" || accessed.Before(oldest) {
				oldestKey, oldest = key, accessed
			}
		}
		if oldestKey == "" {
			return
		}
		delete(l.identityAccess, oldestKey)
		delete(l.latest, oldestKey)
		delete(l.suggestionsLatest, oldestKey)
		delete(l.latestRun, oldestKey)
		delete(l.suggestionsRun, oldestKey)
		delete(l.stoppedRun, oldestKey)
	}
}

// dataExplorerResponseLease closes the check-then-emit race at the command
// endpoint. Once a response has acquired this lease, a newer request cannot
// advance lifecycle state until PatchResponse has serialized the current
// response; if it advanced first, this lease rejects the stale response.
func (h *BrowserHandler) dataExplorerResponseLease(r *stdhttp.Request, command projectsignals.DataExplorerCommand) (func(), bool) {
	if h == nil || r == nil {
		return func() {}, true
	}
	catalog := h.navigationCatalog(r)
	key := h.dataExplorerClientKey(r, h.dataExplorerProjectID(r, catalog), command)
	if key == "" {
		return nil, false
	}
	l := &h.dataExplorerLifecycle
	l.mu.Lock()
	seq := dataExplorerRequestSeq(command)
	current := true
	if command.Explore != nil && command.Explore.FilterSuggestions != nil && dataExplorerAction(command) == "configure" {
		suggestionSeq := dataExplorerSuggestionRequestSeq(command)
		current = suggestionSeq > 0 && suggestionSeq <= dataExplorerMaxSuggestionRequestSeq && l.currentSuggestionsLocked(key, suggestionSeq, "")
	} else if dataExplorerAction(command) == "stop" && l.stoppedLocked(key, dataExplorerRunID(command)) {
		current = true
	} else {
		current = l.currentRunLocked(key, seq, dataExplorerRunID(command))
	}
	if !current {
		l.mu.Unlock()
		return nil, false
	}
	l.touchLocked(key)
	return func() { l.mu.Unlock() }, true
}
