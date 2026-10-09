package credential

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrProviderPaused = errors.New("credential-backed provider work is paused")
	ErrProviderBusy   = errors.New("credential-backed provider work has not drained")
)

// ProviderAdmission tracks provider work, never read-only authorization or
// activation status. Startup is closed; only reconciled runtime installation
// may resume work. Canceling a lease is deliberately not proof of its release.
type ProviderAdmission struct {
	mu      sync.Mutex
	open    bool
	changed chan struct{}
	drained chan struct{}
	work    map[*providerLease]struct{}
}

type providerLease struct{ cancel context.CancelFunc }

func NewProviderAdmission() *ProviderAdmission {
	drained := make(chan struct{})
	close(drained)
	return &ProviderAdmission{changed: make(chan struct{}), drained: drained, work: make(map[*providerLease]struct{})}
}

func (gate *ProviderAdmission) Acquire(ctx context.Context) (context.Context, func(), error) {
	if gate == nil || ctx == nil {
		return nil, nil, ErrUnavailable
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.acquire(ctx)
}

func (gate *ProviderAdmission) acquire(ctx context.Context) (context.Context, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if !gate.open && !gate.preparationAllowed(ctx) {
		return nil, nil, ErrProviderPaused
	}
	work, cancel := context.WithCancel(ctx)
	lease := &providerLease{cancel: cancel}
	if len(gate.work) == 0 {
		gate.drained = make(chan struct{})
	}
	gate.work[lease] = struct{}{}
	var once sync.Once
	release := func() {
		once.Do(func() {
			cancel()
			gate.mu.Lock()
			defer gate.mu.Unlock()
			delete(gate.work, lease)
			if len(gate.work) == 0 {
				close(gate.drained)
			}
		})
	}
	return work, release, nil
}

// Wait is for background dispatchers that must not finalize a domain job as
// failed merely because credential maintenance temporarily closed admission.
func (gate *ProviderAdmission) Wait(ctx context.Context) (context.Context, func(), error) {
	if gate == nil || ctx == nil {
		return nil, nil, ErrUnavailable
	}
	for {
		gate.mu.Lock()
		if gate.open {
			work, release, err := gate.acquire(ctx)
			gate.mu.Unlock()
			return work, release, err
		}
		changed := gate.changed
		gate.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-changed:
		}
	}
}

// Pause remains closed even when its deadline expires. Completion requires
// every admitted consumer to acknowledge that its provider work has stopped.
// The lifecycle coordinator serializes Pause/Resume with durable mutations.
func (gate *ProviderAdmission) Pause(ctx context.Context) error {
	if gate == nil || ctx == nil {
		return ErrUnavailable
	}
	gate.mu.Lock()
	gate.open = false
	cancel := make([]context.CancelFunc, 0, len(gate.work))
	for lease := range gate.work {
		cancel = append(cancel, lease.cancel)
	}
	drained := gate.drained
	gate.mu.Unlock()
	for _, stop := range cancel {
		stop()
	}
	select {
	case <-drained:
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (gate *ProviderAdmission) Resume() error {
	if gate == nil {
		return ErrUnavailable
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if gate.open {
		return nil
	}
	if len(gate.work) != 0 {
		return ErrProviderBusy
	}
	gate.open = true
	close(gate.changed)
	gate.changed = make(chan struct{})
	return nil
}

func (gate *ProviderAdmission) Ready() bool {
	if gate == nil {
		return false
	}
	gate.mu.Lock()
	defer gate.mu.Unlock()
	return gate.open
}
