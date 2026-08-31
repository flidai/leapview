package watermill

// This file owns the app-injectable Watermill Router boundary. The canonical
// PostgreSQL Subscriber remains the delivery authority; Router is only an
// in-process execution loop and has no publisher path.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"sync"
	"time"

	watermill "github.com/ThreeDotsLabs/watermill"
	"github.com/ThreeDotsLabs/watermill/components/metrics"
	"github.com/ThreeDotsLabs/watermill/message"
	"github.com/ThreeDotsLabs/watermill/message/router/middleware"
	"github.com/prometheus/client_golang/prometheus"
)

const (
	// Stop intentionally waits for Router.Close and subscriber persistence
	// watchers, so keep the framework drain below the application's one-minute
	// lifecycle budget.
	maxRuntimeCloseTimeout    = 45 * time.Second
	maxRuntimeHandlerDeadline = 20 * time.Second
	maxRuntimeRetryCount      = 100
	maxRuntimeRetryElapsed    = 20 * time.Minute
)

var (
	ErrRuntimeNotConfigured  = errors.New("watermill runtime is not configured")
	ErrRuntimeClosed         = errors.New("watermill runtime is closed")
	ErrRuntimeAlreadyStarted = errors.New("watermill runtime is already started")
)

// RuntimeConfig is deliberately explicit. All deadlines and retry windows
// must be positive: zero does not silently disable protection. Logger and
// PrometheusRegisterer are injected so construction never reaches process
// globals.
type RuntimeConfig struct {
	CloseTimeout             time.Duration
	HandlerDeadline          time.Duration
	RetryMaxRetries          int
	RetryInitialInterval     time.Duration
	RetryMaxInterval         time.Duration
	RetryMaxElapsedTime      time.Duration
	RetryMultiplier          float64
	RetryRandomizationFactor float64
	Logger                   *slog.Logger
	PrometheusRegisterer     prometheus.Registerer
	MetricsNamespace         string
	MetricsSubsystem         string
}

// HandlerRegistration describes one no-publisher consumer. The concrete
// Subscriber type keeps the durable claim/completion contract at this boundary
// instead of admitting an arbitrary Watermill transport.
type HandlerRegistration struct {
	Name       string
	Topic      string
	Subscriber *Subscriber
	Handler    message.NoPublishHandlerFunc
}

// Runtime owns one core Watermill Router and its registered handlers. Start
// and Stop are the application lifecycle surface; run and close stay private
// to keep framework details out of composition code.
type Runtime struct {
	router      *message.Router
	subscribers []*Subscriber
	prepared    []*preparedSubscriber
	executions  *runtimeExecutions

	fatal     chan error
	fatalWake chan struct{}

	mu         sync.Mutex
	runStarted bool
	closeAsked bool
	closeErr   error
	closeDone  chan struct{}
	runDone    chan struct{}
	fatalErr   error

	shutdownOnce sync.Once
	shutdownDone chan struct{}
	shutdownErr  error
}

// preparedSubscriber is the narrow bridge between runtime preflight and the
// Watermill Router. The canonical Subscriber is enrolled before Router.Run is
// invoked; the Router only receives this adapter and can never trigger a
// second enrollment attempt.
type preparedSubscriber struct {
	subscriber *Subscriber
	topic      string

	mu       sync.Mutex
	prepared bool
	messages <-chan *message.Message
}

var _ message.Subscriber = (*preparedSubscriber)(nil)

func (s *preparedSubscriber) prepare(ctx context.Context) error {
	messages, err := s.subscriber.Subscribe(ctx, s.topic)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.prepared = true
	s.messages = messages
	s.mu.Unlock()
	return nil
}

func (s *preparedSubscriber) Subscribe(ctx context.Context, topic string) (<-chan *message.Message, error) {
	if ctx == nil {
		return nil, errors.New("prepared subscriber context is nil")
	}
	if topic != s.topic {
		return nil, fmt.Errorf("%w: got %q", ErrSubscriberTopic, topic)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.prepared {
		return nil, errors.New("prepared subscriber was not enrolled")
	}
	return s.messages, nil
}

func (s *preparedSubscriber) Close() error { return nil }

// runtimeExecutions owns the runtime's handler execution lifetime. Add and
// Wait are serialized by the gate, so shutdown can close admission without a
// WaitGroup Add racing a zero counter. A deliberate Stop therefore waits for
// Complete and every other middleware frame, even when Router.Close times out.
type runtimeExecutions struct {
	mu         sync.Mutex
	accepting  bool
	active     int
	done       chan struct{}
	doneClosed bool
}

func newRuntimeExecutions() *runtimeExecutions {
	return &runtimeExecutions{accepting: true, done: make(chan struct{})}
}

func (e *runtimeExecutions) begin() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.accepting {
		return false
	}
	e.active++
	return true
}

