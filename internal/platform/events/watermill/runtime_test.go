package watermill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"
)

func runtimeTestConfig() RuntimeConfig {
	return RuntimeConfig{
		CloseTimeout:             time.Second,
		HandlerDeadline:          50 * time.Millisecond,
		RetryMaxRetries:          1,
		RetryInitialInterval:     time.Millisecond,
		RetryMaxInterval:         time.Millisecond,
		RetryMaxElapsedTime:      25 * time.Millisecond,
		RetryMultiplier:          1,
		RetryRandomizationFactor: 0,
		Logger:                   slog.New(slog.NewTextHandler(io.Discard, nil)),
		PrometheusRegisterer:     prometheus.NewRegistry(),
		MetricsNamespace:         "leapview_test",
		MetricsSubsystem:         "events",
	}
}

type runtimeIdleTx struct{}

func (runtimeIdleTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (runtimeIdleTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (runtimeIdleTx) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (runtimeIdleTx) Commit(context.Context) error                            { return nil }
func (runtimeIdleTx) Rollback(context.Context) error                          { return nil }

type runtimePool struct{ tx subscriberTx }

func (p runtimePool) Begin(context.Context) (subscriberTx, error) { return p.tx, nil }

type runtimeRepo struct {
	claimErr    error
	consumerErr error
}

func (r runtimeRepo) Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error) {
	return nil, r.claimErr
}
func (r runtimeRepo) ConsumerByID(context.Context, eventspostgres.Tx, string) (eventspostgres.Consumer, error) {
	if r.consumerErr != nil {
		return eventspostgres.Consumer{}, r.consumerErr
	}
	return eventspostgres.Consumer{ConsumerID: runtimeConsumerID, ConsumerKey: "runtime", Lifecycle: "enabled", AggregateTypes: []string{"agent_conversation", "agent_run"}}, nil
}
func (runtimeRepo) GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error) {
	return eventspostgres.Event{}, nil
}
func (runtimeRepo) Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error {
	return nil
}
func (runtimeRepo) Complete(context.Context, eventspostgres.Tx, string, string, string, int64, eventspostgres.DeliveryOutcome, json.RawMessage) error {
	return nil
}

type runtimeBlockingEnrollmentRepo struct {
	entered chan struct{}
	runtimeRepo
}

func (r *runtimeBlockingEnrollmentRepo) ConsumerByID(ctx context.Context, tx eventspostgres.Tx, id string) (eventspostgres.Consumer, error) {
	select {
	case <-r.entered:
	default:
		close(r.entered)
	}
	<-ctx.Done()
	return eventspostgres.Consumer{}, ctx.Err()
}

const runtimeConsumerID = "00000000-0000-0000-0000-000000000001"

func newRuntimeTestSubscriber(repo subscriberRepository) *Subscriber {
	return &Subscriber{
		pool: &runtimePool{tx: runtimeIdleTx{}}, repo: repo,
		config: SubscriberConfig{
			ConsumerID: runtimeConsumerID, ConsumerKey: "runtime", Topic: TopicAgent, WorkerID: "runtime-worker",
			PollInterval: time.Millisecond, ClaimLease: time.Second, AckDeadline: 100 * time.Millisecond,
			RecoveryMargin: 50 * time.Millisecond, BatchSize: 1, MaxInFlight: 1,
			BaseRetry: time.Millisecond, MaxRetry: time.Millisecond, MaxAttempts: 1,
		},
		fatal: make(chan error, 1),
	}
}

func TestNewRuntimeRegistersExactNoPublisherHandler(t *testing.T) {
	subscriber := newRuntimeTestSubscriber(runtimeRepo{})
	runtime, err := NewRuntime(runtimeTestConfig(), HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error { return nil },
	})
	require.NoError(t, err)
	require.Len(t, runtime.router.Handlers(), 1)
	_, ok := runtime.router.Handlers()["agent-consumer"]
	require.True(t, ok)
}

