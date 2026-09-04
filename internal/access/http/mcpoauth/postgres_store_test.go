package mcpoauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	accesspostgres "github.com/flidai/leapview/internal/access/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/fosite"
)

type oauthPostgresDatabase struct {
	runtime *pgxpool.Pool
}

// newOAuthPostgresDatabase provisions the same least-privilege runtime role
// used by the access PostgreSQL conformance tests. The schema is applied once
// by the migration role; PostgresStore receives only the native runtime pool.
func newOAuthPostgresDatabase(t *testing.T) oauthPostgresDatabase {
	t.Helper()
	h := postgrestest.Start(t)
	owner := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_owner"})
	migrator := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_migrator"})
	runtimeRole := h.EnsureRole(t, postgrestest.Role{Name: "leapview_control_runtime", Password: "leapview-conformance-secret", Login: true})
	h.GrantRole(t, owner, migrator)
	database := h.NewDatabase(t, "")
	h.GrantDatabase(t, database.Name, migrator, "CONNECT", "CREATE")
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, database.AdminURL())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	conn, err := admin.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(ctx, `SET ROLE leapview_control_migrator`); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		conn.Release()
		t.Fatal(err)
	}
	if err := accesspostgres.ApplySchema(ctx, tx); err != nil {
		_ = tx.Rollback(ctx)
		conn.Release()
		t.Fatalf("apply access schema: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		conn.Release()
		t.Fatal(err)
	}
	conn.Release()
	runtime, err := pgxpool.New(ctx, database.URL(runtimeRole))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(runtime.Close)
	return oauthPostgresDatabase{runtime: runtime}
}

type noBeginOAuthDB struct{ accesspostgres.DBTX }

func TestPostgresStoreRequiresNativeTransactions(t *testing.T) {
	db := newOAuthPostgresDatabase(t)
	if _, err := NewPostgresStore(noBeginOAuthDB{DBTX: db.runtime}); err == nil {
		t.Fatal("NewPostgresStore accepted a DBTX without Begin")
	}
	_, err := NewPostgresStore(db.runtime)
	if err != nil {
		t.Fatalf("NewPostgresStore(native pool): %v", err)
	}
}

