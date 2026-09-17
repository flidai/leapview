package access

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

const (
	serviceIssuerID    = "00000000-0000-7000-8000-000000000201"
	serviceRecipientID = "00000000-0000-7000-8000-000000000202"
	serviceExecutionID = "00000000-0000-7000-8000-000000000203"
	serviceGroupID     = "00000000-0000-7000-8000-000000000204"
	serviceTokenID     = "00000000-0000-7000-8000-000000000205"
	serviceSessionID   = "00000000-0000-7000-8000-000000000206"
	serviceProjectID   = "service_grants_project"
	serviceInstanceID  = "service_grants_instance"
	serviceResourceUID = "00000000-0000-7000-8000-000000000207"
)

type durableGrantServiceWriter struct {
	share        ResourceShareGrantInput
	execution    ExecutionGrantInput
	envelope     GrantAdminEnvelopeInput
	revokeKind   GrantKind
	revokeID     string
	revokeActor  string
	revokeReason string
	err          error
}

func (w *durableGrantServiceWriter) CreateResourceShareGrant(_ context.Context, in ResourceShareGrantInput) (ResourceShareGrant, error) {
	w.share = in
	if w.err != nil {
		return ResourceShareGrant{}, w.err
	}
	return ResourceShareGrant{ID: in.ID, Issuer: in.Issuer, Target: in.Target, Permissions: in.Permissions}, nil
}

func (w *durableGrantServiceWriter) CreateExecutionGrant(_ context.Context, in ExecutionGrantInput) (ExecutionGrant, error) {
	w.execution = in
	if w.err != nil {
		return ExecutionGrant{}, w.err
	}
	return ExecutionGrant{ID: in.ID, Issuer: in.Issuer, Target: in.Target, ExecutionPrincipalID: in.ExecutionPrincipalID, Permissions: in.Permissions}, nil
}

func (w *durableGrantServiceWriter) CreateGrantAdminEnvelope(_ context.Context, in GrantAdminEnvelopeInput) (GrantAdminEnvelope, error) {
	w.envelope = in
	if w.err != nil {
		return GrantAdminEnvelope{}, w.err
	}
	return GrantAdminEnvelope{ID: in.ID, Issuer: in.Issuer, BoundPrincipalID: in.BoundPrincipalID, Permissions: in.Permissions}, nil
}

func (w *durableGrantServiceWriter) RevokeResourceShareGrant(_ context.Context, id, actor, reason string) error {
	w.revokeKind, w.revokeID, w.revokeActor, w.revokeReason = DurableGrantKindResourceShare, id, actor, reason
	return w.err
}

func (w *durableGrantServiceWriter) RevokeExecutionGrant(_ context.Context, id, actor, reason string) error {
	w.revokeKind, w.revokeID, w.revokeActor, w.revokeReason = DurableGrantKindExecution, id, actor, reason
	return w.err
}

func (w *durableGrantServiceWriter) RevokeGrantAdminEnvelope(_ context.Context, id, actor, reason string) error {
	w.revokeKind, w.revokeID, w.revokeActor, w.revokeReason = DurableGrantKindAdminEnvelope, id, actor, reason
	return w.err
}

type durableGrantServiceResolver struct {
	authority CurrentAuthoritySnapshot
	err       error
	calls     int
	requests  []CurrentAuthorityRequest
}

func shareWriterCalled(writer *durableGrantServiceWriter) bool {
	return writer.share.Target.InstanceID != "" || writer.share.Issuer.PrincipalID != "" || writer.share.Permissions != nil
}

func envelopeWriterCalled(writer *durableGrantServiceWriter) bool {
	return writer.envelope.Issuer.PrincipalID != "" || writer.envelope.TargetProjectID != "" || writer.envelope.Permissions != nil
}

func (r *durableGrantServiceResolver) ResolveCurrentAuthority(_ context.Context, request CurrentAuthorityRequest) (CurrentAuthoritySnapshot, error) {
	r.calls++
	r.requests = append(r.requests, request)
	if r.err != nil {
		return CurrentAuthoritySnapshot{}, r.err
	}
	return r.authority, nil
}

func serviceShareTarget() DurableGrantTarget {
	return DurableGrantTarget{InstanceID: serviceInstanceID, ProjectID: serviceProjectID, ResourceUID: serviceResourceUID, ResourceID: "dashboard_service", ResourceKind: projectgraph.KindDashboard}
}

