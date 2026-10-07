package http

import (
	"context"
	"errors"
	"fmt"
	"testing"

	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerLifecycleRejectsDelayedBegin(t *testing.T) {
	for _, suggestions := range []bool{false, true} {
		name := "semantic"
		if suggestions {
			name = "suggestions"
		}
		t.Run(name, func(t *testing.T) {
			var lifecycle dataExplorerLifecycle
			accept := lifecycle.acceptSemantic
			begin := lifecycle.beginRun
			current := lifecycle.currentRun
			if suggestions {
				accept = func(key string, seq int64) bool {
					accepted, _ := lifecycle.acceptSuggestions(key, seq)
					return accepted
				}
				begin = lifecycle.beginSuggestions
				current = lifecycle.currentSuggestions
			}
			// Both requests passed acceptance, but projection of the older
			// request finishes only after the newer execution has started.
			if !accept("client", 1) || !accept("client", 2) {
				t.Fatal("initial requests were rejected")
			}
			newer, finishNewer, newerID := begin("client", "newer", 2, context.Background())
			t.Cleanup(finishNewer)
			older, finishOlder, olderID := begin("client", "older", 1, context.Background())
			t.Cleanup(finishOlder)
			if !errors.Is(older.Err(), context.Canceled) {
				t.Fatalf("delayed context error = %v, want context.Canceled", older.Err())
			}
			finishOlder()
			if newer.Err() != nil || !current("client", 2, newerID) {
				t.Fatalf("delayed begin displaced the newer run: %v", newer.Err())
			}
			if current("client", 1, olderID) {
				t.Fatal("delayed begin became current")
			}
			// Cleanup of a rejected begin must also leave cancellation ownership
			// with the newer execution.
			latest, finishLatest, _ := begin("client", "latest", 3, context.Background())
			t.Cleanup(finishLatest)
			if !errors.Is(newer.Err(), context.Canceled) || latest.Err() != nil {
				t.Fatal("rejected begin removed the newer execution from its lane")
			}
		})
	}
}

func TestDataExplorerLifecycleRejectsBeginAfterNewerConfigure(t *testing.T) {
	var lifecycle dataExplorerLifecycle
	if !lifecycle.acceptSemantic("client", 1) || !lifecycle.acceptSemantic("client", 2) {
		t.Fatal("initial requests were rejected")
	}
	run, finish, _ := lifecycle.beginRun("client", "delayed", 1, context.Background())
	t.Cleanup(finish)
	if !errors.Is(run.Err(), context.Canceled) {
		t.Fatalf("delayed context error = %v, want context.Canceled", run.Err())
	}
	if lifecycle.stop("client", "delayed", 2) {
		t.Fatal("delayed begin installed an active run after newer configure")
	}
}

func TestDataExplorerLifecycleRejectsBeginAfterStop(t *testing.T) {
	for _, runID := range []string{"stopped", "", "another-run"} {
		t.Run("delayed-"+runID, func(t *testing.T) {
			var lifecycle dataExplorerLifecycle
			_, finishStopped, _ := lifecycle.beginRun("client", "stopped", 1, context.Background())
			t.Cleanup(finishStopped)
			if !lifecycle.acceptSemantic("client", 1) || !lifecycle.stop("client", "stopped", 1) {
				t.Fatal("initial run or Stop was rejected")
			}
			delayed, finishDelayed, delayedID := lifecycle.beginRun("client", runID, 1, context.Background())
			t.Cleanup(finishDelayed)
			if !errors.Is(delayed.Err(), context.Canceled) {
				t.Fatalf("delayed context error = %v, want context.Canceled", delayed.Err())
			}
			finishDelayed()
			if lifecycle.currentRun("client", 1, delayedID) {
				t.Fatal("delayed begin became current after Stop")
			}
			lifecycle.mu.Lock()
			stopCurrent := lifecycle.stoppedLocked("client", "stopped", 1)
			lifecycle.mu.Unlock()
			if !stopCurrent {
				t.Fatal("delayed begin cleared the accepted Stop tombstone")
			}
			newer, finishNewer, newerID := lifecycle.beginRun("client", "newer", 2, context.Background())
			t.Cleanup(finishNewer)
			if newer.Err() != nil || !lifecycle.currentRun("client", 2, newerID) {
				t.Fatal("Stop prevented a newer run from starting")
			}
		})
	}
}

func TestDataExplorerLifecycleStopBeforeFirstBegin(t *testing.T) {
	for _, stopRunID := range []string{"", "delayed"} {
		t.Run("stop-"+stopRunID, func(t *testing.T) {
			h, request, key, clientID := newDataExplorerLeaseFixture("stop-before-begin", true)
			lifecycle := &h.dataExplorerLifecycle
			if !lifecycle.acceptSemantic(key, 7) {
				t.Fatal("initial request was rejected")
			}
			if lifecycle.stop(key, stopRunID, 7) {
				t.Fatal("Stop reported cancellation without an active execution")
			}
			stopCommand := projectsignals.DataExplorerCommand{
				Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
				RunID: projectsignals.Optional(stopRunID), RequestSeq: 7,
			}
			if release, current := h.dataExplorerResponseLease(request, stopCommand); !current {
				t.Fatal("current no-active Stop lost its response lease")
			} else {
				release()
			}
			delayed, finishDelayed, delayedID := lifecycle.beginRun(key, "delayed", 7, context.Background())
			t.Cleanup(finishDelayed)
			if !errors.Is(delayed.Err(), context.Canceled) {
				t.Fatalf("delayed context error = %v, want context.Canceled", delayed.Err())
			}
			finishDelayed()
			if lifecycle.currentRun(key, 7, delayedID) {
				t.Fatal("delayed begin remained current after Stop")
			}
			runCommand := projectsignals.DataExplorerCommand{
				Action: projectsignals.Optional("run"), ClientID: projectsignals.Optional(clientID),
				RunID: projectsignals.Optional(delayedID), RequestSeq: 7,
			}
			if release, current := h.dataExplorerResponseLease(request, runCommand); current {
				release()
				t.Fatal("delayed run could emit over the Stop acknowledgement")
			}
			if release, current := h.dataExplorerResponseLease(request, stopCommand); !current {
				t.Fatal("delayed begin invalidated the no-active Stop acknowledgement")
			} else {
				release()
			}
			newer, finishNewer, newerID := lifecycle.beginRun(key, "newer", 8, context.Background())
			t.Cleanup(finishNewer)
			if newer.Err() != nil || !lifecycle.currentRun(key, 8, newerID) {
				t.Fatal("no-active Stop prevented a newer run from starting")
			}
		})
	}
}

func TestDataExplorerLifecycleRejectedNoActiveStopDoesNotBlockBegin(t *testing.T) {
	for _, test := range []struct {
		name  string
		runID string
		seq   int64
	}{
		{name: "older sequence", seq: 6},
		{name: "different run", runID: "unrelated", seq: 7},
		{name: "unnamed without sequence"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var lifecycle dataExplorerLifecycle
			_, finish, _ := lifecycle.beginRun("client", "previous", 7, context.Background())
			finish()
			if lifecycle.stop("client", test.runID, test.seq) {
				t.Fatal("Stop reported cancellation without an active execution")
			}
			run, finishRun, runID := lifecycle.beginRun("client", "current", 7, context.Background())
			t.Cleanup(finishRun)
			if run.Err() != nil || !lifecycle.currentRun("client", 7, runID) {
				t.Fatal("rejected no-active Stop prevented the current request from starting")
			}
		})
	}
}

