package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform"
	"github.com/flidai/leapview/internal/workspace"
	workspacesqlite "github.com/flidai/leapview/internal/workspace/sqlite"
)

func TestRepositoryChecksGrantPrivileges(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.SetPrincipalRole(ctx, access.PrincipalRoleInput{
		WorkspaceID: "test",
		Email:       "owner@example.com",
		DisplayName: "Owner",
		Role:        "owner",
	})
	if err != nil {
		t.Fatalf("set principal role: %v", err)
	}
	allowed, err := testAuthorize(ctx, repo, "test", principal.ID, access.PrivilegeActivateDeployment)
	if err != nil {
		t.Fatalf("check privilege: %v", err)
	}
	if !allowed {
		t.Fatal("owner missing deployment activation privilege")
	}
}

func TestDeletePrincipalRejectsOwnedSecurablesWithoutPartialCleanup(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "owner-to-delete", Kind: access.PrincipalKindUser, Email: "owner-to-delete@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := access.ItemObject(access.SecurableDashboard, "test", "owned-dashboard")
	if _, err := repo.UpsertSecurableObject(ctx, object, principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object: object, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeViewItem,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeletePrincipal(ctx, principal.ID); !errors.Is(err, access.ErrPrincipalOwnsSecurableObject) {
		t.Fatalf("delete owner error = %v, want ownership conflict", err)
	}
	if _, err := repo.PrincipalByID(ctx, principal.ID); err != nil {
		t.Fatalf("owner principal was partially deleted: %v", err)
	}
	stored, err := repo.GetSecurableObject(ctx, object)
	if err != nil || stored.OwnerPrincipalID != principal.ID {
		t.Fatalf("owned object after rejected delete = %#v, %v", stored, err)
	}
	grants, err := repo.ListGrants(ctx, object)
	if err != nil || len(grants) != 1 || grants[0].SubjectID != principal.ID {
		t.Fatalf("grants were partially cleaned on ownership conflict: %#v, %v", grants, err)
	}
}

func TestDeletePrincipalRemovesDirectGrantsBeforeStableIDCanBeRecreated(t *testing.T) {
	ctx := access.WithAuthorizationCache(context.Background())
	_, repo := openAccessRepo(t, ctx)
	const principalID = "stable-principal-id"
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: principalID, Kind: access.PrincipalKindUser, Email: "before@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	object := access.ItemObject(access.SecurableDashboard, "test", "shared-dashboard")
	if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object: object, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeViewItem,
	}); err != nil {
		t.Fatal(err)
	}
	decision, err := repo.Authorize(ctx, principal.ID, access.PrivilegeViewItem, object)
	if err != nil || !decision.Allowed {
		t.Fatalf("initial authorization = %#v, %v", decision, err)
	}
	if err := repo.DeletePrincipal(ctx, principal.ID); err != nil {
		t.Fatalf("delete principal: %v", err)
	}
	recreated, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: principalID, Kind: access.PrincipalKindUser, Email: "after@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	decision, err = repo.Authorize(ctx, recreated.ID, access.PrivilegeViewItem, object)
	if err != nil {
		t.Fatal(err)
	}
	if decision.Allowed {
		t.Fatalf("recreated principal inherited stale grant: %#v", decision)
	}
	grants, err := repo.ListGrants(ctx, object)
	if err != nil {
		t.Fatal(err)
	}
	if len(grants) != 0 {
		t.Fatalf("direct grants remain after deletion: %#v", grants)
	}
}

func TestAdversarialGroupParentAndWorkspaceValidation(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "other", Title: "Other"}); err != nil {
		t.Fatal(err)
	}
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "member", Email: "member@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{WorkspaceID: "test", Provider: "local", ExternalID: "g", Name: "G"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddGroupMember(ctx, "other", group.ID, principal.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace add = %v, want sql.ErrNoRows", err)
	}
	if err := repo.RemoveGroupMember(ctx, "other", group.ID, principal.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-workspace remove = %v, want sql.ErrNoRows", err)
	}
}

func TestListGroupMembersPreservesServicePrincipalKind(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "service-member", Kind: access.PrincipalKindServicePrincipal,
		Email: "service@example.com", DisplayName: "Automation",
	})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{WorkspaceID: "test", Provider: "local", ExternalID: "automation", Name: "Automation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddGroupMember(ctx, "test", group.ID, principal.ID); err != nil {
		t.Fatal(err)
	}
	members, err := repo.ListGroupMembers(ctx, "test", group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].Kind != access.PrincipalKindServicePrincipal {
		t.Fatalf("members = %#v, want service-principal kind", members)
	}
}

func TestAdversarialDuplicateLocalCreateDoesNotRotateCredential(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	first, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "duplicate@example.com", DisplayName: "Original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "DUPLICATE@example.com"}); !errors.Is(err, access.ErrPrincipalAlreadyExists) {
		t.Fatalf("duplicate create = %v, want ErrPrincipalAlreadyExists", err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, first.Principal.Email, first.Password); err != nil {
		t.Fatalf("original credential no longer verifies: %v", err)
	}
	stored, err := repo.PrincipalByID(ctx, first.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.DisplayName != "Original" {
		t.Fatalf("display name = %q, want Original", stored.DisplayName)
	}
}

func TestAdversarialConcurrentDuplicateLocalCreateHasOneWinner(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	const attempts = 8
	start := make(chan struct{})
	results := make(chan error, attempts)
	var wg sync.WaitGroup
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.CreateLocalUser(ctx, access.LocalUserInput{
				Email:       "concurrent-duplicate@example.com",
				DisplayName: "Concurrent",
				Password:    "temporary-password",
			})
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)

	winners, conflicts := 0, 0
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, access.ErrPrincipalAlreadyExists):
			conflicts++
		default:
			t.Fatalf("concurrent create error = %v", err)
		}
	}
	if winners != 1 || conflicts != attempts-1 {
		t.Fatalf("winners/conflicts = %d/%d, want 1/%d", winners, conflicts, attempts-1)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, "concurrent-duplicate@example.com", "temporary-password"); err != nil {
		t.Fatalf("winning credential does not verify: %v", err)
	}
}

func TestAdversarialSCIMGlobalGroupRequiresSCIMParents(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "local-member", Email: "local-member@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{ExternalID: "scim-g", Name: "SCIM"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddSCIMGroupMember(ctx, group.ID, principal.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("local principal in SCIM group = %v, want sql.ErrNoRows", err)
	}
	if err := repo.DeleteSCIMGroup(ctx, "missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing SCIM group delete = %v, want sql.ErrNoRows", err)
	}
}

func TestSnapshotPrincipalIDUsesStableEmailIdentity(t *testing.T) {
	if got, want := snapshotPrincipalID("", " Analyst@Example.com "), access.PrincipalIDForEmail("analyst@example.com"); got != want {
		t.Fatalf("snapshot principal id = %q, want %q", got, want)
	}
	if got := snapshotPrincipalID("explicit-principal", "analyst@example.com"); got != "explicit-principal" {
		t.Fatalf("explicit snapshot principal id = %q, want explicit-principal", got)
	}
}

func TestRepositoryRunAuditedMutationRollsBackMutationWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	object := access.ItemObject(access.SecurableDashboard, "test", "audit-rollback")
	var grant access.Grant

	err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var createErr error
		grant, createErr = txRepo.CreateGrant(ctx, access.GrantInput{
			Object: object, SubjectType: access.SubjectPrincipal, SubjectID: "dev", Privilege: access.PrivilegeViewItem,
		})
		return access.AuditEventInput{}, createErr
	})
	if err == nil {
		t.Fatal("audited mutation error = nil, want invalid audit event failure")
	}
	if grant.ID == "" {
		t.Fatal("mutation did not run before the audit failure")
	}
	if _, err := repo.GetGrant(ctx, "test", grant.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get rolled-back grant error = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryRunAuditedMutationCommitsMutationAndAuditTogether(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	object := access.ItemObject(access.SecurableDashboard, "test", "audit-commit")
	var grant access.Grant

	err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var createErr error
		grant, createErr = txRepo.CreateGrant(ctx, access.GrantInput{
			Object: object, SubjectType: access.SubjectPrincipal, SubjectID: "dev", Privilege: access.PrivilegeViewItem,
		})
		return access.AuditEventInput{Action: "grant.created", WorkspaceID: "test"}, createErr
	})
	if err != nil {
		t.Fatalf("run audited mutation: %v", err)
	}
	if _, err := repo.GetGrant(ctx, "test", grant.ID); err != nil {
		t.Fatalf("get committed grant: %v", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Action: "grant.created"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("audit event count = %d, want 1", len(events))
	}
}