func TestNewRuntimeReturnsMetricsRegistryConflict(t *testing.T) {
	config := runtimeTestConfig()
	registry := prometheus.NewRegistry()
	config.PrometheusRegisterer = registry
	require.NoError(t, registry.Register(prometheus.NewGauge(prometheus.GaugeOpts{
		Namespace: config.MetricsNamespace,
		Subsystem: config.MetricsSubsystem,
		Name:      "handler_execution_time_seconds",
		Help:      "conflicting collector",
	})))
	subscriber := newRuntimeTestSubscriber(runtimeRepo{})
	require.NotPanics(t, func() {
		_, err := NewRuntime(config, HandlerRegistration{
			Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
			Handler: func(*message.Message) error { return nil },
		})
		require.Error(t, err)
	})
}

func TestRuntimeStartStopWaitsForRouterReadiness(t *testing.T) {
	subscriber := newRuntimeTestSubscriber(runtimeRepo{})
	config := runtimeTestConfig()
	registry := prometheus.NewRegistry()
	config.PrometheusRegisterer = registry
	runtime, err := NewRuntime(config, HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error { return nil },
	})
	require.NoError(t, err)
	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Second)
	defer cancelStart()
	require.NoError(t, runtime.Start(startCtx))

	stopCtx, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	require.NoError(t, runtime.Stop(stopCtx))
	select {
	case err := <-runtime.Fatal():
		t.Fatalf("clean stop published fatal: %v", err)
	default:
	}
}

func TestRuntimeValidationKeepsBoundsAndRegistrationExact(t *testing.T) {
	base := runtimeTestConfig()
	subscriber := newRuntimeTestSubscriber(runtimeRepo{})
	cases := []struct {
		name   string
		mutate func(*RuntimeConfig, *Subscriber, *HandlerRegistration)
	}{
		{name: "retry count", mutate: func(c *RuntimeConfig, _ *Subscriber, _ *HandlerRegistration) { c.RetryMaxRetries = 0 }},
		{name: "retry window", mutate: func(c *RuntimeConfig, _ *Subscriber, _ *HandlerRegistration) {
			c.RetryMaxElapsedTime = c.HandlerDeadline
		}},
		{name: "retry multiplier nan", mutate: func(c *RuntimeConfig, _ *Subscriber, _ *HandlerRegistration) {
			c.RetryMultiplier = math.NaN()
		}},
		{name: "retry randomization infinity", mutate: func(c *RuntimeConfig, _ *Subscriber, _ *HandlerRegistration) {
			c.RetryRandomizationFactor = math.Inf(1)
		}},
		{name: "typed nil registerer", mutate: func(c *RuntimeConfig, _ *Subscriber, _ *HandlerRegistration) {
			var registry *prometheus.Registry
			c.PrometheusRegisterer = registry
		}},
		{name: "close deadline", mutate: func(c *RuntimeConfig, _ *Subscriber, _ *HandlerRegistration) { c.CloseTimeout = c.HandlerDeadline }},
		{name: "topic mismatch", mutate: func(_ *RuntimeConfig, s *Subscriber, r *HandlerRegistration) {
			s.config.Topic = TopicRelease
			r.Topic = TopicAgent
		}},
		{name: "name bound", mutate: func(_ *RuntimeConfig, _ *Subscriber, r *HandlerRegistration) { r.Name = strings.Repeat("x", 256) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := base
			candidate := newRuntimeTestSubscriber(runtimeRepo{})
			registration := HandlerRegistration{Name: "agent-consumer", Topic: TopicAgent, Subscriber: candidate, Handler: func(*message.Message) error { return nil }}
			tc.mutate(&config, candidate, &registration)
			_, err := NewRuntime(config, registration)
			require.Error(t, err)
		})
	}

	_, err := NewRuntime(base,
		HandlerRegistration{Name: "one", Topic: TopicAgent, Subscriber: subscriber, Handler: func(*message.Message) error { return nil }},
		HandlerRegistration{Name: "two", Topic: TopicAgent, Subscriber: subscriber, Handler: func(*message.Message) error { return nil }},
	)
	require.Error(t, err)
}

