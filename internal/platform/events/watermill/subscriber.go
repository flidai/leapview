package watermill

// This file contains LeapView's canonical Watermill Subscriber. PostgreSQL's
// event_delivery rows are the only delivery authority; Watermill is used only
// as an in-process message boundary.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThreeDotsLabs/watermill/message"
	eventspostgres "github.com/flidai/leapview/internal/platform/events/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultPollInterval   = 250 * time.Millisecond
	defaultClaimLease     = 30 * time.Second
	defaultAckDeadline    = 20 * time.Second
	defaultRecoveryMargin = 5 * time.Second
	defaultBatchSize      = 16
	defaultMaxInFlight    = 16
	defaultBaseRetry      = time.Second
	defaultMaxRetry       = time.Minute
	defaultMaxAttempts    = int64(8)

	// Delivery SQL is intentionally bounded independently of the lease. A
	// caller cannot make a claim transaction wait for an unbounded database
	// operation by configuring the polling loop.
	transactionTimeout = 5 * time.Second
)

var (
	ErrSubscriberNotConfigured     = errors.New("watermill subscriber is not configured")
	ErrSubscriberClosed            = errors.New("watermill subscriber is closed")
	ErrSubscriberTopic             = errors.New("watermill subscriber topic is not configured")
	ErrSubscriberAlreadySubscribed = errors.New("watermill subscriber already subscribed")
	ErrNoDeliveryHandle            = errors.New("watermill message has no event delivery handle")
	ErrProducedMessagesUnsupported = errors.New("watermill completion middleware does not support produced messages")
)

// SubscriberConfig is intentionally explicit about durable enrollment and
// worker identity. Runtime startup never generates or enrolls identities.
// Zero-valued operational fields receive the bounded defaults documented in
// this package; identity fields are always required explicitly.
type SubscriberConfig struct {
	ConsumerID  string
	ConsumerKey string
	Topic       string
	WorkerID    string

	PollInterval   time.Duration
	ClaimLease     time.Duration
	AckDeadline    time.Duration
	RecoveryMargin time.Duration
	BatchSize      int
	MaxInFlight    int
	BaseRetry      time.Duration
	MaxRetry       time.Duration
	MaxAttempts    int64
}

func (c SubscriberConfig) withDefaults() SubscriberConfig {
	if c.PollInterval == 0 {
		c.PollInterval = defaultPollInterval
	}
	if c.ClaimLease == 0 {
		c.ClaimLease = defaultClaimLease
	}
	if c.AckDeadline == 0 {
		c.AckDeadline = defaultAckDeadline
	}
	if c.RecoveryMargin == 0 {
		c.RecoveryMargin = defaultRecoveryMargin
	}
	if c.BatchSize == 0 {
		c.BatchSize = defaultBatchSize
	}
	if c.MaxInFlight == 0 {
		c.MaxInFlight = defaultMaxInFlight
	}
	if c.BaseRetry == 0 {
		c.BaseRetry = defaultBaseRetry
	}
	if c.MaxRetry == 0 {
		c.MaxRetry = defaultMaxRetry
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = defaultMaxAttempts
	}
	return c
}