func servicePipelineTarget() DurableGrantTarget {
	target := serviceShareTarget()
	target.ResourceUID = "00000000-0000-7000-8000-000000000208"
	target.ResourceID = "pipeline_service"
	target.ResourceKind = projectgraph.KindPipeline
	return target
}

func serviceResourcePair(t *testing.T, action Action, target DurableGrantTarget) PermissionPair {
	t.Helper()
	resource, err := NewResourceRef(target.ResourceID, target.ResourceKind)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := NewExactPermissionPair(action, target.ProjectID, resource)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func serviceAuthority(permissions, credentialPermissions []PermissionPair, class string) CurrentAuthoritySnapshot {
	return CurrentAuthoritySnapshot{
		Principal:   Principal{ID: serviceIssuerID, Kind: PrincipalKindUser},
		Permissions: permissions, CredentialPermissions: credentialPermissions,
		Credential: CredentialEvidence{Class: class, ID: func() string {
			if class == GrantCredentialClassSession {
				return serviceSessionID
			}
			return serviceTokenID
		}(), Fingerprint: strings.Repeat("a", 64), PrincipalID: serviceIssuerID, ExpiresAt: time.Now().UTC().Add(time.Hour)},
	}
}

func serviceForTest(t *testing.T, authority CurrentAuthoritySnapshot) (*DurableGrantService, *durableGrantServiceWriter, *durableGrantServiceResolver) {
	t.Helper()
	writer := &durableGrantServiceWriter{}
	resolver := &durableGrantServiceResolver{authority: authority}
	service, err := NewDurableGrantService(writer, resolver)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return time.Now().UTC() }
	return service, writer, resolver
}

func TestDurableGrantServiceDerivesShareIssuancePermissionsAndAcceptsGroupRecipient(t *testing.T) {
	target := serviceShareTarget()
	share := serviceResourcePair(t, ActionResourceShare, target)
	read := serviceResourcePair(t, ActionDashboardRead, target)
	authority := serviceAuthority([]PermissionPair{share, read}, []PermissionPair{share, read}, GrantCredentialClassAPIToken)
	authority.Groups = []SubjectRef{{Kind: SubjectKindGroup, ID: serviceGroupID}}
	service, writer, resolver := serviceForTest(t, authority)
	group := SubjectRef{Kind: SubjectKindGroup, ID: serviceGroupID}
	grant, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{
		ID: "share-service", Target: target, Recipient: group, Permissions: []PermissionPair{read},
		IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "share-service",
	})
	if err != nil {
		t.Fatalf("issue group share: %v", err)
	}
	if grant.Issuer.PrincipalID != serviceIssuerID || writer.share.IssuancePermissions == nil {
		t.Fatalf("service did not derive issuer/issuance authority: grant=%#v input=%#v", grant, writer.share)
	}
	if len(writer.share.IssuancePermissions) != 2 || writer.share.IssuancePermissions[0].Key() != share.Key() || writer.share.IssuancePermissions[1].Key() != read.Key() {
		t.Fatalf("derived issuance permissions = %#v, want coherent share/read authority", writer.share.IssuancePermissions)
	}
	if writer.share.Recipient.Kind != SubjectKindGroup || writer.share.Recipient.ID != serviceGroupID {
		t.Fatalf("group recipient was not preserved: %#v", writer.share.Recipient)
	}
	if resolver.calls != 1 || resolver.requests[0].Target != target {
		t.Fatalf("authority resolution = calls %d requests %#v, want one exact target resolution", resolver.calls, resolver.requests)
	}
}

func TestDurableGrantServiceDoesNotCombineSplitPrincipalAndGroupAuthority(t *testing.T) {
	target := serviceShareTarget()
	share := serviceResourcePair(t, ActionResourceShare, target)
	read := serviceResourcePair(t, ActionDashboardRead, target)
	authority := serviceAuthority([]PermissionPair{share}, []PermissionPair{share, read}, GrantCredentialClassAPIToken)
	authority.Groups = []SubjectRef{{Kind: SubjectKindGroup, ID: serviceGroupID}}
	service, writer, _ := serviceForTest(t, authority)
	_, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{
		Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read},
		IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "split-authority",
	})
	if !errors.Is(err, ErrGrantPermissionCeiling) {
		t.Fatalf("split principal/group authority error = %v, want permission ceiling", err)
	}
	if shareWriterCalled(writer) {
		t.Fatal("writer received authority assembled from separate principal/group evidence")
	}
}

