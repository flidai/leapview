package sqlite

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flidai/leapview/internal/platform"
)

func TestPruneOperationalHistoryDryRunCountsWithoutDeleting(t *testing.T) {
	ctx := context.Background()
	store := testMaintenanceStore(t, ctx)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	seedOperationalHistory(t, ctx, store, now)

	result, err := PruneOperationalHistory(ctx, store.SQLDB(), RetentionOptions{
		Now:                           now,
		AuditEventsMaxAge:             365 * 24 * time.Hour,
		QueryEventsMaxAge:             90 * 24 * time.Hour,
		ArchivedAgentConversationsAge: 180 * 24 * time.Hour,
		DryRun:                        true,
	})
	if err != nil {
		t.Fatalf("prune operational history: %v", err)
	}
	if !result.DryRun {
		t.Fatal("DryRun = false, want true")
	}
	if result.AuditEventsDeleted != 1 || result.DeliveredAuditIntentsDeleted != 1 || result.QueryEventsDeleted != 1 || result.ArchivedAgentConversationsDeleted != 1 {
		t.Fatalf("result = %#v, want one candidate per retention class", result)
	}
	requireTableCount(t, ctx, store, "audit_events", 2)
	requireTableCount(t, ctx, store, "audit_outbox", 3)
	requireTableCount(t, ctx, store, "query_events", 2)
	requireTableCount(t, ctx, store, "agent_conversations", 3)
	requireTableCount(t, ctx, store, "agent_runs", 2)
	requireTableCount(t, ctx, store, "agent_messages", 2)
	requireAgentEventCount(t, ctx, store, 2)
}

func TestPruneOperationalHistoryDeletesOnlyExpiredOperationalRows(t *testing.T) {
	ctx := context.Background()
	store := testMaintenanceStore(t, ctx)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	seedOperationalHistory(t, ctx, store, now)

	result, err := PruneOperationalHistory(ctx, store.SQLDB(), RetentionOptions{
		Now:                           now,
		AuditEventsMaxAge:             365 * 24 * time.Hour,
		QueryEventsMaxAge:             90 * 24 * time.Hour,
		ArchivedAgentConversationsAge: 180 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("prune operational history: %v", err)
	}
	if result.DryRun {
		t.Fatal("DryRun = true, want false")
	}
	if result.AuditEventsDeleted != 1 || result.DeliveredAuditIntentsDeleted != 1 || result.QueryEventsDeleted != 1 || result.ArchivedAgentConversationsDeleted != 1 {
		t.Fatalf("result = %#v, want one deletion per retention class", result)
	}
	requireTableCount(t, ctx, store, "audit_events", 1)
	requireTableCount(t, ctx, store, "audit_outbox", 2)
	requireTableCount(t, ctx, store, "query_events", 1)
	requireTableCount(t, ctx, store, "agent_conversations", 2)
	requireTableCount(t, ctx, store, "agent_runs", 1)
	requireTableCount(t, ctx, store, "agent_messages", 1)
	requireAgentEventCount(t, ctx, store, 1)
	requireRowExists(t, ctx, store, "agent_conversations", "agent_active_old")
	requireRowExists(t, ctx, store, "agent_conversations", "agent_archived_recent")
}

func TestPruneOperationalHistoryDeletesStaleAuthState(t *testing.T) {
	ctx := context.Background()
	store := testMaintenanceStore(t, ctx)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	seedAuthState(t, ctx, store, now)

	result, err := PruneOperationalHistory(ctx, store.SQLDB(), RetentionOptions{
		Now:             now,
		AuthStateMaxAge: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("prune operational history: %v", err)
	}
	if result.ExpiredOAuthStatesDeleted != 1 ||
		result.StaleSessionsDeleted != 1 ||
		result.StaleAPITokensDeleted != 2 ||
		result.StaleServicePrincipalSecretsDeleted != 2 {
		t.Fatalf("auth prune result = %#v", result)
	}
	requireTableCount(t, ctx, store, "oauth_states", 1)
	requireTableCount(t, ctx, store, "sessions", 1)
	requireTableCount(t, ctx, store, "api_tokens", 1)
	requireTableCount(t, ctx, store, "service_principal_secrets", 1)
	requireRowExists(t, ctx, store, "oauth_states", "oauth_recent")
	requireRowExists(t, ctx, store, "sessions", "session_recent")
	requireRowExists(t, ctx, store, "api_tokens", "token_recent")
	requireRowExists(t, ctx, store, "service_principal_secrets", "secret_recent")
}

func TestPruneOperationalHistoryDeletesRFC3339StaleAuthState(t *testing.T) {
	ctx := context.Background()
	store := testMaintenanceStore(t, ctx)
	now := time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)
	seedAuthStateWithRFC3339Times(t, ctx, store, now)

	result, err := PruneOperationalHistory(ctx, store.SQLDB(), RetentionOptions{
		Now:             now,
		AuthStateMaxAge: 30 * 24 * time.Hour,
	})
	if err != nil {
		t.Fatalf("prune operational history: %v", err)
	}
	if result.ExpiredOAuthStatesDeleted != 1 ||
		result.StaleSessionsDeleted != 1 ||
		result.StaleAPITokensDeleted != 2 ||
		result.StaleServicePrincipalSecretsDeleted != 2 {
		t.Fatalf("auth prune result = %#v", result)
	}
	requireTableCount(t, ctx, store, "oauth_states", 1)
	requireTableCount(t, ctx, store, "sessions", 1)
	requireTableCount(t, ctx, store, "api_tokens", 1)
	requireTableCount(t, ctx, store, "service_principal_secrets", 1)
	requireRowExists(t, ctx, store, "oauth_states", "oauth_recent_rfc3339")
	requireRowExists(t, ctx, store, "sessions", "session_recent_rfc3339")
	requireRowExists(t, ctx, store, "api_tokens", "token_recent_rfc3339")
	requireRowExists(t, ctx, store, "service_principal_secrets", "secret_recent_rfc3339")
}

func testMaintenanceStore(t *testing.T, ctx context.Context) *platform.Store {
	t.Helper()
	store, err := platform.Open(ctx, filepath.Join(t.TempDir(), "leapview.db"))
	if err != nil {
		t.Fatalf("open platform store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close platform store: %v", err)
		}
	})
	return store
}

