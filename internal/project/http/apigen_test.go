package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	projectapi "github.com/flidai/leapview/internal/project/api"
	projectgen "github.com/flidai/leapview/internal/project/api/gen"
)

var _ projectgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ projectgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherForwardsProjectIdentityToCapabilityHandler(t *testing.T) {
	handler := &recordingProjectHandler{}
	dispatcher := NewAPIGenDispatcher(handler)

	dispatcher.GetProject(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project:analytics", nil),
		"project:analytics",
	)

	if handler.project != "project:analytics" {
		t.Fatalf("project = %q, want project:analytics", handler.project)
	}
}

type recordingProjectHandler struct {
	project string
}

func (h *recordingProjectHandler) GetProject(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string) {
	h.project = project
}

func (*recordingProjectHandler) Search(stdhttp.ResponseWriter, *stdhttp.Request, projectapi.SearchParams) {
}
