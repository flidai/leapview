package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Lifecycle is the narrow process-owned contract retained by Application.
// Capability modules that own workers may implement it without exposing their
// repositories or internal dependency graphs.
type Lifecycle interface {
	Start(context.Context) error
	Stop(context.Context) error
}

type fatalSource interface {
	Fatal() <-chan error
}

type cleanupFunc func(context.Context) error

// Application is the complete process-facing surface. Construction details
// stay behind the private lifecycle owner instead of leaking a service graph.
type Application struct {
	handler   http.Handler
	lifecycle *applicationLifecycleOwner
}

type applicationLifecycleOwner struct {
	maintenance *maintenanceAdmission
	components  []Lifecycle
	cleanup     []cleanupFunc
	*applicationCoordinator
	cleanupOnce sync.Once
	fatal       chan error
}

type applicationState uint8

const (
	applicationIdle applicationState = iota
	applicationStarting
	applicationRunning
	applicationStopping
	applicationStopped
)

func newApplication(handler http.Handler, components []Lifecycle, cleanup ...cleanupFunc) *Application {
	return &Application{
		handler: handler,
		lifecycle: &applicationLifecycleOwner{
			components:             append([]Lifecycle(nil), components...),
			cleanup:                append([]cleanupFunc(nil), cleanup...),
			applicationCoordinator: &applicationCoordinator{},
			fatal:                  make(chan error, 1),
		},
	}
}

func (a *Application) Handler() http.Handler {
	if a == nil || a.handler == nil {
		return http.NotFoundHandler()
	}
	return a.handler
}

func (a *Application) Start(ctx context.Context) error {
	if a == nil || a.lifecycle == nil {
		return errors.New("application is not initialized")
	}
	return a.lifecycle.start(ctx)
}

func (a *Application) Shutdown(ctx context.Context) error {
	if a == nil || a.lifecycle == nil {
		return nil
	}
	return a.lifecycle.shutdown(ctx)
}

func (a *Application) Fatal() <-chan error {
	if a == nil || a.lifecycle == nil {
		return nil
	}
	return a.lifecycle.fatalChannel()
}

func (a *applicationLifecycleOwner) start(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	switch a.state {
	case applicationRunning, applicationStopped:
		err := a.startErr
		a.mu.Unlock()
		return err
	case applicationStarting:
		done := a.done
		a.mu.Unlock()
		if err := waitApplication(ctx, done); err != nil {
			return err
		}
		a.mu.Lock()
		err := a.startErr
		a.mu.Unlock()
		return err
	case applicationStopping:
		done := a.done
		a.mu.Unlock()
		if err := waitApplication(ctx, done); err != nil {
			return err
		}
		// Applications are single-use. Return the terminal startup result rather
		// than attempting to start components again after shutdown.
		a.mu.Lock()
		err := a.startErr
		a.mu.Unlock()
		return err
	default:
		runCtx, cancel := context.WithCancel(ctx)
		a.state = applicationStarting
		a.done = make(chan struct{})
		a.started = make([]bool, len(a.components))
		a.startErr, a.shutdownErr, a.cleanupErr = nil, nil, nil
		a.stopReq, a.stopCtx, a.stopCancel, a.startCancel = false, nil, nil, cancel
		done := a.done
		a.mu.Unlock()
		return a.startComponents(runCtx, done)
	}
}

