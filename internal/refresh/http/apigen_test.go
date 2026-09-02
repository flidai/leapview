package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	refreshgen "github.com/flidai/leapview/internal/refresh/api/gen"
)

var _ refreshgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ refreshgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherMapsRefreshEventPagination(t *testing.T) {
	limit := int32(10)
	token := "next"
	handler := &recordingRefreshHandler{}
	NewAPIGenDispatcher(handler).ListRefreshRunEvents(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/sales/refresh-runs/run-1/events", nil),
		"sales",
		"run-1",
		refreshgen.GenListRefreshRunEventsParams{Limit: &limit, PageToken: &token},
		refreshgen.GenListRefreshRunEventsHeaders{},
	)
	if handler.limit != &limit || handler.pageToken != &token {
		t.Fatalf("pagination = (%v, %v), want (%v, %v)", handler.limit, handler.pageToken, &limit, &token)
	}
}

func TestAPIGenDispatcherMapsRefreshCreateIdempotency(t *testing.T) {
	handler := &recordingRefreshHandler{}
	NewAPIGenDispatcher(handler).CreateRefreshRun(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/p1/refresh-runs", nil),
		"p1",
		refreshgen.GenCreateRefreshRunHeaders{IdempotencyKey: "refresh-create-1"},
	)
	if got, want := handler.idempotencyKey, "refresh-create-1"; got != want {
		t.Fatalf("idempotency key = %q, want %q", got, want)
	}
}

type recordingRefreshHandler struct {
	limit          *int32
	pageToken      *string
	idempotencyKey string
}

func (*recordingRefreshHandler) ListRefreshRuns(stdhttp.ResponseWriter, *stdhttp.Request, string) {
}
func (h *recordingRefreshHandler) CreateRefreshRun(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _, key string) {
	h.idempotencyKey = key
}
func (*recordingRefreshHandler) GetRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request, string, string) {
}
func (*recordingRefreshHandler) CancelRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request, string, string) {
}
func (h *recordingRefreshHandler) ListRefreshRunEvents(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _, _ string, limit *int32, pageToken *string) {
	h.limit, h.pageToken = limit, pageToken
}
