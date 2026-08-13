package settings

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
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
	localGroup, err := repository.UpsertGroup(ctx, access.GroupInput{WorkspaceID: "test", Name: "Local team"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.AddGroupMember(ctx, "test", localGroup.ID, local.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRoleBinding(ctx, access.RoleBindingInput{WorkspaceID: "test", SubjectType: access.SubjectPrincipal, SubjectID: local.Principal.ID, Role: access.RoleViewer}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateRoleBinding(ctx, access.RoleBindingInput{WorkspaceID: "test", SubjectType: access.SubjectGroup, SubjectID: localGroup.ID, Role: access.RoleEditor}); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.CreateSession(ctx, local.Principal.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := repository.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: actor.Principal.ID, Action: "principal.updated", TargetType: "principal", TargetID: local.Principal.ID, Status: "success"}); err != nil {
		t.Fatal(err)
	}
	externalGroup, err := repository.UpsertGroup(ctx, access.GroupInput{WorkspaceID: "test", Provider: "scim", ExternalID: "directory-team", Name: "Directory team"})
	if err != nil {
		t.Fatal(err)
	}

	state, err := LoadAccessAdministration(ctx, repository, actor.Principal.ID, "test", local.Principal.ID, externalGroup.ID)
	if err != nil {
		t.Fatal(err)
	}
	localSignal := principalSignalForTest(t, state, local.Principal.ID)
	if !localSignal.Capabilities.CanUpdateProfile || !localSignal.Capabilities.CanResetPassword || !localSignal.Capabilities.CanDelete || localSignal.IdentitySource != "local" || len(localSignal.Groups) != 1 {
		t.Fatalf("local principal = %#v", localSignal)
	}
	if localSignal.LastSeenAt == "" || len(state.Sessions) != 1 {
		t.Fatalf("local activity = principal %#v sessions %#v", localSignal, state.Sessions)
	}
	if len(state.RoleAssignments) != 2 || state.RoleAssignments[0].SourceType != "group" || state.RoleAssignments[1].SourceType != "direct" {
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
		if _, err := repository.CreateSession(ctx, target.Principal.ID, 0); err != nil {
			t.Fatal(err)
		}
	}
	result, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, "test", AccessAdministrationCommand{Action: "revoke_all_sessions", PrincipalID: target.Principal.ID})
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

func TestApplyAccessAdministrationCommandCreatesAndBlocksLocalUser(t *testing.T) {
	ctx := context.Background()
	repository := openAccessAdministrationRepository(t, ctx)
	actor, err := repository.CreateLocalUser(ctx, access.LocalUserInput{Email: "admin@example.com", DisplayName: "Admin"})
	if err != nil {
		t.Fatal(err)
	}
	created, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, "test", AccessAdministrationCommand{Action: "create_principal", Email: "new@example.com", DisplayName: "New User"})
	if err != nil {
		t.Fatal(err)
	}
	if created.SelectedPrincipalID == "" || created.TemporaryPassword == "" {
		t.Fatalf("create result = %#v", created)
	}
	if _, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, "test", AccessAdministrationCommand{Action: "block_principal", PrincipalID: created.SelectedPrincipalID}); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.PrincipalByID(ctx, created.SelectedPrincipalID)
	if err != nil || stored.BlockedAt == "" {
		t.Fatalf("blocked principal = %#v, %v", stored, err)
	}
	if _, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, "test", AccessAdministrationCommand{Action: "block_principal", PrincipalID: actor.Principal.ID}); err == nil {
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
	group, err := repository.UpsertGroup(ctx, access.GroupInput{WorkspaceID: "test", Name: "Analysts"})
	if err != nil {
		t.Fatal(err)
	}
	result, err := ApplyAccessAdministrationCommand(ctx, repository, actor.Principal.ID, "test", AccessAdministrationCommand{
		Action: "add_group_member", GroupID: group.ID, PrincipalIDs: []string{" " + first.Principal.ID + " ", second.Principal.ID, first.Principal.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Message != "2 members added." {
		t.Fatalf("result = %#v", result)
	}
	members, err := repository.ListGroupMembers(ctx, "test", group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 {
		t.Fatalf("members = %#v", members)
	}
}

func openAccessAdministrationRepository(t *testing.T, ctx context.Context) *accesssqlite.Repository {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatal(err)
	}
	return accesssqlite.NewRepository(store.SQLDB())
}

func principalSignalForTest(t *testing.T, state AccessAdministrationSignal, id string) AccessPrincipalSignal {
	t.Helper()
	principal, ok := accessPrincipalByID(state.Principals, id)
	if !ok {
		t.Fatalf("principal %q not found", id)
	}
	return principal
}
