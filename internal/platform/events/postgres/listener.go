package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	defaultListenerMinBackoff = 100 * time.Millisecond
	defaultListenerMaxBackoff = 5 * time.Second
)

// ListenerPool supplies one dedicated PostgreSQL connection while LISTEN is
// active. A separately budgeted pool is recommended so event wakeups cannot
// consume the interactive control-plane connection budget.
type ListenerPool interface {
	Acquire(context.Context) (*pgxpool.Conn, error)
}

// ReconcileFunc reads durable authority. hint is empty immediately after each
// successful LISTEN setup and otherwise contains an opaque event identity.
// Implementations must remain correct when hints are missed or duplicated.
type ReconcileFunc func(ctx context.Context, hint string) error

// ListenerOptions configures bounded reconnect behavior. OnError is
// observability only; returning from it does not stop reconciliation.
type ListenerOptions struct {
	MinBackoff time.Duration
	MaxBackoff time.Duration
	OnError    func(error)
}

// Listener owns no durable cursor. It establishes LISTEN, reconciles durable
// state, and then uses commit-time notifications only to reduce wake latency.
type Listener struct {
	pool       ListenerPool
	minBackoff time.Duration
	maxBackoff time.Duration
	onError    func(error)
}

// NewListener constructs a reconnecting durable-state listener.
func NewListener(pool ListenerPool, options ListenerOptions) (*Listener, error) {
	if pool == nil {
		return nil, errors.New("event listener pool is nil")
	}
	minBackoff := options.MinBackoff
	if minBackoff == 0 {
		minBackoff = defaultListenerMinBackoff
	}
	maxBackoff := options.MaxBackoff
	if maxBackoff == 0 {
		maxBackoff = defaultListenerMaxBackoff
	}
	if minBackoff < time.Millisecond || maxBackoff < minBackoff || maxBackoff > time.Minute {
		return nil, errors.New("event listener backoff is invalid")
	}
	return &Listener{pool: pool, minBackoff: minBackoff, maxBackoff: maxBackoff, onError: options.OnError}, nil
}

// Run reconnects until ctx is canceled. Every new connection commits LISTEN
// before performing its initial durable reconciliation, closing PostgreSQL's
// documented setup race without treating the notification stream as a log.
func (l *Listener) Run(ctx context.Context, reconcile ReconcileFunc) error {
	if l == nil || l.pool == nil || reconcile == nil {
		return errors.New("event listener is not configured")
	}
	if ctx == nil {
		return errors.New("event listener context is nil")
	}
	backoff := l.minBackoff
	for {
		if ctx.Err() != nil {
			return nil
		}
		err := l.listen(ctx, reconcile)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && l.onError != nil {
			l.onError(err)
		}
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
		if backoff < l.maxBackoff {
			backoff *= 2
			if backoff > l.maxBackoff {
				backoff = l.maxBackoff
			}
		}
	}
}

func (l *Listener) listen(ctx context.Context, reconcile ReconcileFunc) error {
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire event listener connection: %w", err)
	}
	defer releaseListenerConnection(conn)
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin event LISTEN setup: %w", err)
	}
	// sqlc-exception:listen-protocol. LISTEN is PostgreSQL session control
	// syntax; it cannot be represented as a parameterized data query.
	if _, err := tx.Exec(ctx, `LISTEN `+NotificationChannel); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("establish event LISTEN: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit event LISTEN setup: %w", err)
	}
	if err := reconcile(ctx, ""); err != nil {
		return fmt.Errorf("initial durable event reconciliation: %w", err)
	}
	for {
		notification, err := conn.Conn().WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for durable event wake hint: %w", err)
		}
		if notification == nil {
			// pgx currently returns a non-nil notification on a nil error, but
			// keep a defensive guard so a driver/pool implementation cannot
			// turn a malformed wakeup into a panic in the listener loop.
			return errors.New("wait for durable event wake hint: empty notification")
		}
		if notification.Channel != NotificationChannel {
			continue
		}
		if len(notification.Payload) != 36 || validateUUID("event notification", notification.Payload) != nil {
			if l.onError != nil {
				l.onError(errors.New("discarded invalid durable event wake hint"))
			}
			continue
		}
		if err := reconcile(ctx, notification.Payload); err != nil {
			return fmt.Errorf("durable event wake reconciliation: %w", err)
		}
	}
}

func releaseListenerConnection(conn *pgxpool.Conn) {
	if conn == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// sqlc-exception:listen-protocol. UNLISTEN is PostgreSQL session control
	// syntax used only while releasing the dedicated listener connection.
	_, _ = conn.Exec(ctx, `UNLISTEN *`)
	conn.Release()
}