func (c SubscriberConfig) validate() (SubscriberConfig, error) {
	c = c.withDefaults()
	consumerID, err := uuid.Parse(c.ConsumerID)
	if err != nil || consumerID.String() != c.ConsumerID {
		return c, errors.New("consumer id must be a canonical UUID")
	}
	if strings.TrimSpace(c.ConsumerKey) != c.ConsumerKey || c.ConsumerKey == "" || len(c.ConsumerKey) > 255 {
		return c, errors.New("consumer key must be a non-empty bounded identity")
	}
	if strings.TrimSpace(c.Topic) != c.Topic || c.Topic == "" {
		return c, ErrSubscriberTopic
	}
	if _, err := AggregatesForTopic(c.Topic); err != nil {
		return c, err
	}
	if strings.TrimSpace(c.WorkerID) != c.WorkerID || c.WorkerID == "" || len(c.WorkerID) > 255 {
		return c, errors.New("worker id must be a non-empty bounded identity")
	}
	if c.PollInterval <= 0 || c.PollInterval > time.Minute {
		return c, errors.New("poll interval is out of bounds")
	}
	if c.ClaimLease <= 0 || c.ClaimLease > 24*time.Hour {
		return c, errors.New("claim lease is out of bounds")
	}
	if c.AckDeadline <= 0 || c.AckDeadline > 20*time.Second {
		return c, errors.New("ack deadline must be between 1ns and 20s")
	}
	if c.RecoveryMargin <= 0 || c.RecoveryMargin > 24*time.Hour {
		return c, errors.New("recovery margin is out of bounds")
	}
	if c.ClaimLease <= c.AckDeadline+c.RecoveryMargin {
		return c, errors.New("claim lease must outlive acknowledgement deadline and recovery margin")
	}
	if c.BatchSize <= 0 || c.BatchSize > 1000 || c.MaxInFlight <= 0 || c.MaxInFlight > 1000 || c.BatchSize > c.MaxInFlight {
		return c, errors.New("batch size and max in-flight are out of bounds")
	}
	if c.BaseRetry <= 0 || c.MaxRetry <= 0 || c.BaseRetry > c.MaxRetry || c.MaxRetry > 24*time.Hour {
		return c, errors.New("retry bounds are invalid")
	}
	if c.MaxAttempts <= 0 || c.MaxAttempts > 1000 {
		return c, errors.New("max attempts are out of bounds")
	}
	return c, nil
}

// subscriberTx is deliberately narrower than pgx.Tx. It keeps transaction
// ownership visible to tests and prevents a transaction from crossing the
// output channel or handler boundary.
type subscriberTx interface {
	eventspostgres.Tx
	Commit(context.Context) error
	Rollback(context.Context) error
}

type subscriberBeginner interface {
	Begin(context.Context) (subscriberTx, error)
}

type subscriberRepository interface {
	Claim(context.Context, eventspostgres.Tx, eventspostgres.ClaimOptions) ([]eventspostgres.Delivery, error)
	ConsumerByID(context.Context, eventspostgres.Tx, string) (eventspostgres.Consumer, error)
	GetEvent(context.Context, eventspostgres.Tx, string) (eventspostgres.Event, error)
	Retry(context.Context, eventspostgres.Tx, eventspostgres.RetryOptions) error
	Complete(context.Context, eventspostgres.Tx, string, string, string, int64, eventspostgres.DeliveryOutcome, json.RawMessage) error
}

type pgxPoolBeginner struct{ pool *pgxpool.Pool }

