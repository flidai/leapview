package http

import (
	"context"
	"encoding/json"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/platform"
	apigenapi "github.com/flidai/leapview/internal/platform/http/api/gen"
	jobsqlite "github.com/flidai/leapview/internal/platform/jobs/sqlite"
	"github.com/flidai/leapview/pkg/jobs"
)

func eventRepository(t *testing.T) jobs.Repository {
	t.Helper()
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "platform.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return jobsqlite.NewRepository(store.SQLDB())
}

func appendEvent(t *testing.T, repo jobs.Repository, kind, id, event string, sequence int) jobs.Event {
	t.Helper()
	row, err := repo.AppendEvent(context.Background(), kind, id, event, []byte(fmt.Sprintf(`{"sequence":%d}`, sequence)))
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func TestEventSSEReplaysAfterLastEventIDAndClosesAtTerminalEvent(t *testing.T) {
	repo := eventRepository(t)
	first := appendEvent(t, repo, "release", "rel-a", "release.created", 1)
	appendEvent(t, repo, "release", "rel-a", "release.artifact_uploaded", 2)
	appendEvent(t, repo, "release", "rel-a", "release.ready", 3)
	req := httptest.NewRequest(stdhttp.MethodGet, "/events", nil)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Last-Event-ID", fmt.Sprintf("%020d", first.ID))
	rec := httptest.NewRecorder()

	WriteEventPage(rec, req, repo, "release", "rel-a", nil, nil, "release:project-a:rel-a")

	body := rec.Body.String()
	if rec.Code != stdhttp.StatusOK || strings.Contains(body, "release.created") || !strings.Contains(body, "release.artifact_uploaded") || !strings.Contains(body, "release.ready") {
		t.Fatalf("status=%d body=%s", rec.Code, body)
	}
}

func TestEventHistoryPagesBeyondTwoHundredRecords(t *testing.T) {
	repo := eventRepository(t)
	for index := 1; index <= 205; index++ {
		appendEvent(t, repo, "refresh", "run-a", "refresh.progress", index)
	}
	limit := int32(200)
	firstRec := httptest.NewRecorder()
	WriteEventPage(firstRec, httptest.NewRequest(stdhttp.MethodGet, "/events", nil), repo, "refresh", "run-a", &limit, nil, "refresh:sales:run-a")
	var first apigenapi.AsyncEventListResponse
	if err := json.Unmarshal(firstRec.Body.Bytes(), &first); err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 200 || first.Page.NextCursor == nil {
		t.Fatalf("first page count=%d cursor=%v", len(first.Items), first.Page.NextCursor)
	}
	secondRec := httptest.NewRecorder()
	WriteEventPage(secondRec, httptest.NewRequest(stdhttp.MethodGet, "/events", nil), repo, "refresh", "run-a", &limit, first.Page.NextCursor, "refresh:sales:run-a")
	var second apigenapi.AsyncEventListResponse
	if err := json.Unmarshal(secondRec.Body.Bytes(), &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 5 || second.Page.NextCursor != nil {
		t.Fatalf("second page count=%d cursor=%v", len(second.Items), second.Page.NextCursor)
	}
}

func TestEventResponsePromotesProgressAndErrorFields(t *testing.T) {
	events, err := eventResponses([]jobs.Event{{
		ID: 1, ResourceKind: "refresh", ResourceID: "run-a", EventType: "refresh.failed", CreatedAt: "2026-07-16T12:00:00Z",
		Data: []byte(`{"progress":{"current":7,"total":10,"percent":70},"error":{"code":"QUERY_FAILED","detail":"warehouse unavailable"},"stage":"load"}`),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if events[0].Progress == nil || events[0].Error == nil || events[0].Data["stage"] != "load" {
		t.Fatalf("event envelope = %#v", events[0])
	}
	if _, duplicated := events[0].Data["progress"]; duplicated {
		t.Fatalf("progress duplicated in data: %#v", events[0].Data)
	}
}

func TestEventCursorIsResourceScoped(t *testing.T) {
	repo := eventRepository(t)
	appendEvent(t, repo, "release", "rel-a", "release.created", 1)
	appendEvent(t, repo, "release", "rel-a", "release.ready", 2)
	limit := int32(1)
	firstRec := httptest.NewRecorder()
	WriteEventPage(firstRec, httptest.NewRequest(stdhttp.MethodGet, "/events", nil), repo, "release", "rel-a", &limit, nil, "release:project-a:rel-a")
	var first apigenapi.AsyncEventListResponse
	if err := json.Unmarshal(firstRec.Body.Bytes(), &first); err != nil || first.Page.NextCursor == nil {
		t.Fatalf("cursor=%v err=%v", first.Page.NextCursor, err)
	}
	secondRec := httptest.NewRecorder()
	WriteEventPage(secondRec, httptest.NewRequest(stdhttp.MethodGet, "/events", nil), repo, "release", "rel-a", &limit, first.Page.NextCursor, "release:project-a:rel-b")
	if secondRec.Code != stdhttp.StatusBadRequest || !strings.Contains(secondRec.Body.String(), "INVALID_CURSOR") {
		t.Fatalf("status=%d body=%s", secondRec.Code, secondRec.Body.String())
	}
}
