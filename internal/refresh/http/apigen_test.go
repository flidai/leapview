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
		httptest.NewRequest(stdhttp.MethodGet, "/api/v1/refresh-runs/run-1/events", nil),
		"run-1",
		refreshgen.GenListRefreshRunEventsParams{Limit: &limit, PageToken: &token},
		refreshgen.GenListRefreshRunEventsHeaders{},
	)
	if handler.limit != &limit || handler.pageToken != &token {
		t.Fatalf("pagination = (%v, %v), want (%v, %v)", handler.limit, handler.pageToken, &limit, &token)
	}
}

type recordingRefreshHandler struct {
	limit     *int32
	pageToken *string
}

func (*recordingRefreshHandler) ListRefreshRuns(stdhttp.ResponseWriter, *stdhttp.Request) {
}
func (*recordingRefreshHandler) CreateRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request) {
}
func (*recordingRefreshHandler) GetRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request, string) {
}
func (*recordingRefreshHandler) CancelRefreshRun(stdhttp.ResponseWriter, *stdhttp.Request, string) {
}
func (h *recordingRefreshHandler) ListRefreshRunEvents(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _ string, limit *int32, pageToken *string) {
	h.limit, h.pageToken = limit, pageToken
}
