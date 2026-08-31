package watermill

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type completionFakeTx struct {
	order         *[]string
	commitErr     error
	commitStarted chan struct{}
	commitRelease chan struct{}
}

func (tx *completionFakeTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (tx *completionFakeTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (tx *completionFakeTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (tx *completionFakeTx) Commit(context.Context) error {
	*tx.order = append(*tx.order, "commit")
	if tx.commitStarted != nil {
		close(tx.commitStarted)
	}
	if tx.commitRelease != nil {
		<-tx.commitRelease
	}
	return tx.commitErr
}

func (tx *completionFakeTx) Rollback(context.Context) error {
	*tx.order = append(*tx.order, "rollback")
	return nil
}

type completionFakePool struct {
	tx        *completionFakeTx
	beginCall int
}

func (p *completionFakePool) Begin(context.Context) (subscriberTx, error) {
	p.beginCall++
	return p.tx, nil
}

type completionFakeRepo struct {
	order       *[]string
	complete    int
	completeErr error
	retry       atomic.Int32
	evidence    json.RawMessage
	consumerID  string
	eventID     string
	workerID    string
	generation  int64
	outcome     eventspostgres.DeliveryOutcome
}

func (r *completionFakeRepo) Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error) {
	return nil, nil
}

func (r *completionFakeRepo) ConsumerByID(context.Context, eventspostgres.Tx, string) (eventspostgres.Consumer, error) {
	return eventspostgres.Consumer{}, nil
}

func (r *completionFakeRepo) GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error) {
	return eventspostgres.Event{}, nil
}

func (r *completionFakeRepo) Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error {
	r.retry.Add(1)
	return nil
}

func (r *completionFakeRepo) Complete(_ context.Context, _ eventspostgres.Tx, consumerID, eventID, workerID string, generation int64, outcome eventspostgres.DeliveryOutcome, evidence json.RawMessage) error {
	*r.order = append(*r.order, "complete")
	r.complete++
	r.evidence = append(json.RawMessage(nil), evidence...)
	r.consumerID, r.eventID, r.workerID, r.generation, r.outcome = consumerID, eventID, workerID, generation, outcome
	return r.completeErr
}

func newCompletionTestSubscriber(order *[]string, commitErr error) (*Subscriber, *completionFakeRepo, *completionFakePool, *message.Message) {
	pool := &completionFakePool{tx: &completionFakeTx{order: order, commitErr: commitErr}}
	repo := &completionFakeRepo{order: order}
	subscriber := &Subscriber{
		pool: pool,
		repo: repo,
		config: SubscriberConfig{
			ConsumerID: "00000000-0000-0000-0000-000000000001",
			WorkerID:   "worker",
		},
	}
	handle := &deliveryHandle{subscriber: subscriber, delivery: eventspostgres.Delivery{
		ConsumerID: "00000000-0000-0000-0000-000000000001",
		EventID:    "00000000-0000-0000-0000-000000000002",
		ClaimedBy:  "worker", ClaimGeneration: 7,
	}}
	msg := message.NewMessage(handle.delivery.EventID, nil)
	msg.Metadata.Set(MetadataTopic, TopicAgent)
	msg.SetContext(context.WithValue(context.Background(), deliveryHandleKey{}, handle))
	return subscriber, repo, pool, msg
}

func TestCompleteOnSuccessOrdersHandlerCompletionCommitAndReturn(t *testing.T) {
	var order []string
	_, repo, _, msg := newCompletionTestSubscriber(&order, nil)
	middleware := CompleteOnSuccess()(func(*message.Message) ([]*message.Message, error) {
		order = append(order, "handler")
		return nil, nil
	})

	produced, err := middleware(msg)
	require.NoError(t, err)
	require.Nil(t, produced)
	require.Equal(t, []string{"handler", "complete", "commit"}, order)
	require.Equal(t, eventspostgres.DeliverySucceeded, repo.outcome)
	require.JSONEq(t, `{"outcome":"succeeded"}`, string(repo.evidence))
}

func TestCompleteOnSuccessSkipsCompletionOnHandlerError(t *testing.T) {
	var order []string
	_, repo, _, msg := newCompletionTestSubscriber(&order, nil)
	handlerErr := errors.New("handler failed")
	middleware := CompleteOnSuccess()(func(*message.Message) ([]*message.Message, error) {
		order = append(order, "handler")
		return nil, handlerErr
	})

	produced, err := middleware(msg)
	require.ErrorIs(t, err, handlerErr)
	require.Nil(t, produced)
	require.Equal(t, []string{"handler"}, order)
	require.Zero(t, repo.complete)
}

func TestCompleteOnSuccessRejectsLateNilHandlerAfterDeadline(t *testing.T) {
	var order []string
	_, repo, _, msg := newCompletionTestSubscriber(&order, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg.SetContext(ctx)
	middleware := CompleteOnSuccess()(func(*message.Message) ([]*message.Message, error) {
		order = append(order, "handler")
		return nil, nil
	})

	produced, err := middleware(msg)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, produced)
	require.Equal(t, []string{"handler"}, order)
	require.Zero(t, repo.complete)
}

func TestCompleteOnSuccessUsesCapturedContextWhenHandlerReplacesIt(t *testing.T) {
	var order []string
	_, repo, pool, msg := newCompletionTestSubscriber(&order, nil)
	ctx, cancel := context.WithCancel(context.Background())
	handle, ok := deliveryHandleFromMessage(msg)
	require.True(t, ok)
	msg.SetContext(context.WithValue(ctx, deliveryHandleKey{}, handle))
	cancel()
	middleware := CompleteOnSuccess()(func(msg *message.Message) ([]*message.Message, error) {
		order = append(order, "handler")
		msg.SetContext(context.WithoutCancel(msg.Context()))
		return nil, nil
	})

	produced, err := middleware(msg)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, produced)
	require.Equal(t, []string{"handler"}, order)
	require.Zero(t, repo.complete)
	require.Zero(t, pool.beginCall)
}

