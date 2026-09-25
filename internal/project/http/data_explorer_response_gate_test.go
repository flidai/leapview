package http

import (
	"net/http/httptest"
	"testing"
	"time"

	projectsignals "github.com/flidai/leapview/internal/project/ui/signals"
)

func TestDataExplorerResponseLeaseSerializesOnlyOneClientEmission(t *testing.T) {
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
		t.Fatal("same-client lifecycle state advanced before the current response was emitted")
	case <-time.After(10 * time.Millisecond):
	}

	otherRequest := httptest.NewRequest("POST", "/explore/command", nil)
	otherClientID := "other-client"
	otherKey := h.dataExplorerClientKey(otherRequest, "project:test", projectsignals.DataExplorerCommand{ClientID: projectsignals.Optional(otherClientID)})
	otherLeased := make(chan bool, 1)
	go func() {
		if !h.dataExplorerLifecycle.acceptSemantic(otherKey, 1) {
			otherLeased <- false
			return
		}
		otherUnlock, otherCurrent := h.dataExplorerResponseLease(otherRequest, projectsignals.DataExplorerCommand{ClientID: projectsignals.Optional(otherClientID), RequestSeq: 1})
		if otherCurrent {
			otherUnlock()
		}
		otherLeased <- otherCurrent
	}()
	select {
	case current := <-otherLeased:
		if !current {
			t.Fatal("unrelated client could not acquire a response lease")
		}
	case <-time.After(time.Second):
		t.Fatal("one client's response emission blocked another client")
	}

	unlock()
	select {
	case <-advanced:
	case <-time.After(time.Second):
		t.Fatal("same-client lifecycle state remained blocked after response emission")
	}
	if staleUnlock, current := h.dataExplorerResponseLease(req, command); current {
		staleUnlock()
		t.Fatal("stale same-client response acquired a lease after a newer request advanced")
	}
}
