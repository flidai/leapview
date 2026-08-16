package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	projectgen "github.com/flidai/leapview/internal/project/api/gen"
)

var _ projectgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ projectgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherMapsProjectPaginationToCapabilityHandler(t *testing.T) {
	limit := int32(25)
	token := "next"
	handler := &recordingProjectHandler{}
	dispatcher := NewAPIGenDispatcher(handler)

	dispatcher.ListProjects(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects", nil),
		projectgen.GenListProjectsParams{Limit: &limit, PageToken: &token},
	)

	if handler.limit != &limit || handler.pageToken != &token {
		t.Fatalf("pagination = (%v, %v), want (%v, %v)", handler.limit, handler.pageToken, &limit, &token)
	}
}

type recordingProjectHandler struct {
	limit     *int32
	pageToken *string
}

func (h *recordingProjectHandler) ListProjects(_ stdhttp.ResponseWriter, _ *stdhttp.Request, limit *int32, pageToken *string) {
	h.limit, h.pageToken = limit, pageToken
}

func (*recordingProjectHandler) GetProject(stdhttp.ResponseWriter, *stdhttp.Request, string) {}

func (*recordingProjectHandler) Search(stdhttp.ResponseWriter, *stdhttp.Request, projectgen.GenSearchParams) {
}
