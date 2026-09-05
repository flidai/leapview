package app

import (
	"context"
	"sync"
)

// closeOwner is the only ownership surface the application lifecycle
// needs from runtimehost. Keeping the adapter on Close prevents runtimehost's
// registry and serving implementation from becoming part of app lifecycle
// orchestration.
type closeOwner interface {
	Close() error
}

// runtimeHostLifecycle makes a runtimehost owner an app Lifecycle. Runtime
// startup is performed while composing the runtime host; the lifecycle only
// owns its shutdown, which lets Application stop workers before closing the
// control-plane resources they use.
type runtimeHostLifecycle struct {
	owner closeOwner
	stop  sync.Once
	err   error
}

// newRuntimeHostLifecycle adapts a runtimehost owner for application lifecycle
// shutdown. The returned lifecycle is safe to share with Application and is a
// no-op when owner is nil.
func newRuntimeHostLifecycle(owner closeOwner) *runtimeHostLifecycle {
	return &runtimeHostLifecycle{owner: owner}
}

// Start is intentionally a no-op: runtimehost is fully constructed before it
// is registered with Application, so no second startup transition is needed.
func (l *runtimeHostLifecycle) Start(context.Context) error {
	return nil
}

// Stop closes the owner at most once and returns the same close result on
// every call. This makes shutdown and startup-failure cleanup safe to race or
// repeat without double-closing runtimehost resources.
func (l *runtimeHostLifecycle) Stop(_ context.Context) error {
	if l == nil {
		return nil
	}
	l.stop.Do(func() {
		if l.owner != nil {
			l.err = l.owner.Close()
		}
	})
	return l.err
}
