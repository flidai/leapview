package app

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"
)

type MaintenanceStatus struct {
	Revision  string `json:"revision"`
	Operation string `json:"operation"`
	State     string `json:"state"`
	Drained   bool   `json:"drained"`
}
type maintenanceAdmission struct {
	op                         sync.Mutex
	mu                         sync.Mutex
	revision, operation, state string
	prepareWorkers             func(context.Context) error
	startWorkers               func(context.Context) error
	stopWorkers                func(context.Context) error
	cancelWork                 context.CancelFunc
	lease                      *time.Timer
	expires                    time.Time
	terminal                   bool
	failed                     bool
	stopComplete               bool
	active                     int
	next                       uint64
	streams                    map[uint64]context.CancelFunc
	drained                    chan struct{}
}

func newMaintenanceAdmission(revision string, prepare, start, stop func(context.Context) error) *maintenanceAdmission {
	drained := make(chan struct{})
	close(drained)
	return &maintenanceAdmission{revision: revision, state: "closed", prepareWorkers: prepare, startWorkers: start, stopWorkers: stop, streams: map[uint64]context.CancelFunc{}, drained: drained}
}
func (g *maintenanceAdmission) status() MaintenanceStatus {
	g.mu.Lock()
	defer g.mu.Unlock()
	return MaintenanceStatus{Revision: g.revision, Operation: g.operation, State: g.state, Drained: g.stopComplete}
}
func (g *maintenanceAdmission) prepare(ctx context.Context, operation string) error {
	g.op.Lock()
	defer g.op.Unlock()
	g.mu.Lock()
	state, previous, terminal := g.state, g.operation, g.terminal
	g.mu.Unlock()
	if terminal || (state != "closed" && state != "prepared") || (previous != "" && previous != operation) || operation == "" {
		return errors.New("maintenance process is not available for this operation")
	}
	if g.prepareWorkers == nil || g.startWorkers == nil || g.stopWorkers == nil {
		return errors.New("maintenance worker preparation is unavailable")
	}
	if err := g.prepareWorkers(ctx); err != nil {
		return err
	}
	g.mu.Lock()
	if g.terminal {
		g.mu.Unlock()
		return errors.New("maintenance admission closed during preparation")
	}
	g.operation = operation
	g.state = "prepared"
	g.mu.Unlock()
	return nil
}
func (g *maintenanceAdmission) open(ctx context.Context, operation string, lease time.Duration) error {
	g.op.Lock()
	defer g.op.Unlock()
	if lease <= 0 || lease > 24*time.Hour {
		return errors.New("bounded provisional work lease required")
	}
	g.mu.Lock()
	if g.terminal || g.state != "prepared" || g.operation != operation {
		g.mu.Unlock()
		return errors.New("maintenance operation is not prepared")
	}
	// Preparation is deliberately repeated immediately before work authorization;
	// startup pings are not a perpetual dependency/role health guarantee.
	g.mu.Unlock()
	if err := g.prepareWorkers(ctx); err != nil {
		return err
	}
	workCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	g.mu.Lock()
	if g.terminal {
		g.mu.Unlock()
		cancel()
		return errors.New("maintenance admission closed during preparation")
	}
	g.cancelWork = cancel
	g.state = "opening"
	g.expires = time.Now().Add(lease)
	g.lease = time.AfterFunc(lease, g.expire)
	g.mu.Unlock()
	err := g.startWorkers(workCtx)
	g.mu.Lock()
	if err == nil && (g.state != "opening" || !time.Now().Before(g.expires)) {
		err = errors.New("provisional maintenance admission expired during worker startup")
	}
	if err == nil {
		g.state = "provisional"
		g.mu.Unlock()
		return nil
	}
	g.state = "failed"
	g.failed = true
	if g.lease != nil {
		g.lease.Stop()
	}
	cancel()
	g.mu.Unlock()
	cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cleanupCancel()
	return errors.Join(err, g.stopWorkers(cleanupCtx))
}
func (g *maintenanceAdmission) finalize(operation string) error {
	g.mu.Lock()
	if g.terminal || g.state != "provisional" || g.operation != operation || !time.Now().Before(g.expires) {
		g.mu.Unlock()
		return errors.New("no live provisional maintenance admission to finalize")
	}
	g.lease.Stop()
	g.state = "admitted"
	g.expires = time.Time{}
	g.mu.Unlock()
	return nil
}
func (g *maintenanceAdmission) expire() {
	g.mu.Lock()
	if g.state != "opening" && g.state != "provisional" {
		g.mu.Unlock()
		return
	}
	g.state = "failed"
	g.failed = true
	if g.cancelWork != nil {
		g.cancelWork()
	}
	for _, cancel := range g.streams {
		cancel()
	}
	g.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = g.close(ctx)
}
func (g *maintenanceAdmission) close(ctx context.Context) error {
	// Closing admission and registering an admitted request use the same mutex.
	// No request can enter between observing zero inflight requests and closure.
	g.mu.Lock()
	failed := g.failed
	g.terminal = true
	if g.state == "opening" && g.cancelWork != nil {
		g.cancelWork()
	}
	g.state = "draining"
	if g.lease != nil {
		g.lease.Stop()
	}
	for _, cancel := range g.streams {
		cancel()
	}
	drained := g.drained
	g.mu.Unlock()
	g.op.Lock()
	defer g.op.Unlock()
	var drainErr error
	select {
	case <-drained:
	case <-ctx.Done():
		drainErr = ctx.Err()
	}
	// Stop owns cancellation and drain of existing workers and workload leases.
	// Do not cancel ordinary admitted HTTP writes merely to produce a success.
	stopErr := g.stopWorkers(ctx)
	g.mu.Lock()
	g.stopComplete = drainErr == nil && stopErr == nil
	if g.cancelWork != nil {
		g.cancelWork()
	}
	g.state = "closed"
	if failed || drainErr != nil || stopErr != nil {
		g.state = "failed"
		g.failed = true
	}
	g.mu.Unlock()
	return errors.Join(drainErr, stopErr)
}
func (g *maintenanceAdmission) wrap(next http.Handler, preparedHealth func(context.Context) error) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			next.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/maintenance/readyz" {
			state := g.status().State
			g.mu.Lock()
			terminal := g.terminal
			g.mu.Unlock()
			if terminal || state == "failed" || state == "draining" || preparedHealth(r.Context()) != nil {
				http.Error(w, "not prepared", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusOK)
			return
		}
		g.mu.Lock()
		admitted := g.state == "admitted" || (g.state == "provisional" && time.Now().Before(g.expires))
		if !admitted {
			g.mu.Unlock()
			w.Header().Set("Retry-After", "1")
			http.Error(w, "maintenance", http.StatusServiceUnavailable)
			return
		}
		if g.active == 0 {
			g.drained = make(chan struct{})
		}
		g.active++
		g.next++
		id := g.next
		if r.URL.Path == "/updates" {
			streamCtx, cancel := context.WithCancel(r.Context())
			r = r.WithContext(streamCtx)
			g.streams[id] = cancel
		}
		g.mu.Unlock()
		defer func() {
			g.mu.Lock()
			if cancel := g.streams[id]; cancel != nil {
				cancel()
				delete(g.streams, id)
			}
			g.active--
			if g.active == 0 {
				close(g.drained)
			}
			g.mu.Unlock()
		}()
		next.ServeHTTP(w, r)
	})
}
