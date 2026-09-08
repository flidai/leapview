package ci

import "testing"

func TestEvaluateGate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		plan    Jobs
		results map[string]string
		wantErr bool
	}{
		{
			name: "selected jobs pass and unselected jobs skip",
			plan: Jobs{Docs: true, SiteImage: true},
			results: map[string]string{
				"docs":             "success",
				"site-image":       "success",
				"prepare":          "skipped",
				"production-image": "skipped",
			},
		},
		{
			name: "selected failure fails",
			plan: Jobs{Docs: true},
			results: map[string]string{
				"docs": "failure",
			},
			wantErr: true,
		},
		{
			name: "selected skip fails",
			plan: Jobs{ProductionImage: true},
			results: map[string]string{
				"production-image": "skipped",
			},
			wantErr: true,
		},
		{
			name: "unselected success fails",
			plan: Jobs{},
			results: map[string]string{
				"production-image": "success",
			},
			wantErr: true,
		},
		{
			name: "cancellation fails",
			plan: Jobs{Docs: true},
			results: map[string]string{
				"docs": "cancelled",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := EvaluateGate(tt.plan, tt.results)
			if (err != nil) != tt.wantErr {
				t.Fatalf("EvaluateGate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestEvaluatePlanGateIdentifiesAuditMiss(t *testing.T) {
	t.Parallel()

	plan := Plan{
		Audit:     true,
		Nominal:   Jobs{Docs: true},
		Effective: FullJobs(),
	}
	results := successfulResults(FullJobs())
	results["production-image"] = "failure"

	report := EvaluatePlanGate(plan, results)
	if report.OK {
		t.Fatal("audit failure unexpectedly passed")
	}
	if len(report.AuditMisses) != 1 || report.AuditMisses[0] != "production-image" {
		t.Fatalf("audit misses = %v, want production-image", report.AuditMisses)
	}
}

func successfulResults(jobs Jobs) map[string]string {
	results := map[string]string{}
	for job, selected := range jobs.Selected() {
		if selected {
			results[job] = "success"
		} else {
			results[job] = "skipped"
		}
	}
	return results
}

func TestPRGateRequiresPlannerAndSelectedLanes(t *testing.T) {
	p := PlanChanges(Input{Event: "pull_request", PullRequestNumber: 1}, []Change{{Status: "M", Paths: []string{"README.md"}}})
	if p.PR == nil {
		t.Fatal("missing current PR schema")
	}
	results := map[string]string{"prepare": "success"}
	for name, on := range p.PR.Effective.Selected() {
		results[name] = "skipped"
		if on {
			results[name] = "success"
		}
	}
	if !EvaluatePlanGate(p, results).OK {
		t.Fatal("intentional skips rejected")
	}
	results["prepare"] = "failure"
	if EvaluatePlanGate(p, results).OK {
		t.Fatal("planner failure accepted")
	}
	results["prepare"] = "success"
	results["docs-validation"] = "skipped"
	if EvaluatePlanGate(p, results).OK {
		t.Fatal("selected skip accepted")
	}
}