func seedAuthState(t *testing.T, ctx context.Context, store *platform.Store, now time.Time) {
	t.Helper()
	db := store.SQLDB()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, kind, email, display_name) VALUES
		('principal_1', 'user', 'owner@example.com', 'Owner'),
		('service_1', 'service_principal', '', 'Service')`); err != nil {
		t.Fatalf("seed principals: %v", err)
	}
	old := sqliteTime(now.Add(-(31 * 24 * time.Hour)))
	recent := sqliteTime(now.Add(-time.Hour))
	future := sqliteTime(now.Add(time.Hour))
	if _, err := db.ExecContext(ctx, `INSERT INTO oauth_states (id, state_hash, expires_at) VALUES
		('oauth_old', 'oauth_hash_old', ?),
		('oauth_recent', 'oauth_hash_recent', ?)`, old, recent); err != nil {
		t.Fatalf("seed oauth states: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions (id, principal_id, token_fingerprint, token_verifier, expires_at, revoked_at) VALUES
		('session_old', 'principal_1', 'session_fp_old', 'verifier', ?, NULL),
		('session_recent', 'principal_1', 'session_fp_recent', 'verifier', ?, NULL)`, old, recent); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_tokens (id, principal_id, name, token_fingerprint, token_verifier, expires_at, revoked_at) VALUES
		('token_expired_old', 'principal_1', 'expired old', 'token_fp_expired_old', 'verifier', ?, NULL),
		('token_revoked_old', 'principal_1', 'revoked old', 'token_fp_revoked_old', 'verifier', NULL, ?),
		('token_recent', 'principal_1', 'recent', 'token_fp_recent', 'verifier', ?, NULL)`, old, old, future); err != nil {
		t.Fatalf("seed api tokens: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO service_principal_secrets (id, service_principal_id, name, secret_fingerprint, secret_verifier, expires_at, revoked_at) VALUES
		('secret_expired_old', 'service_1', 'expired old', 'secret_fp_expired_old', 'verifier', ?, NULL),
		('secret_revoked_old', 'service_1', 'revoked old', 'secret_fp_revoked_old', 'verifier', NULL, ?),
		('secret_recent', 'service_1', 'recent', 'secret_fp_recent', 'verifier', ?, NULL)`, old, old, future); err != nil {
		t.Fatalf("seed service principal secrets: %v", err)
	}
}