func (p pgxPoolBeginner) Begin(ctx context.Context) (subscriberTx, error) {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// Subscriber implements message.Subscriber over event.event_delivery.
type Subscriber struct {
	pool   subscriberBeginner
	repo   subscriberRepository
	config SubscriberConfig

	mu          sync.Mutex
	closed      bool
	subscribing bool
	subscribed  bool
	ctx         context.Context
	cancel      context.CancelFunc
	out         chan *message.Message
	done        chan struct{}
	inFlight    chan struct{}
	watchers    sync.WaitGroup

	fatalOnce sync.Once
	fatal     chan error
}

var _ message.Subscriber = (*Subscriber)(nil)

// NewSubscriber binds the concrete production pool and canonical event
// repository. It performs no enrollment and does not allocate identities.
func NewSubscriber(pool *pgxpool.Pool, repo *eventspostgres.Repository, config SubscriberConfig) (*Subscriber, error) {
	if pool == nil || repo == nil {
		return nil, ErrSubscriberNotConfigured
	}
	return newSubscriber(pgxPoolBeginner{pool: pool}, repo, config)
}

// newSubscriber is kept as a narrow constructor seam for package tests. The
// exported constructor only accepts the concrete production dependencies.
func newSubscriber(pool subscriberBeginner, repo subscriberRepository, config SubscriberConfig) (*Subscriber, error) {
	if pool == nil || repo == nil {
		return nil, ErrSubscriberNotConfigured
	}
	validated, err := config.validate()
	if err != nil {
		return nil, err
	}
	return &Subscriber{pool: pool, repo: repo, config: validated, fatal: make(chan error, 1)}, nil
}

// Fatal returns the channel on which the first terminal loop, projection, or
// database error is published. Context cancellation and Close are clean and
// do not publish an error. The channel is intentionally not closed: callers
// can distinguish a published nil-free error from a clean shutdown.
func (s *Subscriber) Fatal() <-chan error {
	if s == nil {
		return nil
	}
	return s.fatal
}

func (s *Subscriber) setFatal(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	// Serialize the decision with Close. Once shutdown has begun, a database
	// operation losing its context is recovery evidence for the next process,
	// not a terminal runtime failure for this one.
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || (s.ctx != nil && s.ctx.Err() != nil) {
		return
	}
	s.fatalOnce.Do(func() {
		s.fatal <- err
		if s.cancel != nil {
			s.cancel()
		}
	})
}

// Subscribe accepts exactly one configured topic and one active subscription.
// Enrollment is checked and committed before any claim transaction starts.
func (s *Subscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	if s == nil || s.pool == nil || s.repo == nil {
		return nil, ErrSubscriberNotConfigured
	}
	if ctx == nil {
		return nil, errors.New("subscriber context is nil")
	}
	if topic != s.config.Topic {
		return nil, fmt.Errorf("%w: got %q", ErrSubscriberTopic, topic)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrSubscriberClosed
	}
	if s.subscribed || s.subscribing {
		s.mu.Unlock()
		return nil, ErrSubscriberAlreadySubscribed
	}
	s.subscribing = true
	s.mu.Unlock()

	if err := s.verifyEnrollment(ctx); err != nil {
		s.mu.Lock()
		s.subscribing = false
		s.mu.Unlock()
		return nil, err
	}

	runCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed {
		s.subscribing = false
		s.mu.Unlock()
		cancel()
		return nil, ErrSubscriberClosed
	}
	s.subscribing = false
	s.subscribed, s.ctx, s.cancel = true, runCtx, cancel
	s.out, s.done = make(chan *message.Message, s.config.MaxInFlight), make(chan struct{})
	s.inFlight = make(chan struct{}, s.config.MaxInFlight)
	out := s.out
	s.mu.Unlock()
	go s.run()
	return out, nil
}

func (s *Subscriber) txContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, transactionTimeout)
}

func (s *Subscriber) claimContext(parent context.Context) (context.Context, context.CancelFunc) {
	// RecoveryMargin is the configured allowance for claim projection, commit,
	// scheduling, and clock skew. A claim that cannot commit within that window
	// must roll back instead of emitting with too little lease left for Ack.
	timeout := minDuration(transactionTimeout, s.config.RecoveryMargin)
	return context.WithTimeout(parent, timeout)
}

func rollbackContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), transactionTimeout)
}

