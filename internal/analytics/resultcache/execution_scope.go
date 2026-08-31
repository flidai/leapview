package resultcache

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/flidai/leapview/pkg/arrowresult"
)

var ErrExecutionScopeClosed = errors.New("result cache execution scope is closed")

// ExecutionScope owns mutable in-flight work for exactly one analytical
// runtime generation. It deliberately owns no reusable cache entries or
// invalidation state.
type ExecutionScope struct {
	mu           sync.Mutex
	closed       bool
	closeDone    chan struct{}
	owners       sync.WaitGroup
	callers      sync.WaitGroup
	flights      map[string]*executionFlight
	arrowFlights map[string]*arrowExecutionFlight
}

type executionFlight struct {
	done    chan struct{}
	cancel  context.CancelCauseFunc
	waiters int
	shared  bool
	value   any
	err     error
}

type arrowExecutionFlight struct {
	done     chan struct{}
	cancel   context.CancelCauseFunc
	waiters  int
	shared   bool
	complete bool
	value    ArrowFlightValue
	err      error
}

type canceledFlight struct{ err error }

func (e canceledFlight) Error() string { return e.err.Error() }
func (e canceledFlight) Unwrap() error { return e.err }

// OwnerCanceled marks a coalesced execution whose owning context was canceled,
// allowing a still-live waiter in the same execution scope to replace it.
func OwnerCanceled(err error) error { return canceledFlight{err: err} }

func NewExecutionScope() *ExecutionScope {
	return &ExecutionScope{
		closeDone:    make(chan struct{}),
		flights:      map[string]*executionFlight{},
		arrowFlights: map[string]*arrowExecutionFlight{},
	}
}

// Close rejects new joins, cancels every owner, and waits until all owner
// goroutines and their Arrow flight holds have drained.
func (s *ExecutionScope) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	if s.closed {
		done := s.closeDone
		s.mu.Unlock()
		<-done
		return nil
	}
	s.closed = true
	cancels := make([]context.CancelCauseFunc, 0, len(s.flights)+len(s.arrowFlights))
	for _, flight := range s.flights {
		cancels = append(cancels, flight.cancel)
	}
	for _, flight := range s.arrowFlights {
		cancels = append(cancels, flight.cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel(ErrExecutionScopeClosed)
	}
	s.owners.Wait()
	s.callers.Wait()
	close(s.closeDone)
	return nil
}

func (s *ExecutionScope) Coalesce(ctx context.Context, key string, execute func(context.Context) (any, error)) (any, bool, error) {
	if s == nil || execute == nil {
		return nil, false, ErrExecutionScopeClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	if err := s.admitCaller(); err != nil {
		return nil, false, err
	}
	defer s.callers.Done()
	for {
		flight, err := s.joinFlight(ctx, key, execute)
		if err != nil {
			return nil, false, err
		}
		select {
		case <-ctx.Done():
			s.leaveFlight(flight)
			return nil, false, ctx.Err()
		case <-flight.done:
		}
		s.mu.Lock()
		value, flightErr, shared := flight.value, flight.err, flight.shared
		s.mu.Unlock()
		s.leaveFlight(flight)
		if flightErr != nil {
			var canceled canceledFlight
			if ctx.Err() == nil && errors.As(flightErr, &canceled) {
				continue
			}
			return nil, shared, flightErr
		}
		return value, shared, nil
	}
}

func (s *ExecutionScope) joinFlight(ctx context.Context, key string, execute func(context.Context) (any, error)) (*executionFlight, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrExecutionScopeClosed
	}
	if existing := s.flights[key]; existing != nil {
		existing.waiters++
		existing.shared = true
		return existing, nil
	}
	execCtx, cancel := context.WithCancelCause(ctx)
	flight := &executionFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	s.flights[key] = flight
	s.owners.Add(1)
	go func() {
		defer s.owners.Done()
		value, err := execute(execCtx)
		if contextErr := executionContextError(execCtx); contextErr != nil {
			err = contextErr
		}
		s.mu.Lock()
		flight.value, flight.err = value, err
		if s.flights[key] == flight {
			delete(s.flights, key)
		}
		close(flight.done)
		s.mu.Unlock()
		cancel(nil)
	}()
	return flight, nil
}

func (s *ExecutionScope) leaveFlight(flight *executionFlight) {
	if flight == nil {
		return
	}
	s.mu.Lock()
	flight.waiters--
	s.mu.Unlock()
}

