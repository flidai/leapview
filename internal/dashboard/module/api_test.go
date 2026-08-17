package module

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestServingSnapshotIsResolvedByDashboardModule(t *testing.T) {
	module := &Module{snapshot: func(_ context.Context) (string, error) {
		return "state-current", nil
	}}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/workspaces/sales/semantic-models/orders/query", nil)
	request.Header.Set("X-Serving-Snapshot", "state-attacker-controlled")

	module.setServingSnapshot(request, "sales")

	if got := request.Header.Get("X-Serving-Snapshot"); got != "state-current" {
		t.Fatalf("serving snapshot = %q, want module-owned state-current", got)
	}
}

func TestBuildConstructsOwnedSemanticAPIHandler(t *testing.T) {
	module, err := Build(t.Context(), Config{Semantic: SemanticConfig{
		CurrentPrincipalID: func(*http.Request) string { return "principal-1" },
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := module.SemanticAPI().CurrentPrincipalID(httptest.NewRequest(http.MethodGet, "/", nil)); got != "principal-1" {
		t.Fatalf("principal = %q", got)
	}
}
