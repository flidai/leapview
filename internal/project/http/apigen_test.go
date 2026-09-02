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
	search projectapi.SearchParams
}

func (h *recordingProjectHandler) Search(_ stdhttp.ResponseWriter, _ *stdhttp.Request, params projectapi.SearchParams) {
	h.search = params
}