func (s *Subscriber) verifyEnrollment(ctx context.Context) error {
	txCtx, cancel := s.txContext(ctx)
	defer cancel()
	tx, err := s.pool.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("verify event consumer enrollment: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			rbCtx, rbCancel := rollbackContext()
			_ = tx.Rollback(rbCtx)
			rbCancel()
		}
	}()

	consumer, err := s.repo.ConsumerByID(txCtx, tx, s.config.ConsumerID)
	if err != nil {
		return err
	}
	expected, err := AggregatesForTopic(s.config.Topic)
	if err != nil {
		return err
	}
	actual := append([]string(nil), consumer.AggregateTypes...)
	sort.Strings(actual)
	if consumer.ConsumerID != s.config.ConsumerID {
		return errors.New("event consumer enrollment does not match configured consumer id")
	}
	if consumer.ConsumerKey != s.config.ConsumerKey {
		return errors.New("event consumer enrollment does not match configured consumer key")
	}
	if consumer.Lifecycle != "enabled" {
		return errors.New("event consumer enrollment is not enabled")
	}
	if !equalStrings(actual, expected) {
		return errors.New("event consumer enrollment aggregate filter does not match topic")
	}
	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("commit event consumer enrollment check: %w", err)
	}
	rollback = false
	return nil
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Subscriber) run() {
	defer func() {
		s.mu.Lock()
		if s.out != nil {
			close(s.out)
		}
		if s.done != nil {
			close(s.done)
		}
		s.mu.Unlock()
	}()

	ticker := time.NewTicker(s.config.PollInterval)
	defer ticker.Stop()
	for {
		if s.ctx.Err() != nil {
			return
		}
		if err := s.claimCycle(); err != nil {
			if s.ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return
			}
			s.setFatal(err)
			return
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Subscriber) claimCycle() error {
	available := s.config.MaxInFlight - len(s.inFlight)
	if available <= 0 {
		return nil
	}
	if available > s.config.BatchSize {
		available = s.config.BatchSize
	}

	txCtx, cancel := s.claimContext(s.ctx)
	defer cancel()
	tx, err := s.pool.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("begin event delivery claim: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			rbCtx, rbCancel := rollbackContext()
			_ = tx.Rollback(rbCtx)
			rbCancel()
		}
	}()

	claimed, err := s.repo.Claim(txCtx, tx, eventspostgres.ClaimOptions{
		ConsumerID: s.config.ConsumerID,
		WorkerID:   s.config.WorkerID,
		Limit:      available,
		Lease:      s.config.ClaimLease,
	})
	if err != nil {
		return err
	}
	type prepared struct {
		delivery eventspostgres.Delivery
		msg      *message.Message
	}
	preparedMessages := make([]prepared, 0, len(claimed))
	for _, delivery := range claimed {
		if delivery.ConsumerID == "" || delivery.ConsumerID != s.config.ConsumerID {
			return fmt.Errorf("project claimed event %s: claimed consumer %q does not match configured consumer %q", delivery.EventID, delivery.ConsumerID, s.config.ConsumerID)
		}
		if delivery.EventID == "" {
			return errors.New("project claimed delivery has empty event identity")
		}
		if delivery.Status != "claimed" {
			return fmt.Errorf("project claimed event %s: status %q is not claimed", delivery.EventID, delivery.Status)
		}
		if delivery.ClaimGeneration <= 0 {
			return fmt.Errorf("project claimed event %s: claim generation must be positive", delivery.EventID)
		}
		if delivery.Attempts <= 0 {
			return fmt.Errorf("project claimed event %s: attempts must be positive", delivery.EventID)
		}
		if delivery.ClaimedBy == "" || delivery.ClaimedBy != s.config.WorkerID {
			return fmt.Errorf("project claimed event %s: claimed worker %q does not match configured worker %q", delivery.EventID, delivery.ClaimedBy, s.config.WorkerID)
		}
		event, getErr := s.repo.GetEvent(txCtx, tx, delivery.EventID)
		if getErr != nil {
			return fmt.Errorf("project claimed event %s: %w", delivery.EventID, getErr)
		}
		if event.EventID != delivery.EventID {
			return fmt.Errorf("project claimed event %s: event identity mismatch", delivery.EventID)
		}
		if topic, topicErr := TopicForAggregate(event.AggregateType); topicErr != nil {
			return fmt.Errorf("project claimed event %s: %w", delivery.EventID, topicErr)
		} else if topic != s.config.Topic {
			return fmt.Errorf("project claimed event %s: aggregate topic %q does not match subscriber topic %q", delivery.EventID, topic, s.config.Topic)
		}
		msg, msgErr := MessageForEvent(s.config.Topic, event)
		if msgErr != nil {
			return fmt.Errorf("project claimed event %s: %w", delivery.EventID, msgErr)
		}
		preparedMessages = append(preparedMessages, prepared{delivery: delivery, msg: msg})
	}
	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("commit event delivery claim: %w", err)
	}
	rollback = false

	// Cancellation and handoff are concurrent after the claim commit. The send
	// below linearizes one winner: if cancellation wins, the row remains claimed
	// until lease recovery; if handoff wins, the canceled message context and
	// exact claim fence still prevent an unfenced terminal transition. Shutdown
	// never synthesizes a retry from either path.
	for _, item := range preparedMessages {
		if s.ctx.Err() != nil {
			break
		}
		select {
		case s.inFlight <- struct{}{}:
		default:
			return errors.New("subscriber in-flight accounting exceeded configured bound")
		}
		handle := &deliveryHandle{subscriber: s, delivery: item.delivery}
		msgCtx, cancel := context.WithCancel(s.ctx)
		item.msg.SetContext(context.WithValue(msgCtx, deliveryHandleKey{}, handle))
		s.watchers.Add(1)
		go s.watch(item.msg, handle, cancel)
		select {
		case s.out <- item.msg:
		case <-s.ctx.Done():
			cancel()
		}
	}
	return nil
}

