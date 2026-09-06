package http

import (
	"context"
	"errors"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

type semanticAuthorizationCapabilityMetrics struct {
	semanticProjectionMetrics
	plannerErr error
	targets    []semanticquery.SemanticAccessTarget
	fields     [][3]string
}

func (m *semanticAuthorizationCapabilityMetrics) SemanticPlanner(context.Context, string) (*semanticquery.Planner, error) {
	return nil, m.plannerErr
}

func (m *semanticAuthorizationCapabilityMetrics) AuthorizeSemanticTarget(_ context.Context, _ string, target semanticquery.SemanticAccessTarget) error {
	m.targets = append(m.targets, target)
	return nil
}

func (m *semanticAuthorizationCapabilityMetrics) AuthorizeSemanticField(_ context.Context, _ string, dataset, field string) error {
	m.fields = append(m.fields, [3]string{dataset, field})
	return nil
}

func TestSemanticPlannerForRequestFailsClosedWhenContextAuthorityErrors(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales"}
	metrics := &semanticAuthorizationCapabilityMetrics{
		semanticProjectionMetrics: semanticProjectionMetrics{model: model},
		plannerErr:                errors.New("authority unavailable"),
	}
	if _, err := semanticPlannerForRequest(context.Background(), metrics, "sales"); !errors.Is(err, metrics.plannerErr) {
		t.Fatalf("planner error = %v, want authority error", err)
	}
}

func TestAuthorizeSemanticRequestUsesFieldCapabilityForQualifiedPhysicalDimension(t *testing.T) {
	model := &semanticmodel.Model{Name: "sales"}
	metrics := &semanticAuthorizationCapabilityMetrics{semanticProjectionMetrics: semanticProjectionMetrics{model: model}}
	request := semanticquery.Request{
		Dataset:    "orders",
		Dimensions: []semanticquery.Field{{Field: "orders.created_at"}},
	}
	if err := authorizeSemanticRequest(context.Background(), metrics, "sales", request); err != nil {
		t.Fatalf("authorize request: %v", err)
	}
	if len(metrics.targets) != 1 || metrics.targets[0] != (semanticquery.SemanticAccessTarget{Dataset: "orders"}) {
		t.Fatalf("targets = %#v, want dataset only", metrics.targets)
	}
	if len(metrics.fields) != 1 || metrics.fields[0] != [3]string{"orders", "orders.created_at", ""} {
		t.Fatalf("fields = %#v", metrics.fields)
	}
}

func TestSemanticConsumerMetadataFailsClosedForUnknownModel(t *testing.T) {
	metrics := semanticProjectionMetrics{}
	if err := authorizeSemanticTarget(context.Background(), metrics, "missing", semanticquery.SemanticAccessTarget{Dataset: "orders"}); !semanticAuthorizationUnavailable(err) {
		t.Fatalf("error = %v, want unavailable", err)
	}
}