func TestRepositoryRunAuditedMutationBatchCommitsEveryAuditEvent(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	object := access.ItemObject(access.SecurableDashboard, "test", "audit-batch")

	err := repo.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		_, mutationErr := txRepo.CreateGrant(ctx, access.GrantInput{
			Object: object, SubjectType: access.SubjectPrincipal, SubjectID: "dev", Privilege: access.PrivilegeViewItem,
		})
		return []access.AuditEventInput{
			{Action: "grant.created", WorkspaceID: "test"},
			{Action: "access.changed", WorkspaceID: "test"},
		}, mutationErr
	})
	if err != nil {
		t.Fatalf("run audited mutation batch: %v", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("audit event count = %d, want 2", len(events))
	}
}

func TestRepositoryInitializeInstanceRollsBackWhenCredentialPreparationFails(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	prepareErr := errors.New("write recovery credentials")

	_, err := repo.InitializeInstance(ctx, access.InstanceInitializationInput{
		Email:       "admin@example.com",
		Environment: "production",
		Now:         time.Now().UTC(),
	}, func(access.InitialInstanceCredentials) error {
		return prepareErr
	})
	if !errors.Is(err, prepareErr) {
		t.Fatalf("initialize instance error = %v, want %v", err, prepareErr)
	}

	if _, err := store.GetSetting(ctx, access.InstanceInitializedSetting); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("instance initialization setting error = %v, want sql.ErrNoRows", err)
	}
	principals, err := repo.ListPrincipals(ctx, access.PrincipalFilter{Email: "admin@example.com"})
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	if len(principals) != 0 {
		t.Fatalf("principals = %#v, want none", principals)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "instance.initialized"})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 0 {
		t.Fatalf("audit events = %#v, want none", events)
	}
}