func (s *Subscriber) watch(msg *message.Message, handle *deliveryHandle, cancel context.CancelFunc) {
	defer func() {
		cancel()
		<-s.inFlight
		s.watchers.Done()
	}()
	timer := time.NewTimer(s.config.AckDeadline)
	defer timer.Stop()
	select {
	case <-s.ctx.Done():
		return
	case <-msg.Acked():
		if s.ctx.Err() != nil {
			return
		}
		if handle.isCompleted() {
			return
		}
		s.retryDelivery(handle, "ack_before_complete")
	case <-msg.Nacked():
		if s.ctx.Err() != nil {
			return
		}
		s.retryDelivery(handle, "handler_nack")
	case <-timer.C:
		if s.ctx.Err() != nil {
			return
		}
		s.retryDelivery(handle, "acknowledgement_timeout")
	}
}

func (s *Subscriber) retryDelivery(handle *deliveryHandle, code string) {
	// Ack/Nack cancels the message context in some Watermill routers. Derive
	// from WithoutCancel so the exact claim fence can still be persisted.
	parent := context.WithoutCancel(s.ctx)
	txCtx, cancel := context.WithTimeout(parent, transactionTimeout)
	defer cancel()
	tx, err := s.pool.Begin(txCtx)
	if err == nil {
		err = s.repo.Retry(txCtx, tx, eventspostgres.RetryOptions{
			ConsumerID:      s.config.ConsumerID,
			EventID:         handle.delivery.EventID,
			WorkerID:        s.config.WorkerID,
			ClaimGeneration: handle.delivery.ClaimGeneration,
			Delay:           retryDelay(s.config.BaseRetry, s.config.MaxRetry, handle.delivery.Attempts),
			MaxAttempts:     s.config.MaxAttempts,
			Evidence:        stableEvidence(code),
		})
		if err == nil {
			err = tx.Commit(txCtx)
			if err != nil {
				rbCtx, rbCancel := rollbackContext()
				_ = tx.Rollback(rbCtx)
				rbCancel()
			}
		} else {
			rbCtx, rbCancel := rollbackContext()
			_ = tx.Rollback(rbCtx)
			rbCancel()
		}
	}
	if err != nil && !errors.Is(err, eventspostgres.ErrDeliveryClaimLost) && !errors.Is(err, context.Canceled) {
		s.setFatal(fmt.Errorf("persist event delivery retry: %w", err))
	}
}

func stableEvidence(code string) json.RawMessage {
	if code == "" {
		code = "retry"
	}
	return json.RawMessage(`{"code":"` + code + `"}`)
}

func retryDelay(base, max time.Duration, attempts int64) time.Duration {
	if attempts <= 1 {
		return minDuration(base, max)
	}
	d := base
	for i := int64(1); i < attempts && d < max; i++ {
		if d > time.Duration(math.MaxInt64/2) {
			d = max
			break
		}
		d *= 2
		if d > max {
			d = max
			break
		}
	}
	return minDuration(d, max)
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// Close stops claims and closes the output. Claimed rows are deliberately
// left untouched so another worker can recover them after lease expiry.
func (s *Subscriber) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		done := s.done
		s.mu.Unlock()
		if done != nil {
			<-done
		}
		s.watchers.Wait()
		return nil
	}
	s.closed = true
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	// The polling loop is the only goroutine that adds watchers. Waiting for it
	// first prevents Add from racing Wait and ensures no delivery transaction
	// can outlive subscriber shutdown (and the PostgreSQL pool it depends on).
	s.watchers.Wait()
	return nil
}

