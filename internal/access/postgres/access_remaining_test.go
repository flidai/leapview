package postgres

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
)

func TestAccessRemainingPostgreSQL18ConcurrentSCIMReconciliation(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{
				ExternalID: "concurrent-scim-user", UserName: "concurrent-scim-user",
				Email: fmt.Sprintf("scim-%d@example.com", i), DisplayName: fmt.Sprintf("worker-%d", i), Active: true,
			})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SCIM reconciliation: %v", err)
		}
	}
	users, err := repo.ListSCIMUsers(ctx, access.SCIMUserFilter{ExternalID: "concurrent-scim-user"})
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 {
		t.Fatalf("concurrent SCIM identities = %#v, want one identity", users)
	}
}

func TestAccessRemainingPostgreSQL18ConcurrentSCIMGroupCreation(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	member, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ExternalID: "concurrent-scim-group-member", UserName: "concurrent-scim-group-member", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	const workers = 8
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, e := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{
				ExternalID: "concurrent-scim-group", Name: fmt.Sprintf("group-%d", i), MemberIDs: []string{member.Principal.ID},
			})
			errs <- e
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent SCIM group reconciliation: %v", err)
		}
	}
	groups, err := repo.ListSCIMGroups(ctx, access.SCIMGroupFilter{ExternalID: "concurrent-scim-group"})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 {
		t.Fatalf("concurrent SCIM groups = %#v, want one group", groups)
	}
	members, err := repo.ListSCIMGroupMembers(ctx, groups[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].PrincipalID != member.Principal.ID {
		t.Fatalf("concurrent SCIM group members = %#v, want %s", members, member.Principal.ID)
	}
}

