package watermill

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/flidai/leapview/internal/platform/postgres/postgrestest"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

const subscriberTestLease = 500 * time.Millisecond

type subscriberPGFixture struct {
	db       *pgxpool.Pool
	repo     *eventspostgres.Repository
	consumer eventspostgres.Consumer
	config   SubscriberConfig
}

func TestPostgreSQL18SubscriberConformance(t *testing.T) {
	h := postgrestest.Start(t)
	database := h.NewDatabase(t, "watermill_subscriber")
	db, err := pgxpool.New(t.Context(), database.AdminURL())
	require.NoError(t, err)
	t.Cleanup(db.Close)
	_, err = db.Exec(t.Context(), eventspostgres.SchemaSQL())
	require.NoError(t, err)

	t.Run("configured mismatches reject before claim", func(t *testing.T) {
		cases := []struct {
			name  string
			setup func(*subscriberPGFixture)
			call  func(*Subscriber) error
		}{
			{
				name: "consumer key",
				call: func(s *Subscriber) error {
					_, err := s.Subscribe(t.Context(), TopicAgent)
					return err
				},
			},
			{
				name: "lifecycle",
				setup: func(f *subscriberPGFixture) {
					tx, beginErr := f.db.Begin(t.Context())
					require.NoError(t, beginErr)
					require.NoError(t, f.repo.PauseConsumer(t.Context(), tx, f.consumer.ConsumerID))
					require.NoError(t, tx.Commit(t.Context()))
				},
				call: func(s *Subscriber) error {
					_, err := s.Subscribe(t.Context(), TopicAgent)
					return err
				},
			},
			{
				name: "aggregate filter",
				setup: func(f *subscriberPGFixture) {
					_, err := f.db.Exec(t.Context(), `DELETE FROM event.event_consumer_aggregate WHERE consumer_id = $1::uuid AND aggregate_type = 'agent_conversation'`, f.consumer.ConsumerID)
					require.NoError(t, err)
					_, err = f.db.Exec(t.Context(), `UPDATE event.event_consumer_aggregate SET aggregate_type = 'release' WHERE consumer_id = $1::uuid AND aggregate_type = 'agent_run'`, f.consumer.ConsumerID)
					require.NoError(t, err)
				},
				call: func(s *Subscriber) error {
					_, err := s.Subscribe(t.Context(), TopicAgent)
					return err
				},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fixture := newSubscriberPGFixture(t, db, tc.name)
				event := appendSubscriberEvent(t, fixture, tc.name)
				if tc.setup != nil {
					tc.setup(&fixture)
				}
				if tc.name == "consumer key" {
					fixture.config.ConsumerKey = fixture.config.ConsumerKey + "-wrong"
				}
				subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
				require.NoError(t, err)
				err = tc.call(subscriber)
				require.Error(t, err)
				require.NoError(t, subscriber.Close())
				assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "pending", 0, 0)
			})
		}

		t.Run("topic", func(t *testing.T) {
			fixture := newSubscriberPGFixture(t, db, "topic")
			event := appendSubscriberEvent(t, fixture, "topic")
			subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
			require.NoError(t, err)
			_, err = subscriber.Subscribe(t.Context(), TopicDashboard)
			require.ErrorIs(t, err, ErrSubscriberTopic)
			require.NoError(t, subscriber.Close())
			assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "pending", 0, 0)
		})
	})

	t.Run("subscribe emits only after committed claim and no watermill tables", func(t *testing.T) {
		fixture := newSubscriberPGFixture(t, db, "committed")
		event := appendSubscriberEvent(t, fixture, "committed")
		subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
		require.NoError(t, err)
		messages, err := subscriber.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		msg := receiveSubscriberMessage(t, messages)
		require.Equal(t, event.EventID, msg.UUID)
		decoded, err := DecodeMessage(TopicAgent, msg)
		require.NoError(t, err)
		require.Equal(t, event.EventID, decoded.EventID)
		require.JSONEq(t, string(event.Payload), string(decoded.Payload))
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "claimed", 1, 1)
		var watermillTables int
		require.NoError(t, fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname NOT IN ('pg_catalog', 'information_schema') AND tablename LIKE 'watermill_%'`).Scan(&watermillTables))
		require.Zero(t, watermillTables)
		require.NoError(t, subscriber.Close())
	})

	t.Run("CompleteOnSuccess commits before Ack and does not retry", func(t *testing.T) {
		fixture := newSubscriberPGFixture(t, db, "completion")
		event := appendSubscriberEvent(t, fixture, "completion")
		subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
		require.NoError(t, err)
		messages, err := subscriber.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		msg := receiveSubscriberMessage(t, messages)
		middleware := CompleteOnSuccess()(func(msg *message.Message) ([]*message.Message, error) {
			select {
			case <-msg.Acked():
				t.Errorf("Ack arrived before completion middleware returned")
			default:
			}
			return nil, nil
		})
		produced, err := middleware(msg)
		require.NoError(t, err)
		require.Nil(t, produced)
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "succeeded", 1, 1)
		require.True(t, msg.Ack())
		require.Never(t, func() bool {
			var status string
			var attempts int64
			err := fixture.db.QueryRow(t.Context(), `SELECT status, attempts FROM event.event_delivery WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, fixture.consumer.ConsumerID, event.EventID).Scan(&status, &attempts)
			return err == nil && (status != "succeeded" || attempts != 1)
		}, 200*time.Millisecond, 10*time.Millisecond)
		require.NoError(t, subscriber.Close())
	})

	for _, mode := range []struct {
		name string
		nack bool
	}{
		{name: "manual Ack"},
		{name: "Nack", nack: true},
	} {
		t.Run(mode.name+" retries with fresh claim generation", func(t *testing.T) {
			fixture := newSubscriberPGFixture(t, db, mode.name)
			event := appendSubscriberEvent(t, fixture, mode.name)
			fixture.config.PollInterval = 100 * time.Millisecond
			subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
			require.NoError(t, err)
			messages, err := subscriber.Subscribe(t.Context(), TopicAgent)
			require.NoError(t, err)
			first := receiveSubscriberMessage(t, messages)
			if mode.nack {
				require.True(t, first.Nack())
			} else {
				require.True(t, first.Ack())
			}
			assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "pending", 1, 1)
			second := receiveSubscriberMessage(t, messages)
			require.Equal(t, first.UUID, second.UUID)
			require.Equal(t, first.Payload, second.Payload)
			assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "claimed", 2, 2)
			require.NoError(t, subscriber.Close())
		})
	}

	t.Run("MaxAttempts dead letters and blocks pruning", func(t *testing.T) {
		fixture := newSubscriberPGFixture(t, db, "dead-letter")
		fixture.config.MaxAttempts = 1
		event := appendSubscriberEvent(t, fixture, "dead-letter")
		subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
		require.NoError(t, err)
		messages, err := subscriber.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		msg := receiveSubscriberMessage(t, messages)
		require.True(t, msg.Nack())
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "dead_letter", 1, 1)
		require.NoError(t, subscriber.Close())
		tx, err := fixture.db.Begin(t.Context())
		require.NoError(t, err)
		removed, err := fixture.repo.Prune(t.Context(), tx, time.Now().Add(time.Minute))
		require.NoError(t, err)
		require.Zero(t, removed)
		require.NoError(t, tx.Commit(t.Context()))
		var eventCount int
		require.NoError(t, fixture.db.QueryRow(t.Context(), `SELECT count(*) FROM event.event_log WHERE event_id = $1::uuid`, event.EventID).Scan(&eventCount))
		require.Equal(t, 1, eventCount)
	})

	t.Run("ack deadline retries", func(t *testing.T) {
		fixture := newSubscriberPGFixture(t, db, "deadline")
		event := appendSubscriberEvent(t, fixture, "deadline")
		fixture.config.PollInterval = 200 * time.Millisecond
		subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
		require.NoError(t, err)
		messages, err := subscriber.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		first := receiveSubscriberMessage(t, messages)
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "pending", 1, 1)
		second := receiveSubscriberMessage(t, messages)
		require.Equal(t, first.UUID, second.UUID)
		require.Equal(t, first.Payload, second.Payload)
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "claimed", 2, 2)
		require.NoError(t, subscriber.Close())
	})

	t.Run("close leaves claim for successor and fences stale completion", func(t *testing.T) {
		fixture := newSubscriberPGFixture(t, db, "process-loss")
		event := appendSubscriberEvent(t, fixture, "process-loss")
		firstSubscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
		require.NoError(t, err)
		firstMessages, err := firstSubscriber.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		first := receiveSubscriberMessage(t, firstMessages)
		require.NoError(t, firstSubscriber.Close())
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "claimed", 1, 1)

		successorConfig := fixture.config
		successorConfig.WorkerID = fixture.config.WorkerID + "-successor"
		successor, err := NewSubscriber(fixture.db, fixture.repo, successorConfig)
		require.NoError(t, err)
		successorMessages, err := successor.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		second := receiveSubscriberMessage(t, successorMessages)
		require.Equal(t, first.UUID, second.UUID)
		require.Equal(t, first.Payload, second.Payload)
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, event.EventID, "claimed", 2, 2)
		require.ErrorIs(t, completeMessage(context.Background(), first, json.RawMessage(`{"stale":true}`)), eventspostgres.ErrDeliveryClaimLost)
		require.NoError(t, successor.Close())
	})

	t.Run("MaxInFlight waits for terminal watcher release", func(t *testing.T) {
		fixture := newSubscriberPGFixture(t, db, "in-flight")
		firstEvent := appendSubscriberEvent(t, fixture, "in-flight-first")
		subscriber, err := NewSubscriber(fixture.db, fixture.repo, fixture.config)
		require.NoError(t, err)
		messages, err := subscriber.Subscribe(t.Context(), TopicAgent)
		require.NoError(t, err)
		first := receiveSubscriberMessage(t, messages)
		require.Equal(t, firstEvent.EventID, first.UUID)
		secondEvent := appendSubscriberEvent(t, fixture, "in-flight-second")
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, secondEvent.EventID, "pending", 0, 0)
		require.NoError(t, completeMessage(context.Background(), first, json.RawMessage(`{"outcome":"succeeded"}`)))
		require.True(t, first.Ack())
		second := receiveSubscriberMessage(t, messages)
		require.Equal(t, secondEvent.EventID, second.UUID)
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, firstEvent.EventID, "succeeded", 1, 1)
		assertDelivery(t, fixture.db, fixture.consumer.ConsumerID, secondEvent.EventID, "claimed", 1, 1)
		require.NoError(t, subscriber.Close())
	})
}

