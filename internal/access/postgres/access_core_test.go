package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func newStandaloneAccessDatabase(t *testing.T) auditDatabase {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true})
	readonlyRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_readonly", Password: "leapview-conformance-secret", Login: true})
	h.GrantRole(t, owner, migrator)
	d := h.NewDatabase(t, "")
	h.GrantDatabase(t, d.Name, migrator, "CONNECT", "CREATE")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, d.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err = ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply standalone access schema: %v", err)
	}
	if err = tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	runtime, err := pgxpool.New(ctx, d.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	readonly, err := pgxpool.New(ctx, d.URL(readonlyRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(readonly.Close)
	return auditDatabase{admin: admin, runtime: runtime, readonly: readonly}
}

func TestAccessCorePostgreSQL18ReadonlyExcludesCredentialMaterial(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()

	var count int
	if err := db.readonly.QueryRow(ctx, `SELECT count(*) FROM access.principal`).Scan(&count); err != nil {
		t.Fatalf("read safe access metadata: %v", err)
	}
	credentialTables := []string{
		"session",
		"local_credential",
		"api_token",
		"service_principal_secret",
		"desktop_authorization_code",
		"device_authorization",
		"authoring_credential",
		"oauth_client",
		"oauth_session",
		"oauth_client_assertion",
	}
	for _, table := range credentialTables {
		if _, err := db.readonly.Exec(ctx, `SELECT * FROM access.`+table+` LIMIT 0`); err == nil || !strings.Contains(err.Error(), "permission denied") {
			t.Errorf("readonly query access.%s error = %v, want permission denied", table, err)
		}
	}
}

func TestAccessCorePostgreSQL18PrincipalCredentialsAndRevocation(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "access-core@example.com", DisplayName: "Access Core", Password: "correct horse battery staple", MustChange: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalByID(t.Context(), p.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.VerifyLocalPassword(t.Context(), p.Principal.Email, "wrong password"); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("wrong password error = %v", err)
	}
	if _, _, err := repo.VerifyLocalPassword(t.Context(), p.Principal.Email, "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	passwordSession, err := repo.CreateSession(t.Context(), p.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ChangeLocalPassword(t.Context(), p.Principal.ID, "correct horse battery staple", "replacement password that is long enough"); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForToken(t.Context(), passwordSession); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("password change retained browser session = %v", err)
	}

	token, err := repo.CreateSession(t.Context(), p.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForToken(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteSession(t.Context(), token); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForToken(t.Context(), token); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked session = %v", err)
	}

	apiSecret, apiToken, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: p.Principal.ID, Name: "ci", Capabilities: []access.Capability{access.CapabilityResourceRead}, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if apiToken.ID == "" || apiToken.ExpiresAt == "" {
		t.Fatalf("token metadata = %#v", apiToken)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), apiSecret); err != nil {
		t.Fatal(err)
	}
	if err := repo.RevokeAPIToken(t.Context(), apiToken.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), apiSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked api token = %v", err)
	}
}

func TestAccessCorePostgreSQL18AtomicMembershipAndConcurrentRevoke(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	group, err := repo.UpsertGroup(t.Context(), access.GroupInput{Name: "Concurrent Members"})
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{Kind: access.PrincipalKindUser, DisplayName: "member"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.AddGroupMember(t.Context(), group.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddGroupMember(t.Context(), group.ID, p.ID); err != nil {
		t.Fatal(err)
	} // idempotent add
	members, err := repo.ListGroupMembersByGroup(t.Context(), group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 {
		t.Fatalf("membership rows = %d, want 1", len(members))
	}

	// Competing role grants are idempotent against the partial active index,
	// and a same-ID user/service race cannot rewrite principal kind.
	var roleWG sync.WaitGroup
	roleErrs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		roleWG.Add(1)
		go func() {
			defer roleWG.Done()
			_, e := repo.SetPlatformRole(t.Context(), access.PlatformRoleInput{PrincipalID: p.ID, Role: access.PlatformRoleAdmin})
			roleErrs <- e
		}()
	}
	roleWG.Wait()
	close(roleErrs)
	for e := range roleErrs {
		if e != nil {
			t.Fatalf("concurrent platform role grant = %v", e)
		}
	}
	var roleCount int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.platform_role_binding WHERE principal_id=$1::uuid AND revoked_at IS NULL`, p.ID).Scan(&roleCount); err != nil {
		t.Fatal(err)
	}
	if roleCount != 1 {
		t.Fatalf("active platform role bindings = %d, want 1", roleCount)
	}

	conflictID := "10000000-0000-0000-0000-000000000098"
	var conflictWG sync.WaitGroup
	conflictErrs := make(chan error, 2)
	conflictWG.Add(2)
	go func() {
		defer conflictWG.Done()
		_, e := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: conflictID, Kind: access.PrincipalKindUser, DisplayName: "race-user"})
		conflictErrs <- e
	}()
	go func() {
		defer conflictWG.Done()
		_, e := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{ID: conflictID, Kind: access.PrincipalKindServicePrincipal, DisplayName: "race-service"})
		conflictErrs <- e
	}()
	conflictWG.Wait()
	close(conflictErrs)
	conflictSuccess := 0
	conflictFailure := 0
	for e := range conflictErrs {
		if e == nil {
			conflictSuccess++
		} else {
			conflictFailure++
		}
	}
	if conflictSuccess != 1 || conflictFailure != 1 {
		t.Fatalf("same-ID principal race results: success=%d failure=%d", conflictSuccess, conflictFailure)
	}
	var conflictKind string
	if err := db.admin.QueryRow(t.Context(), `SELECT principal_type FROM access.principal WHERE id=$1::uuid`, conflictID).Scan(&conflictKind); err != nil {
		t.Fatal(err)
	}
	if conflictKind != "user" && conflictKind != "service" {
		t.Fatalf("same-ID principal race kind = %q", conflictKind)
	}

	desktop, err := repo.CreateDesktopSession(t.Context(), p.ID, "instance_0123456789abcdef0123456789abcdef", "profile_0123456789abcdef0123456789abcdef", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- repo.RevokeDesktopSession(t.Context(), desktop, "instance_0123456789abcdef0123456789abcdef", "profile_0123456789abcdef0123456789abcdef")
		}()
	}
	wg.Wait()
	close(errs)
	count := 0
	for err := range errs {
		if err == nil {
			count++
		} else if !errors.Is(err, pgx.ErrNoRows) {
			t.Fatalf("concurrent revoke = %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("successful concurrent revocations = %d, want 1", count)
	}
}

func TestAccessCoreCleanTargetInvariants(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}

	// Local groups have no external ID and therefore must not collide.
	if _, err := repo.UpsertGroup(t.Context(), access.GroupInput{Name: "local one"}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertGroup(t.Context(), access.GroupInput{Name: "local two"}); err != nil {
		t.Fatal(err)
	}
	groups, err := repo.ListGroups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 {
		t.Fatalf("local groups = %d, want 2", len(groups))
	}

	p, err := repo.UpsertPrincipal(t.Context(), access.PrincipalInput{Kind: access.PrincipalKindUser, DisplayName: "immutable"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateServicePrincipal(t.Context(), access.ServicePrincipalInput{ID: p.ID, DisplayName: "rewrite"}); err == nil {
		t.Fatal("principal kind rewrite accepted")
	}
	service, err := repo.CreateServicePrincipal(t.Context(), access.ServicePrincipalInput{DisplayName: "tombstoned service"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteServicePrincipal(t.Context(), service.ID); err != nil {
		t.Fatal(err)
	}
	services, err := repo.ListServicePrincipals(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, listed := range services {
		if listed.ID == service.ID {
			t.Fatal("revoked service principal remained in active list")
		}
	}
	var serviceHistory int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.principal WHERE id=$1::uuid AND revoked_at IS NOT NULL`, service.ID).Scan(&serviceHistory); err != nil {
		t.Fatal(err)
	}
	if serviceHistory != 1 {
		t.Fatal("service principal tombstone was not retained")
	}
	if err := repo.BootstrapAdmin(t.Context(), "bootstrap@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := repo.BootstrapAdmin(t.Context(), "BOOTSTRAP@example.com"); err != nil {
		t.Fatal(err)
	}
	var bootstrapID string
	if err := db.admin.QueryRow(t.Context(), `SELECT id::text FROM access.principal WHERE email='bootstrap@example.com' AND revoked_at IS NULL`).Scan(&bootstrapID); err != nil {
		t.Fatal(err)
	}
	admin, err := repo.IsPlatformAdmin(t.Context(), bootstrapID)
	if err != nil || !admin {
		t.Fatalf("bootstrap admin status=%v err=%v", admin, err)
	}

	// Pending principals cannot mint or authenticate credentials.
	pendingID := "10000000-0000-0000-0000-000000000099"
	if _, err := db.admin.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type,status) VALUES($1,'user','pending')`, pendingID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateSession(t.Context(), pendingID, time.Hour); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("pending session = %v", err)
	}
	desktopToken, err := repo.CreateDesktopSession(t.Context(), p.ID, "instance_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "profile_bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	before, err := repo.ListSessions(t.Context(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	var previous string
	for _, s := range before {
		if s.Kind == access.SessionKindDesktop {
			previous = s.ExpiresAt
		}
	}
	renewed, err := repo.DesktopSessionForToken(t.Context(), desktopToken)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.ExpiresAt == previous || renewed.ExpiresAt == "" {
		t.Fatalf("desktop expiry was not returned from renewal: before=%q after=%q", previous, renewed.ExpiresAt)
	}

	// Tombstones and revocation cannot be erased by a privileged direct SQL
	// writer, preserving history for audit/reconciliation.
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.access_group SET revoked_at=clock_timestamp() WHERE id=$1::uuid`, groups[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.access_group SET revoked_at=NULL WHERE id=$1::uuid`, groups[0].ID); err == nil {
		t.Fatal("group revocation clear accepted")
	}
	if _, err := db.admin.Exec(t.Context(), `DELETE FROM access.principal WHERE id=$1::uuid`, p.ID); err == nil {
		t.Fatal("principal hard delete accepted")
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal SET id=gen_random_uuid() WHERE id=$1::uuid`, p.ID); err == nil {
		t.Fatal("principal id rewrite accepted")
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal SET created_at=created_at+interval '1 second' WHERE id=$1::uuid`, p.ID); err == nil {
		t.Fatal("principal created_at rewrite accepted")
	}
	if _, err := db.admin.Exec(t.Context(), `UPDATE access.principal SET updated_at=updated_at-interval '1 second' WHERE id=$1::uuid`, p.ID); err == nil {
		t.Fatal("principal updated_at rollback accepted")
	}

	// SCIM replacement revokes removed membership while retaining its row.
	u1, err := repo.UpsertSCIMUser(t.Context(), access.SCIMUserInput{ExternalID: "u1", UserName: "u1", Email: "u1@example.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	u2, err := repo.UpsertSCIMUser(t.Context(), access.SCIMUserInput{ExternalID: "u2", UserName: "u2", Email: "u2@example.com", Active: true})
	if err != nil {
		t.Fatal(err)
	}
	sg, err := repo.UpsertSCIMGroup(t.Context(), access.SCIMGroupInput{ExternalID: "g1", Name: "scim", MemberIDs: []string{u1.Principal.ID, u2.Principal.ID}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.UpsertSCIMGroup(t.Context(), access.SCIMGroupInput{ID: sg.ID, ExternalID: "g1", Name: "scim", MemberIDs: []string{u1.Principal.ID}}); err != nil {
		t.Fatal(err)
	}
	members, err := repo.ListSCIMGroupMembers(t.Context(), sg.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 1 || members[0].PrincipalID != u1.Principal.ID {
		t.Fatalf("SCIM replacement members = %#v", members)
	}
	var history int
	if err := db.admin.QueryRow(t.Context(), `SELECT count(*) FROM access.principal_group WHERE group_id=$1::uuid`, sg.ID).Scan(&history); err != nil {
		t.Fatal(err)
	}
	if history != 2 {
		t.Fatalf("membership history rows = %d, want 2", history)
	}
	credentialToken, err := repo.CreateSession(t.Context(), u1.Principal.ID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	apiSecret, _, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: u1.Principal.ID, Name: "disable-check", ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.DisableSCIMUser(t.Context(), u1.Principal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.PrincipalForToken(t.Context(), credentialToken); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled browser session = %v", err)
	}
	if _, err := repo.PrincipalForAPIToken(t.Context(), apiSecret); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("disabled API token = %v", err)
	}
	var storedProvider, storedTenant, storedSubject string
	if err := db.admin.QueryRow(t.Context(), `SELECT provider,tenant_id,subject FROM access.external_identity WHERE principal_id=$1::uuid`, u1.Principal.ID).Scan(&storedProvider, &storedTenant, &storedSubject); err != nil {
		t.Fatal(err)
	}
	if storedProvider != "scim" || storedTenant != "" || storedSubject != "u1" {
		t.Fatalf("relational external identity = %q/%q/%q", storedProvider, storedTenant, storedSubject)
	}
}

func TestAccessCoreConfiguredFingerprintRequired(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	if _, err := NewAccess(db.runtime, FingerprintConfig{}); err == nil {
		t.Fatal("empty fingerprint key accepted")
	}
}

func TestAccessCoreConcurrentPasswordChangeUsesLockedVerifier(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "password-lock@example.com", Password: "initial password that is long enough"})
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, next := range []string{"first replacement password", "second replacement password"} {
		wg.Add(1)
		go func(value string) {
			defer wg.Done()
			_, e := repo.ChangeLocalPassword(t.Context(), p.Principal.ID, "initial password that is long enough", value)
			results <- e
		}(next)
	}
	wg.Wait()
	close(results)
	success := 0
	for e := range results {
		if e == nil {
			success++
		} else if !errors.Is(e, pgx.ErrNoRows) {
			t.Fatalf("concurrent password change = %v", e)
		}
	}
	if success != 1 {
		t.Fatalf("successful password changes = %d, want 1", success)
	}
}

func TestAccessCoreDatabaseClockExpiryBoundary(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	p, err := repo.CreateLocalUser(t.Context(), access.LocalUserInput{Email: "db-clock@example.com", Password: "database clock password"})
	if err != nil {
		t.Fatal(err)
	}
	var apiExpiry time.Time
	if err := db.runtime.QueryRow(t.Context(), `SELECT clock_timestamp()+interval '2 hours'`).Scan(&apiExpiry); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: p.Principal.ID, Name: "db-clock-api", ExpiresAt: apiExpiry}); err != nil {
		t.Fatalf("database-clock API expiry: %v", err)
	}
	sp, err := repo.CreateServicePrincipal(t.Context(), access.ServicePrincipalInput{DisplayName: "db-clock-service"})
	if err != nil {
		t.Fatal(err)
	}
	var secretExpiry time.Time
	if err := db.runtime.QueryRow(t.Context(), `SELECT clock_timestamp()+interval '2 hours'`).Scan(&secretExpiry); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateServicePrincipalSecret(t.Context(), sp.ID, access.ServicePrincipalSecretInput{Name: "db-clock-secret", ExpiresAt: secretExpiry}); err != nil {
		t.Fatalf("database-clock service secret expiry: %v", err)
	}
	var expired time.Time
	if err := db.runtime.QueryRow(t.Context(), `SELECT clock_timestamp()-interval '1 second'`).Scan(&expired); err != nil {
		t.Fatal(err)
	}
	if _, _, err := repo.CreateAPITokenWithMetadata(t.Context(), access.APITokenInput{PrincipalID: p.Principal.ID, Name: "expired-api", ExpiresAt: expired}); err == nil || !strings.Contains(err.Error(), "expiry is invalid") {
		t.Fatalf("expired API expiry error = %v", err)
	}
}