func (a *applicationLifecycleOwner) shutdown(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	switch a.state {
	case applicationStopped:
		err := a.shutdownErr
		a.mu.Unlock()
		return err
	case applicationStarting:
		a.stopReq = true
		if a.stopCtx == nil {
			a.stopCtx, a.stopCancel = applicationLifecycleContext()
		}
		done, startCancel := a.done, a.startCancel
		stopCtx := a.stopCtx
		a.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		if err := waitApplication(ctx, done); err != nil {
			return err
		}
		_ = stopCtx // startup owns the stop context and records the terminal error
		a.mu.Lock()
		err := a.shutdownErr
		a.mu.Unlock()
		return err
	case applicationStopping:
		done := a.done
		a.mu.Unlock()
		if err := waitApplication(ctx, done); err != nil {
			return err
		}
		a.mu.Lock()
		err := a.shutdownErr
		a.mu.Unlock()
		return err
	default:
		// Shutdown-before-start is terminal and must prevent a later Start.
		a.stopReq = true
		a.state = applicationStopping
		done := make(chan struct{})
		a.done = done
		startCancel := a.startCancel
		indexes := a.startedIndexesLocked()
		for _, index := range indexes {
			a.started[index] = false
		}
		a.mu.Unlock()
		if startCancel != nil {
			startCancel()
		}
		stopCtx, stopCancel := applicationLifecycleContext()
		go a.finishShutdown(stopCtx, stopCancel, done, indexes)
		if err := waitApplication(ctx, done); err != nil {
			return err
		}
		a.mu.Lock()
		err := a.shutdownErr
		a.mu.Unlock()
		return err
	}
}

func (a *applicationLifecycleOwner) runCleanup(ctx context.Context) {
	a.cleanupOnce.Do(func() {
		var errs []error
		for index := len(a.cleanup) - 1; index >= 0; index-- {
			if cleanup := a.cleanup[index]; cleanup != nil {
				errs = append(errs, cleanup(ctx))
			}
		}
		err := errors.Join(errs...)
		a.mu.Lock()
		a.cleanupErr = err
		a.cleanupDone = true
		a.mu.Unlock()
	})
}

func (a *applicationLifecycleOwner) fatalChannel() <-chan error {
	return a.fatal
}

func (a *applicationLifecycleOwner) startComponents(runCtx context.Context, done chan struct{}) error {
	for index, component := range a.components {
		a.mu.Lock()
		stopReq, stopCtx := a.stopReq, a.stopCtx
		a.mu.Unlock()
		if !stopReq {
			if err := runCtx.Err(); err != nil {
				return a.abortStartup(done, index, err)
			}
		}
		if stopReq {
			if stopCtx == nil {
				stopCtx = context.Background()
			}
			return a.finishStartupShutdown(done, stopCtx, context.Canceled)
		}
		if component == nil {
			continue
		}
		if err := component.Start(runCtx); err != nil {
			a.mu.Lock()
			cancel := a.startCancel
			a.state = applicationStopping
			indexes := a.startedIndexesLocked()
			for _, started := range indexes {
				a.started[started] = false
			}
			a.mu.Unlock()
			if cancel != nil {
				cancel()
			}
			stopCtx, stopCancel := applicationLifecycleContext()
			stopErr := a.stopComponents(stopCtx, indexes)
			a.runCleanup(stopCtx)
			stopCancel()
			err = errors.Join(fmt.Errorf("start application component %d: %w", index, err), stopErr, a.cleanupError())
			a.mu.Lock()
			a.startErr, a.shutdownErr = err, errors.Join(stopErr, a.cleanupErr)
			a.state, a.startCancel, a.stopCancel = applicationStopped, nil, nil
			close(done)
			a.mu.Unlock()
			return err
		}
		a.mu.Lock()
		a.started[index] = true
		shutdownRequested := a.stopReq
		a.mu.Unlock()
		a.forwardFatal(runCtx, component)
		if shutdownRequested {
			a.mu.Lock()
			stopCtx = a.stopCtx
			a.mu.Unlock()
			if stopCtx == nil {
				stopCtx, _ = applicationLifecycleContext()
			}
			return a.finishStartupShutdown(done, stopCtx, context.Canceled)
		}
		if err := runCtx.Err(); err != nil {
			return a.abortStartup(done, index, err)
		}
	}
	a.mu.Lock()
	stopRequested := a.stopReq
	stopCtx := a.stopCtx
	startupCanceled := runCtx.Err()
	if stopRequested {
		a.mu.Unlock()
		if stopCtx == nil {
			stopCtx = context.Background()
		}
		return a.finishStartupShutdown(done, stopCtx, context.Canceled)
	}
	if startupCanceled != nil {
		a.mu.Unlock()
		return a.abortStartup(done, len(a.components), startupCanceled)
	}
	a.state = applicationRunning
	a.mu.Unlock()
	close(done)
	return nil
}

