package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	apiidempotencysqlite "github.com/flidai/leapview/internal/platform/http/idempotency/sqlite"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	"github.com/go-chi/chi/v5"
)

func TestBuildConstructsOwnedHTTPHandler(t *testing.T) {
	module, err := Build(t.Context(), Config{
		Layout: func(*http.Request) webpage.Provider {
			return func(webpage.Context) webpage.Layout {
				return webpage.Layout{Presentation: webpage.Presentation{ProductName: "Application"}}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := module.HTTP().Layout(nil)(webpage.Context{}).Presentation.ProductName; got != "Application" {
		t.Fatalf("product name = %q", got)
	}
}

func TestRoleLabelDistinguishesLocalAndConfiguredAccess(t *testing.T) {
	if got := RoleLabel(false, Principal{}, false); got != "Local platform" {
		t.Fatalf("local label = %q", got)
	}
	if got := RoleLabel(true, Principal{DevBypass: true}, true); got != "Platform admin" {
		t.Fatalf("admin label = %q", got)
	}
	if got := RoleLabel(true, Principal{}, true); got != "Platform access" {
		t.Fatalf("access label = %q", got)
	}
}

func TestAdminPublicationMutationPassesUIInvocationIdentity(t *testing.T) {
	service := &adminPublicationInvocationService{}
	m := &Module{
		publications: service,
		publicationCommands: map[string]uicommand.Binding{
			"suspend": dashboardgen.GenUIActionSuspendDashboardPublication(),
			"resume":  dashboardgen.GenUIActionResumeDashboardPublication(),
			"rotate":  dashboardgen.GenUIActionRotateDashboardPublication(),
		},
		currentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-ui", DevBypass: true}, true
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/publications/command", nil)
	r.Header.Set("X-Request-ID", "ui-request-1")
	r.Header.Set("Idempotency-Key", "018f4f2e-0000-7000-8000-000000000011")
	r.Header.Set("If-Match", `"1"`)
	r.Header.Set(uicommand.HeaderOperationID, dashboardgen.GenUIActionSuspendDashboardPublication().OperationID())
	err := m.mutatePublication(r, uisignals.AdminPublicationCommand{ProjectID: "sales", Publication: "executive", Action: "suspend", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if service.invocation.Surface != string(apigencommand.SurfaceUI) || service.invocation.RequestID != "ui-request-1" || service.invocation.IdempotencyKey != "018f4f2e-0000-7000-8000-000000000011" || service.invocation.ExpectedRevision != 1 {
		t.Fatalf("invocation = %#v", service.invocation)
	}
}

func TestAdminPublicationRouteDurablyReplaysAndRechecksAuthorization(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "admin-publication-replay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	protocol, err := apiprotocol.Build(t.Context(), apiprotocol.Config{
		Store:         apiidempotencysqlite.NewStore(store.SQLDB()),
		CursorSigning: cursorsigning.NewEphemeralInitializer(),
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	service := &adminPublicationInvocationService{}
	m := &Module{
		publications: service, publicationCommands: map[string]uicommand.Binding{"suspend": dashboardgen.GenUIActionSuspendDashboardPublication()},
		currentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-ui"}, true },
		currentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
			if allowed {
				return []access.Capability{access.CapabilityResourcePublish}, nil
			}
			return nil, nil
		},
	}
	m.handler.PublicationMutation = m.mutatePublication
	router := chi.NewRouter()
	m.MountAuthenticated(router, RouteGuard{
		Authenticate:              func(next http.Handler) http.Handler { return next },
		RequirePlatformAdmin:      func(next http.Handler) http.Handler { return next },
		BrowserMutationMiddleware: protocol.BrowserMutationMiddleware,
	})
	body := `{"adminPublicationCommand":{"projectId":"sales","publication":"executive","action":"suspend","expectedRevision":1}}`
	request := func(requestID string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/admin/publications/command", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Request-ID", requestID)
		r.Header.Set("Idempotency-Key", "018f4f2e-0000-7000-8000-000000000921")
		r.Header.Set("If-Match", `"1"`)
		r.Header.Set(uicommand.HeaderOperationID, dashboardgen.GenUIActionSuspendDashboardPublication().OperationID())
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, r)
		return recorder
	}
	first := request("018f4f2e-0000-7000-8000-000000000920")
	if first.Code != http.StatusOK {
		t.Fatalf("first publication command = %d body=%s", first.Code, first.Body.String())
	}
	replay := request("018f4f2e-0000-7000-8000-000000000922")
	if replay.Code != http.StatusOK || replay.Header().Get("Idempotency-Replayed") != "true" || replay.Body.String() != first.Body.String() {
		t.Fatalf("publication replay = %d headers=%#v body=%s", replay.Code, replay.Header(), replay.Body.String())
	}
	if service.mutations != 1 {
		t.Fatalf("publication mutation calls = %d, want 1", service.mutations)
	}
	allowed = false
	denied := request("018f4f2e-0000-7000-8000-000000000923")
	if denied.Code != http.StatusForbidden || service.mutations != 1 {
		t.Fatalf("revoked publication replay = %d calls=%d body=%s", denied.Code, service.mutations, denied.Body.String())
	}
}

func TestCapabilityAllowedIntersectsSnapshotAndCredentialScope(t *testing.T) {
	allowed := true
	m := &Module{currentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
		if !allowed {
			return nil, nil
		}
		return []access.Capability{access.CapabilityResourcePublish}, nil
	}}
	scope, err := access.NewAuthoringScope("instance", "sales", []access.Capability{access.CapabilityResourcePublish})
	if err != nil {
		t.Fatal(err)
	}
	credential := access.APICredential{Authoring: &access.AuthoringSession{Scope: scope}}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	if ok, err := m.capabilityAllowed(r, "principal", "operations", access.CapabilityResourcePublish, credential, true); err != nil || ok {
		t.Fatalf("cross-project authoring credential allowed = %v, err=%v", ok, err)
	}
	if ok, err := m.capabilityAllowed(r, "principal", "sales", access.CapabilityResourcePublish, credential, true); err != nil || !ok {
		t.Fatalf("matching authoring credential allowed = %v, err=%v", ok, err)
	}
	allowed = false
	if ok, err := m.capabilityAllowed(r, "principal", "sales", access.CapabilityResourcePublish, credential, true); err != nil || ok {
		t.Fatalf("revoked authoring capability allowed = %v, err=%v", ok, err)
	}
}

func TestCapabilityAllowedPreservesTokenDynamicAndDenyAll(t *testing.T) {
	m := &Module{currentEffectiveCapabilities: func(context.Context, string) ([]access.Capability, error) {
		return []access.Capability{access.CapabilityResourcePublish}, nil
	}}
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	dynamic := access.APICredential{Token: access.APIToken{Capabilities: nil}}
	if ok, err := m.capabilityAllowed(r, "principal", "sales", access.CapabilityResourcePublish, dynamic, true); err != nil || !ok {
		t.Fatalf("dynamic token allowed = %v, err=%v", ok, err)
	}
	denyAll := access.APICredential{Token: access.APIToken{Capabilities: []access.Capability{}}}
	if ok, err := m.capabilityAllowed(r, "principal", "sales", access.CapabilityResourcePublish, denyAll, true); err != nil || ok {
		t.Fatalf("deny-all token allowed = %v, err=%v", ok, err)
	}
}

type adminPublicationInvocationService struct {
	invocation publication.CommandInvocation
	mutations  int
}

func (*adminPublicationInvocationService) PublicationsConfigured() bool { return true }
func (*adminPublicationInvocationService) AllPublications(context.Context) ([]publication.Publication, error) {
	return nil, nil
}
func (*adminPublicationInvocationService) PublicationEvents(context.Context, string) ([]publication.Event, error) {
	return nil, nil
}
func (*adminPublicationInvocationService) PublicationDTO(publication.Publication) dashboardapi.PublicationResponse {
	return dashboardapi.PublicationResponse{}
}
func (*adminPublicationInvocationService) MutatePublication(context.Context, string, string, string, publication.Action) (publication.Publication, error) {
	return publication.Publication{}, nil
}
func (s *adminPublicationInvocationService) MutatePublicationWithInvocation(_ context.Context, _ string, _ string, _ string, _ publication.Action, invocation publication.CommandInvocation) (publication.Publication, error) {
	s.invocation = invocation
	s.mutations++
	return publication.Publication{Revision: 2}, nil
}
