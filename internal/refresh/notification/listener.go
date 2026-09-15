// Package notification bridges committed refresh-run changes from PostgreSQL
// into the process-local page-stream broker. Notifications are hints; the
// database remains the authoritative source of run state.
package notification

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	platformpostgres "github.com/flidai/leapview/internal/platform/postgres"
	"github.com/jackc/pgx/v5"
)

const (
	Channel = "leapview_refresh_changed"
	// StreamID is a process-local invalidation stream. It carries no row data;
	// every page rereads its own authorized project scope before emitting.
	StreamID = "refresh:changed"
)

// Listener holds one PostgreSQL connection per app instance, regardless of
// how many browser streams are open. wake must be nonblocking.
type Listener struct {
	pool   *platformpostgres.Pool
	wake   func(Change)
	mu     sync.Mutex
	cancel context.CancelFunc
	done   chan struct{}
}

// Change identifies the committed scope to reconcile. Resync means the
// listener reconnected and could have missed notifications while detached.
type Change struct {
	ProjectID   string `json:"projectId"`
	Environment string `json:"environment"`
	Resync      bool   `json:"-"`
}

func NewListener(pool *platformpostgres.Pool, wake func(Change)) *Listener {
	return &Listener{pool: pool, wake: wake}
}

func (l *Listener) Start(ctx context.Context) error {
	if l == nil || l.pool == nil || l.wake == nil {
		return errors.New("refresh notification listener is not configured")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done != nil {
		return errors.New("refresh notification listener already started")
	}
	conn, err := l.listen(ctx)
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithCancel(context.Background())
	l.cancel = cancel
	l.done = make(chan struct{})
	go l.run(runCtx, conn, l.done)
	return nil
}

func (l *Listener) Stop(ctx context.Context) error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	cancel, done := l.cancel, l.done
	l.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Listener) listen(ctx context.Context) (*pgx.Conn, error) {
	conn, err := l.pool.ConnectListener(ctx)
	if err != nil {
		return nil, err
	}
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		_ = conn.Close(context.Background())
		return nil, err
	}
	return conn, nil
}

func (l *Listener) run(ctx context.Context, conn *pgx.Conn, done chan struct{}) {
	defer close(done)
	for {
		// Reconcile open pages after every (re)subscription. NOTIFY is not
		// durable across a disconnected listener, while refresh.run is.
		l.wake(Change{Resync: true})
		for {
			notification, err := conn.WaitForNotification(ctx)
			if err != nil {
				break
			}
			var change Change
			if err := json.Unmarshal([]byte(notification.Payload), &change); err != nil || change.ProjectID == "" || change.Environment == "" {
				slog.Warn("ignoring malformed refresh notification")
				continue
			}
			l.wake(change)
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = conn.Close(cleanupCtx)
		cancel()
		if ctx.Err() != nil {
			return
		}
		slog.Warn("refresh notification connection lost; reconnecting")
		for ctx.Err() == nil {
			retryCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			var err error
			conn, err = l.listen(retryCtx)
			cancel()
			if err == nil {
				break
			}
			slog.Warn("refresh notification reconnect failed", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Second):
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}
