package agent

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	agentcore "github.com/flidai/leapview/pkg/agent"
)

type usageFixture struct {
	mu   sync.Mutex
	used int64
	err  error
}

func (s *usageFixture) ModelRequestUsage(context.Context) (ModelRequestUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return ModelRequestUsage{Used: s.used}, s.err
}
func (s *usageFixture) ReserveModelRequest(_ context.Context, limit int64) (ModelRequestUsage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return ModelRequestUsage{}, s.err
	}
	if s.used >= limit {
		return ModelRequestUsage{Used: s.used}, ErrModelRequestLimit
	}
	s.used++
	return ModelRequestUsage{Used: s.used}, nil
}

func TestLimitedModelCountsEveryPurposeAndFailedProviderAttempt(t *testing.T) {
	store := &usageFixture{}
	var calls atomic.Int64
	providerErr := errors.New("provider failed")
	model := &limitedModel{store: store, limit: func(context.Context) (int64, error) { return 3, nil }, inner: agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		calls.Add(1)
		return agentcore.ModelResponse{}, providerErr
	})}
	for _, purpose := range []agentcore.ModelRequestPurpose{agentcore.ModelRequestPurposeTurn, agentcore.ModelRequestPurposeCompaction, "title_generation"} {
		if _, err := model.Complete(t.Context(), agentcore.ModelRequest{Purpose: purpose}, nil); !errors.Is(err, providerErr) {
			t.Fatalf("%s: %v", purpose, err)
		}
	}
	if _, err := model.Complete(t.Context(), agentcore.ModelRequest{}, nil); !errors.Is(err, ErrModelRequestLimit) {
		t.Fatalf("over limit: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	// Raising the limit permits only the difference, without resetting usage.
	model.limit = func(context.Context) (int64, error) { return 4, nil }
	_, _ = model.Complete(t.Context(), agentcore.ModelRequest{}, nil)
	if calls.Load() != 4 || store.used != 4 {
		t.Fatalf("calls=%d usage=%d", calls.Load(), store.used)
	}
	model.limit = func(context.Context) (int64, error) { return 1, nil }
	if _, err := model.Complete(t.Context(), agentcore.ModelRequest{}, nil); !errors.Is(err, ErrModelRequestLimit) {
		t.Fatalf("lower limit: %v", err)
	}
}

func TestLimitedModelConcurrentReservationsAndFailClosed(t *testing.T) {
	var calls atomic.Int64
	store := &usageFixture{}
	model := &limitedModel{store: store, limit: func(context.Context) (int64, error) { return 7, nil }, inner: agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		calls.Add(1)
		return agentcore.ModelResponse{}, nil
	})}
	var wg sync.WaitGroup
	for range 40 {
		wg.Go(func() {
			_, err := model.Complete(t.Context(), agentcore.ModelRequest{}, nil)
			if err != nil && !errors.Is(err, ErrModelRequestLimit) {
				t.Errorf("complete: %v", err)
			}
		})
	}
	wg.Wait()
	if calls.Load() != 7 {
		t.Fatalf("provider calls=%d", calls.Load())
	}
	for _, failure := range []string{"read", "invalid", "store"} {
		t.Run(failure, func(t *testing.T) {
			model.limit = func(context.Context) (int64, error) { return 100, nil }
			store.err = nil
			switch failure {
			case "read":
				model.limit = func(context.Context) (int64, error) { return 0, errors.New("settings unavailable") }
			case "invalid":
				model.limit = func(context.Context) (int64, error) { return 0, nil }
			case "store":
				store.err = errors.New("database unavailable")
			}
			if _, err := model.Complete(t.Context(), agentcore.ModelRequest{}, nil); err == nil {
				t.Fatal("expected failure")
			}
			if calls.Load() != 7 {
				t.Fatal("provider called without reservation")
			}
		})
	}
}

type limitedRepositoryFixture struct {
	Repository
	*usageFixture
}

func TestServiceConfiguresSingleSharedModelLimit(t *testing.T) {
	store := &usageFixture{}
	service := NewService(&limitedRepositoryFixture{usageFixture: store}, Config{}, WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{}, nil
	})))
	for range 2 {
		if err := service.SetDailyRequestLimitProvider(func(context.Context) (int64, error) { return 100, nil }); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.model.Complete(t.Context(), agentcore.ModelRequest{}, nil); err != nil {
		t.Fatal(err)
	}
	if store.used != 1 {
		t.Fatalf("model wrapped more than once: used=%d", store.used)
	}
	unsupported := NewService(nil, Config{}, WithModel(agentcore.ModelFunc(func(context.Context, agentcore.ModelRequest, agentcore.ModelStream) (agentcore.ModelResponse, error) {
		return agentcore.ModelResponse{}, nil
	})))
	if err := unsupported.SetDailyRequestLimitProvider(func(context.Context) (int64, error) { return 100, nil }); err == nil {
		t.Fatal("expected missing persistent store error")
	}
}
