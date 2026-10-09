package duckdb

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	analyticsresource "github.com/flidai/leapview/internal/analytics/resource"
)

type providerTestGate struct {
	closed   bool
	released bool
}

func (g *providerTestGate) Acquire(ctx context.Context) (context.Context, func(), error) {
	if g.closed {
		return nil, nil, errors.New("paused")
	}
	return ctx, func() { g.released = true }, nil
}

type providerTestSessions struct {
	open func(context.Context) (analyticsresource.Session, error)
}

func (s providerTestSessions) Session(ctx context.Context) (analyticsresource.Session, error) {
	return s.open(ctx)
}
func TestSourceProviderAdmissionPrecedesSessionAndAcknowledgesFailure(t *testing.T) {
	gate := &providerTestGate{closed: true}
	runtime := NewSourceRuntime(providerTestSessions{open: func(context.Context) (analyticsresource.Session, error) {
		t.Fatal("opened while paused")
		return nil, nil
	}})
	runtime.providerAdmission = gate
	if _, err := runtime.Prepare(t.Context(), &semanticmodel.Model{}); err == nil {
		t.Fatal("paused source work admitted")
	}
	gate.closed = false
	runtime.db = providerTestSessions{open: func(context.Context) (analyticsresource.Session, error) {
		if gate.released {
			t.Fatal("released before work")
		}
		return nil, errors.New("unavailable")
	}}
	if _, err := runtime.Prepare(t.Context(), &semanticmodel.Model{}); err == nil || !gate.released {
		t.Fatal("early failure did not release safe work")
	}
}
func TestSourceProviderPanicCannotAcknowledgeDrain(t *testing.T) {
	gate := &providerTestGate{}
	runtime := NewSourceRuntime(providerTestSessions{open: func(context.Context) (analyticsresource.Session, error) { panic("provider panic") }})
	runtime.providerAdmission = gate
	defer func() {
		if recover() == nil {
			t.Fatal("expected panic")
		}
		if gate.released {
			t.Fatal("uncertain source work acknowledged")
		}
	}()
	_, _ = runtime.Prepare(t.Context(), &semanticmodel.Model{})
}