func TestCompleteOnSuccessRejectsHandlerMetadataMutation(t *testing.T) {
	cases := []struct {
		name string
		mut  func(message.Metadata)
	}{
		{name: "extra key", mut: func(metadata message.Metadata) { metadata["extra"] = "nope" }},
		{name: "topic", mut: func(metadata message.Metadata) { metadata[MetadataTopic] = TopicDashboard }},
		{name: "missing topic", mut: func(metadata message.Metadata) { delete(metadata, MetadataTopic) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			_, repo, _, msg := newCompletionTestSubscriber(&order, nil)
			middleware := CompleteOnSuccess()(func(*message.Message) ([]*message.Message, error) {
				tc.mut(msg.Metadata)
				return nil, nil
			})
			_, err := middleware(msg)
			require.ErrorIs(t, err, ErrMetadata)
			require.Zero(t, repo.complete)
		})
	}
}

func TestCompletionTopicCannotChangeAcrossRetry(t *testing.T) {
	var order []string
	_, repo, _, msg := newCompletionTestSubscriber(&order, nil)
	var attempts int
	retry := middleware.Retry{
		MaxRetries:          1,
		InitialInterval:     time.Nanosecond,
		MaxInterval:         time.Nanosecond,
		MaxElapsedTime:      time.Second,
		Multiplier:          1,
		RandomizationFactor: 0,
		ResetContextOnRetry: true,
	}
	handler := captureCompletionTopic()(retry.Middleware(CompleteOnSuccess()(func(msg *message.Message) ([]*message.Message, error) {
		attempts++
		msg.Metadata[MetadataTopic] = TopicDashboard
		return nil, errors.New("retry after mutation")
	})))

	_, err := handler(msg)
	require.ErrorIs(t, err, ErrMetadata)
	require.Equal(t, 1, attempts, "the mutated message must be rejected before a second effect attempt")
	require.Zero(t, repo.complete)
}

func TestCompleteMessageCommitFailureDoesNotMarkHandle(t *testing.T) {
	var order []string
	_, repo, pool, msg := newCompletionTestSubscriber(&order, errors.New("commit failed"))

	err := completeMessage(context.Background(), msg, json.RawMessage(`{"attempt":1}`))
	require.Error(t, err)
	handle, ok := deliveryHandleFromMessage(msg)
	require.True(t, ok)
	require.False(t, handle.isCompleted())
	require.Equal(t, 1, repo.complete)
	require.Equal(t, 1, pool.beginCall)
}

