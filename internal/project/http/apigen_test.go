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

func TestAPIGenDispatcherConvertsGeneratedSearchParamsToProjectContract(t *testing.T) {
	handler := &recordingProjectHandler{}
	dispatcher := NewAPIGenDispatcher(handler)
	kinds := []projectgen.SearchKind{projectgen.SearchKindDashboard}
	dispatcher.Search(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodGet, "/api/v1/search?q=sales", nil),
		projectgen.GenSearchParams{Q: "sales", Kind: &kinds},
	)
	if handler.search.Q != "sales" || handler.search.Kind == nil || len(*handler.search.Kind) != 1 || (*handler.search.Kind)[0] != projectapi.SearchKindDashboard {
		t.Fatalf("search params = %#v, want canonical project contract", handler.search)
	}
}

type recordingProjectHandler struct {
	project string
	search  projectapi.SearchParams
}

func (h *recordingProjectHandler) GetProject(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project string) {
	h.project = project
}

func (h *recordingProjectHandler) Search(_ stdhttp.ResponseWriter, _ *stdhttp.Request, params projectapi.SearchParams) {
	h.search = params
}