func (e *runtimeExecutions) end() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.active--
	if !e.accepting && e.active == 0 && !e.doneClosed {
		close(e.done)
		e.doneClosed = true
	}
}

func (e *runtimeExecutions) stopAccepting() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.accepting = false
	if e.active == 0 && !e.doneClosed {
		close(e.done)
		e.doneClosed = true
	}
}

func (e *runtimeExecutions) wait() { <-e.done }

func (e *runtimeExecutions) middleware(next message.HandlerFunc) message.HandlerFunc {
	return func(msg *message.Message) ([]*message.Message, error) {
		if !e.begin() {
			return nil, ErrRuntimeClosed
		}
		defer e.end()
		return next(msg)
	}
}

// NewRuntime constructs the Router and exact no-publisher registrations, but
// does not start subscriptions. Startup and fatal errors are observed by
// Start.
func NewRuntime(config RuntimeConfig, registrations ...HandlerRegistration) (*Runtime, error) {
	if err := validateRuntimeConfig(config, registrations); err != nil {
		return nil, err
	}

	logger := watermill.NewSlogLogger(config.Logger)
	router, err := message.NewRouter(message.RouterConfig{CloseTimeout: config.CloseTimeout}, logger)
	if err != nil {
		return nil, fmt.Errorf("create watermill router: %w", err)
	}

	builder := metrics.NewPrometheusMetricsBuilder(config.PrometheusRegisterer, config.MetricsNamespace, config.MetricsSubsystem)
	// Use only Watermill's handler middleware. The stock Router convenience also
	// decorates subscribers; that decorator starts one Ack/Nack goroutine per
	// message and can outlive the canonical subscriber during shutdown.
	handlerMetrics, err := newPrometheusRouterMiddleware(builder)
	if err != nil {
		return nil, err
	}
	executions := newRuntimeExecutions()

	// Router executes the first-added middleware as the outermost frame. The
	// runtime gate wraps Watermill's handler metrics too, so Stop cannot release
	// PostgreSQL ownership while the full middleware chain is unwinding:
	// execution gate -> Prometheus handler metrics -> topic capture -> Timeout
	// -> Retry -> Recoverer -> Complete -> handler.
	retry := middleware.Retry{
		MaxRetries:          config.RetryMaxRetries,
		InitialInterval:     config.RetryInitialInterval,
		MaxInterval:         config.RetryMaxInterval,
		MaxElapsedTime:      config.RetryMaxElapsedTime,
		Multiplier:          config.RetryMultiplier,
		RandomizationFactor: config.RetryRandomizationFactor,
		ResetContextOnRetry: true,
		Logger:              logger,
	}
	router.AddMiddleware(
		executions.middleware,
		handlerMetrics,
		captureCompletionTopic(),
		middleware.Timeout(config.HandlerDeadline),
		retry.Middleware,
		middleware.Recoverer,
		CompleteOnSuccess(),
	)

	subscribers := make([]*Subscriber, len(registrations))
	prepared := make([]*preparedSubscriber, len(registrations))
	for i, registration := range registrations {
		prepared[i] = &preparedSubscriber{subscriber: registration.Subscriber, topic: registration.Topic}
		router.AddConsumerHandler(registration.Name, registration.Topic, prepared[i], registration.Handler)
		subscribers[i] = registration.Subscriber
	}
	return &Runtime{
		router:       router,
		subscribers:  subscribers,
		prepared:     prepared,
		executions:   executions,
		fatal:        make(chan error, 1),
		fatalWake:    make(chan struct{}, 1),
		runDone:      make(chan struct{}),
		shutdownDone: make(chan struct{}),
	}, nil
}

