package sqlite

import (
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/platform"
)

func TestDesktopSessionMetadataIsTransactionalAndBoundToProfile(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	repository := NewRepository(store.SQLDB())
	principal, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{
		PrincipalID: "principal_desktop",
		Email:       "desktop@example.com",
		DisplayName: "Desktop User",
		Role:        access.PlatformRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	const (
		instanceID = "instance_0123456789abcdef0123456789abcdef"
		profileID  = "profile_0123456789abcdef0123456789abcdef"
	)
	var token string
	err = repository.RunAuditedMutation(t.Context(), func(txRepository access.Repository) (access.AuditEventInput, error) {
		desktop, ok := txRepository.(access.DesktopSessionRepository)
		if !ok {
			t.Fatal("transaction repository does not implement desktop sessions")
		}
		token, err = desktop.CreateDesktopSession(t.Context(), principal.ID, instanceID, profileID, time.Hour)
		return access.AuditEventInput{
			PrincipalID:  principal.ID,
			Action:       "desktop_session.created",
			ResourceKind: "desktop_profile",
			ResourceID:   profileID,
			Status:       "success",
			MetadataJSON: "{}",
		}, err
	})
	if err != nil {
		t.Fatalf("create audited desktop session: %v", err)
	}
	binding, err := repository.DesktopSessionForToken(t.Context(), token)
	if err != nil {
		t.Fatalf("read desktop session: %v", err)
	}
	if binding.PrincipalID != principal.ID || binding.InstanceID != instanceID || binding.ProfileID != profileID {
		t.Fatalf("desktop session binding = %#v", binding)
	}
	if err := repository.RevokeDesktopSession(
		t.Context(),
		token,
		instanceID,
		"profile_ffffffffffffffffffffffffffffffff",
	); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("wrong-profile revocation error = %v, want sql.ErrNoRows", err)
	}
	if _, err := repository.DesktopSessionForToken(t.Context(), token); err != nil {
		t.Fatalf("wrong-profile revocation changed the bound session: %v", err)
	}
	if err := repository.RevokeDesktopSession(t.Context(), token, instanceID, profileID); err != nil {
		t.Fatalf("revoke desktop session: %v", err)
	}
	if _, err := repository.PrincipalForToken(t.Context(), token); err == nil {
		t.Fatal("revoked desktop session still authenticates")
	}
	if _, err := repository.DesktopSessionForToken(t.Context(), token); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked desktop session lookup error = %v, want sql.ErrNoRows", err)
	}

	var rolledBackToken string
	mutationErr := errors.New("force audited mutation rollback")
	err = repository.RunAuditedMutation(t.Context(), func(txRepository access.Repository) (access.AuditEventInput, error) {
		desktop := txRepository.(access.DesktopSessionRepository)
		rolledBackToken, err = desktop.CreateDesktopSession(
			t.Context(), principal.ID, instanceID, profileID, time.Hour,
		)
		return access.AuditEventInput{}, mutationErr
	})
	if !errors.Is(err, mutationErr) {
		t.Fatalf("rolled-back mutation error = %v, want %v", err, mutationErr)
	}
	if _, err := repository.DesktopSessionForToken(t.Context(), rolledBackToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("rolled-back desktop session lookup error = %v, want sql.ErrNoRows", err)
	}
}

func TestDesktopSessionsAreListedWithoutSecretsAndExpireOnIdleOrAbsoluteLifetime(t *testing.T) {
	store, err := platform.Open(t.Context(), filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	defer store.Close()
	repository := NewRepository(store.SQLDB())
	principal, err := repository.SetPlatformRole(t.Context(), access.PlatformRoleInput{
		PrincipalID: "principal_desktop_lifecycle",
		Email:       "desktop-lifecycle@example.com",
		DisplayName: "Desktop Lifecycle User",
		Role:        access.PlatformRoleAdmin,
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	const (
		instanceID = "instance_0123456789abcdef0123456789abcdef"
		profileID  = "profile_0123456789abcdef0123456789abcdef"
	)
	browserToken, err := repository.CreateSession(t.Context(), principal.ID, time.Hour)
	if err != nil {
		t.Fatalf("create browser session: %v", err)
	}
	desktopToken, err := repository.CreateDesktopSession(
		t.Context(), principal.ID, instanceID, profileID, access.DesktopSessionAbsoluteLifetime,
	)
	if err != nil {
		t.Fatalf("create desktop session: %v", err)
	}
	sessions, err := repository.ListSessions(t.Context(), principal.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("session count = %d, want 2", len(sessions))
	}
	var desktop access.Session
	for _, session := range sessions {
		if session.Kind == access.SessionKindDesktop {
			desktop = session
		}
	}
	if desktop.ID == "" || desktop.InstanceID != instanceID ||
		desktop.ProfileID != profileID || desktop.ClientID != "leapview-desktop" ||
		desktop.AbsoluteExpiresAt == "" {
		t.Fatalf("desktop session listing = %#v", desktop)
	}
	if _, err := repository.PrincipalForToken(t.Context(), desktopToken); err != nil {
		t.Fatalf("authenticate desktop session: %v", err)
	}
	sessionsAfterUse, err := repository.ListSessions(t.Context(), principal.ID)
	if err != nil {
		t.Fatalf("list sessions after use: %v", err)
	}
	if len(sessionsAfterUse) != len(sessions) {
		t.Fatalf("session count after use = %d, want %d without silent rotation", len(sessionsAfterUse), len(sessions))
	}
	var desktopAfterUse access.Session
	for _, session := range sessionsAfterUse {
		if session.Kind == access.SessionKindDesktop {
			desktopAfterUse = session
		}
	}
	if desktopAfterUse.ID != desktop.ID || desktopAfterUse.AbsoluteExpiresAt != desktop.AbsoluteExpiresAt {
		t.Fatalf("desktop session silently rotated or extended: before %#v, after %#v", desktop, desktopAfterUse)
	}
	if _, err := repository.db.ExecContext(t.Context(), `
UPDATE sessions SET last_seen_at = datetime('now', '-31 minutes')
WHERE id = ?
`, desktop.ID); err != nil {
		t.Fatalf("age desktop session: %v", err)
	}
	if _, err := repository.PrincipalForToken(t.Context(), desktopToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("idle desktop authentication error = %v, want sql.ErrNoRows", err)
	}
	if _, err := repository.PrincipalForToken(t.Context(), browserToken); err != nil {
		t.Fatalf("browser session was affected by desktop idle policy: %v", err)
	}

	replacementToken, err := repository.CreateDesktopSession(
		t.Context(), principal.ID, instanceID,
		"profile_ffffffffffffffffffffffffffffffff", access.DesktopSessionAbsoluteLifetime,
	)
	if err != nil {
		t.Fatalf("create replacement desktop session: %v", err)
	}
	replacement, err := repository.DesktopSessionForToken(t.Context(), replacementToken)
	if err != nil {
		t.Fatalf("read replacement desktop session: %v", err)
	}
	if _, err := repository.db.ExecContext(t.Context(), `
UPDATE desktop_sessions SET absolute_expires_at = datetime('now', '-1 minute')
WHERE session_id = ?
`, replacement.SessionID); err != nil {
		t.Fatalf("expire desktop absolute lifetime: %v", err)
	}
	if _, err := repository.PrincipalForToken(t.Context(), replacementToken); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("absolute desktop authentication error = %v, want sql.ErrNoRows", err)
	}
}