func TestNewSubscriberRejectsTypedNilTransactionBeginner(t *testing.T) {
	configured := newRuntimeTestSubscriber(runtimeRepo{})
	var pool *runtimePool
	_, err := newSubscriber(pool, runtimeRepo{}, configured.config)
	require.ErrorIs(t, err, ErrSubscriberNotConfigured)
}

func TestRuntimePropagatesSubscriberFatal(t *testing.T) {
	fatal := errors.New("claim failed")
	subscriber := newRuntimeTestSubscriber(runtimeRepo{claimErr: fatal})
	runtime, err := NewRuntime(runtimeTestConfig(), HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error { return nil },
	})
	require.NoError(t, err)
	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Second)
	defer cancelStart()
	require.NoError(t, runtime.Start(startCtx))
	select {
	case got := <-runtime.Fatal():
		require.ErrorIs(t, got, fatal)
	case <-time.After(time.Second):
		t.Fatal("subscriber fatal was not propagated")
	}
}

func TestRuntimePropagatesDatabaseDeadlineWhileParentIsLive(t *testing.T) {
	fatal := fmt.Errorf("claim transaction: %w", context.DeadlineExceeded)
	subscriber := newRuntimeTestSubscriber(runtimeRepo{claimErr: fatal})
	runtime, err := NewRuntime(runtimeTestConfig(), HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error { return nil },
	})
	require.NoError(t, err)
	require.NoError(t, runtime.Start(context.Background()))
	select {
	case got := <-runtime.Fatal():
		require.ErrorIs(t, got, context.DeadlineExceeded)
	case <-time.After(time.Second):
		t.Fatal("database deadline was incorrectly classified as clean parent cancellation")
	}
}

func TestRuntimeStartDeadlineDoesNotPublishFatal(t *testing.T) {
	repo := &runtimeBlockingEnrollmentRepo{entered: make(chan struct{})}
	subscriber := newRuntimeTestSubscriber(repo)
	runtime, err := NewRuntime(runtimeTestConfig(), HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error { return nil },
	})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	require.ErrorIs(t, runtime.Start(ctx), context.DeadlineExceeded)
	select {
	case fatal := <-runtime.Fatal():
		t.Fatalf("parent deadline published fatal: %v", fatal)
	default:
	}
}

func TestRuntimeStartupFailureClosesEverySubscriber(t *testing.T) {
	startupErr := errors.New("enrollment failed")
	first := newRuntimeTestSubscriber(runtimeRepo{})
	second := newRuntimeTestSubscriber(runtimeRepo{consumerErr: startupErr})
	runtime, err := NewRuntime(runtimeTestConfig(),
		HandlerRegistration{Name: "first", Topic: TopicAgent, Subscriber: first, Handler: func(*message.Message) error { return nil }},
		HandlerRegistration{Name: "second", Topic: TopicAgent, Subscriber: second, Handler: func(*message.Message) error { return nil }},
	)
	require.NoError(t, err)
	startErr := runtime.Start(context.Background())
	require.ErrorIs(t, startErr, startupErr)
	// Enrollment failed before Router.Run, so Watermill never created its
	// handlersWg watcher or marked the router running.
	require.False(t, runtime.router.IsRunning())
	require.False(t, runtime.router.IsClosed())
	require.Eventually(t, func() bool {
		first.mu.Lock()
		firstClosed := first.closed
		first.mu.Unlock()
		second.mu.Lock()
		secondClosed := second.closed
		second.mu.Unlock()
		return firstClosed && secondClosed
	}, time.Second, time.Millisecond)
	require.NoError(t, runtime.Stop(context.Background()))
}

