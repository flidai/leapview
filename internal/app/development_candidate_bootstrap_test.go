package app

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	analyticsmodule "github.com/flidai/leapview/internal/analytics/module"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func TestDevelopmentCandidateBootstrapRequiresExactSourcePermission(t *testing.T) {
	marker := accessmodule.BootstrapAuthorization{ProjectID: "project:demo", PrincipalID: "owner", Capability: access.CapabilityResourceRead}
	binding := analyticsmodule.ConnectionTargetBinding{Scope: analyticsmodule.ConnectionBindingScope{ProjectID: marker.ProjectID, Environment: "development"}, TargetID: "local-target", ConnectionID: "connection:warehouse"}
	resource, err := access.NewResourceRef(binding.ConnectionID, projectgraph.KindConnection)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionConnectionUse, marker.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	base := access.APICredential{Principal: access.Principal{ID: "owner"}, Token: access.APIToken{ID: "token", PrincipalID: "owner", PermissionProfile: access.PermissionCatalogProfile, Permissions: []access.PermissionPair{pair}}}
	if bootstrapBindingCredentialAllows(base, "owner", binding, "local-target", access.ActionConnectionManage) {
		t.Fatal("source-use credential was broadened to source management")
	}
	for _, authoring := range []bool{false, true} {
		for _, scenario := range []string{"exact", "build", "wrong-operation", "wrong-capability", "wrong-principal", "wrong-project", "wrong-target", "wrong-environment", "wrong-connection", "missing-source-use", "foreign-credential", "foreign-authoring-target"} {
			t.Run(scenario, func(t *testing.T) {
				m, credential := marker, base
				requested := binding
				operation, target, environment := "createDeliveryPlan", "local-target", "development"
				if authoring {
					credential.Authoring = &access.AuthoringSession{Scope: access.AuthoringScope{TargetID: target, ProjectID: marker.ProjectID, Permissions: []access.PermissionPair{pair}}}
				}
				switch scenario {
				case "build":
					operation, m.Capability = "buildDeliveryPlan", access.CapabilityResourceUse
				case "wrong-operation":
					operation = "publishDeliveryCandidate"
				case "wrong-capability":
					m.Capability = access.CapabilityProjectAdmin
				case "wrong-principal":
					m.PrincipalID = "other"
				case "wrong-project":
					m.ProjectID = "project:other"
				case "wrong-target":
					target = "other-target"
				case "wrong-environment":
					environment = "production"
				case "wrong-connection":
					requested.ConnectionID = "connection:other"
				case "missing-source-use":
					credential.Token.Permissions = nil
					if authoring {
						credential.Authoring.Scope.Permissions = nil
					}
				case "foreign-credential":
					credential.Token.PrincipalID = "other"
				case "foreign-authoring-target":
					if !authoring {
						return
					}
					credential.Authoring.Scope.TargetID = "other-target"
				}
				want := scenario == "exact" || scenario == "build"
				if got := developmentCandidateBootstrapBindingMatches(m, operation, credential, "owner", requested, target, environment); got != want {
					t.Fatalf("authoring=%v allowed=%v want=%v", authoring, got, want)
				}
			})
		}
	}
	for _, production := range []bool{false, true} {
		for _, profile := range []bool{false, true} {
			if developmentCandidateBootstrapAuthorized(t.Context(), production, profile, "owner", binding, "local-target", "development") {
				t.Fatal("candidate request without opaque admitted context was authorized")
			}
		}
	}
}