func TestListPrincipalsDerivesLatestHumanActivity(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	for _, input := range []access.PrincipalInput{
		{ID: "active-user", Kind: access.PrincipalKindUser, Email: "active@example.test", DisplayName: "Active User"},
		{ID: "never-user", Kind: access.PrincipalKindUser, Email: "never@example.test", DisplayName: "Never User"},
		{ID: "automation", Kind: access.PrincipalKindServicePrincipal, Email: "automation@example.test", DisplayName: "Automation"},
	} {
		if _, err := repo.UpsertPrincipal(ctx, input); err != nil {
			t.Fatalf("upsert principal %s: %v", input.ID, err)
		}
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
INSERT INTO sessions (id, principal_id, token_fingerprint, token_verifier, expires_at, created_at, last_seen_at)
VALUES ('browser-session', 'active-user', 'browser-fingerprint', 'browser-verifier', '2026-09-01T00:00:00Z', '2026-08-01T00:00:00Z', '2026-08-08T12:00:00Z')`); err != nil {
		t.Fatalf("insert browser session: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
INSERT INTO oauth_authoring_sessions (id, kind, client_id, principal_id, target_id, project_id, privileges_json, created_at, last_used_at, expires_at)
VALUES ('cli-session', 'human_cli', 'leapview-cli', 'active-user', 'target', 'project', '[]', '2026-08-02T00:00:00Z', '2026-08-09T15:30:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert CLI session: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `
INSERT INTO oauth_authoring_sessions (id, kind, client_id, principal_id, target_id, project_id, privileges_json, created_at, last_used_at, expires_at)
VALUES ('workload-session', 'workload', 'automation-client', 'automation', 'target', 'project', '[]', '2026-08-03T00:00:00Z', '2026-08-10T18:00:00Z', '2026-09-01T00:00:00Z')`); err != nil {
		t.Fatalf("insert workload session: %v", err)
	}

	principals, err := repo.ListPrincipalsWithActivity(ctx, access.PrincipalFilter{})
	if err != nil {
		t.Fatalf("list principals: %v", err)
	}
	byID := make(map[string]access.Principal, len(principals))
	for _, principal := range principals {
		byID[principal.ID] = principal
	}
	if got := byID["active-user"].LastSeenAt; got != "2026-08-09T15:30:00Z" {
		t.Fatalf("active user last seen = %q, want latest CLI activity", got)
	}
	if got := byID["never-user"].LastSeenAt; got != "" {
		t.Fatalf("never user last seen = %q, want empty", got)
	}
	if _, exists := byID["automation"]; exists {
		t.Fatal("administration people directory included a service principal")
	}
}

func TestRepositoryRejectsEvaluationIngestOutsideEvaluation(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	_, err := repo.InitializeInstance(
		ctx,
		access.InstanceInitializationInput{
			Email:                "admin@example.com",
			Environment:          "production",
			Now:                  time.Date(2026, time.July, 29, 12, 0, 0, 0, time.UTC),
			EvaluationDataIngest: true,
		},
		nil,
	)
	if err == nil ||
		!strings.Contains(err.Error(), "restricted to the evaluation environment") {
		t.Fatalf("InitializeInstance() error = %v", err)
	}
	if _, err := store.GetSetting(
		ctx,
		access.InstanceInitializedSetting,
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("instance initialization setting error = %v", err)
	}
}

func TestRepositoryLocalUserPasswordLifecycle(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{
		Email:       "Analyst@Example.com",
		DisplayName: "Analyst",
		MustChange:  true,
	})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	if created.Password == "" {
		t.Fatal("temporary password was empty")
	}
	if created.Principal.Email != "analyst@example.com" || created.Principal.Kind != access.PrincipalKindUser {
		t.Fatalf("created principal = %#v", created.Principal)
	}

	principal, credential, err := repo.VerifyLocalPassword(ctx, "analyst@example.com", created.Password)
	if err != nil {
		t.Fatalf("verify local password: %v", err)
	}
	if principal.ID != created.Principal.ID || !credential.MustChangePassword {
		t.Fatalf("verified principal/credential = %#v / %#v", principal, credential)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, "analyst@example.com", "wrong-password"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong password err = %v, want sql.ErrNoRows", err)
	}

	changed, err := repo.ChangeLocalPassword(ctx, created.Principal.ID, created.Password, "new-strong-password")
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if changed.MustChangePassword {
		t.Fatalf("must change after password change = true")
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, "analyst@example.com", "new-strong-password"); err != nil {
		t.Fatalf("verify changed password: %v", err)
	}

	reset, err := repo.ResetLocalPassword(ctx, created.Principal.ID)
	if err != nil {
		t.Fatalf("reset password: %v", err)
	}
	if reset.Password == "" || reset.Password == "new-strong-password" {
		t.Fatalf("reset password = %q", reset.Password)
	}
	_, resetCredential, err := repo.VerifyLocalPassword(ctx, "analyst@example.com", reset.Password)
	if err != nil {
		t.Fatalf("verify reset password: %v", err)
	}
	if !resetCredential.MustChangePassword {
		t.Fatal("reset credential must_change_password = false, want true")
	}
}

func TestRepositoryLocalPasswordRejectsDisabledPrincipal(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	created, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "disabled@example.com"})
	if err != nil {
		t.Fatalf("create local user: %v", err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, "disabled@example.com", created.Password); err != nil {
		t.Fatalf("verify before disable: %v", err)
	}
	if err := repo.q.DisablePrincipal(ctx, created.Principal.ID); err != nil {
		t.Fatalf("disable principal: %v", err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, "disabled@example.com", created.Password); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("disabled verify err = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryChecksPlatformRolePrivileges(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: "dev",
		Email:       "dev@localhost",
		DisplayName: "Local Developer",
		Role:        access.RoleAdmin,
	})
	if err != nil {
		t.Fatalf("set platform role: %v", err)
	}
	for _, workspaceID := range []string{"test", "other"} {
		if _, err := repo.UpsertSecurableObject(ctx, access.WorkspaceObject(workspaceID), ""); err != nil {
			t.Fatalf("upsert securable workspace %s: %v", workspaceID, err)
		}
		allowed, err := testAuthorize(ctx, repo, workspaceID, principal.ID, access.PrivilegeManageGrants)
		if err != nil {
			t.Fatalf("check privilege for %s: %v", workspaceID, err)
		}
		if !allowed {
			t.Fatalf("platform admin missing token manage privilege for workspace %s", workspaceID)
		}
	}

	limited, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "limited", Email: "limited@example.com", DisplayName: "Limited"})
	if err != nil {
		t.Fatalf("upsert limited principal: %v", err)
	}
	allowed, err := testAuthorize(ctx, repo, "test", limited.ID, access.PrivilegeManageGrants)
	if err != nil {
		t.Fatalf("check limited privilege: %v", err)
	}
	if allowed {
		t.Fatal("principal without platform or workspace role unexpectedly has privilege")
	}
}

func TestRepositoryChecksGroupRolePrivileges(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_group_member",
		Email:       "member@example.com",
		DisplayName: "Group Member",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{
		WorkspaceID: "test",
		Provider:    "azureadv2",
		ExternalID:  "group-object-id",
		Name:        "Deployers",
	})
	if err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	if err := repo.AddGroupMember(ctx, "test", group.ID, principal.ID); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	binding, err := repo.CreateRoleBinding(ctx, access.RoleBindingInput{
		WorkspaceID: "test",
		SubjectType: access.SubjectGroup,
		SubjectID:   group.ID,
		Role:        access.RoleDeployer,
	})
	if err != nil {
		t.Fatalf("create group role binding: %v", err)
	}
	if binding.SubjectType != access.SubjectGroup || binding.SubjectID != group.ID {
		t.Fatalf("binding subject = %q/%q, want group/%q", binding.SubjectType, binding.SubjectID, group.ID)
	}

	allowed, err := testAuthorize(ctx, repo, "test", principal.ID, access.PrivilegeActivateDeployment)
	if err != nil {
		t.Fatalf("check privilege: %v", err)
	}
	if !allowed {
		t.Fatal("group member missing deployment activation privilege")
	}
	if err := repo.RemoveGroupMember(ctx, "test", group.ID, principal.ID); err != nil {
		t.Fatalf("remove group member: %v", err)
	}
	allowed, err = testAuthorize(ctx, repo, "test", principal.ID, access.PrivilegeActivateDeployment)
	if err != nil {
		t.Fatalf("check privilege after remove: %v", err)
	}
	if allowed {
		t.Fatal("removed group member still has deployment activation privilege")
	}
}

func TestRepositoryRejectsCrossWorkspaceGroupMembership(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "other", Title: "Other"}); err != nil {
		t.Fatalf("ensure other workspace: %v", err)
	}
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "cross_workspace_member", Email: "cross-workspace@example.com", DisplayName: "Cross Workspace",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{
		ID: "group_other", WorkspaceID: "other", Provider: "local", ExternalID: "other", Name: "Other",
	})
	if err != nil {
		t.Fatalf("upsert other group: %v", err)
	}
	if err := repo.AddGroupMember(ctx, "test", group.ID, principal.ID); err == nil {
		t.Fatal("cross-workspace group membership error = nil, want workspace mismatch")
	}
}

func TestRepositoryIgnoresMismatchedPersistedGroupMembership(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "other", Title: "Other"}); err != nil {
		t.Fatalf("ensure other workspace: %v", err)
	}
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID: "legacy_cross_workspace_member", Email: "legacy-cross-workspace@example.com", DisplayName: "Legacy Cross Workspace",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{
		ID: "group_other_legacy", WorkspaceID: "other", Provider: "local", ExternalID: "other-legacy", Name: "Other Legacy",
	})
	if err != nil {
		t.Fatalf("upsert other group: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx,
		"INSERT INTO group_members (workspace_id, group_id, principal_id) VALUES (?, ?, ?)",
		"test", group.ID, principal.ID,
	); err != nil {
		t.Fatalf("seed mismatched legacy membership: %v", err)
	}
	object := access.WorkspaceObject("other")
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object: object, SubjectType: access.SubjectGroup, SubjectID: group.ID, Privilege: access.PrivilegeManageGrants,
	}); err != nil {
		t.Fatalf("create group grant: %v", err)
	}
	decision, err := repo.Authorize(ctx, principal.ID, access.PrivilegeManageGrants, object)
	if err != nil {
		t.Fatalf("authorize mismatched member: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("mismatched persisted membership authorized cross-workspace grant: %#v", decision)
	}
	policyObject := access.ItemObjectWithParent(
		access.SecurableDataset,
		"other",
		"sales/orders",
		access.ItemObject(access.SecurableSemanticModel, "other", "sales"),
	)
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		ID:             "policy_legacy_cross_workspace",
		Object:         policyObject,
		SubjectType:    access.SubjectGroup,
		SubjectID:      group.ID,
		PolicyType:     "row_filter",
		ExpressionJSON: `{"field":"region","value":"forged"}`,
	}); err != nil {
		t.Fatalf("upsert group policy: %v", err)
	}
	policies, err := repo.ListEffectiveDataPolicies(ctx, principal.ID, policyObject, true)
	if err != nil {
		t.Fatalf("list effective policies: %v", err)
	}
	if len(policies) != 0 {
		t.Fatalf("mismatched persisted membership applied group policies: %#v", policies)
	}
}

func TestRepositoryKeepsSCIMGroupsOutsideWorkspaceGroupCollections(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{
		ID: "scim_group_global", ExternalID: "directory-engineering", Name: "Engineering",
	})
	if err != nil {
		t.Fatalf("upsert SCIM group: %v", err)
	}
	groups, err := repo.ListGroups(ctx, "test")
	if err != nil {
		t.Fatalf("list workspace groups: %v", err)
	}
	if len(groups) != 0 {
		t.Fatalf("workspace groups = %#v, want no global SCIM groups", groups)
	}
	scimGroups, err := repo.ListSCIMGroups(ctx, access.SCIMGroupFilter{ID: group.ID})
	if err != nil {
		t.Fatalf("list SCIM groups: %v", err)
	}
	if len(scimGroups) != 1 || scimGroups[0].ID != group.ID {
		t.Fatalf("SCIM groups = %#v, want %q", scimGroups, group.ID)
	}
}

func TestRepositoryResolvesDBBackedObjectInheritance(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "dataset_reader",
		Email:       "dataset@example.com",
		DisplayName: "Dataset Reader",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	semanticModel := access.ItemObject(access.SecurableSemanticModel, "test", "sales")
	dataset := access.ItemObjectWithParent(access.SecurableDataset, "test", "sales/orders", semanticModel)
	column := access.ItemObjectWithParent(access.SecurableColumn, "test", "sales/orders/net_revenue", dataset)
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object:      dataset,
		SubjectType: access.SubjectPrincipal,
		SubjectID:   principal.ID,
		Privilege:   access.PrivilegeQueryData,
	}); err != nil {
		t.Fatalf("create dataset grant: %v", err)
	}
	if _, err := repo.UpsertSecurableObject(ctx, column, ""); err != nil {
		t.Fatalf("upsert column securable: %v", err)
	}

	decision, err := repo.Authorize(ctx, principal.ID, access.PrivilegeQueryData, column)
	if err != nil {
		t.Fatalf("authorize column: %v", err)
	}
	if !decision.Allowed || !decision.Inherited || decision.Reason != access.ReasonGrant {
		t.Fatalf("decision = %#v, want inherited dataset grant", decision)
	}
	if decision.GrantObjectID != dataset.CanonicalID() {
		t.Fatalf("grant object = %q, want %q", decision.GrantObjectID, dataset.CanonicalID())
	}
}

func TestRepositoryScopesDeploymentReviewerGrantToProjectEnvironment(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "deployment_reviewer",
		Email:       "reviewer@example.com",
		DisplayName: "Deployment Reviewer",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	target := access.ProjectEnvironmentObject("finance", "production")
	for _, privilege := range []access.Privilege{
		access.PrivilegeViewItem,
		access.PrivilegeApproveDeployment,
		access.PrivilegeActivateDeployment,
	} {
		if _, err := repo.CreateGrant(ctx, access.GrantInput{
			Object: target, SubjectType: access.SubjectPrincipal,
			SubjectID: principal.ID, Privilege: privilege,
		}); err != nil {
			t.Fatalf("create %s grant: %v", privilege, err)
		}
	}
	for _, privilege := range []access.Privilege{
		access.PrivilegeViewItem,
		access.PrivilegeApproveDeployment,
		access.PrivilegeActivateDeployment,
	} {
		decision, err := repo.Authorize(ctx, principal.ID, privilege, target)
		if err != nil {
			t.Fatalf("authorize %s: %v", privilege, err)
		}
		if !decision.Allowed {
			t.Fatalf("%s was denied for intended project environment", privilege)
		}
	}
	for name, object := range map[string]access.ObjectRef{
		"other project":     access.ProjectEnvironmentObject("operations", "production"),
		"other environment": access.ProjectEnvironmentObject("finance", "staging"),
		"platform":          access.PlatformObject(),
	} {
		decision, err := repo.Authorize(
			ctx,
			principal.ID,
			access.PrivilegeApproveDeployment,
			object,
		)
		if err != nil {
			t.Fatalf("authorize %s: %v", name, err)
		}
		if decision.Allowed {
			t.Fatalf("reviewer unexpectedly authorized for %s", name)
		}
	}
}

func TestRepositoryAuthorizeDoesNotCreateUnknownSecurableObject(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "unknown_object_reader",
		Email:       "unknown@example.com",
		DisplayName: "Unknown Reader",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	object := access.ItemObject(access.SecurableDashboard, "test", "missing")
	decision, err := repo.Authorize(ctx, principal.ID, access.PrivilegeViewItem, object)
	if err != nil {
		t.Fatalf("authorize unknown object: %v", err)
	}
	if decision.Allowed || decision.Reason != access.ReasonUnknownObject {
		t.Fatalf("decision = %#v, want denied unknown_object", decision)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM securable_objects WHERE id = ?`, object.CanonicalID()).Scan(&count); err != nil {
		t.Fatalf("count securable objects: %v", err)
	}
	if count != 0 {
		t.Fatalf("authorize created %d securable object rows, want 0", count)
	}
}

