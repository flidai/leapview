// Package watermillproof contains the bounded FAI-591 qualification for the
// Watermill PostgreSQL SQL transport.  It intentionally uses a test-local
// schema: the canonical event authority is not a Watermill table.
package watermillproof

import (
	"database/sql"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill"
	wmsql "github.com/ThreeDotsLabs/watermill-sql/v4/pkg/sql"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const (
	watermillProofTopic = "fai591"
	watermillProofTable = `"watermill_proof"."messages"`
)

// This is deliberately migration-owned SQL.  It is applied before creating a
// Watermill publisher and the publisher is configured with auto-initialization
// disabled.  The schema is equivalent to the stock PostgreSQL adapter only so
// that this proof exercises the real SQL transport without changing the
// product event schema.
const watermillProofMigration = `
CREATE SCHEMA watermill_proof;
CREATE TABLE watermill_proof.source_mutation (
    id text PRIMARY KEY,
    note text NOT NULL
);
CREATE TABLE watermill_proof.messages (
    "offset" BIGSERIAL,
    "uuid" VARCHAR(36) NOT NULL,
    "created_at" TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    "payload" JSON DEFAULT NULL,
    "metadata" JSON DEFAULT NULL,
    "transaction_id" xid8 NOT NULL,
    PRIMARY KEY ("transaction_id", "offset")
);
`

func TestStockPostgreSQLTransportShapeAndOffsetLimits(t *testing.T) {
	schema := wmsql.DefaultPostgreSQLSchema{
		GenerateMessagesTableName: func(string) string { return watermillProofTable },
	}
	queries, err := schema.SchemaInitializingQueries(wmsql.SchemaInitializingQueriesParams{Topic: watermillProofTopic})
	require.NoError(t, err)
	require.Len(t, queries, 2, "stock PostgreSQL schema uses an advisory lock plus CREATE TABLE")
	create := queries[1].Query
	require.Contains(t, create, `"offset" BIGSERIAL`)
	require.Contains(t, create, `"transaction_id" xid8`)
	require.Contains(t, create, `PRIMARY KEY ("transaction_id", "offset")`)
	require.NotContains(t, create, "event_id uuid", "stock transport rows are not canonical event envelopes")

	offsets := wmsql.DefaultPostgreSQLOffsetsAdapter{
		GenerateMessagesOffsetsTableName: func(string) string { return `"watermill_proof"."offsets"` },
	}
	offsetQueries, err := offsets.SchemaInitializingQueries(wmsql.OffsetsSchemaInitializingQueriesParams{Topic: watermillProofTopic})
	require.NoError(t, err)
	require.Len(t, offsetQueries, 1)
	require.Contains(t, offsetQueries[0].Query, "consumer_group VARCHAR(255)")
	require.Contains(t, offsetQueries[0].Query, "offset_acked BIGINT")
	require.Contains(t, offsetQueries[0].Query, "last_processed_transaction_id xid8")
	require.Contains(t, offsetQueries[0].Query, "PRIMARY KEY(consumer_group)")

	// The stock adapter derives a table name from any SQL-safe topic and uses a
	// single integer checkpoint per consumer group.  Neither is the product's
	// UUIDv7 event identity, aggregate ordering, or delivery fence.
	require.Equal(t, `"watermill_fai591"`, (wmsql.DefaultPostgreSQLSchema{}).MessagesTable(watermillProofTopic))
	require.Equal(t, sql.LevelRepeatableRead, schema.SubscribeIsolationLevel())
}

func TestPostgreSQL18WatermillProof(t *testing.T) {
	db := watermillProofDB(t)
	t.Run("BeginnerFromPgx", func(t *testing.T) {
		ctx := t.Context()
		// BeginnerFromPgx adapts a pgx pool to Watermill's database/sql-shaped
		// Beginner without introducing a database/sql pool/connection path or
		// changing transaction ownership.
		beginner := wmsql.BeginnerFromPgx(db)
		tx, err := beginner.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, `INSERT INTO watermill_proof.source_mutation (id, note) VALUES ($1, $2)`, "beginner", "adapter")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())

		var count int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM watermill_proof.source_mutation WHERE id = 'beginner'`).Scan(&count))
		require.Equal(t, 1, count)
	})

	t.Run("MutationAndPublishCommitRollback", func(t *testing.T) {
		ctx := t.Context()
		publish := func(id string, commit bool) {
			rawTx, err := db.Begin(ctx)
			require.NoError(t, err)

			_, err = rawTx.Exec(ctx, `INSERT INTO watermill_proof.source_mutation (id, note) VALUES ($1, $2)`, id, "source")
			require.NoError(t, err)

			// TxFromPgx is the caller-owned transaction bridge.  Its upstream
			// wrapper has a nil internal context, so use it only as the publisher's
			// executor and always commit/rollback the raw pgx transaction with ctx.
			// AutoInitializeSchema is intentionally false: DDL belongs to
			// migrations and must never run on this transaction path.
			publisher, err := wmsql.NewPublisher(
				wmsql.TxFromPgx(rawTx),
				wmsql.PublisherConfig{
					SchemaAdapter:        proofSchemaAdapter(),
					AutoInitializeSchema: false,
				},
				watermill.NopLogger{},
			)
			require.NoError(t, err)
			messageID := id + "-message"
			err = publisher.Publish(watermillProofTopic, message.NewMessage(messageID, []byte(`{"kind":"proof"}`)))
			require.NoError(t, err)
			require.NoError(t, publisher.Close())

			if commit {
				require.NoError(t, rawTx.Commit(ctx))
				return
			}
			require.NoError(t, rawTx.Rollback(ctx))
		}
		publish("committed", true)
		publish("rolled-back", false)

		var sourceCount, messageCount int
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM watermill_proof.source_mutation WHERE id IN ('committed', 'rolled-back')`).Scan(&sourceCount))
		require.NoError(t, db.QueryRow(ctx, `SELECT count(*) FROM watermill_proof.messages`).Scan(&messageCount))
		require.Equal(t, 1, sourceCount, "the rolled-back source mutation must not survive")
		require.Equal(t, 1, messageCount, "the rolled-back Watermill message must not survive")

		var storedUUID string
		require.NoError(t, db.QueryRow(ctx, `SELECT "uuid" FROM watermill_proof.messages`).Scan(&storedUUID))
		require.Equal(t, "committed-message", storedUUID)
	})

	t.Run("PublisherAutoInitializationRejected", func(t *testing.T) {
		ctx := t.Context()
		rawTx, err := db.Begin(ctx)
		require.NoError(t, err)
		defer rawTx.Rollback(ctx)

		_, err = wmsql.NewPublisher(
			wmsql.TxFromPgx(rawTx),
			wmsql.PublisherConfig{
				SchemaAdapter:        proofSchemaAdapter(),
				AutoInitializeSchema: true,
			},
			watermill.NopLogger{},
		)
		require.Error(t, err)
		require.Contains(t, err.Error(), "AutoInitializeSchema")
	})

	t.Run("SubscriberSchemaInitializationDisabled", func(t *testing.T) {
		ctx := t.Context()
		subscriber, err := wmsql.NewSubscriber(
			wmsql.BeginnerFromPgx(db),
			wmsql.SubscriberConfig{
				ConsumerGroup:    "fai591-consumer",
				SchemaAdapter:    proofSchemaAdapter(),
				OffsetsAdapter:   proofOffsetsAdapter(),
				InitializeSchema: false,
				PollInterval:     50 * time.Millisecond,
				ResendInterval:   50 * time.Millisecond,
				RetryInterval:    50 * time.Millisecond,
			},
			watermill.NopLogger{},
		)
		require.NoError(t, err)
		_, err = subscriber.Subscribe(ctx, watermillProofTopic)
		require.Error(t, err, "Subscribe must not auto-create the migration-owned offset table")
		require.NoError(t, subscriber.Close())

		var offsetsTableExists bool
		require.NoError(t, db.QueryRow(ctx, `SELECT to_regclass('watermill_proof.offsets') IS NOT NULL`).Scan(&offsetsTableExists))
		require.False(t, offsetsTableExists, "subscriber InitializeSchema=false must not create migration-owned offset tables")
	})
}

func proofSchemaAdapter() wmsql.DefaultPostgreSQLSchema {
	return wmsql.DefaultPostgreSQLSchema{
		GenerateMessagesTableName: func(string) string { return watermillProofTable },
	}
}

func proofOffsetsAdapter() wmsql.DefaultPostgreSQLOffsetsAdapter {
	return wmsql.DefaultPostgreSQLOffsetsAdapter{
		GenerateMessagesOffsetsTableName: func(string) string { return `"watermill_proof"."offsets"` },
	}
}

func watermillProofDB(t *testing.T) *pgxpool.Pool {
	t.Helper()
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "watermill_proof")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(db.Close)
	_, err = db.Exec(t.Context(), watermillProofMigration)
	require.NoError(t, err)
	return db
}

// Keep both pgx adapter entry points tied to the versions qualified by this
// package.  The assertions fail at compile time if a future Watermill release
// changes either bridge's contract.
var (
	_ wmsql.Beginner        = wmsql.BeginnerFromPgx((*pgxpool.Pool)(nil))
	_ func(pgx.Tx) wmsql.Tx = wmsql.TxFromPgx
)