// CoalesceArrow runs one Arrow-producing execution and gives every live caller
// an independently retained lease. Canceled callers are removed without
// releasing buffers still needed by other waiters. If the owning request is
// canceled, a live same-generation waiter starts a replacement flight.
func (s *ExecutionScope) CoalesceArrow(ctx context.Context, key string, execute func(context.Context) (ArrowFlightValue, error)) (*ArrowFlightLease, ArrowFlightStatus, error) {
	if s == nil || execute == nil {
		return nil, ArrowFlightStatus{}, ErrExecutionScopeClosed
	}
	if err := ctx.Err(); err != nil {
		return nil, ArrowFlightStatus{}, err
	}
	if err := s.admitCaller(); err != nil {
		return nil, ArrowFlightStatus{}, err
	}
	defer s.callers.Done()
	for {
		flight, owner, err := s.joinArrowFlight(ctx, key, execute)
		if err != nil {
			return nil, ArrowFlightStatus{}, err
		}
		select {
		case <-ctx.Done():
			s.leaveArrowFlight(flight)
			return nil, ArrowFlightStatus{}, ctx.Err()
		case <-flight.done:
		}

		s.mu.Lock()
		flightErr, shared, value := flight.err, flight.shared, flight.value
		s.mu.Unlock()
		if flightErr != nil {
			s.leaveArrowFlight(flight)
			var canceled canceledFlight
			if ctx.Err() == nil && errors.As(flightErr, &canceled) {
				continue
			}
			return nil, ArrowFlightStatus{Owner: owner, Shared: shared}, flightErr
		}
		if value.Data == nil {
			s.leaveArrowFlight(flight)
			return nil, ArrowFlightStatus{Owner: owner, Shared: shared}, fmt.Errorf("coalesced Arrow execution returned no data")
		}
		lease, acquireErr := value.Data.Acquire()
		s.leaveArrowFlight(flight)
		if acquireErr != nil {
			return nil, ArrowFlightStatus{Owner: owner, Shared: shared}, acquireErr
		}
		return &ArrowFlightLease{data: lease, metadata: cloneMetadata(value.Metadata), cached: value.Cached, hitSource: value.HitSource}, ArrowFlightStatus{Owner: owner, Shared: shared}, nil
	}
}

func (s *ExecutionScope) joinArrowFlight(ctx context.Context, key string, execute func(context.Context) (ArrowFlightValue, error)) (*arrowExecutionFlight, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, false, ErrExecutionScopeClosed
	}
	if existing := s.arrowFlights[key]; existing != nil {
		existing.waiters++
		existing.shared = true
		return existing, false, nil
	}
	execCtx, cancel := context.WithCancelCause(ctx)
	flight := &arrowExecutionFlight{done: make(chan struct{}), cancel: cancel, waiters: 1}
	s.arrowFlights[key] = flight
	s.owners.Add(1)
	go func() {
		defer s.owners.Done()
		value, err := execute(execCtx)
		if contextErr := executionContextError(execCtx); contextErr != nil {
			err = contextErr
		}
		s.mu.Lock()
		flight.value, flight.err, flight.complete = value, err, true
		if s.arrowFlights[key] == flight {
			delete(s.arrowFlights, key)
		}
		close(flight.done)
		release := flight.waiters == 0 && flight.value.Data != nil
		if release {
			flight.value.Data = nil
		}
		s.mu.Unlock()
		cancel(nil)
		if release {
			value.Data.Release()
		}
	}()
	return flight, true, nil
}

func (s *ExecutionScope) leaveArrowFlight(flight *arrowExecutionFlight) {
	if flight == nil {
		return
	}
	s.mu.Lock()
	flight.waiters--
	release := flight.waiters == 0 && flight.complete && flight.value.Data != nil
	var data *arrowresult.Lease
	if release {
		data = flight.value.Data
		flight.value.Data = nil
	}
	s.mu.Unlock()
	if data != nil {
		data.Release()
	}
}

func executionContextError(ctx context.Context) error {
	if ctx == nil || ctx.Err() == nil {
		return nil
	}
	cause := context.Cause(ctx)
	if errors.Is(cause, ErrExecutionScopeClosed) {
		return ErrExecutionScopeClosed
	}
	if cause == nil {
		cause = ctx.Err()
	}
	return canceledFlight{err: cause}
}

func (s *ExecutionScope) admitCaller() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrExecutionScopeClosed
	}
	s.callers.Add(1)
	return nil
}