type deliveryHandleKey struct{}

type deliveryHandle struct {
	subscriber *Subscriber
	delivery   eventspostgres.Delivery

	mu        sync.Mutex
	completed bool
}

func (h *deliveryHandle) isCompleted() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.completed
}

func deliveryHandleFromMessage(msg *message.Message) (*deliveryHandle, bool) {
	if msg == nil {
		return nil, false
	}
	return deliveryHandleFromContext(msg.Context())
}

func deliveryHandleFromContext(ctx context.Context) (*deliveryHandle, bool) {
	if ctx == nil {
		return nil, false
	}
	h, ok := ctx.Value(deliveryHandleKey{}).(*deliveryHandle)
	return h, ok && h != nil
}

// CompleteMessage records succeeded terminal state for a claimed subscriber
// message. Completion deliberately owns a short, READ COMMITTED transaction:
// handler effects and terminal completion are separate commits, so the effect
// must be idempotent. A future product unit-of-work can provide exact same-tx
// completion by observing its caller's commit; a generic CompleteTx cannot
// safely mark a handle before that caller commits.
//
// The handle is marked completed only after the completion transaction commits.
// Consequently an early Watermill Ack can race this call and trigger Retry;
// Complete then returns ErrDeliveryClaimLost rather than claiming success.
func CompleteMessage(ctx context.Context, msg *message.Message, evidence json.RawMessage) error {
	h, ok := deliveryHandleFromMessage(msg)
	if !ok {
		return ErrNoDeliveryHandle
	}
	s := h.subscriber
	if s == nil || s.pool == nil || s.repo == nil {
		return ErrSubscriberNotConfigured
	}

	// Serialize completion attempts on a handle. Repeats are no-ops only after
	// this process has observed a successful commit; failed attempts leave the
	// handle incomplete and therefore retryable.
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.completed {
		return nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	// Router acknowledgement and handler timeout cancellation must not prevent
	// persistence of terminal delivery state. The bounded timeout still limits
	// how long this terminal write can occupy a connection.
	txCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), transactionTimeout)
	defer cancel()
	tx, err := s.pool.Begin(txCtx)
	if err != nil {
		return fmt.Errorf("begin event delivery completion: %w", err)
	}
	rollback := true
	defer func() {
		if rollback {
			rbCtx, rbCancel := rollbackContext()
			_ = tx.Rollback(rbCtx)
			rbCancel()
		}
	}()

	if err := s.repo.Complete(txCtx, tx, h.delivery.ConsumerID, h.delivery.EventID, h.delivery.ClaimedBy, h.delivery.ClaimGeneration, eventspostgres.DeliverySucceeded, evidence); err != nil {
		return fmt.Errorf("complete event delivery: %w", err)
	}
	if err := tx.Commit(txCtx); err != nil {
		return fmt.Errorf("commit event delivery completion: %w", err)
	}
	rollback = false
	h.completed = true
	return nil
}

// CompleteOnSuccess returns middleware for consumer handlers that do not
// publish produced messages. It completes the durable delivery only after the
// downstream handler succeeds, and only returns nil after that commit so the
// Router's automatic Ack follows terminal persistence.
func CompleteOnSuccess() message.HandlerMiddleware {
	return func(next message.HandlerFunc) message.HandlerFunc {
		return func(msg *message.Message) ([]*message.Message, error) {
			produced, err := next(msg)
			if err != nil {
				return produced, err
			}
			if len(produced) != 0 {
				return nil, ErrProducedMessagesUnsupported
			}
			if err := CompleteMessage(msg.Context(), msg, json.RawMessage(`{"outcome":"succeeded"}`)); err != nil {
				return nil, err
			}
			return nil, nil
		}
	}
}

// Keep the production transaction bridge honest if pgx changes its native
// transaction surface.
var _ subscriberTx = (pgx.Tx)(nil)