func TestRepositoryPlatformAdminCanAuthorizeUnknownWorkspace(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)

	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: "platform_admin",
		Email:       "platform-admin@example.com",
		DisplayName: "Platform Admin",
		Role:        access.RolePlatformAdmin,
	})
	if err != nil {
		t.Fatalf("set platform role: %v", err)
	}
	object := access.WorkspaceObject("new-workspace")
	decision, err := repo.Authorize(ctx, principal.ID, access.PrivilegeViewItem, object)
	if err != nil {
		t.Fatalf("authorize unknown workspace: %v", err)
	}
	if !decision.Allowed || !decision.Platform || decision.Reason != access.ReasonPlatformAdmin {
		t.Fatalf("decision = %#v, want platform admin allow", decision)
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM securable_objects WHERE id = ?`, object.CanonicalID()).Scan(&count); err != nil {
		t.Fatalf("count securable objects: %v", err)
	}
	if count != 0 {
		t.Fatalf("authorize created %d securable object rows, want 0", count)
	}
}

func TestRepositoryExplainsEffectiveAccessAndAuthorizeAny(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "viewer", Email: "viewer@example.com"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	dashboard := access.ItemObject(access.SecurableDashboard, "test", "exec")
	if _, err := repo.CreateGrant(ctx, access.GrantInput{
		Object:      dashboard,
		SubjectType: access.SubjectPrincipal,
		SubjectID:   principal.ID,
		Privilege:   access.PrivilegeViewItem,
	}); err != nil {
		t.Fatalf("create dashboard grant: %v", err)
	}

	decision, err := repo.AuthorizeAny(ctx, principal.ID, access.PrivilegeViewItem, []access.ObjectRef{
		access.ItemObject(access.SecurableSemanticModel, "test", "sales"),
		dashboard,
	})
	if err != nil {
		t.Fatalf("authorize any: %v", err)
	}
	if !decision.Allowed || decision.Object.CanonicalID() != dashboard.CanonicalID() {
		t.Fatalf("decision = %#v, want dashboard grant", decision)
	}

	effective, err := repo.EffectiveAccess(ctx, principal.ID, dashboard)
	if err != nil {
		t.Fatalf("effective access: %v", err)
	}
	var found bool
	for _, row := range effective {
		if row.Privilege == access.PrivilegeViewItem {
			found = true
			if row.Reason != access.ReasonGrant || row.GrantID == "" || row.GrantObjectID != dashboard.CanonicalID() {
				t.Fatalf("view explanation = %#v, want direct grant provenance", row)
			}
		}
	}
	if !found {
		t.Fatalf("effective access = %#v, missing VIEW_ITEM", effective)
	}
}

func TestRepositoryAuthorizeBatchMatchesIndividualAuthorize(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "batch_viewer", Email: "batch@example.com"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	dashboard := access.ItemObject(access.SecurableDashboard, "test", "exec")
	dataset := access.ItemObjectWithParent(access.SecurableDataset, "test", "sales/orders", access.ItemObject(access.SecurableSemanticModel, "test", "sales"))
	for _, grant := range []access.GrantInput{
		{Object: dashboard, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeViewItem},
		{Object: dataset, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeQueryData},
	} {
		if _, err := repo.CreateGrant(ctx, grant); err != nil {
			t.Fatalf("create grant: %v", err)
		}
	}
	column := access.ItemObjectWithParent(access.SecurableColumn, "test", "sales/orders/net_revenue", dataset)
	if _, err := repo.UpsertSecurableObject(ctx, column, ""); err != nil {
		t.Fatalf("upsert column securable: %v", err)
	}
	checks := []access.AuthorizationCheck{
		{Privilege: access.PrivilegeViewItem, Object: dashboard},
		{Privilege: access.PrivilegeQueryData, Object: column},
		{Privilege: access.PrivilegeManageGrants, Object: dashboard},
	}
	batch, err := repo.AuthorizeBatch(ctx, principal.ID, checks)
	if err != nil {
		t.Fatalf("authorize batch: %v", err)
	}
	if len(batch) != len(checks) {
		t.Fatalf("batch decisions = %d, want %d", len(batch), len(checks))
	}
	for i, check := range checks {
		individual, err := repo.Authorize(ctx, principal.ID, check.Privilege, check.Object)
		if err != nil {
			t.Fatalf("authorize individual %d: %v", i, err)
		}
		if batch[i] != individual {
			t.Fatalf("decision %d = %#v, want %#v", i, batch[i], individual)
		}
	}
}

func TestRepositoryAccessMutationsClearRequestAuthorizationCache(t *testing.T) {
	ctx := access.WithAuthorizationCache(context.Background())
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "cached_denial", Email: "cached@example.com"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	object := access.ItemObject(access.SecurableDashboard, "test", "cache")
	if _, err := repo.UpsertSecurableObject(ctx, object, ""); err != nil {
		t.Fatalf("upsert securable object: %v", err)
	}
	denied, err := repo.Authorize(ctx, principal.ID, access.PrivilegeViewItem, object)
	if err != nil {
		t.Fatalf("authorize denied: %v", err)
	}
	if denied.Allowed || denied.Reason != access.ReasonNoGrant {
		t.Fatalf("denied decision = %#v, want no_grant", denied)
	}
	if _, err := repo.CreateGrant(ctx, access.GrantInput{Object: object, SubjectType: access.SubjectPrincipal, SubjectID: principal.ID, Privilege: access.PrivilegeViewItem}); err != nil {
		t.Fatalf("create grant: %v", err)
	}
	allowed, err := repo.Authorize(ctx, principal.ID, access.PrivilegeViewItem, object)
	if err != nil {
		t.Fatalf("authorize allowed: %v", err)
	}
	if !allowed.Allowed || allowed.Reason != access.ReasonGrant {
		t.Fatalf("allowed decision = %#v, want grant after cache clear", allowed)
	}
}

func TestRepositorySupportsServicePrincipalSecrets(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	sp, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{
		ID:          "sp_deployer",
		DisplayName: "Deploy Bot",
	})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	if sp.Kind != access.PrincipalKindServicePrincipal {
		t.Fatalf("kind = %q, want service_principal", sp.Kind)
	}
	secret, row, err := repo.CreateServicePrincipalSecret(ctx, sp.ID, access.ServicePrincipalSecretInput{Name: "ci"})
	if err != nil {
		t.Fatalf("create secret: %v", err)
	}
	if secret == "" || row.Secret != "" {
		t.Fatalf("secret row = %#v, raw secret %q should be returned once and never exposed in metadata", row, secret)
	}
	expiresAt, err := time.Parse(time.RFC3339, row.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at %q: %v", row.ExpiresAt, err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expires_at = %s, want future default", row.ExpiresAt)
	}
	resolved, err := repo.PrincipalForServicePrincipalSecret(ctx, sp.ID, secret)
	if err != nil {
		t.Fatalf("resolve secret: %v", err)
	}
	if resolved.ID != sp.ID || resolved.Kind != access.PrincipalKindServicePrincipal {
		t.Fatalf("resolved = %#v, want service principal", resolved)
	}
	if err := repo.RevokeServicePrincipalSecret(ctx, sp.ID, row.ID); err != nil {
		t.Fatalf("revoke secret: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, sp.ID, secret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("resolve revoked secret error = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryRejectsExpiredServicePrincipalSecrets(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)

	sp, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{
		ID:          "sp_expired_secret",
		DisplayName: "Expired Secret",
	})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	_, _, err = repo.CreateServicePrincipalSecret(ctx, sp.ID, access.ServicePrincipalSecretInput{
		Name:      "expired",
		ExpiresAt: time.Now().Add(-time.Hour),
	})
	if err == nil {
		t.Fatal("create expired service principal secret error = nil")
	}

	secret, row, err := repo.CreateServicePrincipalSecret(ctx, sp.ID, access.ServicePrincipalSecretInput{Name: "ci"})
	if err != nil {
		t.Fatalf("create service principal secret: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE service_principal_secrets SET expires_at = ? WHERE id = ?`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), row.ID); err != nil {
		t.Fatalf("expire service principal secret: %v", err)
	}
	if _, err := repo.PrincipalForServicePrincipalSecret(ctx, sp.ID, secret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("resolve expired secret error = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryStoresDataPoliciesBySecurableObject(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	object := access.ItemObjectWithParent(
		access.SecurableDataset,
		"test",
		"sales/orders",
		access.ItemObject(access.SecurableSemanticModel, "test", "sales"),
	)
	policy, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		ID:             "policy_region",
		Object:         object,
		PolicyType:     "row_filter",
		ExpressionJSON: `{"field":"region","operator":"equals","value":"EMEA"}`,
	})
	if err != nil {
		t.Fatalf("upsert data policy: %v", err)
	}
	if policy.ObjectID != object.CanonicalID() {
		t.Fatalf("object id = %q, want %q", policy.ObjectID, object.CanonicalID())
	}
	policies, err := repo.ListDataPolicies(ctx, object)
	if err != nil {
		t.Fatalf("list data policies: %v", err)
	}
	if len(policies) != 1 || policies[0].ID != "policy_region" || policies[0].PolicyType != "row_filter" {
		t.Fatalf("policies = %#v, want row filter policy", policies)
	}
	if err := repo.DeleteDataPolicy(ctx, "test", "policy_region"); err != nil {
		t.Fatalf("delete data policy: %v", err)
	}
	policies, err = repo.ListDataPolicies(ctx, object)
	if err != nil {
		t.Fatalf("list after delete: %v", err)
	}
	if len(policies) != 0 {
		t.Fatalf("policies after delete = %#v, want empty", policies)
	}
}

