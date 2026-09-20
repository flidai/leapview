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

func TestAPIGenDispatcherPreservesAuditProjectPath(t *testing.T) {
	repository := &auditReadRepository{}
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project:foreign/audit-events", nil)
	response := httptest.NewRecorder()

	NewAPIGenDispatcher(projectAuditHandler(repository)).ListAuditEvents(
		response,
		request,
		"",
		"project:foreign",
		accessgen.GenListAuditEventsParams{},
	)

	if response.Code != stdhttp.StatusNotFound {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
	if repository.called {
		t.Fatal("generated foreign Project path reached the audit repository")
	}
}

func TestAPIGenDispatcherDispatchesProjectRoleCatalog(t *testing.T) {
	dispatcher := NewAPIGenDispatcher(Handler{
		AuthorizationPolicyTargetID:    "target-1",
		AuthorizationPolicyEnvironment: "prod",
	})
	request := httptest.NewRequest(stdhttp.MethodGet, "/api/v1/projects/project-1/roles", nil)
	request = withProjectRoute(request, "project-1")
	response := httptest.NewRecorder()

	dispatcher.ListProjectRoles(response, request, "project-1", accessgen.GenListProjectRolesParams{})

	if response.Code != stdhttp.StatusOK {
		t.Fatalf("status = %d, body=%s", response.Code, response.Body.String())
	}
}

func TestAPIGenDispatcherPreservesProjectRoleBindingDeleteIdempotency(t *testing.T) {
	request := httptest.NewRequest(stdhttp.MethodDelete, "/api/v1/projects/project-1/role-bindings/binding-1", strings.NewReader(`{"expectedRevision":1}`))
	dispatcher := NewAPIGenDispatcher(Handler{
		Repository: func() (access.Repository, error) {
			return nil, errors.New("stop after generated header adaptation")
		},
	})

	dispatcher.DeleteProjectRoleBinding(
		httptest.NewRecorder(), request, "project-1", "binding-1",
		accessgen.GenDeleteProjectRoleBindingHeaders{IdempotencyKey: "delete-1"},
	)

	if got, want := request.Header.Get("Idempotency-Key"), "delete-1"; got != want {
		t.Fatalf("Idempotency-Key = %q, want %q", got, want)
	}
}

func TestAPIGenDispatcherPreservesOwnershipResolutionIdempotency(t *testing.T) {
	request := ownershipResolutionRequest(`{"action":"tombstone","targetPrincipalId":""}`)
	dispatcher := NewAPIGenDispatcher(Handler{
		Repository: func() (access.Repository, error) {
			return nil, errors.New("stop after generated header adaptation")
		},
	})

	dispatcher.ResolvePrincipalOwnership(
		httptest.NewRecorder(), request, "principal_owner",
		accessgen.GenResolvePrincipalOwnershipHeaders{IdempotencyKey: "ownership-key"},
	)

	if got, want := request.Header.Get("Idempotency-Key"), "ownership-key"; got != want {
		t.Fatalf("Idempotency-Key = %q, want %q", got, want)
	}
}
