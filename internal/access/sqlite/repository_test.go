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
)

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
	group, err := repo.UpsertGroup(ctx, access.GroupInput{Provider: "local", ExternalID: "automation", Name: "Automation"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddGroupMember(ctx, group.ID, principal.ID); err != nil {
		t.Fatal(err)
	}
	members, err := repo.ListGroupMembers(ctx, group.ID)
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

func TestRepositoryRunAuditedMutationRollsBackMutationWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	var principal access.Principal

	err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var createErr error
		principal, createErr = txRepo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "audit-rollback", Email: "rollback@example.com"})
		return access.AuditEventInput{}, createErr
	})
	if err == nil {
		t.Fatal("audited mutation error = nil, want invalid audit event failure")
	}
	if principal.ID == "" {
		t.Fatal("mutation did not run before the audit failure")
	}
	if _, err := repo.PrincipalByID(ctx, principal.ID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("get rolled-back principal error = %v, want sql.ErrNoRows", err)
	}
}

func TestRepositoryRunAuditedMutationCommitsMutationAndAuditTogether(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	var principal access.Principal

	err := repo.RunAuditedMutation(ctx, func(txRepo access.Repository) (access.AuditEventInput, error) {
		var createErr error
		principal, createErr = txRepo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "audit-commit", Email: "commit@example.com"})
		return access.AuditEventInput{Action: "principal.created", ResourceKind: "principal", ResourceID: principal.ID}, createErr
	})
	if err != nil {
		t.Fatalf("run audited mutation: %v", err)
	}
	if _, err := repo.PrincipalByID(ctx, principal.ID); err != nil {
		t.Fatalf("get committed principal: %v", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "principal.created", ResourceKind: "principal", ResourceID: principal.ID})
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
	err := repo.RunAuditedMutationBatch(ctx, func(txRepo access.Repository) ([]access.AuditEventInput, error) {
		_, mutationErr := txRepo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "audit-batch", Email: "batch@example.com"})
		return []access.AuditEventInput{
			{Action: "principal.created", ResourceKind: "principal", ResourceID: "audit-batch"},
			{Action: "access.changed", ResourceKind: "principal", ResourceID: "audit-batch"},
		}, mutationErr
	})
	if err != nil {
		t.Fatalf("run audited mutation batch: %v", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{ResourceKind: "principal", ResourceID: "audit-batch"})
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
	INSERT INTO sessions (id, principal_id, token_fingerprint, token_verifier, expires_at, created_at, last_seen_at)
	VALUES ('browser-session-latest', 'active-user', 'browser-fingerprint-latest', 'browser-verifier-latest', '2026-09-01T00:00:00Z', '2026-08-02T00:00:00Z', '2026-08-09T15:30:00Z')`); err != nil {
		t.Fatalf("insert latest session: %v", err)
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
		t.Fatalf("active user last seen = %q, want latest session activity", got)
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

func TestRepositoryChecksPlatformAdminIdentity(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	principal, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{
		PrincipalID: "dev",
		Email:       "dev@localhost",
		DisplayName: "Local Developer",
		Role:        access.PlatformRoleAdmin,
	})
	if err != nil {
		t.Fatalf("set platform role: %v", err)
	}
	allowed, err := repo.IsPlatformAdmin(ctx, principal.ID)
	if err != nil {
		t.Fatalf("check platform admin: %v", err)
	}
	if !allowed {
		t.Fatal("platform admin role was not recognized")
	}

	limited, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "limited", Email: "limited@example.com", DisplayName: "Limited"})
	if err != nil {
		t.Fatalf("upsert limited principal: %v", err)
	}
	allowed, err = repo.IsPlatformAdmin(ctx, limited.ID)
	if err != nil {
		t.Fatalf("check limited platform role: %v", err)
	}
	if allowed {
		t.Fatal("principal without platform role unexpectedly recognized as admin")
	}
}

func TestRepositorySeparatesSCIMGroupsFromLocallyManagedGroups(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{
		ID: "scim_group_global", ExternalID: "directory-engineering", Name: "Engineering",
	})
	if err != nil {
		t.Fatalf("upsert SCIM group: %v", err)
	}
	groups, err := repo.ListAllGroups(ctx)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	if len(groups) != 1 || groups[0].ID != group.ID || groups[0].Provider != "scim" {
		t.Fatalf("groups = %#v, want the externally managed SCIM group", groups)
	}
	scimGroups, err := repo.ListSCIMGroups(ctx, access.SCIMGroupFilter{ID: group.ID})
	if err != nil {
		t.Fatalf("list SCIM groups: %v", err)
	}
	if len(scimGroups) != 1 || scimGroups[0].ID != group.ID {
		t.Fatalf("SCIM groups = %#v, want %q", scimGroups, group.ID)
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

func TestRepositoryResolveExternalPrincipalAttachesBootstrappedEmail(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)

	if err := repo.BootstrapAdmin(ctx, "owner@example.com"); err != nil {
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
	allowed, err := repo.IsPlatformAdmin(ctx, principal.ID)
	if err != nil {
		t.Fatalf("check platform role: %v", err)
	}
	if !allowed {
		t.Fatal("attached Azure identity did not retain platform admin role")
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
	_, repo := openAccessRepo(t, ctx)

	for i := 0; i < 2; i++ {
		if err := repo.BootstrapAdmin(ctx, "owner@example.com"); err != nil {
			t.Fatalf("bootstrap admin %d: %v", i, err)
		}
	}
	admin, err := repo.PrincipalByID(ctx, access.PrincipalIDForEmail("owner@example.com"))
	if err != nil {
		t.Fatalf("lookup bootstrap principal: %v", err)
	}
	allowed, err := repo.IsPlatformAdmin(ctx, admin.ID)
	if err != nil {
		t.Fatalf("check bootstrap role: %v", err)
	}
	if !allowed {
		t.Fatal("bootstrap admin role missing after repeated initialization")
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
	allowed, err := repo.IsPlatformAdmin(ctx, principal.ID)
	if err != nil {
		t.Fatalf("check platform role: %v", err)
	}
	if allowed {
		t.Fatal("new external principal unexpectedly has platform admin role")
	}
}

func TestRepositorySessionsAndAPITokensResolvePrincipals(t *testing.T) {
	ctx := context.Background()
	_, repo := openAccessRepo(t, ctx)
	principal, err := repo.UpsertPrincipal(ctx, access.PrincipalInput{ID: "viewer", Email: "viewer@example.com", DisplayName: "Viewer"})
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
		Name:        "production",
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
	if token.ExpiresAt == "" || token.RevokedAt != "" {
		t.Fatalf("token metadata = expires %q revoked %q", token.ExpiresAt, token.RevokedAt)
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
		Name:        "unscoped",
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
	if credential.Token.ID != created.ID || credential.Token.Name != "unscoped" {
		t.Fatalf("credential token metadata = id %q name %q, want %q/unscoped", credential.Token.ID, credential.Token.Name, created.ID)
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
		PrincipalID:  principal.ID,
		Action:       "principal.updated",
		ResourceKind: "principal",
		ResourceID:   "binding_1",
		MetadataJSON: `{"role":"viewer"}`,
	}); err != nil {
		t.Fatalf("record audit event: %v", err)
	}
	if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{
		PrincipalID:  principal.ID,
		Action:       "session.revoked",
		ResourceKind: "session",
		ResourceID:   "session_1",
		MetadataJSON: `{}`,
	}); err != nil {
		t.Fatalf("record audit event 2: %v", err)
	}
	events, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		Action:       "principal.updated",
		ResourceKind: "principal",
		ResourceID:   "binding_1",
		Limit:        10,
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
		actor        string
		action       string
		resourceKind string
		resourceID   string
		createdAt    string
	}{
		{actor: alice.ID, action: "principal.updated", resourceKind: "principal", resourceID: "principal_old", createdAt: "2026-01-02 10:00:00"},
		{actor: alice.ID, action: "principal.updated", resourceKind: "principal", resourceID: "principal_mid", createdAt: "2026-01-02 11:00:00"},
		{actor: alice.ID, action: "principal.deleted", resourceKind: "principal", resourceID: "principal_new", createdAt: "2026-01-02 12:00:00"},
		{actor: bob.ID, action: "principal.updated", resourceKind: "principal", resourceID: "principal_bob", createdAt: "2026-01-02 13:00:00"},
		{actor: alice.ID, action: "session.revoked", resourceKind: "session", resourceID: "session_1", createdAt: "2026-01-02 14:00:00"},
	}
	for _, row := range seed {
		if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{
			PrincipalID:  row.actor,
			Action:       row.action,
			ResourceKind: row.resourceKind,
			ResourceID:   row.resourceID,
			MetadataJSON: `{}`,
		}); err != nil {
			t.Fatalf("record %s: %v", row.resourceID, err)
		}
		if _, err := store.SQLDB().ExecContext(ctx, `UPDATE audit_events SET created_at = ? WHERE resource_id = ?`, row.createdAt, row.resourceID); err != nil {
			t.Fatalf("set created_at for %s: %v", row.resourceID, err)
		}
	}

	filtered, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		PrincipalID:  alice.ID,
		Action:       "principal.updated",
		ResourceKind: "principal",
		From:         "2026-01-02T10:30:00Z",
		To:           "2026-01-02T12:30:00Z",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list filtered audit events: %v", err)
	}
	if len(filtered) != 1 || filtered[0].ResourceID != "principal_mid" {
		t.Fatalf("filtered events = %#v, want only principal_mid", filtered)
	}

	targeted, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{
		ResourceKind: "principal",
		ResourceID:   "principal_new",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("list targeted audit events: %v", err)
	}
	if len(targeted) != 1 || targeted[0].Action != "principal.deleted" {
		t.Fatalf("targeted events = %#v, want principal_new deletion", targeted)
	}

	firstPage, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Limit: 2})
	if err != nil {
		t.Fatalf("list first page: %v", err)
	}
	if len(firstPage) != 2 || firstPage[0].ResourceID != "session_1" || firstPage[1].ResourceID != "principal_bob" {
		t.Fatalf("first page = %#v, want session_1 then principal_bob", firstPage)
	}
	nextPage, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Limit: 2, PageToken: auditPageToken(firstPage[1].CreatedAt, firstPage[1].ID)})
	if err != nil {
		t.Fatalf("list next page: %v", err)
	}
	if len(nextPage) != 2 || nextPage[0].ResourceID != "principal_new" || nextPage[1].ResourceID != "principal_mid" {
		t.Fatalf("next page = %#v, want principal_new then principal_mid", nextPage)
	}
}

func openAccessRepo(t testing.TB, ctx context.Context) (*platform.Store, *Repository) {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, NewRepository(store.SQLDB())
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