func newPrometheusRouterMiddleware(builder metrics.PrometheusMetricsBuilder) (middleware message.HandlerMiddleware, err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("configure watermill prometheus metrics: %v", recovered)
		}
	}()
	middleware = builder.NewRouterMiddleware().Middleware
	return middleware, nil
}

func validateRuntimeConfig(config RuntimeConfig, registrations []HandlerRegistration) error {
	if config.CloseTimeout <= 0 || config.CloseTimeout > maxRuntimeCloseTimeout || config.CloseTimeout <= config.HandlerDeadline {
		return fmt.Errorf("close timeout must be between 1ns and %s", maxRuntimeCloseTimeout)
	}
	if config.HandlerDeadline <= 0 || config.HandlerDeadline > maxRuntimeHandlerDeadline {
		return fmt.Errorf("handler deadline must be between 1ns and %s", maxRuntimeHandlerDeadline)
	}
	if config.RetryMaxRetries <= 0 || config.RetryMaxRetries > maxRuntimeRetryCount {
		return fmt.Errorf("max retries must be between 1 and %d", maxRuntimeRetryCount)
	}
	if config.RetryInitialInterval <= 0 || config.RetryMaxInterval <= 0 || config.RetryInitialInterval > config.RetryMaxInterval {
		return errors.New("retry intervals must be positive and ordered")
	}
	if config.RetryMaxElapsedTime <= 0 || config.RetryMaxElapsedTime > maxRuntimeRetryElapsed {
		return fmt.Errorf("retry max elapsed time must be between 1ns and %s", maxRuntimeRetryElapsed)
	}
	if config.RetryMaxElapsedTime >= config.HandlerDeadline {
		return errors.New("retry max elapsed time must be less than handler deadline")
	}
	if math.IsNaN(config.RetryMultiplier) || math.IsInf(config.RetryMultiplier, 0) || config.RetryMultiplier < 1 || config.RetryMultiplier > 10 {
		return errors.New("retry multiplier must be between 1 and 10")
	}
	if math.IsNaN(config.RetryRandomizationFactor) || math.IsInf(config.RetryRandomizationFactor, 0) || config.RetryRandomizationFactor < 0 || config.RetryRandomizationFactor > 1 {
		return errors.New("retry randomization factor must be between 0 and 1")
	}
	if config.Logger == nil {
		return errors.New("logger is required")
	}
	if isNilInterface(config.PrometheusRegisterer) {
		return errors.New("prometheus registerer is required")
	}
	if len(registrations) == 0 {
		return errors.New("at least one handler registration is required")
	}

	names := make(map[string]struct{}, len(registrations))
	for _, registration := range registrations {
		if registration.Name == "" || registration.Name != strings.TrimSpace(registration.Name) || len(registration.Name) > 255 {
			return errors.New("handler name must be non-empty, trimmed, and bounded")
		}
		if _, exists := names[registration.Name]; exists {
			return fmt.Errorf("duplicate handler name %q", registration.Name)
		}
		names[registration.Name] = struct{}{}
		if _, err := AggregatesForTopic(registration.Topic); err != nil {
			return err
		}
		if registration.Subscriber == nil {
			return fmt.Errorf("handler %q subscriber is required", registration.Name)
		}
		if registration.Handler == nil {
			return fmt.Errorf("handler %q function is required", registration.Name)
		}
		if registration.Topic != registration.Subscriber.config.Topic {
			return fmt.Errorf("handler %q topic %q does not match subscriber topic %q", registration.Name, registration.Topic, registration.Subscriber.config.Topic)
		}
		if config.HandlerDeadline >= registration.Subscriber.config.AckDeadline {
			return fmt.Errorf("handler %q deadline %s must be less than subscriber acknowledgement deadline %s", registration.Name, config.HandlerDeadline, registration.Subscriber.config.AckDeadline)
		}
		if config.CloseTimeout <= registration.Subscriber.config.AckDeadline+registration.Subscriber.config.RecoveryMargin {
			return fmt.Errorf("handler %q close timeout %s must exceed subscriber acknowledgement deadline and recovery margin", registration.Name, config.CloseTimeout)
		}
	}
	seenSubscribers := make(map[*Subscriber]struct{}, len(registrations))
	for _, registration := range registrations {
		if _, exists := seenSubscribers[registration.Subscriber]; exists {
			return fmt.Errorf("subscriber %p is registered more than once", registration.Subscriber)
		}
		seenSubscribers[registration.Subscriber] = struct{}{}
	}
	return nil
}

