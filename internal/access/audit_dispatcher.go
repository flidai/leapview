package access

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

type AuditDispatcherConfig struct {
	Store         AuditOutboxDeliveryStore
	PollInterval  time.Duration
	LeaseDuration time.Duration
	BaseRetry     time.Duration
	MaxRetry      time.Duration
	MaxAttempts   int
	OwnerFactory  func() string
	Now           func() time.Time
	Logger        *slog.Logger
}

// AuditDispatcher drains the Access audit outbox. It is deliberately a small
// lifecycle instead of a generic fire-and-forget callback: leases survive a
// crash, retries are bounded, and terminal events remain operator-visible.
type AuditDispatcher struct {
	config AuditDispatcherConfig

	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

func NewAuditDispatcher(config AuditDispatcherConfig) (*AuditDispatcher, error) {
	if config.Store == nil {
		return nil, fmt.Errorf("audit outbox store is required")
	}
	if config.PollInterval <= 0 {
		config.PollInterval = 250 * time.Millisecond
	}
	if config.LeaseDuration <= 0 {
		config.LeaseDuration = 30 * time.Second
	}
	if config.BaseRetry <= 0 {
		config.BaseRetry = time.Second
	}
	if config.MaxRetry <= 0 {
		config.MaxRetry = time.Minute
	}
	if config.MaxRetry < config.BaseRetry {
		return nil, fmt.Errorf("audit retry maximum must not be shorter than its base")
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = 8
	}
	if config.OwnerFactory == nil {
		config.OwnerFactory = func() string { return fmt.Sprintf("audit-%d", time.Now().UnixNano()) }
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Logger == nil {
		config.Logger = slog.Default()
	}
	return &AuditDispatcher{config: config}, nil
}

func (d *AuditDispatcher) Start(ctx context.Context) error {
	if d == nil {
		return fmt.Errorf("audit dispatcher is required")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reapLocked()
	if d.cancel != nil {
		return nil
	}
	owner := d.config.OwnerFactory()
	if owner == "" {
		return fmt.Errorf("audit dispatcher owner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	d.cancel, d.done = cancel, done
	go func() {
		defer close(done)
		d.run(runCtx, owner)
	}()
	return nil
}

func (d *AuditDispatcher) Stop(ctx context.Context) error {
	if d == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	d.mu.Lock()
	cancel, done := d.cancel, d.done
	if cancel == nil {
		d.mu.Unlock()
		return nil
	}
	cancel()
	d.mu.Unlock()
	select {
	case <-done:
		d.mu.Lock()
		if d.done == done {
			d.cancel, d.done = nil, nil
		}
		d.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *AuditDispatcher) run(ctx context.Context, owner string) {
	ticker := time.NewTicker(d.config.PollInterval)
	defer ticker.Stop()
	for {
		delivered, err := d.DispatchOne(ctx, owner)
		if err != nil && ctx.Err() == nil {
			d.config.Logger.WarnContext(ctx, "audit outbox dispatch failed", "failure_code", auditDispatchFailureCode(err))
		}
		if delivered {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// DispatchOne is the deterministic worker seam used by conformance and
// failure-injection tests. It never returns event metadata in an error.
func (d *AuditDispatcher) DispatchOne(ctx context.Context, owner string) (bool, error) {
	if d == nil || d.config.Store == nil {
		return false, fmt.Errorf("audit dispatcher is not configured")
	}
	lease, found, err := d.config.Store.ClaimAuditIntent(ctx, owner, d.config.LeaseDuration)
	if err != nil || !found {
		return false, err
	}
	if err := d.config.Store.CompleteAuditIntent(ctx, lease); err == nil {
		return true, nil
	} else if ctx.Err() != nil {
		// Leave the lease recoverable. A new worker can reclaim it after expiry.
		return false, ctx.Err()
	} else if errors.Is(err, ErrAuditIntentConflict) {
		code := auditDispatchFailureCode(err)
		if quarantineErr := d.config.Store.QuarantineAuditIntent(context.WithoutCancel(ctx), lease, code); quarantineErr != nil {
			return false, quarantineErr
		}
		return true, nil
	} else if lease.AttemptCount >= d.config.MaxAttempts {
		code := auditDispatchFailureCode(err)
		if poisonErr := d.config.Store.PoisonAuditIntent(context.WithoutCancel(ctx), lease, code); poisonErr != nil {
			return false, poisonErr
		}
		return true, nil
	} else {
		delay := d.retryDelay(lease.AttemptCount)
		code := auditDispatchFailureCode(err)
		if retryErr := d.config.Store.RetryAuditIntent(context.WithoutCancel(ctx), lease, d.config.Now().UTC().Add(delay), code); retryErr != nil {
			return false, retryErr
		}
		return true, nil
	}
}

func (d *AuditDispatcher) retryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := d.config.BaseRetry
	for n := 1; n < attempt && delay < d.config.MaxRetry; n++ {
		if delay > d.config.MaxRetry/2 {
			return d.config.MaxRetry
		}
		delay *= 2
	}
	if delay > d.config.MaxRetry {
		return d.config.MaxRetry
	}
	return delay
}

func (d *AuditDispatcher) reapLocked() {
	if d.done == nil {
		return
	}
	select {
	case <-d.done:
		d.cancel, d.done = nil, nil
	default:
	}
}

func auditDispatchFailureCode(err error) string {
	switch {
	case errors.Is(err, ErrAuditIntentConflict):
		return "AUDIT_INTENT_CONFLICT"
	case errors.Is(err, ErrAuditIntentFence):
		return "AUDIT_INTENT_FENCE"
	default:
		return "AUDIT_SINK_UNAVAILABLE"
	}
}