func TestCompleteMessageRequiresSubscriberHandle(t *testing.T) {
	err := completeMessage(context.Background(), message.NewMessage("message", nil), nil)
	require.ErrorIs(t, err, ErrNoDeliveryHandle)
}

func TestCompleteMessageRejectsMetadataBeforeBeginningTransaction(t *testing.T) {
	cases := []struct {
		name string
		mut  func(message.Metadata)
	}{
		{name: "extra key", mut: func(metadata message.Metadata) { metadata["extra"] = "nope" }},
		{name: "missing topic", mut: func(metadata message.Metadata) { delete(metadata, MetadataTopic) }},
		{name: "changed topic", mut: func(metadata message.Metadata) { metadata[MetadataTopic] = TopicDashboard }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var order []string
			_, repo, pool, msg := newCompletionTestSubscriber(&order, nil)
			msg.SetContext(context.WithValue(msg.Context(), completionTopicKey{}, TopicAgent))
			tc.mut(msg.Metadata)

			err := completeMessage(context.Background(), msg, nil)
			require.ErrorIs(t, err, ErrMetadata)
			require.Zero(t, repo.complete)
			require.Zero(t, pool.beginCall)
		})
	}
}

func TestCompleteMessageRejectsCancellationBeforeCompletion(t *testing.T) {
	var order []string
	_, repo, pool, msg := newCompletionTestSubscriber(&order, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := completeMessage(ctx, msg, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Zero(t, repo.complete)
	require.Zero(t, pool.beginCall)
}

func TestCompleteMessageIsIdempotentAfterCommit(t *testing.T) {
	var order []string
	_, repo, pool, msg := newCompletionTestSubscriber(&order, nil)

	require.NoError(t, completeMessage(context.Background(), msg, json.RawMessage(`{"attempt":1}`)))
	require.NoError(t, completeMessage(context.Background(), msg, json.RawMessage(`{"attempt":2}`)))
	require.Equal(t, 1, repo.complete)
	require.Equal(t, 1, pool.beginCall)
	require.Equal(t, []string{"complete", "commit"}, order)
}

func TestAcknowledgementTimeoutWaitsForCompletionOutcome(t *testing.T) {
	var order []string
	subscriber, repo, pool, msg := newCompletionTestSubscriber(&order, nil)
	pool.tx.commitStarted = make(chan struct{})
	pool.tx.commitRelease = make(chan struct{})
	subscriber.config.AckDeadline = 5 * time.Millisecond
	subscriber.ctx = context.Background()
	subscriber.inFlight = make(chan struct{}, 1)
	subscriber.inFlight <- struct{}{}
	handle, ok := deliveryHandleFromMessage(msg)
	require.True(t, ok)

	completionDone := make(chan error, 1)
	go func() { completionDone <- completeMessage(msg.Context(), msg, nil) }()
	select {
	case <-pool.tx.commitStarted:
	case <-time.After(time.Second):
		t.Fatal("completion transaction did not reach commit")
	}
	subscriber.watchers.Add(1)
	watchDone := make(chan struct{})
	go func() {
		subscriber.watch(msg, handle, func() {})
		close(watchDone)
	}()
	select {
	case <-watchDone:
		t.Fatal("acknowledgement watcher returned before in-flight completion resolved")
	case <-time.After(20 * time.Millisecond):
	}
	close(pool.tx.commitRelease)
	require.NoError(t, <-completionDone)
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("acknowledgement watcher did not observe committed completion")
	}
	require.Zero(t, repo.retry.Load())
}

type claimValidationTx struct {
	commits   int
	rollbacks int
}

func (tx *claimValidationTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (tx *claimValidationTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (tx *claimValidationTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (tx *claimValidationTx) Commit(context.Context) error {
	tx.commits++
	return nil
}

func (tx *claimValidationTx) Rollback(context.Context) error {
	tx.rollbacks++
	return nil
}

type claimValidationPool struct{ tx *claimValidationTx }

func (p *claimValidationPool) Begin(context.Context) (subscriberTx, error) { return p.tx, nil }

type claimValidationRepo struct{ delivery eventspostgres.Delivery }

func (r *claimValidationRepo) Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error) {
	return []eventspostgres.Delivery{r.delivery}, nil
}

func (r *claimValidationRepo) ConsumerByID(context.Context, eventspostgres.Tx, string) (eventspostgres.Consumer, error) {
	return eventspostgres.Consumer{}, nil
}

func (r *claimValidationRepo) GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error) {
	return eventspostgres.Event{EventID: r.delivery.EventID, AggregateType: "agent_run", ScopeID: "scope", AggregateID: "aggregate", AggregateVersion: 1, EventType: "agent_run.completed", SchemaVersion: 1, OccurredAt: time.Now().UTC(), Payload: json.RawMessage(`{"ok":true}`)}, nil
}

func (r *claimValidationRepo) Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error {
	return nil
}

func (r *claimValidationRepo) Complete(context.Context, eventspostgres.Tx, string, string, string, int64, eventspostgres.DeliveryOutcome, json.RawMessage) error {
	return nil
}

func TestClaimRejectsUnfencedDeliveryBeforeCommit(t *testing.T) {
	const consumerID = "00000000-0000-0000-0000-000000000001"
	const eventID = "00000000-0000-7000-8000-000000000001"
	base := eventspostgres.Delivery{ConsumerID: consumerID, EventID: eventID, Status: "claimed", Attempts: 1, ClaimGeneration: 1, ClaimedBy: "worker"}
	cases := []struct {
		name string
		mut  func(*eventspostgres.Delivery)
	}{
		{name: "consumer", mut: func(d *eventspostgres.Delivery) { d.ConsumerID = "" }},
		{name: "event", mut: func(d *eventspostgres.Delivery) { d.EventID = "" }},
		{name: "status", mut: func(d *eventspostgres.Delivery) { d.Status = "pending" }},
		{name: "attempts", mut: func(d *eventspostgres.Delivery) { d.Attempts = 0 }},
		{name: "generation", mut: func(d *eventspostgres.Delivery) { d.ClaimGeneration = 0 }},
		{name: "worker", mut: func(d *eventspostgres.Delivery) { d.ClaimedBy = "" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			delivery := base
			tc.mut(&delivery)
			tx := &claimValidationTx{}
			s := &Subscriber{
				pool: &claimValidationPool{tx: tx}, repo: &claimValidationRepo{delivery: delivery},
				config: SubscriberConfig{ConsumerID: consumerID, WorkerID: "worker", Topic: TopicAgent, ClaimLease: time.Second, AckDeadline: 100 * time.Millisecond, RecoveryMargin: 50 * time.Millisecond, BatchSize: 1, MaxInFlight: 1, PollInterval: time.Millisecond, BaseRetry: time.Millisecond, MaxRetry: time.Millisecond, MaxAttempts: 1},
				ctx:    context.Background(), inFlight: make(chan struct{}, 1),
			}
			require.Error(t, s.claimCycle())
			require.Zero(t, tx.commits)
			require.Equal(t, 1, tx.rollbacks)
		})
	}
}

type shutdownTestTx struct {
	commitStarted chan struct{}
	allowCommit   <-chan struct{}
}

func (tx *shutdownTestTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (tx *shutdownTestTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, nil
}

func (tx *shutdownTestTx) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (tx *shutdownTestTx) Commit(context.Context) error {
	if tx.commitStarted != nil {
		close(tx.commitStarted)
	}
	if tx.allowCommit != nil {
		<-tx.allowCommit
	}
	return nil
}

func (tx *shutdownTestTx) Rollback(context.Context) error { return nil }

type shutdownTestPool struct{ tx *shutdownTestTx }

func (p *shutdownTestPool) Begin(context.Context) (subscriberTx, error) { return p.tx, nil }

type shutdownTestRepo struct {
	retryStarted chan struct{}
	retryRelease <-chan struct{}
	retryErr     error

	claimStarted chan struct{}
	claimRelease <-chan struct{}
	claimErr     error
}

type enrollmentShutdownRepo struct {
	started  chan struct{}
	canceled chan struct{}
	release  chan struct{}
}

func (r *enrollmentShutdownRepo) Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error) {
	return nil, nil
}

func (r *enrollmentShutdownRepo) ConsumerByID(ctx context.Context, _ eventspostgres.Tx, _ string) (eventspostgres.Consumer, error) {
	close(r.started)
	select {
	case <-ctx.Done():
		close(r.canceled)
	case <-r.canceled:
	}
	<-r.release
	return eventspostgres.Consumer{}, errors.New("enrollment verification released")
}

func (r *enrollmentShutdownRepo) GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error) {
	return eventspostgres.Event{}, nil
}

func (r *enrollmentShutdownRepo) Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error {
	return nil
}

func (r *enrollmentShutdownRepo) Complete(context.Context, eventspostgres.Tx, string, string, string, int64, eventspostgres.DeliveryOutcome, json.RawMessage) error {
	return nil
}

func (r *shutdownTestRepo) Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error) {
	if r.claimStarted != nil {
		close(r.claimStarted)
	}
	if r.claimRelease != nil {
		<-r.claimRelease
	}
	return nil, r.claimErr
}

