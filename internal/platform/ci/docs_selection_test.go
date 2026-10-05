package ci

import (
	"slices"
	"testing"
)

func TestEveryDocumentationSelectionIncludesSiteShard(t *testing.T) {
	// Exercise classifier boundaries and audit/full projections, including a
	// deferred plan whose nominal coverage is retained while execution is empty.
	for _, path := range []string{
		"README.md", "docs/articles/start/installation.md", "site/web/site-page.ts",
		"Dockerfile.site", "Taskfile.yml", "flake.lock", "web/components/shared/datastar-lit.ts",
		"internal/app/site/http/router.go", "unknown-resource.xyz",
	} {
		for _, deferred := range []bool{false, true} {
			t.Run(path, func(t *testing.T) {
				plan := PlanChanges(Input{Event: "pull_request", PullRequestNumber: 1},
					[]Change{{Status: "M", Paths: []string{path}}})
				if deferred {
					plan.PR.Deferred = true
					plan.PR.Effective = PRJobs{}
				}
				for _, jobs := range []PRJobs{plan.PR.Nominal, plan.PR.Effective} {
					if jobs.Docs && !slices.Contains(jobs.Frontend, "site") {
						t.Fatalf("docs selection lacks site coverage: %+v", jobs)
					}
				}
				if err := ValidatePRPlan(plan); err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}