func TestRepositoryValidatesDataPoliciesBeforeMutation(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	object := access.ItemObjectWithParent(
		access.SecurableDataset, "test", "sales/orders",
		access.ItemObject(access.SecurableSemanticModel, "test", "sales"),
	)
	original, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		ID: "policy_mask", Object: object, PolicyType: "column_mask",
		ExpressionJSON: `{"field":"orders.email","mask":"null"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	if original.Compiled.ColumnMask == nil {
		t.Fatal("stored policy was returned without a compiled representation")
	}

	_, err = repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		ID: "policy_mask", Object: object, PolicyType: "column_mask",
		ExpressionJSON: `{"field":"orders.email","mask":"hash"}`,
	})
	if err == nil {
		t.Fatal("unsupported mask was persisted")
	}
	stored, err := repo.GetDataPolicy(ctx, "test", "policy_mask")
	if err != nil {
		t.Fatal(err)
	}
	if stored.ExpressionJSON != original.ExpressionJSON {
		t.Fatalf("stored expression = %s, want unchanged %s", stored.ExpressionJSON, original.ExpressionJSON)
	}
}

func TestRepositoryValidatesDataPolicySubjectBeforeCreatingSecurables(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	object := access.ItemObjectWithParent(
		access.SecurableDataset, "test", "sales/orphan",
		access.ItemObject(access.SecurableSemanticModel, "test", "sales"),
	)
	for _, input := range []access.DataPolicyInput{
		{ID: "missing-subject", Object: object, SubjectType: access.SubjectGroup, PolicyType: "row_filter", ExpressionJSON: `{"field":"region","value":"EU"}`},
		{ID: "unknown-subject", Object: object, SubjectType: access.SubjectType("unknown"), SubjectID: "subject", PolicyType: "row_filter", ExpressionJSON: `{"field":"region","value":"EU"}`},
		{ID: "untyped-subject", Object: object, SubjectID: "subject", PolicyType: "row_filter", ExpressionJSON: `{"field":"region","value":"EU"}`},
	} {
		if _, err := repo.UpsertDataPolicy(ctx, input); err == nil {
			t.Fatalf("UpsertDataPolicy(%s) accepted invalid subject", input.ID)
		}
	}
	var count int
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM securable_objects WHERE id = ?`, object.CanonicalID()).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("invalid policies created %d securable object rows", count)
	}
}

func TestRepositoryCompilesExistingStoredPoliciesAndFailsClosed(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	object := access.ItemObjectWithParent(
		access.SecurableDataset, "test", "sales/orders",
		access.ItemObject(access.SecurableSemanticModel, "test", "sales"),
	)
	if _, err := repo.UpsertDataPolicy(ctx, access.DataPolicyInput{
		ID: "legacy_policy", Object: object, PolicyType: "row_filter",
		ExpressionJSON: `{"field":"orders.region","value":"EMEA"}`,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SQLDB().ExecContext(ctx,
		`UPDATE data_policies SET expression_json = ? WHERE id = ?`,
		`{"field":"orders.region","value":"APAC"}`, "legacy_policy",
	); err != nil {
		t.Fatal(err)
	}
	migrated, err := repo.GetDataPolicy(ctx, "test", "legacy_policy")
	if err != nil {
		t.Fatal(err)
	}
	if migrated.Compiled.RowFilter == nil || migrated.Compiled.RowFilter.Filters[0].Values[0] != "APAC" {
		t.Fatalf("compiled migrated policy = %#v", migrated.Compiled)
	}

	if _, err := store.SQLDB().ExecContext(ctx,
		`UPDATE data_policies SET expression_json = ? WHERE id = ?`,
		`{"field":"orders.region","operator":"in","values":[]}`, "legacy_policy",
	); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.GetDataPolicy(ctx, "test", "legacy_policy"); err == nil {
		t.Fatal("invalid existing stored policy was returned to query execution")
	}
}

func TestInvalidDataPolicyRollsBackAuditedMutation(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	object := access.ItemObjectWithParent(
		access.SecurableDataset, "test", "sales/orders",
		access.ItemObject(access.SecurableSemanticModel, "test", "sales"),
	)
	err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		policy, mutationErr := txRepo.UpsertDataPolicy(ctx, access.DataPolicyInput{
			ID: "invalid_policy", Object: object, PolicyType: "row_filter", ExpressionJSON: `{}`,
		})
		return access.AuditEventInput{
			Action: "data_policy.created", WorkspaceID: "test", TargetType: "data_policy", TargetID: policy.ID,
		}, mutationErr
	})
	if err == nil {
		t.Fatal("invalid policy audited mutation succeeded")
	}
	if _, err := repo.GetDataPolicy(ctx, "test", "invalid_policy"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("invalid policy lookup error = %v, want sql.ErrNoRows", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("audit events after rollback = %#v", events)
	}
}

func TestRepositoryFiltersDataPoliciesBySubject(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	alice, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_policy_alice", Email: "alice@example.com", DisplayName: "Alice"})
	if err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	bob, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_policy_bob", Email: "bob@example.com", DisplayName: "Bob"})
	if err != nil {
		t.Fatalf("upsert bob: %v", err)
	}
	group, err := repo.UpsertGroup(ctx, access.GroupInput{ID: "group_policy", WorkspaceID: "test", Provider: "local", ExternalID: "analysts", Name: "Analysts"})
	if err != nil {
		t.Fatalf("upsert group: %v", err)
	}
	if err := repo.AddGroupMember(ctx, "test", group.ID, bob.ID); err != nil {
		t.Fatalf("add group member: %v", err)
	}
	object := access.ItemObjectWithParent(access.SecurableDataset, "test", "sales/orders", access.ItemObject(access.SecurableSemanticModel, "test", "sales"))
	for _, input := range []access.DataPolicyInput{
		{ID: "policy_global", Object: object, PolicyType: "row_filter", ExpressionJSON: `{"field":"region","value":"global"}`},
		{ID: "policy_alice", Object: object, PolicyType: "row_filter", ExpressionJSON: `{"field":"region","value":"alice"}`, SubjectType: access.SubjectPrincipal, SubjectID: alice.ID},
		{ID: "policy_group", Object: object, PolicyType: "row_filter", ExpressionJSON: `{"field":"region","value":"group"}`, SubjectType: access.SubjectGroup, SubjectID: group.ID},
	} {
		if _, err := repo.UpsertDataPolicy(ctx, input); err != nil {
			t.Fatalf("upsert %s: %v", input.ID, err)
		}
	}
	alicePolicies, err := repo.ListEffectiveDataPolicies(ctx, alice.ID, object, true)
	if err != nil {
		t.Fatalf("list alice policies: %v", err)
	}
	if got := policyIDs(alicePolicies); !equalStringSets(got, []string{"policy_global", "policy_alice"}) {
		t.Fatalf("alice policies = %#v", got)
	}
	bobPolicies, err := repo.ListEffectiveDataPolicies(ctx, bob.ID, object, true)
	if err != nil {
		t.Fatalf("list bob policies: %v", err)
	}
	if got := policyIDs(bobPolicies); !equalStringSets(got, []string{"policy_global", "policy_group"}) {
		t.Fatalf("bob policies = %#v", got)
	}
}

func TestRepositoryObjectOwnershipGrantsFullControl(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	owner, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_object_owner", Email: "owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatalf("upsert owner: %v", err)
	}
	object := access.ItemObject(access.SecurableDashboard, "test", "executive")
	if _, err := repo.SetObjectOwner(ctx, object, owner.ID); err != nil {
		t.Fatalf("set object owner: %v", err)
	}
	decision, err := repo.Authorize(ctx, owner.ID, access.PrivilegeManageGrants, object)
	if err != nil {
		t.Fatalf("authorize owner: %v", err)
	}
	if !decision.Allowed || !decision.Owner || decision.Reason != access.ReasonOwner {
		t.Fatalf("owner decision = %#v", decision)
	}
}

