package accessadapter

import (
	"context"
	"errors"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	"github.com/flidai/leapview/internal/dashboard/authoring"
	"github.com/flidai/leapview/internal/dashboard/authoring/service"
)

type authorizationCall struct {
	actor     string
	privilege access.Privilege
	object    access.ObjectRef
}

func TestAuthorizeMapsEveryActionToScopedDashboardPrivilege(t *testing.T) {
	tests := []struct {
		action    authoring.AuthorizationAction
		privilege access.Privilege
	}{
		{authoring.AuthorizationActionView, access.PrivilegeViewItem},
		{authoring.AuthorizationActionEdit, access.PrivilegeEditItem},
		{authoring.AuthorizationActionPublish, access.PrivilegeManageItem},
		{authoring.AuthorizationActionArchive, access.PrivilegeManageItem},
	}
	for _, test := range tests {
		t.Run(string(test.action), func(t *testing.T) {
			var calls []authorizationCall
			adapter, err := New(func(_ context.Context, actor string, privilege access.Privilege, object access.ObjectRef) (bool, error) {
				calls = append(calls, authorizationCall{actor: actor, privilege: privilege, object: object})
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.Authorize(t.Context(), service.AuthorizationRequest{
				ActorID: " actor-1 ", ProjectID: " project-1 ", DashboardID: "dashboard-1",
				OwnerPrincipalID: "owner-not-an-authorization-input", SemanticModel: "semantic-not-an-authorization-input", Action: test.action,
			})
			if err != nil {
				t.Fatalf("Authorize() error = %v", err)
			}
			if len(calls) != 1 {
				t.Fatalf("authorization calls = %d, want 1", len(calls))
			}
			call := calls[0]
			if call.actor != "actor-1" || call.privilege != test.privilege {
				t.Fatalf("authorization call = %#v, want actor and privilege %q/%q", call, "actor-1", test.privilege)
			}
			wantObject := access.ItemObject(access.SecurableDashboard, "project-1", "dashboard-1")
			if call.object != wantObject {
				t.Fatalf("object = %#v, want %#v", call.object, wantObject)
			}
		})
	}
}

func TestAuthorizeCreationUsesSuppliedDashboardID(t *testing.T) {
	var object access.ObjectRef
	adapter, err := New(func(_ context.Context, _ string, _ access.Privilege, got access.ObjectRef) (bool, error) {
		object = got
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Authorize(t.Context(), service.AuthorizationRequest{
		ActorID: "actor", ProjectID: "project", DashboardID: authoring.DashboardID("allocated-dashboard"), Action: authoring.AuthorizationActionEdit,
	}); err != nil {
		t.Fatal(err)
	}
	if object.CanonicalID() != "dashboard:project:allocated-dashboard" {
		t.Fatalf("creation-time object = %#v", object)
	}
}

func TestAuthorizeDeniedDecisionIsForbidden(t *testing.T) {
	adapter, err := New(func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error) { return false, nil })
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Authorize(t.Context(), validRequest())
	if !errors.Is(err, access.ErrForbidden) || !errors.Is(err, accessmodule.ErrForbidden) {
		t.Fatalf("denied error = %v, want canonical access forbidden", err)
	}
}

func TestAuthorizePreservesDecisionErrors(t *testing.T) {
	backendErr := errors.New("authorization backend unavailable")
	adapter, err := New(func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error) {
		return false, backendErr
	})
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Authorize(t.Context(), validRequest())
	if !errors.Is(err, backendErr) {
		t.Fatalf("backend error = %v, want %v", err, backendErr)
	}
	if errors.Is(err, access.ErrForbidden) {
		t.Fatalf("backend error was collapsed into forbidden: %v", err)
	}
}

func TestNewAndAuthorizeRejectInvalidInputs(t *testing.T) {
	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) succeeded")
	}
	calls := 0
	adapter, err := New(func(context.Context, string, access.Privilege, access.ObjectRef) (bool, error) {
		calls++
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]service.AuthorizationRequest{
		"missing actor":     {ProjectID: "project", DashboardID: "dashboard", Action: authoring.AuthorizationActionView},
		"missing project":   {ActorID: "actor", DashboardID: "dashboard", Action: authoring.AuthorizationActionView},
		"missing dashboard": {ActorID: "actor", ProjectID: "project", Action: authoring.AuthorizationActionView},
		"invalid dashboard": {ActorID: "actor", ProjectID: "project", DashboardID: "bad id", Action: authoring.AuthorizationActionView},
		"invalid action":    {ActorID: "actor", ProjectID: "project", DashboardID: "dashboard", Action: "unknown"},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			err := adapter.Authorize(t.Context(), request)
			if err == nil {
				t.Fatal("Authorize() succeeded")
			}
		})
	}
	if calls != 0 {
		t.Fatalf("authorization calls = %d, want 0 for invalid requests", calls)
	}
}

func validRequest() service.AuthorizationRequest {
	return service.AuthorizationRequest{
		ActorID: "actor", ProjectID: "project", DashboardID: "dashboard", Action: authoring.AuthorizationActionView,
	}
}
