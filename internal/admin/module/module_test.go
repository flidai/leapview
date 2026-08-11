package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	apigencommand "github.com/Yacobolo/toolbelt/apigen/runtime/command"
	uisignals "github.com/flidai/leapview/internal/admin/ui/signals"
	dashboardapi "github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/publication"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
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
		currentPrincipal: func(*http.Request) (Principal, bool) {
			return Principal{ID: "principal-ui", DevBypass: true}, true
		},
	}
	r := httptest.NewRequest(http.MethodPost, "/admin/publications/command", nil)
	r.Header.Set("X-Request-ID", "ui-request-1")
	err := m.mutatePublication(r, uisignals.AdminPublicationCommand{WorkspaceID: "sales", Publication: "executive", Action: "suspend"})
	if err != nil {
		t.Fatal(err)
	}
	if service.invocation.Surface != apigencommand.SurfaceUI || service.invocation.RequestID != "ui-request-1" || service.invocation.IdempotencyKey != "ui-request-1" || service.invocation.TargetValues["workspace"] != "sales" {
		t.Fatalf("invocation = %#v", service.invocation)
	}
}

type adminPublicationInvocationService struct {
	invocation apigencommand.Invocation
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
func (s *adminPublicationInvocationService) MutatePublicationWithInvocation(_ context.Context, _ string, _ string, _ string, _ publication.Action, invocation apigencommand.Invocation) (publication.Publication, error) {
	s.invocation = invocation
	return publication.Publication{}, nil
}