// Fatal returns the first Router or Subscriber fatal error. It is never
// closed; clean context cancellation and Stop do not publish errors.
func (r *Runtime) Fatal() <-chan error {
	if r == nil {
		return nil
	}
	return r.fatal
}

func (r *Runtime) publishFatal(err error) {
	if err == nil || errors.Is(err, context.Canceled) {
		return
	}
	r.mu.Lock()
	if r.closeAsked || r.fatalErr != nil {
		r.mu.Unlock()
		return
	}
	r.fatalErr = err
	r.mu.Unlock()
	select {
	case r.fatal <- err:
	default:
	}
	select {
	case r.fatalWake <- struct{}{}:
	default:
	}
}

func isCleanRuntimeContextError(err error, parent context.Context) bool {
	if errors.Is(err, context.Canceled) {
		return true
	}
	return errors.Is(err, context.DeadlineExceeded) && parent != nil && parent.Err() != nil
}

func (r *Runtime) fatalError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fatalErr
}

// Start launches the Router and waits until every handler has subscribed.
// Errors that happen after readiness are delivered through Fatal.
func (r *Runtime) Start(ctx context.Context) error {
	if r == nil || r.router == nil {
		return ErrRuntimeNotConfigured
	}
	if ctx == nil {
		return errors.New("runtime context is nil")
	}
	if err := r.reserveRun(); err != nil {
		return err
	}
	runResult := make(chan error, 1)
	go func() { runResult <- r.run(ctx) }()
	select {
	case <-r.router.Running():
		return nil
	case err := <-runResult:
		return err
	case <-ctx.Done():
		err := <-runResult
		if err != nil && !errors.Is(err, context.Canceled) {
			return errors.Join(ctx.Err(), err)
		}
		return ctx.Err()
	}
}

func (r *Runtime) reserveRun() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closeAsked {
		return ErrRuntimeClosed
	}
	if r.runStarted {
		return ErrRuntimeAlreadyStarted
	}
	r.runStarted = true
	return nil
}

// Stop requests Router shutdown. ctx is validated for lifecycle consistency,
// but RouterConfig.CloseTimeout is only the framework's soft drain alert:
// Stop waits for runtime-owned execution to finish before returning so callers
// may safely tear down the PostgreSQL pool.
func (r *Runtime) Stop(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("runtime context is nil")
	}
	// Do not return on ctx while runtime execution is still draining: callers
	// may close the canonical PostgreSQL pool immediately after Stop returns.
	return r.close()
}

func (r *Runtime) run(ctx context.Context) error {
	defer close(r.runDone)
	// Verify and enroll every canonical subscriber before invoking Router.Run.
	// This keeps Watermill's handlersWg and startup watcher entirely out of the
	// enrollment-failure path.
	for _, subscriber := range r.prepared {
		if err := subscriber.prepare(ctx); err != nil {
			return errors.Join(err, r.shutdown(false))
		}
	}
	r.mu.Lock()
	if r.closeAsked {
		r.mu.Unlock()
		return r.shutdown(false)
	}
	r.mu.Unlock()

	monitorCtx, stopMonitor := context.WithCancel(context.Background())
	var monitorWG sync.WaitGroup
	for _, subscriber := range r.subscribers {
		monitorWG.Add(1)
		go func(subscriber *Subscriber) {
			defer monitorWG.Done()
			select {
			case err := <-subscriber.Fatal():
				if !isCleanRuntimeContextError(err, ctx) {
					r.publishFatal(err)
				}
			case <-monitorCtx.Done():
				// Subscriber.setFatal publishes before canceling its run context.
				// Drain once on cancellation so a Router.Run nil result cannot
				// race away the buffered fatal value.
				select {
				case err := <-subscriber.Fatal():
					if !isCleanRuntimeContextError(err, ctx) {
						r.publishFatal(err)
					}
				default:
				}
			}
		}(subscriber)
	}

	runResult := make(chan error, 1)
	go func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				runResult <- fmt.Errorf("watermill router panic: %v", recovered)
			}
		}()
		runResult <- r.router.Run(ctx)
	}()
	var result error
	select {
	case err := <-runResult:
		if err != nil && !isCleanRuntimeContextError(err, ctx) && !r.closeRequested() {
			r.publishFatal(err)
		}
		if err != nil {
			// Router.Run returns startup errors after RunHandlers has incremented
			// handlersWg for every registration, including handlers it never
			// started. Router.Close would therefore wait its full timeout. The
			// canonical subscribers and execution gate are the resources that need
			// cleanup on this path; do not call Router.Close.
		}
		// A nil Router result can race parent cancellation or an automatic close.
		// Drain runtime-owned execution here as well as in explicit Stop so runDone
		// is always a pool-safe postcondition.
		result = errors.Join(err, r.shutdown(r.router.IsRunning()))
	case <-r.fatalWake:
		result = r.fatalError()
		shutdownErr := r.shutdown(r.router.IsRunning())
		result = errors.Join(result, shutdownErr)
		if routerErr := <-runResult; routerErr != nil {
			result = errors.Join(result, routerErr)
		}
	case <-ctx.Done():
		shutdownErr := r.shutdown(r.router.IsRunning())
		result = <-runResult
		result = errors.Join(result, shutdownErr)
		if result != nil && !isCleanRuntimeContextError(result, ctx) && ctx.Err() == nil && !r.closeRequested() {
			r.publishFatal(result)
		}
	}
	stopMonitor()
	monitorWG.Wait()
	r.mu.Lock()
	closeErr := r.closeErr
	fatalErr := r.fatalErr
	closeAsked := r.closeAsked
	r.mu.Unlock()
	if result == nil && fatalErr == nil && !closeAsked && ctx.Err() == nil {
		result = errors.New("watermill router stopped unexpectedly")
		r.publishFatal(result)
	}
	if result == nil && closeErr != nil {
		result = closeErr
	}
	if result == nil && fatalErr != nil {
		result = fatalErr
	}
	return result
}

