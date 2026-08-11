package sqlite_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/access"
	accesssqlite "github.com/flidai/leapview/internal/access/sqlite"
	"github.com/flidai/leapview/internal/platform"
)

func TestAuthoringAuthSQLiteDeviceExchangeIsAtomicAndRefreshReplayRevokesFamily(t *testing.T) {
	ctx := context.Background()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	repository := accesssqlite.NewRepository(store.SQLDB())
	principal, err := repository.UpsertPrincipal(ctx, access.PrincipalInput{
		Email: "developer@example.com", DisplayName: "Developer",
	})
	if err != nil {
		t.Fatalf("create principal: %v", err)
	}
	now := time.Date(2026, 7, 29, 12, 0, 0, 0, time.UTC)
	service, err := access.NewAuthoringAuthService(repository, access.AuthoringAuthConfig{
		InstanceID: "instance-prod", CanonicalOrigin: "https://prod.leapview.example",
		DeviceTTL: 10 * time.Minute, PollInterval: 5 * time.Second,
		AccessTokenTTL: 15 * time.Minute, RefreshTokenTTL: 8 * time.Hour,
		WorkloadMaxTTL: 30 * time.Minute, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("new authoring auth service: %v", err)
	}
	scope, err := access.NewAuthoringScope("instance-prod", "finance", []access.Privilege{access.PrivilegeDeploy})
	if err != nil {
		t.Fatalf("new scope: %v", err)
	}
	started, err := service.BeginDeviceAuthorization(ctx, scope)
	if err != nil {
		t.Fatalf("begin device authorization: %v", err)
	}
	if err := service.ApproveDeviceAuthorization(ctx, principal, started.UserCode); err != nil {
		t.Fatalf("approve device authorization: %v", err)
	}

	var wait sync.WaitGroup
	results := make(chan access.AuthoringTokenSet, 2)
	errs := make(chan error, 2)
	for range 2 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			tokens, exchangeErr := service.ExchangeDeviceCode(ctx, started.DeviceCode)
			results <- tokens
			errs <- exchangeErr
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	successes := 0
	var tokens access.AuthoringTokenSet
	for result := range results {
		if result.AccessToken != "" {
			successes++
			tokens = result
		}
	}
	if successes != 1 {
		t.Fatalf("successful concurrent exchanges = %d, want 1", successes)
	}
	failures := 0
	for exchangeErr := range errs {
		if exchangeErr != nil {
			failures++
		}
	}
	if failures != 1 {
		t.Fatalf("failed concurrent exchanges = %d, want 1", failures)
	}

	rotated, err := service.Refresh(ctx, tokens.RefreshToken)
	if err != nil {
		t.Fatalf("refresh credential: %v", err)
	}
	if _, err := service.Refresh(ctx, tokens.RefreshToken); !errors.Is(err, access.ErrAuthoringRefreshReplay) {
		t.Fatalf("replay refresh error = %v", err)
	}
	if _, err := service.Authenticate(ctx, rotated.AccessToken, "instance-prod", "finance", access.PrivilegeDeploy); !errors.Is(err, access.ErrInvalidAuthoringCredential) {
		t.Fatalf("authenticate replay-revoked family error = %v", err)
	}
	events, err := repository.ListAuditEvents(ctx, access.AuditEventFilter{})
	if err != nil {
		t.Fatalf("list authoring audit events: %v", err)
	}
	actions := map[string]bool{}
	for _, event := range events {
		actions[event.Action] = true
	}
	for _, action := range []string{
		"authoring.device.started", "authoring.device.decided", "authoring.session.created",
		"authoring.token.refreshed", "authoring.refresh.replay",
	} {
		if !actions[action] {
			t.Errorf("authoring audit action %q is missing from %#v", action, actions)
		}
	}

	var leaked int
	for _, secret := range []string{started.DeviceCode, started.UserCode, tokens.AccessToken, tokens.RefreshToken, rotated.AccessToken, rotated.RefreshToken} {
		if err := store.SQLDB().QueryRowContext(ctx, `
			SELECT
			  (SELECT count(*) FROM oauth_device_authorizations WHERE device_code_hash = ? OR user_code_hash = ?)
			  + (SELECT count(*) FROM oauth_authoring_credentials WHERE access_token_hash = ? OR refresh_token_hash = ?)
		`, secret, strings.ReplaceAll(secret, "-", ""), secret, secret).Scan(&leaked); err != nil {
			t.Fatalf("inspect stored credential material: %v", err)
		}
		if leaked != 0 {
			t.Fatalf("plaintext credential material persisted for %q", secret[:min(12, len(secret))])
		}
		if err := store.SQLDB().QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE metadata_json LIKE '%' || ? || '%'`, secret).Scan(&leaked); err != nil {
			t.Fatalf("inspect audit metadata: %v", err)
		}
		if leaked != 0 {
			t.Fatalf("plaintext credential material audited for %q", secret[:min(12, len(secret))])
		}
	}
}
