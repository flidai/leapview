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

	jobs := readArchitectureFixture(t, root, "internal/deployment/module/jobs.go")
	if count := strings.Count(jobs, "if m.jobs.ValidateActivation != nil {"); count != 2 {
		t.Fatalf("deployment activation has %d ownership admission checks, want normal and approval paths", count)
	}
	for _, commit := range []string{
		"row, err := activator.ActivateApprovedPublication",
		"row, err = m.jobs.Coordinator.Activate",
	} {
		commitIndex := strings.Index(jobs, commit)
		if commitIndex < 0 {
			t.Errorf("deployment activation commit call %q is missing", commit)
			continue
		}
		validationIndex := strings.LastIndex(jobs[:commitIndex], "if m.jobs.ValidateActivation != nil {")
		if validationIndex < 0 {
			t.Errorf("deployment activation does not validate publication ownership before %q", commit)
		}
	}

	router := readArchitectureFixture(t, root, "internal/app/runtime_router.go")
	validationStart := strings.Index(router, "config.Jobs.ValidateActivation =")
	if validationStart < 0 {
		t.Fatal("native activation ownership validation wiring is missing")
	}
	validationEnd := strings.Index(router[validationStart:], "apiConfig :=")
	if validationEnd < 0 {
		t.Fatal("native activation ownership validation wiring has no bounded composition segment")
	}
	validation := router[validationStart : validationStart+validationEnd]
	for _, required := range []string{
		"runtime.runtimeHostModule.ProjectID()",
		"runtime.runtimeHostModule.Environment()",
		"runtime.dashboardPublicationReconciler.Reconcile",
	} {
		if !strings.Contains(validation, required) {
			t.Errorf("native activation ownership validation is missing %q", required)
		}
	}
	if strings.Contains(validation, "ProjectID: runtimeConfig.ProjectID") {
		t.Fatal("native activation ownership validation captures the bootstrap-time project ID")
	}

	postgresBuild := readArchitectureFixture(t, root, "internal/app/postgres_build.go")
	for _, required := range []string{
		"ResolveSealedActiveState: resolveSealedActiveState",
		"reconciler.Reconcile(resolveCtx, graph.ServingState, candidate)",
		"claimedProject, found, claimErr := readClaim(resolveCtx)",
	} {
		if !strings.Contains(postgresBuild, required) {
			t.Errorf("sealed startup/reconciliation ownership validation is missing %q", required)
		}
	}
}
