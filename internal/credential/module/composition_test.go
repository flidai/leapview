package module

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"testing"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/credential"
	platformbootstrap "github.com/flidai/leapview/internal/platform/bootstrap/postgres"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestCredentialBuildKeepsUnconfiguredSetupOptional(t *testing.T) {
	services, err := Build(t.Context(), Config{
		CustomerOwner: credentialOwnerReader{err: platformbootstrap.ErrNotFound},
	})
	if err != nil || services != nil {
		t.Fatalf("Build() = %v, %v; want optional nil services", services, err)
	}
}

func TestConnectionCredentialAuthorizerPassesOnlyExactConnectionPermissions(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	resource, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionConnectionManage, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	called := false
	authorizer := connectionCredentialAuthorizer{authorize: func(_ context.Context, actorID, project, connection string, action access.Action) (bool, error) {
		called = true
		if actorID != "actor" || project != projectID.String() || connection != connectionID.String() || action != access.ActionConnectionManage {
			t.Fatalf("authorization scope = %q %q %q %q", actorID, project, connection, action)
		}
		return true, nil
	}}
	if err := authorizer.RequirePermission(t.Context(), "actor", pair); err != nil || !called {
		t.Fatalf("RequirePermission() = %v; callback called=%v", err, called)
	}

	dashboard, err := access.NewResourceRef("dashboard_main", projectgraph.KindDashboard)
	if err != nil {
		t.Fatal(err)
	}
	otherPair, err := access.NewExactPermissionPair(access.ActionDashboardRead, projectID, dashboard)
	if err != nil {
		t.Fatal(err)
	}
	called = false
	if err := authorizer.RequirePermission(t.Context(), "actor", otherPair); !errors.Is(err, credential.ErrForbidden) || called {
		t.Fatalf("non-connection authorization = %v; callback called=%v", err, called)
	}

	denied := connectionCredentialAuthorizer{authorize: func(context.Context, string, string, string, access.Action) (bool, error) { return false, nil }}
	if err := denied.RequirePermission(t.Context(), "actor", pair); !errors.Is(err, credential.ErrForbidden) {
		t.Fatalf("denied connection authorization = %v, want forbidden", err)
	}
}

func TestValidationAuthorizerRechecksManageAndUseBeforeCurrentPermissions(t *testing.T) {
	projectID := projectgraph.ResourceID("project_one")
	connectionID := projectgraph.ResourceID("connection_one")
	resource, err := access.NewResourceRef(connectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	manage, err := access.NewExactPermissionPair(access.ActionConnectionManage, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	use, err := access.NewExactPermissionPair(access.ActionConnectionUse, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	var rechecked, authorized []access.Action
	authorizer := connectionCredentialAuthorizer{
		recheck: func(_ context.Context, actorID string, pair access.PermissionPair) error {
			if actorID != "actor" {
				t.Fatalf("rechecked actor = %q", actorID)
			}
			rechecked = append(rechecked, pair.Action)
			return nil
		},
		authorize: func(_ context.Context, actorID, project, connection string, action access.Action) (bool, error) {
			if actorID != "actor" || project != projectID.String() || connection != connectionID.String() {
				t.Fatalf("current authorization = %q %q %q %q", actorID, project, connection, action)
			}
			authorized = append(authorized, action)
			return true, nil
		},
	}
	if err := authorizer.RequirePermission(t.Context(), "actor", manage); err != nil {
		t.Fatalf("require manage: %v", err)
	}
	if err := authorizer.RequirePermission(t.Context(), "actor", use); err != nil {
		t.Fatalf("require use: %v", err)
	}
	want := []access.Action{access.ActionConnectionManage, access.ActionConnectionUse}
	if !slices.Equal(rechecked, want) || !slices.Equal(authorized, want) {
		t.Fatalf("rechecked=%v authorized=%v, want both exact prerequisites %v", rechecked, authorized, want)
	}

	rechecked, authorized = nil, nil
	authorizer.recheck = func(_ context.Context, _ string, pair access.PermissionPair) error {
		rechecked = append(rechecked, pair.Action)
		if pair.Action == access.ActionConnectionUse {
			return access.ErrForbidden
		}
		return nil
	}
	if err := authorizer.RequirePermission(t.Context(), "actor", manage); err != nil {
		t.Fatalf("require manage with current authority: %v", err)
	}
	if err := authorizer.RequirePermission(t.Context(), "actor", use); !errors.Is(err, access.ErrForbidden) {
		t.Fatalf("live use denial = %v, want forbidden", err)
	}
	if !slices.Equal(rechecked, want) || !slices.Equal(authorized, []access.Action{access.ActionConnectionManage}) {
		t.Fatalf("denied use rechecked=%v authorized=%v", rechecked, authorized)
	}
}

func TestCredentialBuildRejectsConfiguredOwnerWithoutKeyring(t *testing.T) {
	services, err := Build(t.Context(), Config{
		InstanceID:    "lvinst_0123456789abcdefghijklmnopqrstuv",
		KeyringPath:   filepath.Join(t.TempDir(), "missing-keyring.json"),
		CustomerOwner: credentialOwnerReader{owner: "customer:one"},
	})
	if services != nil || err == nil {
		t.Fatalf("Build() = %v, %v; want setup failure", services, err)
	}
	if errors.Is(err, platformbootstrap.ErrNotFound) {
		t.Fatalf("Build() unexpectedly treated configured owner without keyring as optional: %v", err)
	}
}
