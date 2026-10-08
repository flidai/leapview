package app

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
)

func TestDevelopmentProfileBootstrapBindingRequiresExactAdmittedScope(t *testing.T) {
	marker := accessmodule.BootstrapAuthorization{ProjectID: "project:demo", PrincipalID: "owner", Capability: access.CapabilityResourceManage}
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