func TestDurableGrantServiceRejectsPrincipalAuthorityLoss(t *testing.T) {
	target := serviceShareTarget()
	read := serviceResourcePair(t, ActionDashboardRead, target)
	authority := serviceAuthority([]PermissionPair{serviceResourcePair(t, ActionResourceShare, target), read}, []PermissionPair{serviceResourcePair(t, ActionResourceShare, target), read}, GrantCredentialClassAPIToken)
	service, writer, _ := serviceForTest(t, authority)
	service.resolver.(*durableGrantServiceResolver).err = ErrGrantPrincipalInactive
	_, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read}, IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "authority-loss"})
	if !errors.Is(err, ErrGrantAuthorityUnavailable) {
		t.Fatalf("principal authority loss error = %v, want unavailable", err)
	}
	if shareWriterCalled(writer) {
		t.Fatal("writer received a grant after principal authority loss")
	}
}

func TestDurableGrantServiceAttenuatesAPIToken(t *testing.T) {
	target := serviceShareTarget()
	share := serviceResourcePair(t, ActionResourceShare, target)
	read := serviceResourcePair(t, ActionDashboardRead, target)
	service, writer, _ := serviceForTest(t, serviceAuthority([]PermissionPair{share, read}, []PermissionPair{share}, GrantCredentialClassAPIToken))
	_, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read}, IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "credential-attenuation"})
	if !errors.Is(err, ErrGrantPermissionCeiling) {
		t.Fatalf("attenuated credential error = %v, want permission ceiling", err)
	}
	if shareWriterCalled(writer) {
		t.Fatal("writer received grant outside credential ceiling")
	}
}

func TestDurableGrantServiceSupportsBrowserSessionOnlyWithCurrentEvidence(t *testing.T) {
	target := serviceShareTarget()
	share := serviceResourcePair(t, ActionResourceShare, target)
	read := serviceResourcePair(t, ActionDashboardRead, target)
	service, writer, _ := serviceForTest(t, serviceAuthority([]PermissionPair{share, read}, nil, GrantCredentialClassSession))
	if _, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read}, IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "session-current"}); err != nil {
		t.Fatalf("current browser session issue: %v", err)
	}
	if writer.share.Issuer.Credential.Class != GrantCredentialClassSession || writer.share.Issuer.Credential.ID != serviceSessionID {
		t.Fatalf("session evidence was not persisted: %#v", writer.share.Issuer)
	}

	service, writer, _ = serviceForTest(t, serviceAuthority([]PermissionPair{share, read}, nil, GrantCredentialClassSession))
	service.resolver.(*durableGrantServiceResolver).authority.Credential.Fingerprint = ""
	if _, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read}, IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "session-missing-evidence"}); !errors.Is(err, ErrGrantCredentialInvalid) {
		t.Fatalf("missing session evidence error = %v, want credential invalid", err)
	}
	if shareWriterCalled(writer) {
		t.Fatal("writer received grant without current session evidence")
	}
}

func TestDurableGrantServiceRejectsOnwardDelegation(t *testing.T) {
	target := serviceShareTarget()
	share := serviceResourcePair(t, ActionResourceShare, target)
	read := serviceResourcePair(t, ActionDashboardRead, target)
	service, writer, _ := serviceForTest(t, serviceAuthority([]PermissionPair{share, read}, []PermissionPair{share, read}, GrantCredentialClassAPIToken))
	_, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read}, IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "onward", AllowOnwardDelegation: true})
	if !errors.Is(err, ErrGrantNoOnwardDelegation) {
		t.Fatalf("onward delegation error = %v, want no-onward-delegation", err)
	}
	if shareWriterCalled(writer) {
		t.Fatal("writer received onward-delegating share")
	}
}