func TestDataExplorerLifecycleLegacyRunCanRestartAfterStop(t *testing.T) {
	h, request, key, clientID := newDataExplorerLeaseFixture("legacy-restart", true)
	lifecycle := &h.dataExplorerLifecycle
	_, finishFirst, _ := lifecycle.beginRun(key, "first", 0, context.Background())
	t.Cleanup(finishFirst)
	if !lifecycle.stop(key, "first", 0) {
		t.Fatal("legacy named Stop was rejected")
	}
	retry, finishRetry, _ := lifecycle.beginRun(key, "first", 0, context.Background())
	t.Cleanup(finishRetry)
	if !errors.Is(retry.Err(), context.Canceled) {
		t.Fatal("stopped legacy run was allowed to restart with the same identity")
	}
	second, finishSecond, _ := lifecycle.beginRun(key, "second", 0, context.Background())
	t.Cleanup(finishSecond)
	if second.Err() != nil {
		t.Fatalf("distinct legacy run was rejected after Stop: %v", second.Err())
	}
	command := projectsignals.DataExplorerCommand{
		Action: projectsignals.Optional("run"), ClientID: projectsignals.Optional(clientID),
		RunID: projectsignals.Optional("second"),
	}
	if release, current := h.dataExplorerResponseLease(request, command); !current {
		t.Fatal("distinct legacy run lost its response lease")
	} else {
		release()
	}
}

func TestDataExplorerLifecycleStopBeforeNextBegin(t *testing.T) {
	for _, acceptBeforeStop := range []bool{false, true} {
		t.Run(fmt.Sprintf("accepted-%t", acceptBeforeStop), func(t *testing.T) {
			h, request, key, clientID := newDataExplorerLeaseFixture("stop-next-begin", true)
			lifecycle := &h.dataExplorerLifecycle
			_, finishPrevious, _ := lifecycle.beginRun(key, "previous", 7, context.Background())
			finishPrevious()
			if acceptBeforeStop && !lifecycle.acceptSemantic(key, 8) {
				t.Fatal("next request was rejected")
			}
			if lifecycle.stop(key, "next", 8) {
				t.Fatal("Stop reported cancellation without an active execution")
			}
			stopCommand := projectsignals.DataExplorerCommand{
				Action: projectsignals.Optional("stop"), ClientID: projectsignals.Optional(clientID),
				RunID: projectsignals.Optional("next"), RequestSeq: 8,
			}
			if release, current := h.dataExplorerResponseLease(request, stopCommand); !current {
				t.Fatal("completed run prevented the next Stop acknowledgement")
			} else {
				release()
			}
			if !acceptBeforeStop && !lifecycle.acceptSemantic(key, 8) {
				t.Fatal("delayed next request was rejected before begin")
			}
			delayed, finishDelayed, _ := lifecycle.beginRun(key, "next", 8, context.Background())
			t.Cleanup(finishDelayed)
			if !errors.Is(delayed.Err(), context.Canceled) {
				t.Fatalf("delayed next context error = %v, want context.Canceled", delayed.Err())
			}
		})
	}
}
