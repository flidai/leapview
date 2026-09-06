package module

import (
	"context"
	"errors"
	"testing"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	"github.com/flidai/leapview/internal/dashboard/queryruntime"
)

type semanticPortFixture struct {
	queryruntime.Metrics
	err   error
	calls int
}

func (f *semanticPortFixture) AuthorizeSemanticTarget(context.Context, string, semanticquery.SemanticAccessTarget) error {
	f.calls++
	return f.err
}
func (f *semanticPortFixture) AuthorizeSemanticField(context.Context, string, string, string) error {
	f.calls++
	return f.err
}
func (f *semanticPortFixture) AuthorizeSemanticModelProjection(context.Context, string) error {
	f.calls++
	return f.err
}
func (f *semanticPortFixture) SemanticPlanner(context.Context, string) (*semanticquery.Planner, error) {
	f.calls++
	return nil, f.err
}
func (f *semanticPortFixture) SemanticConsumerCacheAllowed(string) bool { f.calls++; return false }

func TestSemanticConsumerPortsSurviveProductionDecorators(t *testing.T) {
	denied := errors.New("denied by compiled policy")
	fixture := &semanticPortFixture{err: denied}
	wrapped := auditedMetrics{Metrics: admittedMetrics{Metrics: fixture}}
	ctx := context.Background()
	if err := wrapped.AuthorizeSemanticTarget(ctx, "model", semanticquery.SemanticAccessTarget{Dataset: "orders"}); !errors.Is(err, denied) {
		t.Fatalf("target admission lost denial: %v", err)
	}
	if err := wrapped.AuthorizeSemanticField(ctx, "model", "orders", "secret"); !errors.Is(err, denied) {
		t.Fatalf("option admission lost denial: %v", err)
	}
	if _, err := wrapped.SemanticPlanner(ctx, "model"); !errors.Is(err, denied) {
		t.Fatalf("planner admission lost denial: %v", err)
	}
	if err := wrapped.AuthorizeSemanticModelProjection(ctx, "model"); !errors.Is(err, denied) {
		t.Fatalf("model projection admission lost denial: %v", err)
	}
	if wrapped.SemanticConsumerCacheAllowed("model") {
		t.Fatal("decorators widened cache admission")
	}
	if fixture.calls != 5 {
		t.Fatalf("forwarded calls = %d, want 5", fixture.calls)
	}
}

func TestSemanticConsumerPortsFailClosedWhenAbsent(t *testing.T) {
	wrapped := auditedMetrics{Metrics: admittedMetrics{}}
	ctx := context.Background()
	if err := wrapped.AuthorizeSemanticTarget(ctx, "model", semanticquery.SemanticAccessTarget{}); err == nil {
		t.Fatal("missing target port allowed")
	}
	if err := wrapped.AuthorizeSemanticField(ctx, "model", "orders", "secret"); err == nil {
		t.Fatal("missing field port allowed")
	}
	if _, err := wrapped.SemanticPlanner(ctx, "model"); err == nil {
		t.Fatal("missing planner port allowed")
	}
	if err := wrapped.AuthorizeSemanticModelProjection(ctx, "model"); err == nil {
		t.Fatal("missing model projection port allowed")
	}
	if wrapped.SemanticConsumerCacheAllowed("model") {
		t.Fatal("missing cache admission port allowed")
	}
}