func TestRuntimeStopDuringEnrollmentPreflightDoesNotStartRouter(t *testing.T) {
	repo := &runtimeBlockingEnrollmentRepo{entered: make(chan struct{})}
	subscriber := newRuntimeTestSubscriber(repo)
	runtime, err := NewRuntime(runtimeTestConfig(), HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error { return nil },
	})
	require.NoError(t, err)
	startDone := make(chan error, 1)
	go func() { startDone <- runtime.Start(context.Background()) }()
	select {
	case <-repo.entered:
	case <-time.After(time.Second):
		t.Fatal("runtime did not enter enrollment preflight")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- runtime.Stop(context.Background()) }()
	select {
	case stopErr := <-stopDone:
		require.NoError(t, stopErr)
	case <-time.After(time.Second):
		t.Fatal("Stop did not cancel enrollment preflight")
	}
	select {
	case startErr := <-startDone:
		require.ErrorIs(t, startErr, context.Canceled)
	case <-time.After(time.Second):
		t.Fatal("Start did not finish after preflight cancellation")
	}
	require.False(t, runtime.router.IsRunning())
	require.False(t, runtime.router.IsClosed())
}

func TestRuntimeRepeatedStartStopWithCompatibleRegistry(t *testing.T) {
	registry := prometheus.NewRegistry()
	for i := 0; i < 2; i++ {
		config := runtimeTestConfig()
		config.PrometheusRegisterer = registry
		subscriber := newRuntimeTestSubscriber(runtimeRepo{})
		runtime, err := NewRuntime(config, HandlerRegistration{
			Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
			Handler: func(*message.Message) error { return nil },
		})
		require.NoError(t, err)
		require.NoError(t, runtime.Start(context.Background()))
		require.ErrorIs(t, runtime.Start(context.Background()), ErrRuntimeAlreadyStarted)
		var stopWG sync.WaitGroup
		errs := make(chan error, 2)
		stopWG.Add(2)
		go func() { defer stopWG.Done(); errs <- runtime.Stop(context.Background()) }()
		go func() { defer stopWG.Done(); errs <- runtime.Stop(context.Background()) }()
		stopWG.Wait()
		close(errs)
		for stopErr := range errs {
			require.NoError(t, stopErr)
		}
	}
}

type runtimeDeliveryRepo struct {
	mu           sync.Mutex
	claimed      bool
	order        []string
	completed    chan struct{}
	completeOnce sync.Once
}