func seedAuthStateWithRFC3339Times(t *testing.T, ctx context.Context, store *platform.Store, now time.Time) {
	t.Helper()
	db := store.SQLDB()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, kind, email, display_name) VALUES
		('principal_1', 'user', 'owner@example.com', 'Owner'),
		('service_1', 'service_principal', '', 'Service')`); err != nil {
		t.Fatalf("seed principals: %v", err)
	}
	old := now.Add(-(31 * 24 * time.Hour)).UTC().Format(time.RFC3339)
	recent := now.Add(-time.Hour).UTC().Format(time.RFC3339)
	future := now.Add(time.Hour).UTC().Format(time.RFC3339)
	if _, err := db.ExecContext(ctx, `INSERT INTO oauth_states (id, state_hash, expires_at) VALUES
		('oauth_old_rfc3339', 'oauth_hash_old_rfc3339', ?),
		('oauth_recent_rfc3339', 'oauth_hash_recent_rfc3339', ?)`, old, recent); err != nil {
		t.Fatalf("seed oauth states: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO sessions (id, principal_id, token_fingerprint, token_verifier, expires_at, revoked_at) VALUES
		('session_old_rfc3339', 'principal_1', 'session_fp_old_rfc3339', 'verifier', ?, NULL),
		('session_recent_rfc3339', 'principal_1', 'session_fp_recent_rfc3339', 'verifier', ?, NULL)`, old, recent); err != nil {
		t.Fatalf("seed sessions: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_tokens (id, principal_id, name, token_fingerprint, token_verifier, expires_at, revoked_at) VALUES
		('token_expired_old_rfc3339', 'principal_1', 'expired old', 'token_fp_expired_old_rfc3339', 'verifier', ?, NULL),
		('token_revoked_old_rfc3339', 'principal_1', 'revoked old', 'token_fp_revoked_old_rfc3339', 'verifier', NULL, ?),
		('token_recent_rfc3339', 'principal_1', 'recent', 'token_fp_recent_rfc3339', 'verifier', ?, NULL)`, old, old, future); err != nil {
		t.Fatalf("seed api tokens: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO service_principal_secrets (id, service_principal_id, name, secret_fingerprint, secret_verifier, expires_at, revoked_at) VALUES
		('secret_expired_old_rfc3339', 'service_1', 'expired old', 'secret_fp_expired_old_rfc3339', 'verifier', ?, NULL),
		('secret_revoked_old_rfc3339', 'service_1', 'revoked old', 'secret_fp_revoked_old_rfc3339', 'verifier', NULL, ?),
		('secret_recent_rfc3339', 'service_1', 'recent', 'secret_fp_recent_rfc3339', 'verifier', ?, NULL)`, old, old, future); err != nil {
		t.Fatalf("seed service principal secrets: %v", err)
	}
}

