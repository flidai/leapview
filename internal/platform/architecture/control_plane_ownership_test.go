package architecture

import (
	"strings"
	"testing"
)

// FAI-616 keeps analytics deployment capabilities separate from instance
// control-plane mutation. Behavioral tests exercise rejected authored kinds
// and publication snapshots; this guard makes the production capability split
// explicit so a database-backed reconciler cannot be reintroduced unnoticed.
func TestAnalyticsDeploymentCannotOwnControlPlaneMutation(t *testing.T) {
	root := repoRoot(t)
	discovery := readArchitectureFixture(t, root, "internal/project/compiler/resource_discovery.go")
	for _, declaration := range []string{
		`{directory: "connections", kind: "Connection"}`,
		`{directory: "sources", kind: "Source"}`,
		`{directory: "models", kind: "Model"}`,
		`{directory: "semantic-models", kind: "SemanticModel"}`,
		`{directory: "pipelines", kind: "Pipeline"}`,
		`{directory: "dashboards", kind: "Dashboard"}`,
	} {
		if !strings.Contains(discovery, declaration) {
			t.Errorf("analytics authoring registry is missing %s", declaration)
		}
	}
	if count := strings.Count(discovery, "{directory:"); count != 6 {
		t.Errorf("analytics authoring registry contains %d kinds, want exactly 6", count)
	}

	guard := readArchitectureFixture(t, root, "internal/app/dashboardpublication/activation.go")
	for _, required := range []string{
		"publication and sharing state is control-plane owned",
		"len(publications) != 0",
		"type NativeDashboardPublicationReconciler struct{}",
	} {
		if !strings.Contains(guard, required) {
			t.Errorf("dashboard publication deployment guard is missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"internal/access/module",
		"internal/dashboard/publication/postgres",
		"internal/project/postgres",
		"github.com/jackc/pgx",
		"ReconcileTx(",
		".Begin(ctx)",
	} {
		if strings.Contains(guard, forbidden) {
			t.Errorf("analytics deployment guard retains control-plane mutation capability %q", forbidden)
		}
	}
}