func (a *applicationLifecycleOwner) finishStartupShutdown(done chan struct{}, stopCtx context.Context, cause error) error {
	a.mu.Lock()
	indexes := a.startedIndexesLocked()
	for _, started := range indexes {
		a.started[started] = false
	}
	stopCleanupCancel := a.stopCancel
	a.state = applicationStopping
	a.mu.Unlock()
	stopErr := a.stopComponents(stopCtx, indexes)
	a.runCleanup(stopCtx)
	if stopCleanupCancel != nil {
		stopCleanupCancel()
	}
	shutdownErr := errors.Join(stopErr, a.cleanupError())
	err := errors.Join(cause, shutdownErr)
	a.mu.Lock()
	a.shutdownErr = shutdownErr
	a.startErr = err
	a.state, a.startCancel, a.stopCancel = applicationStopped, nil, nil
	close(done)
	a.mu.Unlock()
	return err
}

func (a *applicationLifecycleOwner) abortStartup(done chan struct{}, index int, startupErr error) error {
	a.mu.Lock()
	indexes := a.startedIndexesLocked()
	for _, started := range indexes {
		a.started[started] = false
	}
	cancel := a.startCancel
	a.state = applicationStopping
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	stopCtx, stopCancel := applicationLifecycleContext()
	stopErr := a.stopComponents(stopCtx, indexes)
	a.runCleanup(stopCtx)
	stopCancel()
	err := errors.Join(fmt.Errorf("start application component %d: %w", index, startupErr), stopErr, a.cleanupError())
	a.mu.Lock()
	a.startErr, a.shutdownErr = err, errors.Join(stopErr, a.cleanupErr)
	a.state, a.startCancel, a.stopCancel = applicationStopped, nil, nil
	close(done)
	a.mu.Unlock()
	return err
}

func (a *applicationLifecycleOwner) finishShutdown(ctx context.Context, cancel context.CancelFunc, done chan struct{}, indexes []int) {
	stopErr := a.stopComponents(ctx, indexes)
	a.runCleanup(ctx)
	cancel()
	a.mu.Lock()
	a.shutdownErr = errors.Join(stopErr, a.cleanupErr)
	if a.startErr == nil && a.stopReq {
		a.startErr = context.Canceled
	}
	a.state, a.startCancel, a.stopCancel = applicationStopped, nil, nil
	a.mu.Unlock()
	close(done)
}

func (a *applicationLifecycleOwner) cleanupError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.cleanupErr
}

func (a *applicationLifecycleOwner) startedIndexesLocked() []int {
	indexes := make([]int, 0, len(a.started))
	for index, started := range a.started {
		if started {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func (a *applicationLifecycleOwner) stopComponents(ctx context.Context, indexes []int) error {
	var errs []error
	for i := len(indexes) - 1; i >= 0; i-- {
		if component := a.components[indexes[i]]; component != nil {
			errs = append(errs, component.Stop(ctx))
		}
	}
	return errors.Join(errs...)
}

func waitApplication(ctx context.Context, done <-chan struct{}) error {
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func applicationLifecycleContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), time.Minute)
}

func (a *applicationLifecycleOwner) forwardFatal(ctx context.Context, component Lifecycle) {
	source, ok := component.(fatalSource)
	if !ok || source.Fatal() == nil {
		return
	}
	go func() {
		select {
		case <-ctx.Done():
		case err, open := <-source.Fatal():
			if !open || err == nil {
				return
			}
			select {
			case a.fatal <- err:
			default:
			}
		}
	}()
}