func (r *shutdownTestRepo) ConsumerByID(context.Context, eventspostgres.Tx, string) (eventspostgres.Consumer, error) {
	return eventspostgres.Consumer{}, nil
}

func (r *shutdownTestRepo) GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error) {
	return eventspostgres.Event{}, nil
}

func (r *shutdownTestRepo) Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error {
	if r.retryStarted != nil {
		close(r.retryStarted)
	}
	if r.retryRelease != nil {
		<-r.retryRelease
	}
	return r.retryErr
}

func (r *shutdownTestRepo) Complete(context.Context, eventspostgres.Tx, string, string, string, int64, eventspostgres.DeliveryOutcome, json.RawMessage) error {
	return nil
}

func newShutdownTestSubscriber(pool subscriberBeginner, repo subscriberRepository) (*Subscriber, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	close(done)
	return &Subscriber{
		pool: pool,
		repo: repo,
		config: SubscriberConfig{
			ConsumerID:     "00000000-0000-0000-0000-000000000001",
			Topic:          TopicAgent,
			WorkerID:       "worker",
			PollInterval:   time.Millisecond,
			ClaimLease:     time.Second,
			AckDeadline:    time.Millisecond,
			RecoveryMargin: 100 * time.Millisecond,
			BatchSize:      1,
			MaxInFlight:    1,
			BaseRetry:      time.Millisecond,
			MaxRetry:       time.Millisecond,
			MaxAttempts:    1,
		},
		ctx:      ctx,
		cancel:   cancel,
		done:     done,
		inFlight: make(chan struct{}, 1),
		fatal:    make(chan error, 1),
	}, cancel
}

