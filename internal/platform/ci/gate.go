package ci

import (
	"fmt"
	"sort"
	"strings"
)

type GateReport struct {
	OK          bool     `json:"ok"`
	Problems    []string `json:"problems,omitempty"`
	AuditMisses []string `json:"audit_misses,omitempty"`
}

func EvaluateGate(plan Jobs, results map[string]string) error {
	report := evaluateJobs(plan, results)
	if report.OK {
		return nil
	}
	return fmt.Errorf("CI gate rejected workflow:\n- %s", strings.Join(report.Problems, "\n- "))
}

func EvaluatePlanGate(plan Plan, results map[string]string) GateReport {
	if plan.Version == PRPlanVersion || plan.PR != nil {
		return evaluatePRGate(plan, results)
	}
	if plan.Version != 0 && plan.Version != PlanVersion {
		return GateReport{Problems: []string{"unsupported plan schema"}}
	}
	report := evaluateJobs(plan.Effective, results)
	if !plan.Audit {
		return report
	}
	nominal := plan.Nominal.Selected()
	for job, selected := range plan.Effective.Selected() {
		if selected && !nominal[job] && results[job] != "success" {
			report.AuditMisses = append(report.AuditMisses, job)
		}
	}
	sort.Strings(report.AuditMisses)
	return report
}

func evaluateJobs(plan Jobs, results map[string]string) GateReport {
	selected := plan.Selected()
	var problems []string
	for job, expected := range selected {
		result, present := results[job]
		if expected {
			if !present {
				problems = append(problems, fmt.Sprintf("%s has no result", job))
				continue
			}
			if result != "success" {
				problems = append(problems, fmt.Sprintf("%s was selected but concluded %s", job, result))
			}
			continue
		}
		if present && result != "skipped" {
			problems = append(problems, fmt.Sprintf("%s was not selected but concluded %s", job, result))
		}
	}
	if len(problems) == 0 {
		return GateReport{OK: true}
	}
	sort.Strings(problems)
	return GateReport{Problems: problems}
}