func TestRepositoryAPITokensExpireByDefault(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_default_token_expiry", Email: "token@example.com", DisplayName: "Token"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	_, token, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: principal.ID, Name: "default-expiry"})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if token.ExpiresAt == "" {
		t.Fatal("token expires_at is empty, want default expiry")
	}
	expiresAt, err := time.Parse(time.RFC3339, token.ExpiresAt)
	if err != nil {
		t.Fatalf("parse expires_at %q: %v", token.ExpiresAt, err)
	}
	if !expiresAt.After(time.Now()) {
		t.Fatalf("expires_at = %s, want future default", token.ExpiresAt)
	}
}

func TestRepositoryRejectsExpiredAPITokenCreate(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_reject_expired_token", Email: "expired-token@example.com", DisplayName: "Expired Token"})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	_, _, err = repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: principal.ID,
		Name:        "expired-token",
		ExpiresAt:   time.Now().Add(-time.Hour),
	})
	if err == nil {
		t.Fatal("create expired api token error = nil")
	}
}

func TestRepositoryManagesRoleBindingsByID(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_binding_subject",
		Email:       "subject@example.com",
		DisplayName: "Subject",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	created, err := repo.CreateRoleBinding(ctx, access.RoleBindingInput{
		WorkspaceID: "test",
		SubjectType: access.SubjectPrincipal,
		SubjectID:   principal.ID,
		Role:        access.RoleViewer,
	})
	if err != nil {
		t.Fatalf("create role binding: %v", err)
	}
	if created.ID == "" {
		t.Fatal("created binding id is empty")
	}
	if created.Role != access.RoleViewer {
		t.Fatalf("created role = %q, want viewer", created.Role)
	}

	updated, err := repo.UpdateRoleBinding(ctx, "test", created.ID, access.RoleBindingInput{
		SubjectType: access.SubjectPrincipal,
		SubjectID:   principal.ID,
		Role:        access.RoleEditor,
	})
	if err != nil {
		t.Fatalf("update role binding: %v", err)
	}
	if updated.ID != created.ID {
		t.Fatalf("updated binding id = %q, want %q", updated.ID, created.ID)
	}
	if updated.Role != access.RoleEditor {
		t.Fatalf("updated role = %q, want editor", updated.Role)
	}
	got, err := repo.GetRoleBinding(ctx, "test", created.ID)
	if err != nil {
		t.Fatalf("get role binding: %v", err)
	}
	if got.SubjectType != access.SubjectPrincipal || got.SubjectID != principal.ID {
		t.Fatalf("got subject = %q/%q, want principal/%q", got.SubjectType, got.SubjectID, principal.ID)
	}
	if err := repo.DeleteRoleBinding(ctx, "test", created.ID); err != nil {
		t.Fatalf("delete role binding: %v", err)
	}
	bindings, err := repo.ListRoleBindings(ctx, "test")
	if err != nil {
		t.Fatalf("list role bindings: %v", err)
	}
	for _, binding := range bindings {
		if binding.ID == created.ID {
			t.Fatal("deleted role binding was listed")
		}
	}
}

func TestRepositoryResolveExternalPrincipalAttachesBootstrappedEmail(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	if err := repo.BootstrapAdmin(ctx, "test", "owner@example.com"); err != nil {
		t.Fatalf("bootstrap admin: %v", err)
	}
	principal, err := repo.ResolveExternalPrincipal(ctx, access.ExternalIdentityInput{
		Provider:    "azureadv2",
		TenantID:    "tenant",
		Subject:     "object-id",
		Email:       "OWNER@example.com",
		DisplayName: "Owner",
	})
	if err != nil {
		t.Fatalf("resolve external principal: %v", err)
	}
	if principal.ID != access.PrincipalIDForEmail("owner@example.com") {
		t.Fatalf("principal id = %q, want bootstrapped email principal", principal.ID)
	}
	allowed, err := testAuthorize(ctx, repo, "test", principal.ID, access.PrivilegeActivateDeployment)
	if err != nil {
		t.Fatalf("check privilege: %v", err)
	}
	if !allowed {
		t.Fatal("attached Azure identity did not inherit owner privileges")
	}

	again, err := repo.ResolveExternalPrincipal(ctx, access.ExternalIdentityInput{
		Provider:    "azureadv2",
		TenantID:    "tenant",
		Subject:     "object-id",
		Email:       "owner@example.com",
		DisplayName: "Owner Updated",
	})
	if err != nil {
		t.Fatalf("resolve existing identity: %v", err)
	}
	if again.ID != principal.ID {
		t.Fatalf("existing identity principal = %q, want %q", again.ID, principal.ID)
	}
}

func TestRepositoryBootstrapAdminIsIdempotent(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)

	for i := 0; i < 2; i++ {
		if err := repo.BootstrapAdmin(ctx, "test", "owner@example.com"); err != nil {
			t.Fatalf("bootstrap admin %d: %v", i, err)
		}
	}
	var count int
	err := store.SQLDB().QueryRowContext(ctx, `
		SELECT count(*)
		FROM role_bindings rb
		JOIN roles r ON r.id = rb.role_id
		WHERE rb.workspace_id = ? AND rb.principal_id = ? AND r.name = ?
	`, "test", access.PrincipalIDForEmail("owner@example.com"), "owner").Scan(&count)
	if err != nil {
		t.Fatalf("count role bindings: %v", err)
	}
	if count != 1 {
		t.Fatalf("owner role bindings = %d, want 1", count)
	}
}

func TestRepositoryResolveExternalPrincipalWithoutEmailCreatesUnprivilegedPrincipal(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.ResolveExternalPrincipal(ctx, access.ExternalIdentityInput{
		Provider:    "azureadv2",
		TenantID:    "tenant",
		Subject:     "new-object-id",
		DisplayName: "New User",
	})
	if err != nil {
		t.Fatalf("resolve external principal: %v", err)
	}
	allowed, err := testAuthorize(ctx, repo, "test", principal.ID, access.PrivilegeActivateDeployment)
	if err != nil {
		t.Fatalf("check privilege: %v", err)
	}
	if allowed {
		t.Fatal("new external principal unexpectedly has deployment activation privilege")
	}
}

func TestRepositorySessionsAndAPITokensResolvePrincipals(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.SetPrincipalRole(ctx, access.PrincipalRoleInput{
		WorkspaceID: "test",
		Email:       "viewer@example.com",
		DisplayName: "Viewer",
		Role:        "viewer",
	})
	if err != nil {
		t.Fatalf("set principal role: %v", err)
	}

	sessionToken, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessionPrincipal, err := repo.PrincipalForToken(ctx, sessionToken)
	if err != nil {
		t.Fatalf("principal for session: %v", err)
	}
	if sessionPrincipal.ID != principal.ID {
		t.Fatalf("session principal = %q, want %q", sessionPrincipal.ID, principal.ID)
	}

	apiToken, err := repo.CreateAPIToken(ctx, principal.ID, "test")
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	apiPrincipal, err := repo.PrincipalForAPIToken(ctx, apiToken)
	if err != nil {
		t.Fatalf("principal for api token: %v", err)
	}
	if apiPrincipal.ID != principal.ID {
		t.Fatalf("api token principal = %q, want %q", apiPrincipal.ID, principal.ID)
	}
}