func TestPostgresStoreSessionTransactionsAndRotation(t *testing.T) {
	db := newOAuthPostgresDatabase(t)
	store, err := NewPostgresStore(db.runtime)
	if err != nil {
		t.Fatal(err)
	}
	principalID := uuid.NewString()
	if _, err := db.runtime.Exec(t.Context(), `INSERT INTO access.principal(id,principal_type,status,email,display_name) VALUES($1,'service','active',$2,$2)`, principalID, "oauth-principal@example.com"); err != nil {
		t.Fatalf("create OAuth principal: %v", err)
	}
	client := StoredClient{
		ID: "oauth-test-client", Name: "OAuth Test Client", RedirectURIs: []string{"https://client.example/callback"},
		GrantTypes: []string{"authorization_code", "refresh_token"}, ResponseTypes: []string{"code"},
		Scopes: []string{ScopeMCPUse}, Audience: []string{"https://leapview.example/mcp"}, Public: true,
		TokenEndpointAuthMethod: "none", PrincipalID: principalID,
	}
	if err := store.CreateClient(t.Context(), client); err != nil {
		t.Fatalf("create OAuth client: %v", err)
	}
	persisted, err := accesspostgres.GetOAuthClient(t.Context(), db.runtime, client.ID)
	if err != nil {
		t.Fatalf("read persisted OAuth client: %v", err)
	}
	if persisted.PrincipalID != principalID {
		t.Fatalf("persisted OAuth principal id = %q, want %q", persisted.PrincipalID, principalID)
	}
	request := testOAuthRequester(client.ID, "request-commit")
	txCtx, err := store.BeginTX(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAuthorizeCodeSession(txCtx, "authorize-commit", request); err != nil {
		t.Fatalf("create authorize session: %v", err)
	}
	if err := store.Commit(txCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAuthorizeCodeSession(t.Context(), "authorize-commit", nil); err != nil {
		t.Fatalf("read committed authorize session: %v", err)
	}

	txCtx, err = store.BeginTX(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.CreateAccessTokenSession(txCtx, "access-rollback", request); err != nil {
		t.Fatalf("create rollback session: %v", err)
	}
	if err := store.Rollback(txCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetAccessTokenSession(t.Context(), "access-rollback", nil); !errors.Is(err, fosite.ErrNotFound) {
		t.Fatalf("rolled-back access session error = %v", err)
	}

	if err := store.CreateAuthorizeCodeSession(t.Context(), "authorize-invalidate", request); err != nil {
		t.Fatal(err)
	}
	if err := store.InvalidateAuthorizeCodeSession(t.Context(), "authorize-invalidate"); err != nil {
		t.Fatalf("invalidate authorize session: %v", err)
	}
	if _, err := store.GetAuthorizeCodeSession(t.Context(), "authorize-invalidate", nil); !errors.Is(err, fosite.ErrInvalidatedAuthorizeCode) {
		t.Fatalf("invalidated authorize session error = %v", err)
	}

	rotateRequest := testOAuthRequester(client.ID, "request-rotate")
	if err := store.CreateAccessTokenSession(t.Context(), "access-rotate", rotateRequest); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRefreshTokenSession(t.Context(), "refresh-rotate", "access-rotate", rotateRequest); err != nil {
		t.Fatal(err)
	}
	if err := store.RotateRefreshToken(t.Context(), "request-rotate", "refresh-rotate"); err != nil {
		t.Fatalf("rotate refresh session: %v", err)
	}
	if _, err := store.GetRefreshTokenSession(t.Context(), "refresh-rotate", nil); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Fatalf("rotated refresh session error = %v", err)
	}
	if _, err := store.GetAccessTokenSession(t.Context(), "access-rotate", nil); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Fatalf("rotated access session error = %v", err)
	}
	// RevokeRefreshToken has the same single-statement atomicity guarantee when
	// called directly, without an enclosing fosite transaction wrapper.
	revokeRequest := testOAuthRequester(client.ID, "request-revoke")
	if err := store.CreateAccessTokenSession(t.Context(), "access-revoke", revokeRequest); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateRefreshTokenSession(t.Context(), "refresh-revoke", "access-revoke", revokeRequest); err != nil {
		t.Fatal(err)
	}
	if err := store.RevokeRefreshToken(t.Context(), "request-revoke"); err != nil {
		t.Fatalf("revoke refresh session: %v", err)
	}
	if _, err := store.GetRefreshTokenSession(t.Context(), "refresh-revoke", nil); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Fatalf("revoked refresh session error = %v", err)
	}
	if _, err := store.GetAccessTokenSession(t.Context(), "access-revoke", nil); !errors.Is(err, fosite.ErrInactiveToken) {
		t.Fatalf("revoked access session error = %v", err)
	}
}

func TestPostgresStoreJTIConcurrencyAndExpiredReplacement(t *testing.T) {
	db := newOAuthPostgresDatabase(t)
	store, err := NewPostgresStore(db.runtime)
	if err != nil {
		t.Fatal(err)
	}
	jti := "concurrent-jti"
	expires := time.Now().UTC().Add(time.Hour)
	const workers = 16
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- store.SetClientAssertionJWT(t.Context(), jti, expires)
		}()
	}
	wg.Wait()
	close(errs)
	var success, known int
	for err := range errs {
		switch {
		case err == nil:
			success++
		case errors.Is(err, fosite.ErrJTIKnown):
			known++
		default:
			t.Fatalf("concurrent JTI error = %v", err)
		}
	}
	if success != 1 || known != workers-1 {
		t.Fatalf("concurrent JTI outcomes = %d success, %d known", success, known)
	}
	if err := store.ClientAssertionJWTValid(t.Context(), jti); !errors.Is(err, fosite.ErrJTIKnown) {
		t.Fatalf("stored JTI validity = %v", err)
	}

	expired := "expired-replacement"
	if _, err := db.runtime.Exec(t.Context(), `INSERT INTO access.oauth_client_assertion(jti, expires_at) VALUES($1, clock_timestamp() - interval '1 minute')`, expired); err != nil {
		t.Fatal(err)
	}
	if err := store.ClientAssertionJWTValid(t.Context(), expired); err != nil {
		t.Fatalf("expired JTI validity: %v", err)
	}
	if err := store.SetClientAssertionJWT(t.Context(), expired, time.Now().UTC().Add(time.Hour)); err != nil {
		t.Fatalf("expired JTI replacement: %v", err)
	}
}

func testOAuthRequester(clientID, requestID string) fosite.Requester {
	request := fosite.NewRequest()
	request.ID = requestID
	request.RequestedAt = time.Now().UTC()
	request.Client = &fosite.DefaultClient{ID: clientID, Public: true}
	request.Session = &fosite.DefaultSession{}
	request.RequestedScope = fosite.Arguments{ScopeMCPUse}
	request.GrantedScope = fosite.Arguments{ScopeMCPUse}
	return request
}