func (r *Runtime) closeRequested() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closeAsked
}

func (r *Runtime) close() error {
	r.mu.Lock()
	if r.closeAsked {
		done := r.closeDone
		r.mu.Unlock()
		if done != nil {
			<-done
		}
		r.mu.Lock()
		err := r.closeErr
		r.mu.Unlock()
		return err
	}
	r.closeAsked = true
	r.closeDone = make(chan struct{})
	done := r.closeDone
	started := r.runStarted
	closeRouter := started && r.router != nil && r.router.IsRunning()
	r.mu.Unlock()
	err := r.shutdown(closeRouter)
	if started {
		// Router.Close closes its internal completion channel before Router.Run and
		// this supervisor necessarily finish. Wait for the supervisor as well so
		// Stop is a true lifecycle drain and no runtime goroutine can outlive the
		// PostgreSQL resources that application shutdown closes next.
		<-r.runDone
	}
	r.mu.Lock()
	r.closeErr = err
	close(done)
	r.mu.Unlock()
	return err
}

// shutdown closes the Router (when requested and started) and every canonical
// subscriber exactly once. It deliberately does not wait for runDone: run
// invokes this helper while it is the goroutine responsible for closing
// runDone, while Stop waits for runDone after this helper returns.
func (r *Runtime) shutdown(closeRouter bool) error {
	r.mu.Lock()
	if r.shutdownDone == nil {
		r.shutdownDone = make(chan struct{})
	}
	done := r.shutdownDone
	r.mu.Unlock()
	r.shutdownOnce.Do(func() {
		var errs []error
		r.executions.stopAccepting()
		r.mu.Lock()
		started := r.runStarted
		r.mu.Unlock()
		// Canonical subscribers own the preflight subscription context, so stop
		// them first to close each Router handler's message channel. Router.Close
		// then drains its handler bookkeeping; a still-running user handler can
		// make that call hit its soft timeout, which is reported below while the
		// execution gate continues to enforce the hard safety drain.
		for _, subscriber := range r.subscribers {
			if subscriber == nil {
				continue
			}
			if err := subscriber.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		if closeRouter && started && r.router != nil {
			if err := r.router.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		// Router.Close is an alert/soft bound. A safe component Stop still waits
		// for runtime-owned middleware execution, including Complete, before the
		// caller is allowed to tear down shared PostgreSQL resources.
		r.executions.wait()
		r.mu.Lock()
		r.shutdownErr = errors.Join(errs...)
		r.mu.Unlock()
		close(done)
	})
	<-done
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.shutdownErr
}