func TestRepositoryRejectsExpiredSessionsAndAPITokens(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_expired_credentials",
		Email:       "expired@example.com",
		DisplayName: "Expired",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}

	sessionSecret, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessions, err := repo.ListSessions(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(sessions))
	}
	expiredAt := time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE sessions SET expires_at = ? WHERE id = ?`, expiredAt, sessions[0].ID); err != nil {
		t.Fatalf("expire session: %v", err)
	}
	if _, err := repo.PrincipalForToken(ctx, sessionSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired session err = %v, want sql.ErrNoRows", err)
	}

	apiSecret, apiToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: principal.ID,
		Name:        "expired-api-token",
	})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE api_tokens SET expires_at = ? WHERE id = ?`, expiredAt, apiToken.ID); err != nil {
		t.Fatalf("expire api token: %v", err)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, apiSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("expired api token err = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryStoresNewCredentialsWithFingerprintsAndVerifiers(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_hardened_credentials",
		Email:       "hardened@example.com",
		DisplayName: "Hardened",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}

	sessionSecret, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	assertStoredSecret(t, store.SQLDB(), sessionSecret, `
		SELECT token_fingerprint, token_verifier
		FROM sessions
		WHERE principal_id = ?
	`, principal.ID)
	if _, err := repo.PrincipalForToken(ctx, sessionSecret+"wrong"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong session secret err = %v, want sql.ErrNoRows", err)
	}

	apiSecret, apiToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: principal.ID,
		Name:        "hardened",
	})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	assertStoredSecret(t, store.SQLDB(), apiSecret, `
		SELECT token_fingerprint, token_verifier
		FROM api_tokens
		WHERE id = ?
	`, apiToken.ID)
	badVerifier, err := newSecretVerifier("different-secret")
	if err != nil {
		t.Fatalf("new bad verifier: %v", err)
	}
	if _, err := store.SQLDB().ExecContext(ctx, `UPDATE api_tokens SET token_verifier = ? WHERE id = ?`, badVerifier, apiToken.ID); err != nil {
		t.Fatalf("tamper api token verifier: %v", err)
	}
	if _, err := repo.CredentialForAPIToken(ctx, apiSecret); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("tampered api token err = %v, want sql.ErrNoRows", err)
	}

	sp, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{ID: "sp_hardened", DisplayName: "Hardened Bot"})
	if err != nil {
		t.Fatalf("create service principal: %v", err)
	}
	spSecret, spSecretRow, err := repo.CreateServicePrincipalSecret(ctx, sp.ID, access.ServicePrincipalSecretInput{Name: "ci"})
	if err != nil {
		t.Fatalf("create service principal secret: %v", err)
	}
	assertStoredSecret(t, store.SQLDB(), spSecret, `
		SELECT secret_fingerprint, secret_verifier
		FROM service_principal_secrets
		WHERE id = ?
	`, spSecretRow.ID)
}

func TestRepositoryCredentialCreationFailsWithoutSecureRandomness(t *testing.T) {
	restore := setSecretRandomReaderForTest(errReader{})
	defer restore()
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_no_random",
		Email:       "norandom@example.com",
		DisplayName: "No Random",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	sp, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "sp_no_random",
		Kind:        access.PrincipalKindServicePrincipal,
		DisplayName: "No Random Bot",
	})
	if err != nil {
		t.Fatalf("upsert service principal: %v", err)
	}

	if _, err := repo.CreateSession(ctx, principal.ID, time.Hour); err == nil {
		t.Fatal("CreateSession error = nil, want secure randomness error")
	}
	if _, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: principal.ID, Name: "cli"}); err == nil {
		t.Fatal("CreateAPITokenWithMetadata error = nil, want secure randomness error")
	}
	if _, _, err := repo.CreateServicePrincipalSecret(ctx, sp.ID, access.ServicePrincipalSecretInput{Name: "ci"}); err == nil {
		t.Fatal("CreateServicePrincipalSecret error = nil, want secure randomness error")
	}
	if _, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "local-no-random@example.com"}); err == nil {
		t.Fatal("CreateLocalUser error = nil, want secure randomness error")
	}
	if _, err := repo.ResetLocalPassword(ctx, principal.ID); err == nil {
		t.Fatal("ResetLocalPassword error = nil, want secure randomness error")
	}
}

func TestRepositoryListsAndRevokesSessionsByID(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_session_owner",
		Email:       "sessions@example.com",
		DisplayName: "Sessions",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	token, err := repo.CreateSession(ctx, principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	sessions, err := repo.ListSessions(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("sessions len = %d, want 1", len(sessions))
	}
	if sessions[0].ID == "" || sessions[0].RevokedAt != "" {
		t.Fatalf("session metadata = id %q revoked %q, want id and no revocation", sessions[0].ID, sessions[0].RevokedAt)
	}
	if err := repo.RevokeSession(ctx, sessions[0].ID); err != nil {
		t.Fatalf("revoke session: %v", err)
	}
	sessions, err = repo.ListSessions(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list sessions after revoke: %v", err)
	}
	if sessions[0].RevokedAt == "" {
		t.Fatal("revoked session missing revoked_at")
	}
	if _, err := repo.PrincipalForToken(ctx, token); err == nil {
		t.Fatal("revoked session token still resolves")
	}
}

func TestRepositoryListsAndRevokesAPITokens(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_api_token_owner",
		Email:       "tokens@example.com",
		DisplayName: "Tokens",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	expiresAt := time.Now().Add(time.Hour).UTC()
	secret, created, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: principal.ID,
		WorkspaceID: "test",
		Name:        "production",
		Privileges:  []access.Privilege{access.PrivilegeViewItem},
		ExpiresAt:   expiresAt,
	})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}
	if secret == "" || created.ID == "" {
		t.Fatal("api token secret or id is empty")
	}
	tokens, err := repo.ListAPITokens(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list api tokens: %v", err)
	}
	if len(tokens) != 1 {
		t.Fatalf("tokens len = %d, want 1", len(tokens))
	}
	token := tokens[0]
	if token.WorkspaceID != "test" || token.ExpiresAt == "" || token.RevokedAt != "" {
		t.Fatalf("token metadata = workspace %q expires %q revoked %q", token.WorkspaceID, token.ExpiresAt, token.RevokedAt)
	}
	if len(token.Privileges) != 1 || token.Privileges[0] != access.PrivilegeViewItem {
		t.Fatalf("token privileges = %#v, want dashboard view", token.Privileges)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, secret); err != nil {
		t.Fatalf("principal for api token: %v", err)
	}
	if err := repo.RevokeAPIToken(ctx, token.ID); err != nil {
		t.Fatalf("revoke api token: %v", err)
	}
	tokens, err = repo.ListAPITokens(ctx, principal.ID)
	if err != nil {
		t.Fatalf("list api tokens after revoke: %v", err)
	}
	if tokens[0].RevokedAt == "" {
		t.Fatal("revoked api token missing revoked_at")
	}
	if _, err := repo.PrincipalForAPIToken(ctx, secret); err == nil {
		t.Fatal("revoked api token still resolves")
	}
}

func TestRepositoryAPITokenCredentialIncludesTokenMetadata(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_api_credential",
		Email:       "credential@example.com",
		DisplayName: "Credential",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	secret, created, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: principal.ID,
		WorkspaceID: "test",
		Name:        "scoped",
		Privileges:  []access.Privilege{access.PrivilegeUseWorkspace, access.PrivilegeManageGrants},
	})
	if err != nil {
		t.Fatalf("create api token: %v", err)
	}

	credential, err := repo.CredentialForAPIToken(ctx, secret)
	if err != nil {
		t.Fatalf("credential for api token: %v", err)
	}
	if credential.Principal.ID != principal.ID {
		t.Fatalf("credential principal = %q, want %q", credential.Principal.ID, principal.ID)
	}
	if credential.Token.ID != created.ID || credential.Token.WorkspaceID != "test" {
		t.Fatalf("credential token metadata = id %q workspace %q, want %q/test", credential.Token.ID, credential.Token.WorkspaceID, created.ID)
	}
	if len(credential.Token.Privileges) != 2 || credential.Token.Privileges[0] != access.PrivilegeUseWorkspace || credential.Token.Privileges[1] != access.PrivilegeManageGrants {
		t.Fatalf("credential token privileges = %#v", credential.Token.Privileges)
	}
}

