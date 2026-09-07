package http

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	apigenapi "github.com/flidai/leapview/internal/platform/http/api/gen"
	"github.com/flidai/leapview/pkg/jobs"
)

func eventRepository(t *testing.T) jobs.Repository {
	t.Helper()
	return &memoryEventRepository{}
}

type memoryEventRepository struct {
	mu   sync.Mutex
	rows []jobs.Event
}

func (*memoryEventRepository) Enqueue(context.Context, jobs.EnqueueInput) (jobs.Job, error) {
	return jobs.Job{}, errors.New("test queue execution is unavailable")
}
func (*memoryEventRepository) Get(context.Context, string) (jobs.Job, error) {
	return jobs.Job{}, jobs.ErrNotFound
}
func (*memoryEventRepository) Cancel(context.Context, string) error { return jobs.ErrNotFound }
func (r *memoryEventRepository) AppendEvent(_ context.Context, kind, id, event string, data []byte) (jobs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next int64 = 1
	for _, existing := range r.rows {
		if existing.ResourceKind == kind && existing.ResourceID == id && existing.ID >= next {
			next = existing.ID + 1
		}
	}
	row := jobs.Event{ID: next, ResourceKind: kind, ResourceID: id, EventType: event, Data: append([]byte(nil), data...), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	r.rows = append(r.rows, row)
	return row, nil
}
func (r *memoryEventRepository) ListEvents(_ context.Context, kind, id string, after int64, limit int) ([]jobs.Event, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]jobs.Event, 0, limit)
	for _, row := range r.rows {
		if row.ResourceKind == kind && row.ResourceID == id && row.ID > after && len(out) < limit {
			out = append(out, row)
		}
	}
	return out, nil
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

func TestEventHistoryUsesResourceScopedDomainIDsForJSONAndSSE(t *testing.T) {
	repo := eventRepository(t)
	a1 := appendEvent(t, repo, "release", "rel-a", "release.a.created", 1)
	b1 := appendEvent(t, repo, "release", "rel-b", "release.b.created", 1)
	a2 := appendEvent(t, repo, "release", "rel-a", "release.a.ready", 2)
	b2 := appendEvent(t, repo, "release", "rel-b", "release.b.ready", 2)
	if a1.ID != b1.ID || a2.ID != b2.ID {
		t.Fatalf("fixture must have resource-scoped IDs: a=(%d,%d) b=(%d,%d)", a1.ID, a2.ID, b1.ID, b2.ID)
	}

	jsonRec := httptest.NewRecorder()
	WriteEventPage(jsonRec, httptest.NewRequest(stdhttp.MethodGet, "/events", nil), repo, "release", "rel-a", nil, nil, "release:project-a:rel-a")
	var history eventListResponse
	if err := json.Unmarshal(jsonRec.Body.Bytes(), &history); err != nil {
		t.Fatal(err)
	}
	if len(history.Items) != 2 || history.Items[0].ID != fmt.Sprintf("%020d", a1.ID) || history.Items[1].ID != fmt.Sprintf("%020d", a2.ID) || history.Items[0].ResourceID != "rel-a" || history.Items[1].ResourceID != "rel-a" {
		t.Fatalf("JSON history = %#v", history.Items)
	}
	for _, item := range history.Items {
		encoded, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		for _, transportField := range []string{"event_id", "eventId", "offset", "claim", "attempt"} {
			if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(transportField)) {
				t.Fatalf("JSON event leaked transport metadata %q: %s", transportField, encoded)
			}
		}
	}

	sseReq := httptest.NewRequest(stdhttp.MethodGet, "/events", nil)
	sseReq.Header.Set("Accept", "text/event-stream")
	sseReq.Header.Set("Last-Event-ID", fmt.Sprintf("%020d", a1.ID))
	sseRec := httptest.NewRecorder()
	WriteEventPage(sseRec, sseReq, repo, "release", "rel-a", nil, nil, "release:project-a:rel-a")
	body := sseRec.Body.String()
	if !strings.Contains(body, fmt.Sprintf("id: %020d", a2.ID)) || !strings.Contains(body, "event: release.a.ready") || strings.Contains(body, "release.b.") {
		t.Fatalf("SSE resume crossed resource boundary: %s", body)
	}
	for _, transportField := range []string{"event_id", "eventId", "offset", "claim", "attempt"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(transportField)) {
			t.Fatalf("SSE event leaked transport metadata %q: %s", transportField, body)
		}
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