func TestDurableGrantServiceRequiresManageAndDelegateForAdminEnvelope(t *testing.T) {
	manage, err := NewProjectPermissionPair(ActionProjectAccessManage, serviceProjectID)
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := NewProjectPermissionPair(ActionProjectAccessDelegate, serviceProjectID)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := NewProjectPermissionPair(ActionProjectSettingsRead, serviceProjectID)
	if err != nil {
		t.Fatal(err)
	}
	request := GrantAdminEnvelopeRequest{ID: "admin-service", TargetProjectID: serviceProjectID, Permissions: []PermissionPair{settings}, RecipientSelector: "group:" + serviceGroupID, RoleVersion: "v1", IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "admin-service"}
	service, writer, _ := serviceForTest(t, serviceAuthority([]PermissionPair{manage, delegate, settings}, []PermissionPair{manage, delegate, settings}, GrantCredentialClassAPIToken))
	if _, err := service.IssueGrantAdminEnvelope(t.Context(), request); err != nil {
		t.Fatalf("admin envelope with manage+delegate: %v", err)
	}
	if writer.envelope.BoundPrincipalID != serviceIssuerID || writer.envelope.IssuancePermissions == nil {
		t.Fatalf("admin envelope authority fields = %#v", writer.envelope)
	}

	service, writer, _ = serviceForTest(t, serviceAuthority([]PermissionPair{manage, settings}, []PermissionPair{manage, settings}, GrantCredentialClassAPIToken))
	if _, err := service.IssueGrantAdminEnvelope(t.Context(), request); !errors.Is(err, ErrGrantPermissionCeiling) {
		t.Fatalf("admin envelope without delegate error = %v, want permission ceiling", err)
	}
	if envelopeWriterCalled(writer) {
		t.Fatal("writer received admin envelope without delegate authority")
	}
}

func TestDurableGrantServiceIssuesExecutionGrantAndUsesDerivedCeiling(t *testing.T) {
	target := servicePipelineTarget()
	delegate := serviceResourcePair(t, ActionWorkloadDelegate, target)
	run := serviceResourcePair(t, ActionPipelineRun, target)
	service, writer, _ := serviceForTest(t, serviceAuthority([]PermissionPair{delegate, run}, []PermissionPair{delegate, run}, GrantCredentialClassAPIToken))
	grant, err := service.IssueExecutionGrant(t.Context(), ExecutionGrantRequest{ID: "execution-service", Target: target, ExecutionPrincipalID: serviceExecutionID, Permissions: []PermissionPair{run}, WorkflowID: "workflow", WorkflowRevision: "revision", ClosureDigest: "sha256:" + strings.Repeat("a", 64), BindingDigest: "sha256:" + strings.Repeat("b", 64), DestinationDigest: "sha256:" + strings.Repeat("c", 64), TriggerDigest: "sha256:" + strings.Repeat("d", 64), IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "execution-service"})
	if err != nil {
		t.Fatalf("issue execution grant: %v", err)
	}
	if grant.ExecutionPrincipalID != serviceExecutionID || len(writer.execution.IssuancePermissions) != 2 {
		t.Fatalf("execution grant = %#v input=%#v", grant, writer.execution)
	}
}

func TestDurableGrantServicePropagatesRepositoryAuditFailureAndUsesCurrentActorForRevoke(t *testing.T) {
	target := serviceShareTarget()
	share := serviceResourcePair(t, ActionResourceShare, target)
	read := serviceResourcePair(t, ActionDashboardRead, target)
	repositoryErr := errors.New("audit transaction failed")
	authority := serviceAuthority([]PermissionPair{share, read}, []PermissionPair{share, read}, GrantCredentialClassAPIToken)
	authority.Credential.ExpiresAt = time.Now().UTC().Add(time.Hour)
	service, writer, resolver := serviceForTest(t, authority)
	writer.err = repositoryErr
	_, err := service.IssueResourceShare(t.Context(), ResourceShareGrantRequest{Target: target, RecipientPrincipalID: serviceRecipientID, Permissions: []PermissionPair{read}, IssuedAt: time.Now().UTC(), TTL: time.Hour, IdempotencyKey: "audit-failure"})
	if !errors.Is(err, repositoryErr) {
		t.Fatalf("repository/audit failure = %v, want original error", err)
	}
	writer.err = nil
	if err := service.RevokeExecutionGrant(t.Context(), "execution-service", "operator requested"); err != nil {
		t.Fatalf("revoke execution grant: %v", err)
	}
	if writer.revokeKind != DurableGrantKindExecution || writer.revokeID != "execution-service" || writer.revokeActor != serviceIssuerID || writer.revokeReason != "operator requested" {
		t.Fatalf("revoke call = kind %q id %q actor %q reason %q", writer.revokeKind, writer.revokeID, writer.revokeActor, writer.revokeReason)
	}
	if resolver.calls != 2 {
		t.Fatalf("resolver calls = %d, want issuance plus revoke", resolver.calls)
	}
}
