package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	adminsettings "github.com/flidai/leapview/internal/admin/settings"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	apiprotocol "github.com/flidai/leapview/internal/app/api/protocol"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/dashboard/publication"
	"github.com/flidai/leapview/internal/platform/http/cursorsigning"
	apiidempotencypostgres "github.com/flidai/leapview/internal/platform/http/idempotency/postgres"
	operationpostgres "github.com/flidai/leapview/internal/platform/operation/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
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

func TestServiceAccountRenameSettingsCommandPersistsAcrossReload(t *testing.T) {
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	repository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("admin-module-settings-test-key", 2))})
	if err != nil {
		t.Fatal(err)
	}
	actor, err := repository.UpsertPrincipal(t.Context(), access.PrincipalInput{
		Kind: access.PrincipalKindUser, Email: "service-account-settings-admin@example.test", DisplayName: "Settings Admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	service, err := repository.CreateServicePrincipal(t.Context(), access.ServicePrincipalInput{DisplayName: "Before rename"})
	if err != nil {
		t.Fatal(err)
	}

	module, err := Build(t.Context(), Config{
		SettingsAccess: repository,
		CurrentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: actor.ID, DisplayName: actor.DisplayName}, true
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	passthrough := func(next http.Handler) http.Handler { return next }
	router := chi.NewRouter()
	module.MountAuthenticated(router, RouteGuard{Authenticate: passthrough, RequirePlatformAdmin: passthrough})

	request := httptest.NewRequest(http.MethodPost, "/admin/service-accounts/command", strings.NewReader(`{"adminServiceAccountCommand":{"action":"update","accountId":"`+service.ID+`","displayName":"After rename"}}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Request-ID", "service-account-rename-settings")
	request.Header.Set(uicommand.HeaderOperationID, accessgen.GenUIActionUpdateServicePrincipal().OperationID())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("rename command status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "After rename") {
		t.Fatalf("rename response did not expose refreshed signal: %s", recorder.Body.String())
	}

	reloaded, err := adminsettings.LoadServiceAccounts(t.Context(), repository, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Items) != 1 || reloaded.Items[0].DisplayName != "After rename" {
		t.Fatalf("reloaded service-account signal = %#v, want After rename", reloaded.Items)
	}
	events, err := repository.ListAuditEvents(t.Context(), access.AuditEventFilter{Action: "service_principal.updated", ResourceID: service.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PrincipalID != actor.ID || events[0].Status != "success" {
		t.Fatalf("rename audit events = %#v, want one successful event by %s", events, actor.ID)
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
		currentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:server-bound", nil
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/publications/command", nil)
	r.Header.Set("X-Request-ID", "ui-request-1")
	r.Header.Set("Idempotency-Key", "018f4f2e-0000-7000-8000-000000000011")
	r.Header.Set("If-Match", `"1"`)
	r.Header.Set(uicommand.HeaderOperationID, dashboardgen.GenUIActionSuspendDashboardPublication().OperationID())
	err := m.mutatePublication(r, uisignals.AdminPublicationCommand{Publication: "executive", Action: "suspend", ExpectedRevision: 1})
	if err != nil {
		t.Fatal(err)
	}
	if service.invocation.Surface != string(apigencommand.SurfaceUI) || service.invocation.RequestID != "ui-request-1" || service.invocation.IdempotencyKey != "018f4f2e-0000-7000-8000-000000000011" || service.invocation.ExpectedRevision != 1 {
		t.Fatalf("invocation = %#v", service.invocation)
	}
	if service.mutationProject != "project:server-bound" {
		t.Fatalf("mutation Project = %q, want authoritative server Project", service.mutationProject)
	}
}

func TestAdminPublicationRouteDurablyReplaysAndRechecksAuthorization(t *testing.T) {
	pool := postgrestest.Open(t, operationpostgres.ApplySchema)
	protocol, err := apiprotocol.Build(t.Context(), apiprotocol.Config{
		Store:         apiidempotencypostgres.NewStore(pool),
		CursorSigning: cursorsigning.NewEphemeralInitializer(),
		AuthoritativeScope: func(*http.Request) (apiprotocol.AuthoritativeScope, error) {
			return apiprotocol.AuthoritativeScope{TargetID: "target:test", ProjectID: "project:server-bound", Environment: "test", GenerationID: "generation:test"}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	allowed := true
	service := &adminPublicationInvocationService{}
	m := &Module{
		publications: service, publicationCommands: map[string]uicommand.Binding{"suspend": dashboardgen.GenUIActionSuspendDashboardPublication()},
		currentPrincipal: func(*http.Request) (Principal, bool) { return Principal{ID: "principal-ui"}, true },
		currentProjectID: func(context.Context) (projectgraph.ResourceID, error) { return "project:server-bound", nil },
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
	body := `{"adminPublicationCommand":{"publication":"executive","action":"suspend","expectedRevision":1}}`
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
	if service.mutationProject != "project:server-bound" {
		t.Fatalf("mutation Project = %q, want authoritative server Project", service.mutationProject)
	}
	allowed = false
	denied := request("018f4f2e-0000-7000-8000-000000000923")
	if denied.Code != http.StatusForbidden || service.mutations != 1 {
		t.Fatalf("revoked publication replay = %d calls=%d body=%s", denied.Code, service.mutations, denied.Body.String())
	}
}

func TestAdminPublicationRouteRejectsClientProjectSelectorsBeforeIdempotency(t *testing.T) {
	service := &adminPublicationInvocationService{}
	m := &Module{
		publications:        service,
		publicationCommands: map[string]uicommand.Binding{"suspend": dashboardgen.GenUIActionSuspendDashboardPublication()},
		currentPrincipal:    func(*http.Request) (Principal, bool) { return Principal{ID: "principal-ui", DevBypass: true}, true },
		currentProjectID:    func(context.Context) (projectgraph.ResourceID, error) { return "project:server-bound", nil },
	}
	m.handler.PublicationMutation = m.mutatePublication
	router := chi.NewRouter()
	m.MountAuthenticated(router, RouteGuard{
		Authenticate:         func(next http.Handler) http.Handler { return next },
		RequirePlatformAdmin: func(next http.Handler) http.Handler { return next },
		BrowserMutationMiddleware: func(_ func(*http.Request) bool, next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				t.Fatal("client Project selector reached durable idempotency")
				next.ServeHTTP(w, r)
			})
		},
	})
	for _, selector := range []string{"projectId", "project_id", "projectUid", "PROJECT-ID"} {
		body := `{"adminPublicationCommand":{"` + selector + `":"project:foreign","publication":"executive","action":"suspend","expectedRevision":1}}`
		request := httptest.NewRequest(http.MethodPost, "/admin/publications/command", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		router.ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || service.mutations != 0 {
			t.Fatalf("selector %q: status=%d mutations=%d body=%s", selector, response.Code, service.mutations, response.Body.String())
		}
		if strings.Contains(response.Body.String(), "project:foreign") {
			t.Fatalf("selector %q value leaked into response", selector)
		}
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

func TestAdminPublicationsDoNotDiscloseForeignProjectRows(t *testing.T) {
	service := &adminPublicationInvocationService{publications: []publication.Publication{
		{ID: "publication-local", ProjectID: "project:test", Name: "local"},
		{ID: "publication-foreign", ProjectID: "project:foreign", Name: "foreign"},
	}}
	m := &Module{
		publications: service,
		currentProjectID: func(context.Context) (projectgraph.ResourceID, error) {
			return "project:test", nil
		},
		currentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-admin", DevBypass: true}, true
		},
	}

	rows, allowed, err := m.adminPublications(httptest.NewRequest(http.MethodGet, "/admin/publications", nil))
	if err != nil {
		t.Fatal(err)
	}
	if service.requestedProject != "project:test" {
		t.Fatalf("publication repository Project = %q", service.requestedProject)
	}
	if !allowed || len(rows) != 1 || rows[0].ProjectID != "project:test" || rows[0].Name != "local" {
		t.Fatalf("publications = %#v, allowed=%v", rows, allowed)
	}
}

type adminPublicationInvocationService struct {
	invocation       publication.CommandInvocation
	mutations        int
	mutationProject  string
	publications     []publication.Publication
	requestedProject projectgraph.ResourceID
}

func (*adminPublicationInvocationService) PublicationsConfigured() bool { return true }
func (s *adminPublicationInvocationService) ProjectPublications(_ context.Context, projectID projectgraph.ResourceID) ([]publication.Publication, error) {
	s.requestedProject = projectID
	return append([]publication.Publication(nil), s.publications...), nil
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
func (s *adminPublicationInvocationService) MutatePublicationWithInvocation(_ context.Context, projectID string, _ string, _ string, _ publication.Action, invocation publication.CommandInvocation) (publication.Publication, error) {
	s.invocation = invocation
	s.mutationProject = projectID
	s.mutations++
	return publication.Publication{Revision: 2}, nil
}
