package sqlite

import (
	"testing"

	"github.com/flidai/leapview/internal/access"
)

func TestPrincipalIdentityManagementDistinguishesLocalAndExternalOwnership(t *testing.T) {
	_, repository := openAccessRepo(t, t.Context())
	local, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{
		Email: "local-settings@example.com", DisplayName: "Local Settings",
	})
	if err != nil {
		t.Fatal(err)
	}
	localManagement, err := repository.PrincipalIdentityManagement(t.Context(), local.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if localManagement.Source != access.IdentityManagementLocal || !localManagement.HasLocalPassword || localManagement.Provider != "" {
		t.Fatalf("local identity management = %#v", localManagement)
	}

	external, err := repository.ResolveExternalPrincipal(t.Context(), access.ExternalIdentityInput{
		Provider: "azure", TenantID: "tenant", Subject: "subject", Email: "external-settings@example.com", DisplayName: "External Settings",
	})
	if err != nil {
		t.Fatal(err)
	}
	externalManagement, err := repository.PrincipalIdentityManagement(t.Context(), external.ID)
	if err != nil {
		t.Fatal(err)
	}
	if externalManagement.Source != access.IdentityManagementExternal || externalManagement.HasLocalPassword || externalManagement.Provider != "azure" {
		t.Fatalf("external identity management = %#v", externalManagement)
	}
}

func TestPrincipalIdentityManagementKeepsExternalProfileOwnershipWhenLocalCredentialIsLinked(t *testing.T) {
	_, repository := openAccessRepo(t, t.Context())
	local, err := repository.CreateLocalUser(t.Context(), access.LocalUserInput{
		Email: "linked-settings@example.com", DisplayName: "Linked Settings",
	})
	if err != nil {
		t.Fatal(err)
	}
	linked, err := repository.ResolveExternalPrincipal(t.Context(), access.ExternalIdentityInput{
		Provider: "scim", TenantID: "tenant", Subject: "linked", Email: local.Principal.Email, DisplayName: "Provider Name",
	})
	if err != nil {
		t.Fatal(err)
	}
	management, err := repository.PrincipalIdentityManagement(t.Context(), linked.ID)
	if err != nil {
		t.Fatal(err)
	}
	if management.Source != access.IdentityManagementExternal || management.Provider != "scim" || !management.HasLocalPassword {
		t.Fatalf("linked identity management = %#v", management)
	}
}