func newShutdownTestHandle(s *Subscriber) (*message.Message, *deliveryHandle) {
	handle := &deliveryHandle{subscriber: s, delivery: eventspostgres.Delivery{
		ConsumerID: s.config.ConsumerID, EventID: "00000000-0000-7000-8000-000000000002",
		ClaimedBy: s.config.WorkerID, ClaimGeneration: 1, Attempts: 1,
	}}
	msg := message.NewMessage(handle.delivery.EventID, nil)
	msg.SetContext(context.WithValue(context.Background(), deliveryHandleKey{}, handle))
	return msg, handle
}

func waitForSubscriberClosed(t *testing.T, s *Subscriber) {
	t.Helper()
	require.Eventually(t, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return s.closed
	}, time.Second, time.Millisecond)
}

func TestSubscriberCloseWaitsForRetryWatcherTransaction(t *testing.T) {
	commitStarted := make(chan struct{})
	allowCommit := make(chan struct{})
	tx := &shutdownTestTx{commitStarted: commitStarted, allowCommit: allowCommit}
	s, _ := newShutdownTestSubscriber(&shutdownTestPool{tx: tx}, &shutdownTestRepo{})
	s.inFlight <- struct{}{}
	msg, handle := newShutdownTestHandle(s)
	msg.Ack()
	s.watchers.Add(1)
	go s.watch(msg, handle, func() {})

	select {
	case <-commitStarted:
	case <-time.After(time.Second):
		t.Fatal("retry transaction did not reach commit")
	}

	closeDone := make(chan struct{})
	go func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		close(closeDone)
	}()
	waitForSubscriberClosed(t, s)
	select {
	case <-closeDone:
		t.Fatal("Close returned while retry transaction was still running")
	default:
	}

	close(allowCommit)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for retry watcher completion")
	}
}

