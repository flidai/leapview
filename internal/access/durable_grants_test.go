package access

import (
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

func durableGrantTestTarget(t *testing.T) DurableGrantTarget {
	t.Helper()
	return DurableGrantTarget{
		InstanceID:   "instance_test",
		ProjectID:    "project_test",
		ResourceUID:  "00000000-0000-7000-8000-000000000001",
		ResourceID:   "dashboard_test",
		ResourceKind: projectgraph.KindDashboard,
	}
}

func durableGrantTestIssuer() GrantIssuerEvidence {
	return GrantIssuerEvidence{
		PrincipalID: "00000000-0000-7000-8000-000000000010",
		Credential:  GrantCredentialEvidence{Class: GrantCredentialClassAPIToken, ID: "00000000-0000-7000-8000-000000000011", Fingerprint: strings.Repeat("a", 64)},
	}
}

func TestResourceShareGrantRequiresExplicitIssuerCeilingAndExactPairs(t *testing.T) {
	target := durableGrantTestTarget(t)
	resource, err := NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	share, err := NewExactPermissionPair(ActionResourceShare, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	read, err := NewExactPermissionPair(ActionDashboardRead, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	base := ResourceShareGrantInput{
		Target: target, Issuer: durableGrantTestIssuer(), RecipientPrincipalID: "00000000-0000-7000-8000-000000000012",
		Permissions: []PermissionPair{read}, IdempotencyKey: "share-1",
		IssuancePermissions: []PermissionPair{share, read},
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("valid share input: %v", err)
	}
	withoutCeiling := base
	withoutCeiling.IssuancePermissions = nil
	if err := withoutCeiling.Validate(); err == nil || !strings.Contains(err.Error(), "issuance authority") {
		t.Fatalf("missing issuer ceiling error = %v", err)
	}
	foreign, err := NewExactPermissionPair(ActionDashboardRead, "project_other", resource)
	if err == nil {
		foreignInput := base
		foreignInput.Permissions = []PermissionPair{foreign}
		if err := foreignInput.Validate(); err == nil {
			t.Fatal("foreign exact pair unexpectedly accepted")
		}
	}
	first, err := ResourceShareGrantFingerprint(base, time.Unix(100, 0), time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	second := base
	second.ID = "different-retry-generated-id"
	secondFingerprint, err := ResourceShareGrantFingerprint(second, time.Unix(100, 0), time.Unix(200, 0))
	if err != nil {
		t.Fatal(err)
	}
	if first != secondFingerprint {
		t.Fatalf("retry fingerprint changed with generated ID: %q != %q", first, secondFingerprint)
	}
}

func TestDurableGrantCredentialAndExecutionBounds(t *testing.T) {
	issuer := durableGrantTestIssuer()
	issuer.Credential.Class = GrantCredentialClassSession
	if err := issuer.Validate(); err != nil {
		t.Fatal(err)
	}
	issuer.Credential.Class = "service_secret"
	if err := issuer.Validate(); err == nil {
		t.Fatal("service-secret grant issuer unexpectedly accepted")
	}
	target := durableGrantTestTarget(t)
	target.ResourceKind = projectgraph.KindPipeline
	target.ResourceID = "pipeline_test"
	resource, err := NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	run, err := NewExactPermissionPair(ActionPipelineRun, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := NewExactPermissionPair(ActionWorkloadDelegate, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	in := ExecutionGrantInput{
		Target: target, Issuer: durableGrantTestIssuer(), ExecutionPrincipalID: "00000000-0000-7000-8000-000000000012",
		Permissions: []PermissionPair{run}, IssuancePermissions: []PermissionPair{run, delegate}, WorkflowID: "workflow",
		WorkflowRevision: "revision", ClosureDigest: "sha256:" + strings.Repeat("a", 64), BindingDigest: "sha256:" + strings.Repeat("b", 64),
		DestinationDigest: "sha256:" + strings.Repeat("c", 64), TriggerDigest: "sha256:" + strings.Repeat("d", 64),
		TTL: time.Hour, IdempotencyKey: "execution-1",
	}
	if err := in.Validate(); err != nil {
		t.Fatalf("valid execution input: %v", err)
	}
	withoutDelegate := in
	withoutDelegate.IssuancePermissions = []PermissionPair{run}
	if err := withoutDelegate.Validate(); err == nil || !strings.Contains(err.Error(), "workload.delegate") {
		t.Fatalf("missing workload delegation error = %v", err)
	}
}
