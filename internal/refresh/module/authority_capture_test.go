package module

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/jobs"
)

type captureGrantReader struct {
	grant     access.ExecutionGrant
	err       error
	id        string
	recipient string
}

func (r *captureGrantReader) CurrentExecutionGrant(_ context.Context, id, recipient string) (access.ExecutionGrant, error) {
	r.id, r.recipient = id, recipient
	return r.grant, r.err
}

func captureTestGrant(t *testing.T) (access.ExecutionGrant, projectgraph.ServingIdentity, projectgraph.ResourceID) {
	t.Helper()
	projectID := projectgraph.ResourceID("project:sales")
	pipelineID := projectgraph.ResourceID("pipeline:daily")
	identity := projectgraph.ServingIdentity{ProjectID: projectID, Environment: "prod", GenerationID: "generation:one"}
	resource, err := access.NewResourceRef(pipelineID, projectgraph.KindPipeline)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := access.NewExactPermissionPair(access.ActionPipelineRun, projectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return access.ExecutionGrant{
		ID: "grant:daily", Profile: access.DurableGrantProfile,
		Target:               access.DurableGrantTarget{InstanceID: "instance:one", ProjectID: projectID, ResourceUID: "00000000-0000-7000-8000-000000000001", ResourceID: pipelineID, ResourceKind: projectgraph.KindPipeline},
		Issuer:               access.GrantIssuerEvidence{PrincipalID: "00000000-0000-7000-8000-000000000010", Credential: access.GrantCredentialEvidence{Class: access.GrantCredentialClassAPIToken, ID: "token:issuer", Fingerprint: strings.Repeat("a", 32)}},
		ExecutionPrincipalID: "00000000-0000-7000-8000-000000000012", Permissions: []access.PermissionPair{pair},
		WorkflowID: "workflow:daily", WorkflowRevision: "revision:one", ClosureDigest: "sha256:" + strings.Repeat("1", 64), BindingDigest: "sha256:" + strings.Repeat("2", 64), DestinationDigest: "sha256:" + strings.Repeat("3", 64), TriggerDigest: "sha256:" + strings.Repeat("4", 64),
		IssuedAt: time.Now().UTC().Add(-time.Minute), ExpiresAt: time.Now().UTC().Add(time.Hour), Fingerprint: "grant-fingerprint-daily",
	}, identity, pipelineID
}

func TestDelegatedWorkloadAuthorityServiceCapturesOnlyCurrentGrant(t *testing.T) {
	grant, identity, pipelineID := captureTestGrant(t)
	source, err := access.NewResourceRef("source:orders", projectgraph.KindSource)
	if err != nil {
		t.Fatal(err)
	}
	readSource, err := access.NewExactPermissionPair(access.ActionSourceRead, identity.ProjectID, source)
	if err != nil {
		t.Fatal(err)
	}
	grant.Permissions = append(grant.Permissions, readSource)
	reader := &captureGrantReader{grant: grant}
	service, err := NewDelegatedWorkloadAuthorityService(reader, grant.Target.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	authority, err := service.Capture(t.Context(), grant.ID, identity, pipelineID)
	if err != nil {
		t.Fatalf("capture delegated authority: %v", err)
	}
	if reader.id != grant.ID || reader.recipient != "" {
		t.Fatalf("current grant lookup = id %q recipient %q, want %q and empty recipient", reader.id, reader.recipient, grant.ID)
	}
	if authority.Mode != jobs.DelegatedWorkloadMode || authority.ActorPrincipalID != grant.Issuer.PrincipalID || authority.ExecutionPrincipalID != grant.ExecutionPrincipalID {
		t.Fatalf("authority principals = %#v", authority)
	}
	if authority.Target.ResourceUID != grant.Target.ResourceUID || authority.Target.ResourceID != pipelineID.String() {
		t.Fatalf("authority target = %#v", authority.Target)
	}
	evidence := authority.ExecutionGrant
	if evidence == nil || evidence.WorkflowRevision != grant.WorkflowRevision || evidence.ClosureDigest != grant.ClosureDigest || evidence.BindingDigest != grant.BindingDigest || evidence.DestinationDigest != grant.DestinationDigest || evidence.TriggerDigest != grant.TriggerDigest {
		t.Fatalf("authority closure evidence = %#v", evidence)
	}
	if len(authority.Permissions) != 2 || authority.Permissions[0].Action != "pipeline.run" || authority.Permissions[1].Action != "source.read" {
		t.Fatalf("authority permissions = %#v", authority.Permissions)
	}
}

func TestDelegatedWorkloadAuthorityServiceFailsClosedForInactiveGrant(t *testing.T) {
	grant, identity, pipelineID := captureTestGrant(t)
	tests := map[string]func(*captureGrantReader){
		"revoked":           func(reader *captureGrantReader) { reader.err = access.ErrGrantRevoked },
		"expired":           func(reader *captureGrantReader) { reader.err = access.ErrGrantExpired },
		"disabled workload": func(reader *captureGrantReader) { reader.err = access.ErrGrantPrincipalInactive },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			reader := &captureGrantReader{grant: grant}
			mutate(reader)
			service, err := NewDelegatedWorkloadAuthorityService(reader, grant.Target.InstanceID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := service.Capture(t.Context(), grant.ID, identity, pipelineID); err == nil {
				t.Fatal("capture unexpectedly reused drifted or inactive grant")
			}
		})
	}
}

func TestDelegatedWorkloadAuthorityServiceBindsCurrentEvidence(t *testing.T) {
	grant, identity, pipelineID := captureTestGrant(t)
	reader := &captureGrantReader{grant: grant}
	service, err := NewDelegatedWorkloadAuthorityService(reader, grant.Target.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	reader.grant.WorkflowRevision = "revision:two"
	reader.grant.BindingDigest = "sha256:" + strings.Repeat("9", 64)
	reader.grant.DestinationDigest = "sha256:" + strings.Repeat("8", 64)
	reader.grant.TriggerDigest = "sha256:" + strings.Repeat("7", 64)
	authority, err := service.Capture(t.Context(), grant.ID, identity, pipelineID)
	if err != nil {
		t.Fatalf("capture current grant evidence: %v", err)
	}
	if authority.ExecutionGrant.WorkflowRevision != reader.grant.WorkflowRevision || authority.ExecutionGrant.BindingDigest != reader.grant.BindingDigest || authority.ExecutionGrant.DestinationDigest != reader.grant.DestinationDigest || authority.ExecutionGrant.TriggerDigest != reader.grant.TriggerDigest {
		t.Fatalf("captured stale evidence = %#v, current grant = %#v", authority.ExecutionGrant, reader.grant)
	}
}

func TestDelegatedWorkloadAuthorityServiceRejectsCurrentGrantForAnotherPipeline(t *testing.T) {
	grant, identity, pipelineID := captureTestGrant(t)
	reader := &captureGrantReader{grant: grant}
	service, err := NewDelegatedWorkloadAuthorityService(reader, grant.Target.InstanceID)
	if err != nil {
		t.Fatal(err)
	}
	reader.grant.Target.ResourceID = "pipeline:other"
	if _, err := service.Capture(t.Context(), grant.ID, identity, pipelineID); err == nil {
		t.Fatal("capture accepted a current grant bound to another pipeline")
	}
}

func TestDelegatedWorkloadAuthorityServiceRequiresExplicitSchedulerEvidence(t *testing.T) {
	if _, err := NewDelegatedWorkloadAuthorityService(nil, "instance:one"); err == nil {
		t.Fatal("constructor accepted missing current execution grant reader")
	}
	reader := &captureGrantReader{}
	service, err := NewDelegatedWorkloadAuthorityService(reader, "instance:one")
	if err != nil {
		t.Fatal(err)
	}
	identity := projectgraph.ServingIdentity{ProjectID: "project:sales", Environment: "prod", GenerationID: "generation:one"}
	if _, err := service.Capture(t.Context(), "", identity, "pipeline:daily"); err == nil {
		t.Fatal("capture accepted missing explicit grant selector")
	}
}

var _ ExecutionGrantReader = (*captureGrantReader)(nil)