func TestSubscriberCloseSuppressesFatalFromRetryError(t *testing.T) {
	retryStarted := make(chan struct{})
	retryRelease := make(chan struct{})
	retryErr := errors.New("retry persistence failed")
	repo := &shutdownTestRepo{retryStarted: retryStarted, retryRelease: retryRelease, retryErr: retryErr}
	s, _ := newShutdownTestSubscriber(&shutdownTestPool{tx: &shutdownTestTx{}}, repo)
	s.inFlight <- struct{}{}
	msg, handle := newShutdownTestHandle(s)
	msg.Ack()
	s.watchers.Add(1)
	go s.watch(msg, handle, func() {})

	select {
	case <-retryStarted:
	case <-time.After(time.Second):
		t.Fatal("retry watcher did not begin persistence")
	}

	closeDone := make(chan struct{})
	go func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		close(closeDone)
	}()
	waitForSubscriberClosed(t, s)
	close(retryRelease)
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not finish after retry persistence returned")
	}
	select {
	case err := <-s.Fatal():
		t.Fatalf("clean Close published Fatal: %v", err)
	default:
	}
}

func TestSubscriberCloseWaitsForEnrollmentVerification(t *testing.T) {
	repo := &enrollmentShutdownRepo{started: make(chan struct{}), canceled: make(chan struct{}), release: make(chan struct{})}
	s, _ := newShutdownTestSubscriber(&shutdownTestPool{tx: &shutdownTestTx{}}, repo)
	subscribeDone := make(chan error, 1)
	go func() {
		_, err := s.Subscribe(context.Background(), TopicAgent)
		subscribeDone <- err
	}()

	select {
	case <-repo.started:
	case <-time.After(time.Second):
		t.Fatal("enrollment verification did not begin")
	}
	closeDone := make(chan struct{})
	go func() {
		if err := s.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
		close(closeDone)
	}()
	waitForSubscriberClosed(t, s)
	select {
	case <-closeDone:
		t.Fatal("Close returned while enrollment verification was still running")
	default:
	}
	select {
	case <-repo.canceled:
	case <-time.After(time.Second):
		t.Fatal("Close did not cancel enrollment verification")
	}
	select {
	case <-closeDone:
		t.Fatal("Close returned while enrollment verification was still running")
	default:
	}
	close(repo.release)
	select {
	case err := <-subscribeDone:
		require.Error(t, err)
	case <-time.After(time.Second):
		t.Fatal("Subscribe did not observe enrollment cancellation")
	}
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close did not wait for enrollment verification completion")
	}
}

func TestSubscriberCancellationSuppressesFatalFromPollError(t *testing.T) {
	claimStarted := make(chan struct{})
	claimRelease := make(chan struct{})
	pollErr := errors.New("poll failed")
	repo := &shutdownTestRepo{claimStarted: claimStarted, claimRelease: claimRelease, claimErr: pollErr}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	s := &Subscriber{
		pool: &shutdownTestPool{tx: &shutdownTestTx{}},
		repo: repo,
		config: SubscriberConfig{
			ConsumerID: "00000000-0000-0000-0000-000000000001", Topic: TopicAgent, WorkerID: "worker",
			PollInterval: time.Millisecond, ClaimLease: time.Second, AckDeadline: time.Millisecond,
			RecoveryMargin: 100 * time.Millisecond, BatchSize: 1, MaxInFlight: 1,
			BaseRetry: time.Millisecond, MaxRetry: time.Millisecond, MaxAttempts: 1,
		},
		ctx: ctx, cancel: cancel, done: done, inFlight: make(chan struct{}, 1), fatal: make(chan error, 1),
	}
	go s.run()

	select {
	case <-claimStarted:
	case <-time.After(time.Second):
		t.Fatal("poll transaction did not begin")
	}
	cancel()
	<-ctx.Done()
	close(claimRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("subscriber run did not stop after cancellation")
	}
	select {
	case err := <-s.Fatal():
		t.Fatalf("clean cancellation published Fatal: %v", err)
	default:
	}
}
