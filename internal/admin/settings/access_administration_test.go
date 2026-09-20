package settings

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
)

func TestLoadAccessAdministrationDerivesSourceAwareCapabilities(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "admin@example.com", DisplayName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}
	local, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "local@example.com", DisplayName: "Local"})
	if err != nil {
		t.Fatal(err)
	}
	external, err := repository.ResolveExternalPrincipal(ctx, access.ExternalIdentityInput{Provider: "okta", TenantID: "tenant", Subject: "external-1", Email: "external@example.com", DisplayName: "External"})
	if err != nil {
		t.Fatal(err)
	}
	localGroup, err := repository.UpsertGroup(ctx, access.GroupInput{Provider: "local", Name: "Local team"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AddGroupMember(ctx, localGroup.ID, local.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateSession(ctx, local.Principal.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: actor.Principal.ID, Action: "principal.updated", ResourceKind: "principal", ResourceID: local.Principal.ID, Status: "success"}); err != nil {
		t.Fatal(err)
	}
	externalGroup, err := repository.UpsertGroup(ctx, access.GroupInput{Provider: "scim", ExternalID: "directory-team", Name: "Directory team"})
	if err != nil {
		t.Fatal(err)
	}

	state, err := LoadAccessAdministration(ctx, repository, actor.Principal.ID, local.Principal.ID, externalGroup.ID, func(context.Context, string) ([]AccessRoleAssignmentSignal, error) {
		return []AccessRoleAssignmentSignal{{ProjectID: "project-1", ResourceKind: "dashboard", ResourceID: "dashboard-1", Role: "viewer", Capabilities: []string{string(access.CapabilityResourceRead)}, SourceType: "direct", SourceID: local.Principal.ID, SourceName: "Local"}}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	localSignal := principalSignalForTest(t, state, local.Principal.ID)
	if !localSignal.Capabilities.CanUpdateProfile || !localSignal.Capabilities.CanResetPassword || !localSignal.Capabilities.CanDelete || !localSignal.Capabilities.CanRevokeAllCredentials || localSignal.IdentitySource != "local" || len(localSignal.Groups) != 1 {
		t.Fatalf("local principal = %#v", localSignal)
	}
	if localSignal.LastSeenAt == "" || len(state.Sessions) != 1 {
		t.Fatalf("local activity = principal %#v sessions %#v", localSignal, state.Sessions)
	}
	actorSignal := principalSignalForTest(t, state, actor.Principal.ID)
	if actorSignal.Capabilities.CanRevokeAllCredentials {
		t.Fatalf("self credential-revocation capability = %#v, want disabled", actorSignal.Capabilities)
	}
	if len(state.RoleAssignments) != 1 || state.RoleAssignments[0].ResourceKind != "dashboard" || len(state.RoleAssignments[0].Capabilities) != 1 {
		t.Fatalf("role assignments = %#v", state.RoleAssignments)
	}
	if len(state.Activity) != 1 || state.Activity[0].Action != "principal.updated" || state.Activity[0].ActorName != "Admin" {
		t.Fatalf("activity = %#v", state.Activity)
	}
	externalSignal := principalSignalForTest(t, state, external.ID)
	if externalSignal.Capabilities.CanUpdateProfile || externalSignal.Capabilities.CanDelete || !externalSignal.Capabilities.CanBlock || externalSignal.IdentityProvider != "okta" {
		t.Fatalf("external principal = %#v", externalSignal)
	}
	groupSignal, ok := accessGroupSignalByID(state.Groups, externalGroup.ID)
	if !ok || groupSignal.Capabilities.CanUpdate || groupSignal.Capabilities.CanDelete || groupSignal.Capabilities.CanManageMembers {
		t.Fatalf("external group = %#v", groupSignal)
	}
}

func TestApplyAccessAdministrationCommandRevokesAllPrincipalSessions(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "admin@example.com", DisplayName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "target@example.com", DisplayName: "Target"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := repository.CreateSession(ctx, target.Principal.ID, time.Hour); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{Action: "revoke_all_sessions", PrincipalID: target.Principal.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "All active sessions revoked." {
		t.Fatalf("result = %#v", result)
	}
	sessions, err := repository.ListSessions(ctx, target.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, session := range sessions {
		if session.RevokedAt == "" {
			t.Fatalf("session not revoked: %#v", session)
		}
	}
}

func TestApplyAccessAdministrationCommandRevokesAllPrincipalSessionsBeyondListPage(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "bulk-admin@example.com", DisplayName: "Bulk admin"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "bulk-target@example.com", DisplayName: "Bulk target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateSession(ctx, target.Principal.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.DB().Exec(ctx, `
		INSERT INTO access.session(id, principal_id, token_fingerprint, verifier, expires_at, created_at)
		SELECT gen_random_uuid(), $1::uuid,
		       decode(md5(value::text) || md5('settings-session-' || value::text), 'hex'),
		       decode(md5('settings-verifier-' || value::text) || md5('settings-secret-' || value::text), 'hex'),
		       clock_timestamp() + interval '1 hour',
		       clock_timestamp() - (value || ' seconds')::interval
		FROM generate_series(1, 1001) AS value`, target.Principal.ID); err != nil {
		t.Fatalf("seed sessions beyond list page: %v", err)
	}
	result, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{Action: "revoke_all_sessions", PrincipalID: target.Principal.ID})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "All active sessions revoked." {
		t.Fatalf("result = %#v", result)
	}
	var active int
	if err := repository.DB().QueryRow(ctx, `SELECT count(*) FROM access.session WHERE principal_id = $1::uuid AND revoked_at IS NULL`, target.Principal.ID).Scan(&active); err != nil {
		t.Fatalf("count active sessions: %v", err)
	}
	if active != 0 {
		t.Fatalf("active sessions after Settings revoke-all = %d", active)
	}
}

func TestApplyAccessAdministrationCommandProtectsActorFromCredentialRevocation(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "self-protection@example.com", DisplayName: "Self protection"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateSession(ctx, actor.Principal.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{
		Action: "revoke_all_credentials", PrincipalID: actor.Principal.ID,
	}); err == nil || !strings.Contains(err.Error(), "own credentials") {
		t.Fatalf("self credential revocation error = %v", err)
	}
	sessions, err := repository.ListSessions(ctx, actor.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].RevokedAt != "" {
		t.Fatalf("self-protection changed sessions = %#v", sessions)
	}
	principal, err := repository.PrincipalByID(ctx, actor.Principal.ID)
	if err != nil || principal.AccessDisabled() {
		t.Fatalf("self-protection principal = %#v, %v", principal, err)
	}
}

func TestApplyAccessAdministrationCommandAuditsAllCredentialRevocation(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "credential-auditor@example.com", DisplayName: "Credential auditor"})
	if err != nil {
		t.Fatal(err)
	}
	target, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "credential-target@example.com", DisplayName: "Credential target"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateSession(ctx, target.Principal.ID, time.Hour); err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := repository.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: target.Principal.ID, Name: "credential-target-api", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{
		Action: "revoke_all_credentials", PrincipalID: target.Principal.ID, RequestID: "credential-revoke-request", CorrelationID: "credential-revoke-correlation",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "All credentials revoked; the principal remains enabled." {
		t.Fatalf("result = %#v", result)
	}
	if _, err := repository.PrincipalForAPIToken(ctx, apiSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("API token after revoke-all = %v, want no rows", err)
	}
	events, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{Action: "principal.credentials.revoked_all", ResourceID: target.Principal.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].PrincipalID != actor.Principal.ID || events[0].ResourceID != target.Principal.ID || events[0].RequestID != "credential-revoke-request" || events[0].CorrelationID != "credential-revoke-correlation" {
		t.Fatalf("credential revocation audit = %#v", events)
	}
	if !strings.Contains(events[0].MetadataJSON, "authoring_sessions") || !strings.Contains(events[0].MetadataJSON, target.Principal.ID) {
		t.Fatalf("credential revocation audit metadata = %s", events[0].MetadataJSON)
	}
	principal, err := repository.PrincipalByID(ctx, target.Principal.ID)
	if err != nil || principal.AccessDisabled() {
		t.Fatalf("target principal after revoke-all = %#v, %v", principal, err)
	}
}

func TestApplyAccessAdministrationCommandProtectsLastPlatformAdministrator(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "operator@example.com", DisplayName: "Operator"})
	if err != nil {
		t.Fatal(err)
	}
	admin, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "sole-admin@example.com", DisplayName: "Sole admin"})
	if err != nil {
		t.Fatal(err)
	}
	state, err := repository.ListPlatformAdministrators(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.GrantPlatformAdmin(ctx, access.PlatformAdminGrantInput{PrincipalID: admin.Principal.ID, ExpectedRevision: state.Revision, IdempotencyKey: "sole-admin-grant"}); err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := repository.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: admin.Principal.ID, Name: "last-admin-token", ExpiresAt: time.Now().UTC().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{
		Action: "revoke_all_credentials", PrincipalID: admin.Principal.ID,
	}); err == nil || !errors.Is(err, access.ErrPlatformAdminLastAdmin) {
		t.Fatalf("last-admin credential revocation error = %v", err)
	}
	if _, err := repository.PrincipalForAPIToken(ctx, apiSecret); err != nil {
		t.Fatalf("last-admin token was revoked: %v", err)
	}
	principal, err := repository.PrincipalByID(ctx, admin.Principal.ID)
	if err != nil || principal.AccessDisabled() {
		t.Fatalf("last-admin principal = %#v, %v", principal, err)
	}
}

func TestApplyAccessAdministrationCommandCreatesAndBlocksLocalUser(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "admin@example.com", DisplayName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{Action: "create_principal", Email: "new@example.com", DisplayName: "New User"})
	if err != nil {
		t.Fatal(err)
	}
	if created.SelectedPrincipalID == "" || created.TemporaryPassword == "" {
		t.Fatalf("create result = %#v", created)
	}
	if _, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{Action: "block_principal", PrincipalID: created.SelectedPrincipalID}); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.PrincipalByID(ctx, created.SelectedPrincipalID)
	if err != nil || stored.BlockedAt == "" {
		t.Fatalf("blocked principal = %#v, %v", stored, err)
	}
	if _, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{Action: "block_principal", PrincipalID: actor.Principal.ID}); err == nil {
		t.Fatal("self-block succeeded")
	}
}

func TestApplyAccessAdministrationCommandAddsMultipleGroupMembers(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "admin@example.com", DisplayName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "first@example.com", DisplayName: "First"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "second@example.com", DisplayName: "Second"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repository.UpsertGroup(ctx, access.GroupInput{Provider: "local", Name: "Analysts"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, AccessAdministrationCommand{
		Action: "add_group_member", GroupID: group.ID, PrincipalIDs: []string{" " + first.Principal.ID + " ", second.Principal.ID, first.Principal.ID},
		RequestID: "req-group-members", CorrelationID: "corr-group-members",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "2 members added." {
		t.Fatalf("result = %#v", result)
	}
	members, err := repository.ListGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %#v", members)
	}
	events, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{Action: "group.members_added", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].RequestID != "req-group-members" || events[0].CorrelationID != "corr-group-members" {
		t.Fatalf("group audit context = %#v", events)
	}
}

func openAccessAdministrationRepository(t *testing.T, _ context.Context) *accesspostgres.Repository {
	t.Helper()
	pool := postgrestest.Open(t, accesspostgres.ApplySchema)
	repository, err := accesspostgres.NewAccess(pool, accesspostgres.FingerprintConfig{Key: []byte(strings.Repeat("admin-settings-test-key", 2))})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func principalSignalForTest(t *testing.T, state AccessAdministrationSignal, id string) AccessPrincipalSignal {
	t.Helper()
	principal, ok := accessPrincipalByID(state.Principals, id)
	if !ok {
		t.Fatalf("principal %q not found", id)
	}
	return principal
}