func (r *runtimeDeliveryRepo) Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.claimed {
		return nil, nil
	}
	r.claimed = true
	return []eventspostgres.Delivery{{
		ConsumerID: runtimeConsumerID, EventID: runtimeEventID, Status: "claimed", Attempts: 1,
		ClaimGeneration: 1, ClaimedBy: "runtime-worker",
	}}, nil
}
func (r *runtimeDeliveryRepo) ConsumerByID(context.Context, eventspostgres.Tx, string) (eventspostgres.Consumer, error) {
	return eventspostgres.Consumer{ConsumerID: runtimeConsumerID, ConsumerKey: "runtime", Lifecycle: "enabled", AggregateTypes: []string{"agent_conversation", "agent_run"}}, nil
}
func (r *runtimeDeliveryRepo) GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error) {
	return eventspostgres.Event{EventID: runtimeEventID, ScopeID: "scope", AggregateType: "agent_run", AggregateID: "aggregate", AggregateVersion: 1, EventType: "agent.completed", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"ok":true}`)}, nil
}
func (r *runtimeDeliveryRepo) Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error {
	return nil
}
func (r *runtimeDeliveryRepo) Complete(context.Context, eventspostgres.Tx, string, string, string, int64, eventspostgres.DeliveryOutcome, json.RawMessage) error {
	r.mu.Lock()
	r.order = append(r.order, "complete")
	r.mu.Unlock()
	return nil
}

type runtimeOrderTx struct{ repo *runtimeDeliveryRepo }

func (tx *runtimeOrderTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}
func (tx *runtimeOrderTx) Query(context.Context, string, ...any) (pgx.Rows, error) { return nil, nil }
func (tx *runtimeOrderTx) QueryRow(context.Context, string, ...any) pgx.Row        { return nil }
func (tx *runtimeOrderTx) Commit(context.Context) error {
	tx.repo.mu.Lock()
	tx.repo.order = append(tx.repo.order, "commit")
	if len(tx.repo.order) >= 2 && tx.repo.order[len(tx.repo.order)-2] == "complete" {
		tx.repo.completeOnce.Do(func() { close(tx.repo.completed) })
	}
	tx.repo.mu.Unlock()
	return nil
}
func (tx *runtimeOrderTx) Rollback(context.Context) error { return nil }

const runtimeEventID = "0198f0c0-1234-7abc-8abc-123456789abc"

func TestRuntimeMiddlewareRetriesRecoveredPanicAndCompletesBeforeAck(t *testing.T) {
	repo := &runtimeDeliveryRepo{completed: make(chan struct{})}
	subscriber := newRuntimeTestSubscriber(repo)
	subscriber.pool = &runtimePool{tx: &runtimeOrderTx{repo: repo}}
	config := runtimeTestConfig()
	registry := prometheus.NewRegistry()
	config.PrometheusRegisterer = registry
	var attempts atomic.Int32
	var metadata map[string]string
	runtime, err := NewRuntime(config, HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(msg *message.Message) error {
			metadata = msg.Metadata
			if attempts.Add(1) == 1 {
				panic("retry me")
			}
			return nil
		},
	})
	require.NoError(t, err)
	startCtx, cancelStart := context.WithTimeout(context.Background(), time.Second)
	defer cancelStart()
	require.NoError(t, runtime.Start(startCtx))
	select {
	case <-repo.completed:
	case <-time.After(time.Second):
		t.Fatal("completion did not commit")
	}
	require.Equal(t, int32(2), attempts.Load())
	require.Equal(t, map[string]string{MetadataTopic: TopicAgent}, metadata)
	repo.mu.Lock()
	completionCommitted := false
	for i := 0; i+1 < len(repo.order); i++ {
		if repo.order[i] == "complete" && repo.order[i+1] == "commit" {
			completionCommitted = true
			break
		}
	}
	require.True(t, completionCommitted, "completion must be followed by its commit: %v", repo.order)
	repo.mu.Unlock()
	require.Eventually(t, func() bool {
		families, gatherErr := registry.Gather()
		if gatherErr != nil {
			return false
		}
		for _, family := range families {
			if family.GetName() != "leapview_test_events_handler_execution_time_seconds" {
				continue
			}
			for _, metric := range family.GetMetric() {
				labels := make(map[string]string, len(metric.GetLabel()))
				for _, label := range metric.GetLabel() {
					labels[label.GetName()] = label.GetValue()
				}
				if labels["success"] == "true" && labels["handler_name"] == "agent-consumer" && metric.GetHistogram().GetSampleCount() >= 1 {
					return true
				}
			}
		}
		return false
	}, time.Second, time.Millisecond)
	require.NoError(t, runtime.Stop(context.Background()))
}

func TestRuntimeStopWaitsForTrackedHandlerAfterRouterCloseTimeout(t *testing.T) {
	repo := &runtimeDeliveryRepo{completed: make(chan struct{})}
	subscriber := newRuntimeTestSubscriber(repo)
	config := runtimeTestConfig()
	config.CloseTimeout = 200 * time.Millisecond
	config.PrometheusRegisterer = prometheus.NewRegistry()
	handlerStarted := make(chan struct{})
	handlerRelease := make(chan struct{})
	var startedOnce sync.Once
	runtime, err := NewRuntime(config, HandlerRegistration{
		Name: "agent-consumer", Topic: TopicAgent, Subscriber: subscriber,
		Handler: func(*message.Message) error {
			startedOnce.Do(func() { close(handlerStarted) })
			<-handlerRelease
			return nil
		},
	})
	require.NoError(t, err)
	require.NoError(t, runtime.Start(context.Background()))
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("handler did not start")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- runtime.Stop(context.Background()) }()
	// Router.Close has a soft timeout, but the runtime-owned execution gate
	// keeps Stop blocked until this handler leaves the full middleware chain.
	select {
	case err := <-stopDone:
		t.Fatalf("Stop returned while handler was still running: %v", err)
	case <-time.After(config.CloseTimeout + 50*time.Millisecond):
	}
	close(handlerRelease)
	select {
	case err := <-stopDone:
		require.Error(t, err, "the intentionally expired handler deadline should surface a drain error")
	case <-time.After(time.Second):
		t.Fatal("Stop did not finish after handler release")
	}
}
