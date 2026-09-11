package access

import (
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
}