func seedOperationalHistory(t *testing.T, ctx context.Context, store *platform.Store, now time.Time) {
	t.Helper()
	db := store.SQLDB()
	if _, err := db.ExecContext(ctx, `INSERT INTO principals (id, email, display_name) VALUES ('principal_1', 'owner@example.com', 'Owner')`); err != nil {
		t.Fatalf("seed principal: %v", err)
	}
	oldAudit := sqliteTime(now.Add(-(366 * 24 * time.Hour)))
	recentAudit := sqliteTime(now.Add(-(30 * 24 * time.Hour)))
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_events (id, project_id, principal_id, action, resource_kind, resource_id, capability, status, created_at) VALUES
		('audit_old', 'project_demo', 'principal_1', 'old', 'product', 'instance', 'RESOURCE_MANAGE', 'success', ?),
		('audit_recent', 'project_demo', 'principal_1', 'recent', 'product', 'instance', 'RESOURCE_MANAGE', 'success', ?)`, oldAudit, recentAudit); err != nil {
		t.Fatalf("seed audit events: %v", err)
	}
	digest := "sha256:" + strings.Repeat("a", 64)
	if _, err := db.ExecContext(ctx, `INSERT INTO audit_outbox
		(event_id, source, operation, action, outcome, aggregate_key, aggregate_sequence, payload_digest, state, created_at, delivered_at)
	VALUES
		('outbox_old', 'test', 'old', 'old', 'success', 'outbox_old', 0, ?, 'delivered', ?, ?),
		('outbox_recent', 'test', 'recent', 'recent', 'success', 'outbox_recent', 0, ?, 'delivered', ?, ?),
		('outbox_pending_old', 'test', 'pending', 'pending', 'success', 'outbox_pending_old', 0, ?, 'pending', ?, NULL)`,
		digest, oldAudit, oldAudit, digest, recentAudit, recentAudit, digest, oldAudit); err != nil {
		t.Fatalf("seed audit outbox: %v", err)
	}
	oldQuery := sqliteTime(now.Add(-(91 * 24 * time.Hour)))
	recentQuery := sqliteTime(now.Add(-(10 * 24 * time.Hour)))
	if _, err := db.ExecContext(ctx, `INSERT INTO query_events (id, project_id, principal_id, status, created_at) VALUES
		('query_old', 'project_demo', 'principal_1', 'success', ?),
		('query_recent', 'project_demo', 'principal_1', 'success', ?)`, oldQuery, recentQuery); err != nil {
		t.Fatalf("seed query events: %v", err)
	}
	oldArchived := sqliteTime(now.Add(-(181 * 24 * time.Hour)))
	recentArchived := sqliteTime(now.Add(-(10 * 24 * time.Hour)))
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_conversations (id, principal_id, title, status, archived_at, created_at, updated_at) VALUES
		('agent_archived_old', 'principal_1', 'Old Archived', 'archived', ?, ?, ?),
		('agent_archived_recent', 'principal_1', 'Recent Archived', 'archived', ?, ?, ?),
		('agent_active_old', 'principal_1', 'Active Old', 'active', NULL, ?, ?)`,
		oldArchived, oldArchived, oldArchived,
		recentArchived, recentArchived, recentArchived,
		oldArchived, oldArchived); err != nil {
		t.Fatalf("seed agent conversations: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_runs (id, conversation_id, status) VALUES
		('run_old', 'agent_archived_old', 'done'),
		('run_recent', 'agent_archived_recent', 'done')`); err != nil {
		t.Fatalf("seed agent runs: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO agent_messages (id, conversation_id, run_id, seq, role) VALUES
		('message_old', 'agent_archived_old', 'run_old', 1, 'user'),
		('message_recent', 'agent_archived_recent', 'run_recent', 1, 'user')`); err != nil {
		t.Fatalf("seed agent messages: %v", err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO api_async_events (resource_kind, resource_id, event_id, event_type, data_json) VALUES
		('agent_run', 'run_old', 1, 'step', '{"severity":"info","payload":{}}'),
		('agent_run', 'run_recent', 1, 'step', '{"severity":"info","payload":{}}')`); err != nil {
		t.Fatalf("seed agent events: %v", err)
	}
}

func requireAgentEventCount(t *testing.T, ctx context.Context, store *platform.Store, want int64) {
	t.Helper()
	var got int64
	if err := store.SQLDB().QueryRowContext(ctx, `SELECT COUNT(*) FROM api_async_events WHERE resource_kind = 'agent_run'`).Scan(&got); err != nil {
		t.Fatalf("count agent events: %v", err)
	}
	if got != want {
		t.Fatalf("agent event count = %d, want %d", got, want)
	}
}

func requireTableCount(t *testing.T, ctx context.Context, store *platform.Store, table string, want int64) {
	t.Helper()
	var got int64
	if err := store.SQLDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table).Scan(&got); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	if got != want {
		t.Fatalf("%s count = %d, want %d", table, got, want)
	}
}

func requireRowExists(t *testing.T, ctx context.Context, store *platform.Store, table, id string) {
	t.Helper()
	var got int64
	if err := store.SQLDB().QueryRowContext(ctx, "SELECT COUNT(*) FROM "+table+" WHERE id = ?", id).Scan(&got); err != nil {
		t.Fatalf("count %s.%s: %v", table, id, err)
	}
	if got != 1 {
		t.Fatalf("%s.%s count = %d, want 1", table, id, got)
	}
}
