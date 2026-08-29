package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accessdb "github.com/flidai/leapview/internal/access/postgres/internal/db"
	"github.com/flidai/leapview/internal/project/graph"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Database-clock helpers must not depend on a connection's presentation
// settings. Epoch microseconds remain stable when PostgreSQL emits timestamps in a
// non-default timezone or DateStyle.
func TestAuthoringDatabaseClockIgnoresSessionPresentation(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	conn, err := db.runtime.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(t.Context(), "SET TIME ZONE 'America/Los_Angeles'"); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(t.Context(), "SET DateStyle TO 'SQL, DMY'"); err != nil {
		t.Fatal(err)
	}
	repo, err := NewAccess(conn.Conn(), FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	result, err := repo.InitializeInstance(t.Context(), access.InstanceInitializationInput{Email: "clock-settings@example.com", Environment: "production"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.PublisherToken == "" || result.PublisherTokenExpiresAt.Before(time.Now().UTC().Add(23*time.Hour)) {
		t.Fatalf("database-clock expiry under session settings = %s", result.PublisherTokenExpiresAt)
	}
}

func TestAuthoringPrincipalRotationLockOrdersRevocation(t *testing.T) {
	db := newStandaloneAccessDatabase(t)
	repo, err := NewAccess(db.runtime, FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	principal, credential, rotation := seedRotatableWorkloadCredential(t, db, repo, "first", "a", "b")
	principalID, err := pgUUID(principal.ID)
	if err != nil {
		t.Fatal(err)
	}

	gateConn, err := db.runtime.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gateConn.Release()
	rotateConn, err := db.runtime.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer rotateConn.Release()
	disableConn, err := db.runtime.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer disableConn.Release()
	probeConn, err := db.runtime.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer probeConn.Release()
	for _, conn := range []*pgxpool.Conn{rotateConn, disableConn} {
		if _, err := conn.Exec(ctx, "SET application_name = 'access-rotation-lock-order'"); err != nil {
			t.Fatal(err)
		}
	}
	gateTx, err := gateConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer gateTx.Rollback(ctx)
	var gatedPrincipalID string
	if err := gateTx.QueryRow(ctx, `SELECT id FROM access.principal WHERE id=$1 AND status='active' AND revoked_at IS NULL AND disabled_at IS NULL AND blocked_at IS NULL FOR UPDATE`, principalID).Scan(&gatedPrincipalID); err != nil {
		t.Fatal(err)
	}
	if gatedPrincipalID == "" {
		t.Fatal("principal lock gate returned an empty principal id")
	}

	rotateRepo, err := NewAccess(rotateConn.Conn(), FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	rotateDone := make(chan error, 1)
	go func() {
		_, rotateErr := rotateRepo.RotateAuthoringCredential(ctx, rotation)
		rotateDone <- rotateErr
	}()
	select {
	case rotateErr := <-rotateDone:
		t.Fatalf("rotation completed before lock gate: %v", rotateErr)
	case <-time.After(100 * time.Millisecond):
	}
	waitForAuthoringLock(t, db, rotateConn.Conn().PgConn().PID())

	disableRepo, err := NewAccess(disableConn.Conn(), FingerprintConfig{Key: []byte("0123456789abcdef0123456789abcdef")})
	if err != nil {
		t.Fatal(err)
	}
	disableDone := make(chan error, 1)
	go func() {
		_, disableErr := disableRepo.DisableProvisionedPrincipal(ctx, principal.ID)
		disableDone <- disableErr
	}()
	waitForAuthoringLock(t, db, disableConn.Conn().PgConn().PID())

	probeTx, err := probeConn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var probedCredentialID string
	if err := probeTx.QueryRow(ctx, `SELECT id FROM access.authoring_credential WHERE refresh_token_hash=$1 FOR UPDATE NOWAIT`, rotation.RefreshTokenHash).Scan(&probedCredentialID); err != nil {
		_ = probeTx.Rollback(ctx)
		t.Fatalf("credential lock was acquired before principal share lock: %v", err)
	}
	if probedCredentialID == "" {
		t.Fatal("credential lock probe returned an empty credential id")
	}
	if err := probeTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := gateTx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-rotateDone; err != nil {
		t.Fatalf("rotation after principal lock release: %v", err)
	}
	if err := <-disableDone; err != nil {
		t.Fatalf("disable after rotation: %v", err)
	}
	var revoked bool
	if err := db.admin.QueryRow(ctx, `SELECT revoked_at IS NOT NULL FROM access.authoring_session WHERE id=$1`, credential.Session.ID).Scan(&revoked); err != nil {
		t.Fatal(err)
	}
	if !revoked {
		t.Fatal("disable committed without revoking the rotated authoring session")
	}
	if _, err := repo.AuthoringCredentialByAccessTokenHash(ctx, rotation.AccessTokenHash, time.Now().UTC()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("revoked rotated credential lookup error = %v", err)
	}

	secondPrincipal, _, secondRotation := seedRotatableWorkloadCredential(t, db, repo, "second", "1", "2")
	if _, err := repo.DisableProvisionedPrincipal(ctx, secondPrincipal.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RotateAuthoringCredential(ctx, secondRotation); !errors.Is(err, access.ErrInvalidAuthoringPrincipal) {
		t.Fatalf("rotation after committed disable error = %v", err)
	}
}

func seedRotatableWorkloadCredential(t *testing.T, db auditDatabase, repo *Repository, suffix, accessChar, refreshChar string) (access.Principal, access.AuthoringCredential, access.AuthoringCredentialRotation) {
	t.Helper()
	principal, err := repo.CreateServicePrincipal(t.Context(), access.ServicePrincipalInput{DisplayName: "rotation-" + suffix})
	if err != nil {
		t.Fatal(err)
	}
	projectID, err := graph.NewResourceID("project_rotation_" + suffix)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	credential, err := repo.CreateWorkloadCredential(t.Context(), access.WorkloadCredentialIssue{
		Session: access.AuthoringSession{ID: "rotation-session-" + suffix, Kind: access.AuthoringSessionWorkload, ClientID: principal.ID, PrincipalID: principal.ID,
			Scope: access.AuthoringScope{TargetID: "target-" + suffix, ProjectID: projectID, Capabilities: []access.Capability{access.CapabilityResourceRead}}, CreatedAt: now, ExpiresAt: now.Add(2 * time.Hour)},
		CredentialID: "rotation-credential-" + suffix, AccessTokenHash: strings.Repeat(accessChar, 64), AccessExpiresAt: now.Add(30 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	refreshHash := strings.Repeat(refreshChar, 64)
	tx, err := db.runtime.Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(t.Context())
	q := accessdb.New(tx)
	if err := q.MarkAuthoringCredentialInactive(t.Context(), credential.ID); err != nil {
		t.Fatal(err)
	}
	if err := q.InsertRotatedAuthoringCredential(t.Context(), accessdb.InsertRotatedAuthoringCredentialParams{ID: "rotation-refresh-" + suffix, SessionID: credential.Session.ID,
		AccessTokenHash: strings.Repeat(string(rune(accessChar[0]+2)), 64), RefreshTokenHash: &refreshHash, AccessExpiresAt: pgTimestamp(now.Add(30 * time.Minute)), RefreshExpiresAt: pgTimestamp(now.Add(90 * time.Minute))}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(t.Context()); err != nil {
		t.Fatal(err)
	}
	rotation := access.AuthoringCredentialRotation{RefreshTokenHash: refreshHash, Now: time.Unix(1, 0), CredentialID: "rotation-next-" + suffix,
		AccessTokenHash: strings.Repeat(string(rune(accessChar[0]+3)), 64), RefreshTokenHashNew: strings.Repeat(string(rune(refreshChar[0]+1)), 64), AccessExpiresAt: now.Add(45 * time.Minute), RefreshExpiresAt: now.Add(2 * time.Hour)}
	return principal, credential, rotation
}

func waitForAuthoringLock(t *testing.T, db auditDatabase, pid uint32) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	for {
		var waiting bool
		if err := db.admin.QueryRow(ctx, `SELECT cardinality(pg_blocking_pids($1)) > 0`, pid).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("backend %d did not enter a lock wait: %v", pid, ctx.Err())
		case <-time.After(10 * time.Millisecond):
		}
	}
}
