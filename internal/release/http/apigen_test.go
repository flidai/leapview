package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	releasegen "github.com/flidai/leapview/internal/release/api/gen"
)

var _ releasegen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ releasegen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherMapsReleaseTransportInputs(t *testing.T) {
	handler := &recordingReleaseHandler{}
	dispatcher := NewAPIGenDispatcher(handler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/api/v1/projects/p1/releases", nil)

	dispatcher.CreateRelease(recorder, request, "p1", releasegen.GenCreateReleaseHeaders{IdempotencyKey: "create-1"})
	dispatcher.ListReleases(recorder, request, "p1", releasegen.GenListReleasesParams{Limit: int32Ptr(25), PageToken: stringPtr("page-1")})
	dispatcher.FinalizeRelease(recorder, request, "p1", "r1", releasegen.GenFinalizeReleaseHeaders{IdempotencyKey: "finalize-1"})
	dispatcher.ListReleaseEvents(recorder, request, "p1", "r1", releasegen.GenListReleaseEventsParams{
		Limit: int32Ptr(10), PageToken: stringPtr("event-page"),
	}, releasegen.GenListReleaseEventsHeaders{})

	if got, want := handler.createKey, "create-1"; got != want {
		t.Fatalf("create idempotency key = %q, want %q", got, want)
	}
	if got, want := *handler.listLimit, int32(25); got != want {
		t.Fatalf("list limit = %d, want %d", got, want)
	}
	if got, want := *handler.listPageToken, "page-1"; got != want {
		t.Fatalf("list page token = %q, want %q", got, want)
	}
	if got, want := handler.finalizeKey, "finalize-1"; got != want {
		t.Fatalf("finalize idempotency key = %q, want %q", got, want)
	}
	if got, want := *handler.eventLimit, int32(10); got != want {
		t.Fatalf("event limit = %d, want %d", got, want)
	}
	if got, want := *handler.eventPageToken, "event-page"; got != want {
		t.Fatalf("event page token = %q, want %q", got, want)
	}
}

type recordingReleaseHandler struct {
	createKey, finalizeKey        string
	listLimit, eventLimit         *int32
	listPageToken, eventPageToken *string
}

func (h *recordingReleaseHandler) CreateRelease(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _, key string) {
	h.createKey = key
}

func (h *recordingReleaseHandler) ListReleases(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _ string, limit *int32, pageToken *string) {
	h.listLimit, h.listPageToken = limit, pageToken
}

func (*recordingReleaseHandler) GetRelease(stdhttp.ResponseWriter, *stdhttp.Request, string, string) {
}

func (h *recordingReleaseHandler) FinalizeRelease(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _, _, key string) {
	h.finalizeKey = key
}

func (h *recordingReleaseHandler) ListReleaseEvents(_ stdhttp.ResponseWriter, _ *stdhttp.Request, _, _ string, limit *int32, pageToken *string) {
	h.eventLimit, h.eventPageToken = limit, pageToken
}

func int32Ptr(value int32) *int32    { return &value }
func stringPtr(value string) *string { return &value }
