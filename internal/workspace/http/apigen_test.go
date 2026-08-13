package http

import (
	stdhttp "net/http"
	"net/http/httptest"
	"testing"

	workspacegen "github.com/flidai/leapview/internal/workspace/api/gen"
)

var _ workspacegen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ workspacegen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherMapsSearchParameters(t *testing.T) {
	handler := &recordingAPIGenHandler{}
	types := []workspacegen.SearchResultType{workspacegen.SearchResultTypeDashboard, workspacegen.SearchResultTypePage}
	workspaces := []string{"sales", "operations"}
	dispatcher := NewAPIGenDispatcher(handler)
	dispatcher.Search(
		httptest.NewRecorder(),
		httptest.NewRequest(stdhttp.MethodGet, "/api/v1/search", nil),
		workspacegen.GenSearchParams{
			Q: stringPointer("revenue"), Workspace: &workspaces, Type: &types,
			ContextWorkspace: stringPointer("sales"), ContextDashboard: stringPointer("executive"),
			ContextPage: stringPointer("overview"), Limit: int32Pointer(20), PageToken: stringPointer("next"),
		},
	)

	if got, want := *handler.search.Query, "revenue"; got != want {
		t.Fatalf("query = %q, want %q", got, want)
	}
	if got, want := *handler.search.Types, []string{"dashboard", "page"}; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("types = %v, want %v", got, want)
	}
	if got, want := *handler.search.Workspaces, workspaces; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("workspaces = %v, want %v", got, want)
	}
	if got, want := *handler.search.Limit, int32(20); got != want {
		t.Fatalf("limit = %d, want %d", got, want)
	}
	if got, want := *handler.search.PageToken, "next"; got != want {
		t.Fatalf("page token = %q, want %q", got, want)
	}
}

type recordingAPIGenHandler struct {
	search APIGenSearchParams
}

func (h *recordingAPIGenHandler) Search(_ stdhttp.ResponseWriter, _ *stdhttp.Request, params APIGenSearchParams) {
	h.search = params
}

func (*recordingAPIGenHandler) ListWorkspaces(stdhttp.ResponseWriter, *stdhttp.Request)       {}
func (*recordingAPIGenHandler) GetWorkspace(stdhttp.ResponseWriter, *stdhttp.Request, string) {}
func (*recordingAPIGenHandler) GetWorkspaceAdministration(stdhttp.ResponseWriter, *stdhttp.Request, string) {
}
func (*recordingAPIGenHandler) GetWorkspaceActiveAssetGraph(stdhttp.ResponseWriter, *stdhttp.Request) {
}
func (*recordingAPIGenHandler) ListWorkspaceAssetEdges(stdhttp.ResponseWriter, *stdhttp.Request) {}
func (*recordingAPIGenHandler) ListWorkspaceAssets(stdhttp.ResponseWriter, *stdhttp.Request)     {}
func (*recordingAPIGenHandler) GetWorkspaceAsset(stdhttp.ResponseWriter, *stdhttp.Request)       {}
func (*recordingAPIGenHandler) GetWorkspaceAssetLineage(stdhttp.ResponseWriter, *stdhttp.Request) {
}
func (*recordingAPIGenHandler) UpdateDashboardAppearance(stdhttp.ResponseWriter, *stdhttp.Request, string, string) {
}

func stringPointer(value string) *string { return &value }
func int32Pointer(value int32) *int32    { return &value }