func newSubscriberPGFixture(t *testing.T, db *pgxpool.Pool, name string) subscriberPGFixture {
	t.Helper()
	repo := eventspostgres.New()
	consumerID, err := uuid.NewV7()
	require.NoError(t, err)
	key := fmt.Sprintf("subscriber-%s-%s", name, consumerID.String()[24:])
	aggregates, err := AggregatesForTopic(TopicAgent)
	require.NoError(t, err)
	tx, err := db.Begin(t.Context())
	require.NoError(t, err)
	consumer, err := repo.EnrollConsumer(t.Context(), tx, eventspostgres.ConsumerInput{
		ConsumerID: consumerID.String(), ConsumerKey: key, ReplayFrom: time.Now().Add(time.Hour).UTC(), AggregateTypes: aggregates,
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	tx, err = db.Begin(t.Context())
	require.NoError(t, err)
	backfill, err := repo.Backfill(t.Context(), tx, consumer.ConsumerID, 100)
	require.NoError(t, err)
	require.True(t, backfill.Done)
	require.NoError(t, tx.Commit(t.Context()))
	return subscriberPGFixture{db: db, repo: repo, consumer: consumer, config: SubscriberConfig{
		ConsumerID: consumer.ConsumerID, ConsumerKey: consumer.ConsumerKey, Topic: TopicAgent, WorkerID: "worker-" + consumerID.String()[24:],
		PollInterval: 10 * time.Millisecond, ClaimLease: subscriberTestLease, AckDeadline: 100 * time.Millisecond,
		RecoveryMargin: 50 * time.Millisecond, BatchSize: 1, MaxInFlight: 1, BaseRetry: time.Millisecond, MaxRetry: 2 * time.Millisecond, MaxAttempts: 8,
	}}
}

func appendSubscriberEvent(t *testing.T, fixture subscriberPGFixture, suffix string) eventspostgres.Event {
	t.Helper()
	tx, err := fixture.db.Begin(t.Context())
	require.NoError(t, err)
	event, err := fixture.repo.AppendEvent(t.Context(), tx, eventspostgres.EventInput{
		ScopeID: "subscriber-scope", AggregateType: "agent_run", AggregateID: "run-" + suffix + "-" + fixture.consumer.ConsumerID[24:],
		EventType: "agent_run.completed", SchemaVersion: 1, Payload: json.RawMessage(`{"ok":true,"suffix":"` + suffix + `"}`),
	})
	require.NoError(t, err)
	require.NoError(t, tx.Commit(t.Context()))
	return event
}

func receiveSubscriberMessage(t *testing.T, messages <-chan *message.Message) *message.Message {
	t.Helper()
	select {
	case msg := <-messages:
		require.NotNil(t, msg)
		return msg
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for subscriber message")
		return nil
	}
}

func assertDelivery(t *testing.T, db *pgxpool.Pool, consumerID, eventID, status string, attempts, generation int64) {
	t.Helper()
	require.Eventually(t, func() bool {
		var gotStatus string
		var gotAttempts, gotGeneration int64
		err := db.QueryRow(t.Context(), `SELECT status, attempts, claim_generation FROM event.event_delivery WHERE consumer_id = $1::uuid AND event_id = $2::uuid`, consumerID, eventID).Scan(&gotStatus, &gotAttempts, &gotGeneration)
		return err == nil && gotStatus == status && gotAttempts == attempts && gotGeneration == generation
	}, 3*time.Second, 10*time.Millisecond)
}
