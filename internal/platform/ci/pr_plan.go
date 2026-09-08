package ci

import (
	"fmt"
	"reflect"
	"slices"
)

var frontendShards = []string{"core", "reports", "chat", "data", "site"}

// PRPlan adapts the existing dependency classifier to the hosted PR contract.
// Legacy Jobs remain in artifacts for historical compatibility; only this typed
// projection is consumed by the current workflow.
type PRPlan struct {
	Nominal   PRJobs `json:"nominal"`
	Effective PRJobs `json:"effective"`
	Base      string `json:"base"`
	Head      string `json:"head"`
	RunID     string `json:"run_id"`
	Attempt   string `json:"attempt"`
	Deferred  bool   `json:"deferred"`
}

type PRJobs struct {
	APIGen        bool     `json:"apigen"`
	GoPackages    bool     `json:"go_packages"`
	GoApplication bool     `json:"go_application"`
	Frontend      []string `json:"frontend"`
	Postgres      bool     `json:"postgres"`
	DBT           bool     `json:"dbt"`
	Spatial       bool     `json:"spatial"`
	Docs          bool     `json:"docs"`
}

func (j PRJobs) Selected() map[string]bool {
	return map[string]bool{
		"apigen-validation": j.APIGen, "go-packages-validation": j.GoPackages,
		"go-application-validation": j.GoApplication, "frontend-validation": len(j.Frontend) > 0,
		"postgres-isolation-validation": j.Postgres, "dbt-warehouse-boundary-validation": j.DBT,
		"spatial-tile-benchmarks": j.Spatial, "docs-validation": j.Docs,
	}
}

func currentPRJobs(j Jobs) PRJobs {
	if reflect.DeepEqual(j, FullJobs()) {
		return FullPRJobs()
	}
	// Any backend selection includes the external-service inventory in the app
	// lane: a package-only test change may add a PostgreSQL harness consumer.
	backend := len(j.GoMatrix) > 0 || j.DeploymentContracts || (j.ProductionImage && len(j.Frontend) == 0)
	result := PRJobs{GoPackages: backend, GoApplication: backend, Postgres: backend, DBT: backend, Spatial: backend, Docs: j.Docs || j.SiteImage, Frontend: append([]string(nil), j.Frontend...)}
	// Browser source is shared across feature boundaries; preserve all frontend
	// consumers until an import graph can safely narrow production changes.
	if j.UIRouteQA || (j.ProductionImage && len(j.Frontend) > 0) {
		result.Frontend = append([]string(nil), frontendShards...)
		result.Docs = true
	}
	if result.Docs {
		result.Frontend = orderedStrings(unionStrings(result.Frontend, []string{"site"}), frontendShards)
	}
	return result
}

func FullPRJobs() PRJobs {
	return PRJobs{APIGen: true, GoPackages: true, GoApplication: true, Frontend: append([]string(nil), frontendShards...), Postgres: true, DBT: true, Spatial: true, Docs: true}
}

func (j PRJobs) ExpectedJobs() []string {
	var names []string
	for name, selected := range j.Selected() {
		if !selected {
			continue
		}
		if name == "frontend-validation" {
			for _, shard := range j.Frontend {
				names = append(names, name+"/"+shard)
			}
		} else {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func ValidatePRPlan(plan Plan) error {
	if plan.Version != PRPlanVersion || plan.PR == nil {
		return fmt.Errorf("unsupported PR plan schema %d", plan.Version)
	}
	for _, jobs := range []PRJobs{plan.PR.Nominal, plan.PR.Effective} {
		seen := map[string]bool{}
		for _, shard := range jobs.Frontend {
			if !slices.Contains(frontendShards, shard) || seen[shard] {
				return fmt.Errorf("invalid frontend shard %q", shard)
			}
			seen[shard] = true
		}
	}
	if plan.PR.Deferred {
		if !reflect.DeepEqual(plan.PR.Effective, PRJobs{}) {
			return fmt.Errorf("deferred plan selects work")
		}
	} else {
		if !reflect.DeepEqual(plan.PR.Nominal, currentPRJobs(plan.Nominal)) || !reflect.DeepEqual(plan.PR.Effective, currentPRJobs(plan.Effective)) {
			return fmt.Errorf("PR projection differs from dependency plan")
		}
		if len(plan.PR.Effective.ExpectedJobs()) == 0 {
			return fmt.Errorf("empty PR plan")
		}
	}
	return nil
}

func evaluatePRGate(plan Plan, results map[string]string) GateReport {
	if err := ValidatePRPlan(plan); err != nil {
		return GateReport{Problems: []string{err.Error()}}
	}
	selected := plan.PR.Effective.Selected()
	selected["prepare"] = true
	report := GateReport{OK: true}
	for job, expected := range selected {
		want := "skipped"
		if expected {
			want = "success"
		}
		if results[job] != want {
			report.Problems = append(report.Problems, fmt.Sprintf("%s: expected %s, got %q", job, want, results[job]))
		}
	}
	for job := range results {
		if _, ok := selected[job]; !ok {
			report.Problems = append(report.Problems, "unexpected result: "+job)
		}
	}
	if plan.Audit {
		for job, on := range plan.PR.Effective.Selected() {
			if on && !plan.PR.Nominal.Selected()[job] && results[job] != "success" {
				report.AuditMisses = append(report.AuditMisses, job)
			}
		}
	}
	slices.Sort(report.Problems)
	slices.Sort(report.AuditMisses)
	report.OK = len(report.Problems) == 0
	return report
}
