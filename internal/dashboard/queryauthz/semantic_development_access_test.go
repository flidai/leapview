package authz

import (
	"context"
	"testing"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
)

// Resource-read admission for a trusted development request must not replace
// the independent semantic consumer authority check used by API/MCP queries.
func TestSemanticDevelopmentResourceReadDoesNotAuthorizeProtectedModels(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *Metrics, *semanticmodel.Model)
		allow   bool
	}{
		{name: "protected requires real authority"},
		{name: "public compiled model", allow: true, prepare: func(t *testing.T, m *Metrics, model *semanticmodel.Model) {
			model.AccessPolicy = semanticmodel.SemanticAccessPolicy{}
			planner, err := semanticquery.NewCompiledPlanner(model)
			if err != nil {
				t.Fatal(err)
			}
			underlying := m.Metrics.(semanticDiscoveryMetrics)
			underlying.planner = planner
			m.Metrics = underlying
		}},
		{name: "unknown protection from stripped policy", prepare: func(_ *testing.T, _ *Metrics, model *semanticmodel.Model) {
			model.AccessPolicy = semanticmodel.SemanticAccessPolicy{}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			m, model, _ := semanticDiscoveryFixture(t)
			if test.prepare != nil {
				test.prepare(t, &m, model)
			}
			m.principalFromContext = func(context.Context) (Principal, bool) {
				return Principal{ID: "alice", DevBypass: true}, true
			}
			for attempt := 0; attempt < 10; attempt++ {
				consumer, err := m.SemanticConsumer(t.Context(), "sales")
				if (err == nil) != test.allow {
					t.Fatalf("attempt %d: consumer error = %v, want allow %v", attempt, err, test.allow)
				}
				if err == nil {
					if err := consumer.Authorize(semanticquery.SemanticAccessTarget{Dataset: "orders"}); err != nil {
						t.Fatalf("public dataset authorization: %v", err)
					}
				}
				if _, err := m.BindSemanticConsumer(t.Context(), "sales"); (err == nil) != test.allow {
					t.Fatalf("attempt %d: binding error = %v, want allow %v", attempt, err, test.allow)
				}
			}
		})
	}
}
