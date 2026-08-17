package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
)

var _ dashboardgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ dashboardgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherForwardsProjectAndModelOperations(t *testing.T) {
	handler := &recordingAPIGenHandler{}
	dispatcher := NewAPIGenDispatcher(handler)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(stdhttp.MethodPost, "/", nil)

	dispatcher.QuerySemanticModel(recorder, request, "orders", dashboardgen.GenQuerySemanticModelHeaders{})
	dispatcher.RotateDashboardPublication(recorder, request, "operations", "public-board", dashboardgen.GenRotateDashboardPublicationHeaders{})

	if got, want := handler.queryModel, "orders"; got != want {
		t.Fatalf("query model = %q, want %q", got, want)
	}
	if gotWorkspace, gotPublication := handler.publicationWorkspace, handler.publicationName; gotWorkspace != "operations" || gotPublication != "public-board" {
		t.Fatalf("publication scope = %q/%q, want operations/public-board", gotWorkspace, gotPublication)
	}
}

type recordingAPIGenHandler struct {
	APIGenHandler
	queryModel           string
	publicationWorkspace string
	publicationName      string
}

func (h *recordingAPIGenHandler) QuerySemanticModel(_ stdhttp.ResponseWriter, _ *stdhttp.Request, model string, _ dashboardgen.GenQuerySemanticModelHeaders) {
	h.queryModel = model
}

func (h *recordingAPIGenHandler) RotateDashboardPublication(_ stdhttp.ResponseWriter, _ *stdhttp.Request, project, publication string, _ dashboardgen.GenRotateDashboardPublicationHeaders) {
	h.publicationWorkspace, h.publicationName = project, publication
}
