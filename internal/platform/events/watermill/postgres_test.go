package watermill

import (
	"context"
	"testing"
	"time"

	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQL18AdapterTransactionIdentityAndNoWatermillTables(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "watermill_adapter")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(db.Close)
	_, err = db.Exec(t.Context(), eventspostgres.SchemaSQL())
	require.NoError(t, err)
	_, err = db.Exec(t.Context(), `CREATE TABLE source_mutation (id text primary key)`)
	require.NoError(t, err)

	authority := eventspostgres.New()
	appender, err := New(authority)
	require.NoError(t, err)
	input := EventInput{
		ScopeID: "scope", AggregateType: "agent_run", AggregateID: "run-1",
		EventType: "agent_run.completed", SchemaVersion: 1,
		Payload: []byte(`{"amount":1e3,"large":9007199254740993}`),
	}

	// Source mutation and append share the caller-owned transaction. Rollback
	// leaves no event row behind and the next append receives version one.
	tx, err := db.Begin(t.Context())
	require.NoError(t, err)
	_, err = tx.Exec(t.Context(), `INSERT INTO source_mutation VALUES ('rolled-back')`)
	require.NoError(t, err)
	_, err = appender.AppendEvent(t.Context(), tx, TopicAgent, input)
	require.NoError(t, err)
	require.NoError(t, tx.Rollback(t.Context()))
	var count, sourceCount int
	require.NoError(t, db.QueryRow(t.Context(), `SELECT count(*) FROM source_mutation`).Scan(&sourceCount))
	require.Equal(t, 0, sourceCount)
	require.NoError(t, db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&count))
	require.Equal(t, 0, count)

	// Enroll and enable one durable consumer before the committed append. The
	// producer transaction must create exactly one canonical delivery row; the
	// Watermill projection does not add a framework-owned transport row.
	enrollTx, err := db.Begin(t.Context())
	require.NoError(t, err)
	consumer, err := authority.EnrollConsumer(t.Context(), enrollTx, eventspostgres.ConsumerInput{
		ConsumerKey: "watermill-router", ReplayFrom: time.Unix(0, 0),
	})
	require.NoError(t, err)
	require.NoError(t, enrollTx.Commit(t.Context()))
	backfillTx, err := db.Begin(t.Context())
	require.NoError(t, err)
	backfill, err := authority.Backfill(t.Context(), backfillTx, consumer.ConsumerID, 100)
	require.NoError(t, err)
	require.True(t, backfill.Done)
	require.NoError(t, backfillTx.Commit(t.Context()))

	// Database-owned identity and occurrence are returned in the message.
	tx, err = db.Begin(t.Context())
	require.NoError(t, err)
	finalized, err := appender.AppendEvent(t.Context(), tx, TopicAgent, input)
	require.NoError(t, err)
	require.NotEmpty(t, finalized.EventID)
	require.Len(t, finalized.EventID, 36)
	parsed, err := uuid.Parse(finalized.EventID)
	require.NoError(t, err)
	require.Equal(t, uuid.Version(7), parsed.Version())
	msg, err := appender.MessageForEvent(TopicAgent, finalized)
	require.NoError(t, err)
	require.Equal(t, finalized.EventID, msg.UUID)
	require.Equal(t, int64(1), finalized.AggregateVersion)
	require.False(t, finalized.OccurredAt.IsZero())
	require.Equal(t, time.UTC, finalized.OccurredAt.Location())
	require.NoError(t, tx.Commit(t.Context()))

	// Explicit replay is idempotent; a different immutable field is a conflict.
	tx, err = db.Begin(t.Context())
	require.NoError(t, err)
	replayed, err := appender.AppendEvent(t.Context(), tx, TopicAgent, EventInput{
		EventID: finalized.EventID, ScopeID: input.ScopeID, AggregateType: input.AggregateType,
		AggregateID: input.AggregateID, EventType: input.EventType, SchemaVersion: input.SchemaVersion,
		Payload: []byte(`{"large":9007199254740993,"amount":1000}`),
	})
	require.NoError(t, err)
	require.Equal(t, finalized.AggregateVersion, replayed.AggregateVersion)
	require.Equal(t, finalized.OccurredAt, replayed.OccurredAt)
	replayMessage, err := appender.MessageForEvent(TopicAgent, replayed)
	require.NoError(t, err)
	require.Equal(t, msg.Payload, replayMessage.Payload)
	conflictInput := input
	conflictInput.EventID = finalized.EventID
	conflictInput.EventType = "agent_run.failed"
	_, err = appender.AppendEvent(t.Context(), tx, TopicAgent, conflictInput)
	var conflict *eventspostgres.EventConflictError
	require.ErrorAs(t, err, &conflict)
	require.NoError(t, tx.Rollback(t.Context()))

	require.NoError(t, db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log`).Scan(&count))
	require.Equal(t, 1, count)
	var deliveryCount int
	require.NoError(t, db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_delivery WHERE consumer_id = $1::uuid`, consumer.ConsumerID).Scan(&deliveryCount))
	require.Equal(t, 1, deliveryCount)
	var watermillTables int
	require.NoError(t, db.QueryRow(t.Context(), `
		SELECT count(*) FROM pg_catalog.pg_tables
		WHERE schemaname NOT IN ('pg_catalog', 'information_schema')
		  AND tablename LIKE 'watermill_%'`).Scan(&watermillTables))
	require.Equal(t, 0, watermillTables)
}

// Ensure the production adapter remains usable with a context cancellation
// and does not hide transaction ownership behind a background context.
func TestAdapterContextIsForwarded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	appender := &countingAppender{err: context.Canceled}
	adapter, err := newAdapter(appender)
	require.NoError(t, err)
	_, err = adapter.AppendEvent(ctx, nil, TopicAgent, EventInput{ScopeID: "scope", AggregateType: "agent_run", AggregateID: "run-1", EventType: "agent_run.completed", SchemaVersion: 1, Payload: []byte(`{"ok":true}`)})
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 1, appender.calls)
}
