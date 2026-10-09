package app

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDevelopmentProfileBootstrapUsesOuterProjectActionForMetadata(t *testing.T) {
	binding := analyticsmodule.ConnectionTargetBinding{Scope: analyticsmodule.ConnectionBindingScope{ProjectID: "project:demo", Environment: "development"}, TargetID: "local-target", ConnectionID: "connection:warehouse"}
	settings, err := access.NewProjectPermissionPair(access.ActionProjectSettingsUpdate, binding.Scope.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	use, err := access.NewExactPermissionPair(access.ActionConnectionUse, binding.Scope.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	base := access.APICredential{Principal: access.Principal{ID: "owner"}, Token: access.APIToken{ID: "token", PrincipalID: "owner", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{settings, use}}}
	for _, authoring := range []bool{false, true} {
		for _, scenario := range []string{"exact", "missing-settings", "missing-use", "foreign-principal", "foreign-project", "foreign-connection", "foreign-authoring-target", "invalid-profile", "invalid-permissions"} {
			t.Run(scenario, func(t *testing.T) {
				credential, requested := base, binding
				if authoring {
					credential.Authoring = &access.AuthoringSession{Scope: access.AuthoringScope{TargetID: "local-target", ProjectID: binding.Scope.ProjectID, Permissions: []access.PermissionPair{settings, use}}}
				}
				switch scenario {
				case "missing-settings":
					credential.Token.Permissions = []access.PermissionPair{use}
					if authoring {
						credential.Authoring.Scope.Permissions = []access.PermissionPair{use}
					}
				case "missing-use":
					credential.Token.Permissions = []access.PermissionPair{settings}
					if authoring {
						credential.Authoring.Scope.Permissions = []access.PermissionPair{settings}
					}
				case "foreign-principal":
					credential.Token.PrincipalID = "other"
				case "foreign-project":
					requested.Scope.ProjectID = "project:other"
				case "foreign-connection":
					requested.ConnectionID = "connection:other"
				case "foreign-authoring-target":
					if !authoring {
						return
					}
					credential.Authoring.Scope.TargetID = "other-target"
				case "invalid-profile":
					if authoring {
						return
					}
					credential.Token.PermissionProfile = "unknown"
				case "invalid-permissions":
					if authoring {
						return
					}
					credential.Token.Permissions = []access.PermissionPair{{Action: access.ActionProjectSettingsUpdate}}
				}
				wantMetadata := scenario == "exact" || scenario == "missing-use" || scenario == "foreign-connection"
				wantUse := scenario == "exact" || scenario == "missing-settings"
				if got := developmentProfileBootstrapCredentialAllows(credential, "owner", analyticsmodule.PermissionManageConnectionMetadata, requested, "local-target"); got != wantMetadata {
					t.Fatalf("authoring=%v metadata=%v want=%v", authoring, got, wantMetadata)
				}
				if got := developmentProfileBootstrapCredentialAllows(credential, "owner", analyticsmodule.PermissionUseConnection, requested, "local-target"); got != wantUse {
					t.Fatalf("authoring=%v use=%v want=%v", authoring, got, wantUse)
				}
				if bootstrapBindingCredentialAllows(credential, "owner", requested, "local-target", access.ActionConnectionManage) {
					t.Fatal("profile application conferred ordinary connection.manage")
				}
			})
		}
	}
}

func TestDevelopmentProfileBootstrapBindingRequiresExactAdmittedScope(t *testing.T) {
	marker := accessmodule.BootstrapAuthorization{ProjectID: "project:demo", PrincipalID: "owner", Capability: access.CapabilityResourceManage}
	if !developmentProfileBootstrapOperationMatches(false, "applyDevelopmentProfile", marker) {
		t.Fatal("admitted development profile operation was denied")
	}
	for _, operation := range []string{"", "createTargetConnectionBinding", "applyDevelopmentProfile ", "createDeliveryPlan", "buildDeliveryPlan"} {
		if developmentProfileBootstrapOperationMatches(false, operation, marker) {
			t.Fatalf("unrelated operation %q inherited profile metadata authority", operation)
		}
	}
	if developmentProfileBootstrapOperationMatches(true, "applyDevelopmentProfile", marker) {
		t.Fatal("production accepted development profile bootstrap authority")
	}
	binding := analyticsmodule.ConnectionTargetBinding{Scope: analyticsmodule.ConnectionBindingScope{ProjectID: marker.ProjectID, Environment: "development"}, TargetID: "local-target"}
	for _, permission := range []analyticsmodule.ConnectionAdministrationPermission{analyticsmodule.PermissionManageConnectionMetadata, analyticsmodule.PermissionUseConnection} {
		if !developmentProfileBootstrapBindingMatches(marker, "owner", permission, binding, "local-target", "development") {
			t.Fatal("exact admitted profile binding was denied")
		}
	}
	for _, scenario := range []string{"principal", "project", "capability", "target", "environment", "permission"} {
		t.Run(scenario, func(t *testing.T) {
			changed := marker
			target, environment := "local-target", "development"
			permission := analyticsmodule.PermissionManageConnectionMetadata
			switch scenario {
			case "principal":
				changed.PrincipalID = "other"
			case "project":
				changed.ProjectID = "project:other"
			case "capability":
				changed.Capability = access.CapabilityProjectAdmin
			case "target":
				target = "other-target"
			case "environment":
				environment = "production"
			case "permission":
				permission = analyticsmodule.PermissionViewConnectionHealth
			}
			if developmentProfileBootstrapBindingMatches(changed, "owner", permission, binding, target, environment) {
				t.Fatal("mismatched bootstrap authority was accepted")
			}
		})
	}
	for _, production := range []bool{false, true} {
		if _, admitted := developmentProfileBootstrapAuthority(t.Context(), production); admitted {
			t.Fatal("request without opaque bootstrap authorization was admitted")
		}
	}
}
