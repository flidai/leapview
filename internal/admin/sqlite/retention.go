package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	retentiondb "github.com/flidai/leapview/internal/admin/internal/db"
)

type RetentionOptions struct {
	Now                           time.Time
	AuditEventsMaxAge             time.Duration
	QueryEventsMaxAge             time.Duration
	ArchivedAgentConversationsAge time.Duration
	AuthStateMaxAge               time.Duration
	DryRun                        bool
}

type RetentionResult struct {
	DryRun                              bool
	AuditEventsDeleted                  int64
	DeliveredAuditIntentsDeleted        int64
	QueryEventsDeleted                  int64
	ArchivedAgentConversationsDeleted   int64
	ExpiredOAuthStatesDeleted           int64
	StaleSessionsDeleted                int64
	StaleAPITokensDeleted               int64
	StaleServicePrincipalSecretsDeleted int64
}

func PruneOperationalHistory(ctx context.Context, database *sql.DB, options RetentionOptions) (RetentionResult, error) {
	if database == nil {
		return RetentionResult{}, fmt.Errorf("retention database is not open")
	}
	now := options.Now
	if now.IsZero() {
		now = time.Now()
	}
	result := RetentionResult{DryRun: options.DryRun}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return result, err
	}
	defer tx.Rollback()

	result.AuditEventsDeleted, err = pruneByCreatedAt(ctx, tx, "audit_events", now, options.AuditEventsMaxAge, options.DryRun)
	if err != nil {
		return result, err
	}
	result.DeliveredAuditIntentsDeleted, err = pruneDeliveredAuditIntents(ctx, tx, now, options.AuditEventsMaxAge, options.DryRun)
	if err != nil {
		return result, err
	}
	result.QueryEventsDeleted, err = pruneByCreatedAt(ctx, tx, "query_events", now, options.QueryEventsMaxAge, options.DryRun)
	if err != nil {
		return result, err
	}
	result.ArchivedAgentConversationsDeleted, err = pruneArchivedAgentConversations(ctx, tx, retentiondb.New(tx), now, options.ArchivedAgentConversationsAge, options.DryRun)
	if err != nil {
		return result, err
	}
	if options.AuthStateMaxAge > 0 {
		result.ExpiredOAuthStatesDeleted, err = pruneByTimeColumn(ctx, tx, "oauth_states", "expires_at", now, options.AuthStateMaxAge, options.DryRun)
		if err != nil {
			return result, err
		}
		result.StaleSessionsDeleted, err = pruneStaleExpirableOrRevocable(ctx, tx, "sessions", now, options.AuthStateMaxAge, options.DryRun)
		if err != nil {
			return result, err
		}
		result.StaleAPITokensDeleted, err = pruneStaleExpirableOrRevocable(ctx, tx, "api_tokens", now, options.AuthStateMaxAge, options.DryRun)
		if err != nil {
			return result, err
		}
		result.StaleServicePrincipalSecretsDeleted, err = pruneStaleExpirableOrRevocable(ctx, tx, "service_principal_secrets", now, options.AuthStateMaxAge, options.DryRun)
		if err != nil {
			return result, err
		}
	}
	if options.DryRun {
		return result, nil
	}
	if err := tx.Commit(); err != nil {
		return result, err
	}
	return result, nil
}

func pruneDeliveredAuditIntents(ctx context.Context, tx *sql.Tx, now time.Time, maxAge time.Duration, dryRun bool) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := sqliteTime(now.Add(-maxAge))
	condition := "state = 'delivered' AND delivered_at IS NOT NULL AND created_at < ?"
	if dryRun {
		return countWhere(ctx, tx, "SELECT COUNT(*) FROM audit_outbox WHERE "+condition, cutoff)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM audit_outbox WHERE "+condition, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func pruneByCreatedAt(ctx context.Context, tx *sql.Tx, table string, now time.Time, maxAge time.Duration, dryRun bool) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := sqliteTime(now.Add(-maxAge))
	if dryRun {
		return countWhere(ctx, tx, "SELECT COUNT(*) FROM "+table+" WHERE created_at < ?", cutoff)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE created_at < ?", cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func pruneArchivedAgentConversations(ctx context.Context, tx *sql.Tx, q *retentiondb.Queries, now time.Time, maxAge time.Duration, dryRun bool) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := sqliteTime(now.Add(-maxAge))
	query := "FROM agent_conversations WHERE archived_at IS NOT NULL AND archived_at <> '' AND archived_at < ?"
	if dryRun {
		return countWhere(ctx, tx, "SELECT COUNT(*) "+query, cutoff)
	}
	if err := q.DeleteAsyncEventsForArchivedAgentRuns(ctx, sql.NullString{String: cutoff, Valid: true}); err != nil {
		return 0, err
	}
	result, err := tx.ExecContext(ctx, "DELETE "+query, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func pruneByTimeColumn(ctx context.Context, tx *sql.Tx, table, column string, now time.Time, maxAge time.Duration, dryRun bool) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := sqliteTime(now.Add(-maxAge))
	condition := "datetime(" + column + ") < datetime(?)"
	if dryRun {
		return countWhere(ctx, tx, "SELECT COUNT(*) FROM "+table+" WHERE "+condition, cutoff)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+condition, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func pruneStaleExpirableOrRevocable(ctx context.Context, tx *sql.Tx, table string, now time.Time, maxAge time.Duration, dryRun bool) (int64, error) {
	if maxAge <= 0 {
		return 0, nil
	}
	cutoff := sqliteTime(now.Add(-maxAge))
	condition := "((expires_at IS NOT NULL AND expires_at <> '' AND datetime(expires_at) < datetime(?)) OR (revoked_at IS NOT NULL AND revoked_at <> '' AND datetime(revoked_at) < datetime(?)))"
	if dryRun {
		return countWhere(ctx, tx, "SELECT COUNT(*) FROM "+table+" WHERE "+condition, cutoff, cutoff)
	}
	result, err := tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE "+condition, cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func countWhere(ctx context.Context, tx *sql.Tx, query string, args ...any) (int64, error) {
	var count int64
	if err := tx.QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func sqliteTime(value time.Time) string {
	return value.UTC().Format("2006-01-02 15:04:05")
}