func TestRepositoryScopedRevocationRejectsForeignOrUnknownIDs(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	owner, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_revoke_owner", Email: "owner@example.com", DisplayName: "Owner"})
	if err != nil {
		t.Fatalf("upsert owner: %v", err)
	}
	foreign, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "principal_revoke_foreign", Email: "foreign@example.com", DisplayName: "Foreign"})
	if err != nil {
		t.Fatalf("upsert foreign: %v", err)
	}
	ownerSessionSecret, err := repo.CreateSession(ctx, owner.ID, time.Hour)
	if err != nil {
		t.Fatalf("create owner session: %v", err)
	}
	foreignSessionSecret, err := repo.CreateSession(ctx, foreign.ID, time.Hour)
	if err != nil {
		t.Fatalf("create foreign session: %v", err)
	}
	ownerSessions, err := repo.ListSessions(ctx, owner.ID)
	if err != nil {
		t.Fatalf("list owner sessions: %v", err)
	}
	foreignSessions, err := repo.ListSessions(ctx, foreign.ID)
	if err != nil {
		t.Fatalf("list foreign sessions: %v", err)
	}
	if err := repo.RevokeSessionForPrincipal(ctx, owner.ID, foreignSessions[0].ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoke foreign session err = %v, want sql.ErrNoRows", err)
	}
	if _, err := repo.PrincipalForToken(ctx, foreignSessionSecret); err != nil {
		t.Fatalf("foreign session was revoked by owner: %v", err)
	}
	if err := repo.RevokeSessionForPrincipal(ctx, owner.ID, ownerSessions[0].ID); err != nil {
		t.Fatalf("revoke owner session: %v", err)
	}
	if _, err := repo.PrincipalForToken(ctx, ownerSessionSecret); err == nil {
		t.Fatal("owner session still resolves after scoped revoke")
	}

	ownerSecret, ownerToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: owner.ID, Name: "owner"})
	if err != nil {
		t.Fatalf("create owner api token: %v", err)
	}
	foreignSecret, foreignToken, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: foreign.ID, Name: "foreign"})
	if err != nil {
		t.Fatalf("create foreign api token: %v", err)
	}
	if err := repo.RevokeAPITokenForPrincipal(ctx, owner.ID, foreignToken.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoke foreign api token err = %v, want sql.ErrNoRows", err)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, foreignSecret); err != nil {
		t.Fatalf("foreign api token was revoked by owner: %v", err)
	}
	if err := repo.RevokeAPITokenForPrincipal(ctx, owner.ID, ownerToken.ID); err != nil {
		t.Fatalf("revoke owner api token: %v", err)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, ownerSecret); err == nil {
		t.Fatal("owner api token still resolves after scoped revoke")
	}
	if err := repo.RevokeAPITokenForPrincipal(ctx, owner.ID, "token_missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoke unknown api token err = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryListsAuditEvents(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_audit_actor",
		Email:       "audit@example.com",
		DisplayName: "Audit",
	})
	if err != nil {
		t.Fatalf("upsert principal: %v", err)
	}
	if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{
		WorkspaceID:  "test",
		PrincipalID:  principal.ID,
		Action:       "role_binding.created",
		TargetType:   "role_binding",
		TargetID:     "binding_1",
		MetadataJSON: `{"role":"viewer"}`,
	}); err != nil {
		t.Fatalf("record audit event: %v", err)
	}
	if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{
		WorkspaceID:  "test",
		PrincipalID:  principal.ID,
		Action:       "session.revoked",
		TargetType:   "session",
		TargetID:     "session_1",
		MetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("record audit event 2: %v", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		WorkspaceID: "test",
		Action:      "role_binding.created",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list audit events: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events len = %d, want 1", len(events))
	}
	if events[0].PrincipalID != principal.ID || events[0].MetadataJSON != `{"role":"viewer"}` {
		t.Fatalf("event = %#v, want recorded role binding event", events[0])
	}
}

func TestRepositoryFiltersAndPaginatesAuditEvents(t *testing.T) {
	ctx := context.Background()
	store, repo := openAccessRepo(t, ctx)
	alice, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_audit_alice",
		Email:       "alice@example.com",
		DisplayName: "Alice",
	})
	if err != nil {
		t.Fatalf("upsert alice: %v", err)
	}
	bob, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{
		ID:          "principal_audit_bob",
		Email:       "bob@example.com",
		DisplayName: "Bob",
	})
	if err != nil {
		t.Fatalf("upsert bob: %v", err)
	}
	seed := []struct {
		actor      string
		action     string
		targetType string
		targetID   string
		createdAt  string
	}{
		{actor: alice.ID, action: "role_binding.created", targetType: "role_binding", targetID: "binding_old", createdAt: "2026-01-02 10:00:00"},
		{actor: alice.ID, action: "role_binding.created", targetType: "role_binding", targetID: "binding_mid", createdAt: "2026-01-02 11:00:00"},
		{actor: alice.ID, action: "role_binding.deleted", targetType: "role_binding", targetID: "binding_new", createdAt: "2026-01-02 12:00:00"},
		{actor: bob.ID, action: "role_binding.created", targetType: "role_binding", targetID: "binding_bob", createdAt: "2026-01-02 13:00:00"},
		{actor: alice.ID, action: "session.revoked", targetType: "session", targetID: "session_1", createdAt: "2026-01-02 14:00:00"},
	}
	for _, row := range seed {
		if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{
			WorkspaceID:  "test",
			PrincipalID:  row.actor,
			Action:       row.action,
			TargetType:   row.targetType,
			TargetID:     row.targetID,
			MetadataJSON: `{}`,
		}); err != nil {
			t.Fatalf("record %s: %v", row.targetID, err)
		}
		if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_events SET created_at = ? WHERE target_id = ?`, row.createdAt, row.targetID); err != nil {
			t.Fatalf("set created_at for %s: %v", row.targetID, err)
		}
	}

	filtered, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		WorkspaceID: "test",
		PrincipalID: alice.ID,
		Action:      "role_binding.created",
		TargetType:  "role_binding",
		From:        "2026-01-02T10:30:00Z",
		To:          "2026-01-02T12:30:00Z",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list filtered audit events: %v", err)
	}
	if len(filtered) != 1 || filtered[0].TargetID != "binding_mid" {
		t.Fatalf("filtered events = %#v, want only binding_mid", filtered)
	}

	targeted, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		WorkspaceID: "test",
		TargetType:  "role_binding",
		TargetID:    "binding_new",
		Limit:       10,
	})
	if err != nil {
		t.Fatalf("list targeted audit events: %v", err)
	}
	if len(targeted) != 1 || targeted[0].Action != "role_binding.deleted" {
		t.Fatalf("targeted events = %#v, want binding_new deletion", targeted)
	}

	firstPage, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].TargetID != "session_1" || firstPage[1].TargetID != "binding_bob" {
		t.Fatalf("first page = %#v, want session_1 then binding_bob", firstPage)
	}
	nextPage, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{WorkspaceID: "test", Limit: 2, PageToken: auditPageToken(firstPage[1].CreatedAt, firstPage[1].ID)})
	if err != nil {
		t.Fatalf("list next page: %v", err)
	}
	if len(nextPage) != 2 || nextPage[0].TargetID != "binding_new" || nextPage[1].TargetID != "binding_mid" {
		t.Fatalf("next page = %#v, want binding_new then binding_mid", nextPage)
	}
}

func openAccessRepo(t testing.TB, ctx context.Context) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := workspacesqlite.NewRepository(store.SQLDB()).Ensure(ctx, workspace.EnsureInput{ID: "test", Title: "Test"}); err != nil {
		t.Fatalf("ensure workspace: %v", err)
	}
	return store, NewRepository(store.SQLDB())
}

func testAuthorize(ctx context.Context, repo *Repository, workspaceID, principalID string, privilege access.Privilege) (bool, error) {
	decision, err := repo.Authorize(ctx, principalID, privilege, access.WorkspaceObject(workspaceID))
	return decision.Allowed, err
}

func policyIDs(policies []access.DataPolicy) []string {
	out := make([]string, 0, len(policies))
	for _, policy := range policies {
		out = append(out, policy.ID)
	}
	return out
}

func equalStringSets(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := map[string]int{}
	for _, value := range got {
		counts[value]++
	}
	for _, value := range want {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

func assertStoredSecret(t *testing.T, db *sql.DB, rawSecret, query, id string) {
	t.Helper()
	var fingerprint, verifier string
	if err := db.QueryRowContext(context.Background(), query, id).Scan(&fingerprint, &verifier); err != nil {
		t.Fatalf("query stored secret: %v", err)
	}
	wantFingerprint := secretFingerprint(rawSecret)
	if fingerprint != wantFingerprint {
		t.Fatalf("fingerprint = %q, want %q", fingerprint, wantFingerprint)
	}
	if strings.Contains(fingerprint, rawSecret) {
		t.Fatalf("fingerprint %q exposes raw secret", fingerprint)
	}
	if !verifySecret(rawSecret, verifier) {
		t.Fatalf("verifier does not accept raw secret")
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

func setSecretRandomReaderForTest(reader io.Reader) func() {
	previous := secretRandomReader
	secretRandomReader = reader
	return func() {
		secretRandomReader = previous
	}
}