func TestAccessRemainingPostgreSQL18AuditFiltersCursorAndBootstrapEvidence(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	user, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "audit-filter@example.com", DisplayName: "Audit Filter", Password: "audit password long enough"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetPlatformRole(ctx, access.PlatformRoleInput{PrincipalID: user.Principal.ID, Role: access.PlatformRoleAdmin}); err != nil {
		t.Fatal(err)
	}
	_, token, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{
		PrincipalID: user.Principal.ID, Name: "bootstrap-evidence", Capabilities: []access.Capability{access.CapabilityResourcePublish}, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := repo.BootstrapAPITokenEvidence(ctx, user.Principal.ID, token.ID, time.Now())
	if err != nil || evidence.ID != token.ID {
		t.Fatalf("bootstrap token evidence = %#v, err=%v", evidence, err)
	}
	if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: user.Principal.ID, Action: "scim.user.updated", ResourceKind: "principal", ResourceID: user.Principal.ID, Capability: access.CapabilityResourceEdit, Status: "success", MetadataJSON: `{"source":"scim"}`}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: user.Principal.ID, Action: "dashboard.published", ResourceKind: "dashboard", ResourceID: "dashboard-1", Capability: access.CapabilityResourcePublish, Status: "success", MetadataJSON: `{"source":"ui"}`}); err != nil {
		t.Fatal(err)
	}
	if err := repo.RecordAuditEvent(ctx, access.AuditEventInput{PrincipalID: user.Principal.ID, Action: "scim.user.updated", ResourceKind: "principal", ResourceID: user.Principal.ID, Capability: access.CapabilityResourceRead, Status: "success", MetadataJSON: `{"source":"scim","revision":2}`}); err != nil {
		t.Fatal(err)
	}
	filtered, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Action: "scim.user.updated", ResourceKind: "principal", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(filtered) != 2 {
		t.Fatalf("filtered audit rows = %d, want 2", len(filtered))
	}
	page, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 1 {
		t.Fatalf("first audit page = %d, want 1", len(page))
	}
	pageToken := base64.RawURLEncoding.EncodeToString([]byte(page[0].CreatedAt + "\x00" + page[0].ID))
	next, err := repo.ListAuditEvents(ctx, access.AuditEventFilter{PageToken: pageToken, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range next {
		if event.ID == page[0].ID {
			t.Fatalf("cursor repeated first audit event %s", event.ID)
		}
	}
	if len(next) != 2 {
		t.Fatalf("second audit page = %d, want 2", len(next))
	}
	if _, err := repo.DisableProvisionedPrincipal(ctx, user.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.BootstrapAPITokenEvidence(ctx, user.Principal.ID, token.ID, time.Now()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled principal token evidence = %v, want no rows", err)
	}
	if strings.TrimSpace(page[0].MetadataJSON) == "" {
		t.Fatal("audit metadata was empty")
	}
}

func TestAccessRemainingPostgreSQL18SCIMDeactivationRevokesAllCredentials(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	user, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ExternalID: "scim-cascade", UserName: "scim-cascade", Email: "scim-cascade@example.com", DisplayName: "SCIM Cascade", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := secretVerifier("scim cascade password")
	if err != nil {
		t.Fatal(err)
	}
	principalID, err := pgUUID(user.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := accessdb.New(tx).InsertLocalCredential(ctx, accessdb.InsertLocalCredentialParams{PrincipalID: principalID, Verifier: verifier}); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, user.Principal.Email, "scim cascade password"); err != nil {
		t.Fatalf("pre-deactivation local credential: %v", err)
	}
	browserToken, err := repo.CreateSession(ctx, user.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := repo.CreateAPITokenWithMetadata(ctx, access.APITokenInput{PrincipalID: user.Principal.ID, Name: "scim-cascade", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := graph.NewResourceID("scim-cascade-project")
	if err != nil {
		t.Fatal(err)
	}
	scope, err := access.NewAuthoringScope("scim-cascade-target", projectID, []access.Capability{access.CapabilityResourceRead})
	if err != nil {
		t.Fatal(err)
	}
	created := time.Now().UTC()
	tx, err = db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertAuthoringSessionAndCredential(ctx, tx, "scim-cascade-session", access.AuthoringSessionHumanCLI, "scim-cascade-client", user.Principal.ID, scope, created, created.Add(time.Hour), "scim-cascade-credential", strings.Repeat("a", 64), strings.Repeat("b", 64), created.Add(30*time.Minute), created.Add(2*time.Hour)); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AuthoringCredentialByAccessTokenHash(ctx, strings.Repeat("a", 64), created); err != nil {
		t.Fatalf("pre-deactivation human CLI credential: %v", err)
	}
	if _, err := db.runtime.Exec(ctx, `
INSERT INTO access.oauth_session(kind, signature, request_id, request_json, access_signature)
VALUES
  ('access_token', 'scim-cascade-oauth-access', 'scim-cascade-oauth', jsonb_build_object('session', jsonb_build_object('subject', $1::text)), ''),
  ('refresh_token', 'scim-cascade-oauth-refresh', 'scim-cascade-oauth', jsonb_build_object('session', jsonb_build_object('subject', $1::text)), 'scim-cascade-oauth-access')`, user.Principal.ID); err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{
		ExternalID: "scim-cascade-group", Name: "SCIM Cascade Group", MemberIDs: []string{user.Principal.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	var activeOAuth int
	if err := db.admin.QueryRow(ctx, `
SELECT count(*) FROM access.oauth_session
WHERE active = true AND request_json->'session'->>'subject' = $1`, user.Principal.ID).Scan(&activeOAuth); err != nil {
		t.Fatal(err)
	}
	if activeOAuth != 2 {
		t.Fatalf("pre-deactivation active OAuth sessions = %d, want 2", activeOAuth)
	}
	members, err := repo.ListSCIMGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].PrincipalID != user.Principal.ID {
		t.Fatalf("pre-deactivation group memberships = %#v", members)
	}
	if _, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ExternalID: "scim-cascade", UserName: "scim-cascade", Email: "scim-cascade@example.com", DisplayName: "SCIM Cascade", Active: false}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForToken(ctx, browserToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SCIM deactivation retained browser session: %v", err)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, apiSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SCIM deactivation retained API token: %v", err)
	}
	var localRevoked bool
	if err := db.admin.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.local_credential WHERE principal_id=$1::uuid`, user.Principal.ID).Scan(&localRevoked); err != nil {
		t.Fatal(err)
	}
	if !localRevoked {
		t.Fatal("SCIM deactivation retained local credential")
	}
	sessions, err := repo.ListAuthoringSessions(ctx, user.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 || sessions[0].RevokedAt.IsZero() {
		t.Fatalf("SCIM deactivation authoring sessions = %#v", sessions)
	}
	if err := db.admin.QueryRow(ctx, `
SELECT count(*) FROM access.oauth_session
WHERE active = true AND request_json->'session'->>'subject' = $1`, user.Principal.ID).Scan(&activeOAuth); err != nil {
		t.Fatal(err)
	}
	if activeOAuth != 0 {
		t.Fatalf("SCIM deactivation retained %d active OAuth sessions", activeOAuth)
	}
	members, err = repo.ListSCIMGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("SCIM deactivation retained group memberships: %#v", members)
	}
	if _, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ExternalID: "scim-cascade", UserName: "scim-cascade", Email: "scim-cascade@example.com", DisplayName: "SCIM Cascade", Active: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForToken(ctx, browserToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SCIM reactivation revived browser session: %v", err)
	}
	if _, err := repo.PrincipalForAPIToken(ctx, apiSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SCIM reactivation revived API token: %v", err)
	}
	if _, _, err := repo.VerifyLocalPassword(ctx, user.Principal.Email, "scim cascade password"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SCIM reactivation revived local credential: %v", err)
	}
	if _, err := repo.AuthoringCredentialByAccessTokenHash(ctx, strings.Repeat("a", 64), time.Now().UTC()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("SCIM reactivation revived human CLI credential: %v", err)
	}
	if err := db.admin.QueryRow(ctx, `
SELECT count(*) FROM access.oauth_session
WHERE active = true AND request_json->'session'->>'subject' = $1`, user.Principal.ID).Scan(&activeOAuth); err != nil {
		t.Fatal(err)
	}
	if activeOAuth != 0 {
		t.Fatalf("SCIM reactivation revived %d OAuth sessions", activeOAuth)
	}
	members, err = repo.ListSCIMGroupMembers(ctx, group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 0 {
		t.Fatalf("SCIM reactivation revived group memberships: %#v", members)
	}
	newSession, err := repo.CreateSession(ctx, user.Principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create post-reactivation browser session: %v", err)
	}
	if _, err := repo.PrincipalForToken(ctx, newSession); err != nil {
		t.Fatalf("post-reactivation browser session: %v", err)
	}
}

func TestAccessRemainingPostgreSQL18SCIMRevocationFailureRollsBack(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	user, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ExternalID: "scim-rollback", UserName: "scim-rollback", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	browserToken, err := repo.CreateSession(ctx, user.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.runtime.Exec(ctx, `
INSERT INTO access.oauth_session(kind, signature, request_id, request_json, access_signature)
VALUES ('access_token', 'scim-rollback-oauth', 'scim-rollback',
  jsonb_build_object('session', jsonb_build_object('subject', $1::text)), '')`, user.Principal.ID); err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertSCIMGroup(ctx, access.SCIMGroupInput{
		ExternalID: "scim-rollback-group", Name: "SCIM Rollback Group", MemberIDs: []string{user.Principal.ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `
CREATE FUNCTION access.reject_scim_group_revoke() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'test SCIM group revocation failure';
END;
$$`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(ctx, `
CREATE TRIGGER reject_scim_group_revoke BEFORE UPDATE ON access.principal_group
FOR EACH ROW WHEN (OLD.revoked_at IS NULL AND NEW.revoked_at IS NOT NULL)
EXECUTE FUNCTION access.reject_scim_group_revoke()`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ExternalID: "scim-rollback", UserName: "scim-rollback", Active: false}); err == nil {
		t.Fatal("SCIM deactivation succeeded despite group revocation failure")
	}
	principal, err := repo.PrincipalByID(ctx, user.Principal.ID)
	if err != nil || principal.AccessDisabled() {
		t.Fatalf("failed deactivation changed principal: %#v, %v", principal, err)
	}
	if _, err := repo.PrincipalForToken(ctx, browserToken); err != nil {
		t.Fatalf("failed deactivation revoked browser session: %v", err)
	}
	var active bool
	if err := db.admin.QueryRow(ctx, `SELECT active FROM access.oauth_session WHERE signature = 'scim-rollback-oauth'`).Scan(&active); err != nil || !active {
		t.Fatalf("failed deactivation revoked MCP OAuth session: active=%v, err=%v", active, err)
	}
	members, err := repo.ListSCIMGroupMembers(ctx, group.ID)
	if err != nil || len(members) != 1 || members[0].PrincipalID != user.Principal.ID {
		t.Fatalf("failed deactivation changed group membership: %#v, %v", members, err)
	}
}

func TestAccessRemainingPostgreSQL18SCIMCascadeRevokesServiceSecret(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "SCIM Cascade Service"})
	if err != nil {
		t.Fatal(err)
	}
	_, secret, err := repo.CreateServicePrincipalSecret(ctx, service.ID, access.ServicePrincipalSecretInput{Name: "scim-cascade-secret", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	principalID, err := pgUUID(service.ID)
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.runtime.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := revokeSCIMPrincipalCredentials(ctx, tx, principalID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	revoked, err := repo.GetServicePrincipalSecret(ctx, service.ID, secret.ID)
	if err != nil {
		t.Fatal(err)
	}
	if revoked.RevokedAt == "" {
		t.Fatalf("service secret %s was not revoked by cascade: %#v", secret.ID, revoked)
	}
}

func TestAccessRemainingPostgreSQL18SCIMIDCollisionFailsClosed(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	local, err := repo.CreateLocalUser(ctx, access.LocalUserInput{Email: "scim-collision-local@example.com", DisplayName: "Local Original", Password: "scim collision password"})
	if err != nil {
		t.Fatal(err)
	}
	beforeLocal, err := repo.PrincipalIdentityManagement(ctx, local.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ID: local.Principal.ID, ExternalID: "collision-local", UserName: "collision-local", Email: "mutated-local@example.com", DisplayName: "Mutated Local", Active: false}); err == nil {
		t.Fatal("SCIM local-principal ID collision unexpectedly succeeded")
	}
	afterLocal, err := repo.PrincipalByID(ctx, local.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLocal.Email != local.Principal.Email || afterLocal.DisplayName != local.Principal.DisplayName || afterLocal.AccessDisabled() {
		t.Fatalf("local principal changed after SCIM collision: before=%#v after=%#v", local.Principal, afterLocal)
	}
	afterLocalManagement, err := repo.PrincipalIdentityManagement(ctx, local.Principal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterLocalManagement != beforeLocal {
		t.Fatalf("local identity management changed after SCIM collision: before=%#v after=%#v", beforeLocal, afterLocalManagement)
	}
	if users, err := repo.ListSCIMUsers(ctx, access.SCIMUserFilter{ExternalID: "collision-local"}); err != nil {
		t.Fatal(err)
	} else if len(users) != 0 {
		t.Fatalf("local collision created SCIM identity: %#v", users)
	}

	service, err := repo.CreateServicePrincipal(ctx, access.ServicePrincipalInput{DisplayName: "Service Original"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertSCIMUser(ctx, access.SCIMUserInput{ID: service.ID, ExternalID: "collision-service", UserName: "collision-service", DisplayName: "Mutated Service", Active: false}); err == nil {
		t.Fatal("SCIM service-principal ID collision unexpectedly succeeded")
	}
	afterService, err := repo.PrincipalByID(ctx, service.ID)
	if err != nil {
		t.Fatal(err)
	}
	if afterService.Kind != access.PrincipalKindServicePrincipal || afterService.DisplayName != service.DisplayName || afterService.AccessDisabled() {
		t.Fatalf("service principal changed after SCIM collision: before=%#v after=%#v", service, afterService)
	}
	if users, err := repo.ListSCIMUsers(ctx, access.SCIMUserFilter{ExternalID: "collision-service"}); err != nil {
		t.Fatal(err)
	} else if len(users) != 0 {
		t.Fatalf("service collision created SCIM identity: %#v", users)
	}
}
