package access

import (
	"errors"
	"strings"
	"testing"
)

func testAuthorizationRoleBinding(id, subject string) RoleBinding {
	return RoleBinding{ID: id, Name: id, Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: subject}, Role: ProjectRoleViewer, Capabilities: ProjectRoleCapabilities(ProjectRoleViewer)}
}

func TestAuthorizationPolicyDigestIsQualifiedAndOrderIndependent(t *testing.T) {
	scope := AuthorizationPolicyScope{TargetID: "target-a", ProjectID: "project-a", Environment: "production"}
	first := testAuthorizationRoleBinding("binding-a", "principal-a")
	second := testAuthorizationRoleBinding("binding-b", "principal-b")
	digest, err := AuthorizationPolicyDigest(scope, []RoleBinding{second, first})
	if err != nil {
		t.Fatal(err)
	}
	reordered, err := AuthorizationPolicyDigest(scope, []RoleBinding{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if digest != reordered || !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("policy digest changed with order or has invalid shape: %q vs %q", digest, reordered)
	}
	otherScope, err := AuthorizationPolicyDigest(AuthorizationPolicyScope{TargetID: "target-b", ProjectID: scope.ProjectID, Environment: scope.Environment}, []RoleBinding{first, second})
	if err != nil {
		t.Fatal(err)
	}
	if digest == otherScope {
		t.Fatal("policy digest omitted target scope")
	}
}

func TestAuthorizationPolicyDigestRejectsInvalidRoleBinding(t *testing.T) {
	scope := AuthorizationPolicyScope{TargetID: "target", ProjectID: "project", Environment: "prod"}
	invalid := testAuthorizationRoleBinding("binding", "principal")
	invalid.Capabilities = []Capability{CapabilityResourceRead}
	if _, err := AuthorizationPolicyDigest(scope, []RoleBinding{invalid}); err == nil {
		t.Fatal("accepted non-canonical role capability bundle")
	}
	if _, err := AuthorizationPolicyDigest(scope, []RoleBinding{testAuthorizationRoleBinding("binding", "principal"), testAuthorizationRoleBinding("binding", "other")}); err == nil {
		t.Fatal("accepted duplicate role binding id")
	}
	if _, err := AuthorizationPolicyDigest(scope, []RoleBinding{{ID: "binding", Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "principal"}, Role: ProjectRole("unknown")}}); err == nil {
		t.Fatal("accepted unknown role")
	}
	invalid = testAuthorizationRoleBinding("binding", "principal")
	invalid.Name = " Legacy administrator "
	if _, err := AuthorizationPolicyDigest(scope, []RoleBinding{invalid}); err == nil {
		t.Fatal("accepted non-canonical target-policy role binding name")
	}
}

func TestValidateAuthorizationRoleBindingRemovalProtectsLastProjectAdmin(t *testing.T) {
	viewer := testAuthorizationRoleBinding("viewer", "principal-viewer")
	admin := viewer
	admin.ID = "admin"
	admin.Subject.ID = "principal-admin"
	admin.Role = ProjectRoleAdmin
	admin.Capabilities = ProjectRoleCapabilities(ProjectRoleAdmin)
	owner := admin
	owner.ID = "owner"
	owner.Subject.ID = "principal-owner"
	owner.Role = ProjectRoleOwner

	if err := ValidateAuthorizationRoleBindingRemoval([]RoleBinding{admin, viewer}, admin.ID); !errors.Is(err, ErrAuthorizationPolicyConflict) {
		t.Fatalf("removing administrator with only viewer remaining = %v, want ErrAuthorizationPolicyConflict", err)
	}
	if err := ValidateAuthorizationRoleBindingRemoval([]RoleBinding{admin, owner}, admin.ID); err != nil {
		t.Fatalf("removing one of two administrators = %v, want allowed", err)
	}
	if err := ValidateAuthorizationRoleBindingRemoval([]RoleBinding{admin}, admin.ID); !errors.Is(err, ErrAuthorizationPolicyConflict) {
		t.Fatalf("removing last administrator error = %v, want ErrAuthorizationPolicyConflict", err)
	}
	if err := ValidateAuthorizationRoleBindingRemoval([]RoleBinding{viewer}, "missing"); !errors.Is(err, ErrAuthorizationPolicyNotFound) {
		t.Fatalf("removing missing binding error = %v, want ErrAuthorizationPolicyNotFound", err)
	}
}
