package http

import (
	"errors"
	stdhttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
)

var _ accessgen.GenOperationDispatcher = (*APIGenDispatcher)(nil)
var _ accessgen.GenTransportErrorResponder = APIGenTransportErrorResponder{}

func TestAPIGenDispatcherPreservesGeneratedIfMatchHeader(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodPatch, "/api/v1/principals/principal-1", strings.NewReader(`{}`))
	dispatcher := NewAPIGenDispatcher(Handler{
		Repository: func() (access.Repository, error) {
			return nil, errors.New("stop after generated header adaptation")
		},
	})

	dispatcher.UpdatePrincipal(
		httptest.NewRecorder(),
		request,
		"principal-1",
		accessgen.GenUpdatePrincipalHeaders{IfMatch: `"revision-1"`},
	)

	if got, want := request.Header.Get("If-Match"), `"revision-1"`; got != want {
		t.Fatalf("If-Match = %q, want %q", got, want)
	}
}

func TestAPIGenDispatcherPreservesCurrentPrincipalIfMatchHeader(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodPatch, "/api/v1/me", strings.NewReader(`{"displayName":"Updated"}`))
	dispatcher := NewAPIGenDispatcher(Handler{})

	dispatcher.UpdateCurrentPrincipal(
		httptest.NewRecorder(),
		request,
		accessgen.GenUpdateCurrentPrincipalHeaders{IfMatch: `"profile-1"`},
	)

	if got, want := request.Header.Get("If-Match"), `"profile-1"`; got != want {
		t.Fatalf("If-Match = %q, want %q", got, want)
	}
}
