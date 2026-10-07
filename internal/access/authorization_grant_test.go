package access

import (
	"strings"
	"testing"

	"github.com/flidai/leapview/internal/project/graph"
)

func TestAuthorizationGrantDigestPreservesLegacyAndBindsExactResource(t *testing.T) {
	scope := AuthorizationPolicyScope{TargetID: "target", ProjectID: "project", Environment: "prod"}
	base, err := AuthorizationPolicyDigest(scope, nil)
	if err != nil {
		t.Fatal(err)
	}
	if base != "sha256:4b3623fb0cd1608bf05979f8bdb00d2bb2812c79c9db6a22b8dc5940f87b1e2a" {
		t.Fatal("changed pre-grant policy digest", base)
	}
	empty, err := AuthorizationPolicyDigest(scope, nil, []AuthorizationGrant{}...)
	if err != nil || base != empty {
		t.Fatal("empty grants changed legacy digest", err)
	}
	resource, _ := NewResourceRef("dashboard:sales", graph.KindDashboard)
	grant := AuthorizationGrant{ID: "sales-read", Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "demo"}, Resource: resource, Capability: CapabilityResourceRead}
	granted, err := AuthorizationPolicyDigest(scope, nil, grant)
	if err != nil {
		t.Fatal(err)
	}
	// Golden digest from the pre-typed AuthorizationGrant wire shape. Keep this
	// fixed so adding typed grant fields cannot silently rewrite persisted
	// legacy target-policy identities.
	if granted != "sha256:57b3a07eaf7c0dadc055535bd9f429cd6d60ca455b9f309382e1a9f9784c6685" {
		t.Fatalf("changed pre-typed legacy-grant digest: %s", granted)
	}
	if granted == base {
		t.Fatal("grant missing from digest", err)
	}
	grant.Resource, _ = NewResourceRef("dashboard:other", graph.KindDashboard)
	other, err := AuthorizationPolicyDigest(scope, nil, grant)
	if err != nil || other == granted {
		t.Fatal("resource missing from digest", err)
	}
	if _, err := AuthorizationPolicyDigest(scope, nil, grant, grant); err == nil {
		t.Fatal("accepted duplicate")
	}
	grant.Capability = CapabilityProjectAdmin
	if err := ValidateAuthorizationGrant(grant); err == nil {
		t.Fatal("accepted project admin on dashboard")
	}
	grant.ID = strings.Repeat("x", 256)
	if err := ValidateAuthorizationGrant(grant); err == nil {
		t.Fatal("accepted oversized id")
	}
}

func TestTypedAuthorizationGrantIsExactAndProjectBound(t *testing.T) {
	projectID, err := graph.NewResourceID("project")
	if err != nil {
		t.Fatal(err)
	}
	resource, err := NewResourceRef("pipeline_refresh", graph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := NewExactPermissionPair(ActionPipelineRun, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	grant := AuthorizationGrant{
		ID: "refresh-runner", Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "principal"}, Resource: resource,
		PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{pair},
	}
	if err := ValidateAuthorizationGrantForScope(grant, AuthorizationPolicyScope{TargetID: "target", ProjectID: projectID.String(), Environment: "prod"}); err != nil {
		t.Fatalf("valid typed exact grant rejected: %v", err)
	}
	if err := ValidateAuthorizationGrantForScope(grant, AuthorizationPolicyScope{TargetID: "target", ProjectID: "another-project", Environment: "prod"}); err == nil {
		t.Fatal("accepted typed permission for another project")
	}

	bad := grant
	bad.Capability = CapabilityResourceUse
	if err := ValidateAuthorizationGrant(bad); err == nil {
		t.Fatal("accepted mixed legacy and typed grant")
	}
	bad = grant
	bad.Permissions = nil
	if err := ValidateAuthorizationGrant(bad); err == nil {
		t.Fatal("accepted omitted typed permission set")
	}
	bad = grant
	other, _ := NewResourceRef("pipeline_other", graph.KindPipeline)
	bad.Permissions = []PermissionPair{{Action: ActionPipelineRun, Profile: PermissionCatalogProfile, Target: PermissionTarget{Scope: PermissionScopeResource, ProjectID: projectID, ResourceKind: graph.KindPipeline, ResourceID: other.ID()}}}
	if err := ValidateAuthorizationGrant(bad); err == nil {
		t.Fatal("accepted typed permission for a different resource")
	}
	projectAction, err := NewProjectPermissionPair(ActionDeliveryRead, projectID)
	if err != nil {
		t.Fatal(err)
	}
	bad = grant
	bad.Permissions = []PermissionPair{projectAction}
	if err := ValidateAuthorizationGrant(bad); err == nil {
		t.Fatal("accepted project-scoped action in an exact resource grant")
	}
}

func TestTypedAuthorizationGrantDigestCanonicalizesPairsAndRejectsScopeDrift(t *testing.T) {
	scope := AuthorizationPolicyScope{TargetID: "target", ProjectID: "project", Environment: "prod"}
	projectID := graph.ResourceID(scope.ProjectID)
	resource, _ := NewResourceRef("pipeline_refresh", graph.KindPipeline)
	run, _ := NewExactPermissionPair(ActionPipelineRun, projectID, resource)
	read, _ := NewExactPermissionPair(ActionPipelineRead, projectID, resource)
	grant := AuthorizationGrant{ID: "refresh", Subject: SubjectRef{Kind: SubjectKindPrincipal, ID: "principal"}, Resource: resource, PermissionProfile: PermissionCatalogProfile, Permissions: []PermissionPair{run, read}}
	forward, err := AuthorizationPolicyDigest(scope, nil, grant)
	if err != nil {
		t.Fatal(err)
	}
	grant.Permissions = []PermissionPair{read, run}
	reverse, err := AuthorizationPolicyDigest(scope, nil, grant)
	if err != nil || reverse != forward {
		t.Fatalf("typed pair order changed policy digest: %q %q %v", forward, reverse, err)
	}
	grant.Permissions[0].Target.ProjectID = "other-project"
	if _, err := AuthorizationPolicyDigest(scope, nil, grant); err == nil {
		t.Fatal("digest accepted typed authority outside policy project")
	}
}
