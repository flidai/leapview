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
	if err != nil || granted == base {
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
