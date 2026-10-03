package ci

import "strings"

const (
	hostRecoveryHealthLane    = "host-recovery-validation"
	hostRecoveryRecoveryJob   = hostRecoveryHealthLane + "/recovery"
	hostRecoveryHistoricalJob = hostRecoveryHealthLane + "/historical-transition"
)

// Health lane names are observational identifiers, not workflow selection rules.
// Keep aliases and exhaustive workflow membership together. A changed workflow
// inventory is incomplete evidence until this registry is updated.
type healthLane struct {
	id        string
	names     []string
	workflows []string
}

var healthLanes = []healthLane{
	{"frontend-validation", []string{"Frontend tests (PR, not-selected)", "Frontend tests (PR, ${{ matrix.shard }})"}, nil},
	{"prepare", []string{"Plan PR validation"}, []string{"ci.yml"}},
	{"docs-validation", []string{"Documentation and public site (PR)"}, []string{"ci.yml"}},
	{"quality-validation", []string{"Cross-language quality (PR)", "Cross-language quality"}, []string{"ci.yml"}},
	{"apigen-validation", []string{"APIGen tests"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"go-packages-validation", []string{"Go package tests"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"go-application-validation", []string{"Go application tests"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"postgres-isolation-validation", []string{"PostgreSQL topology isolation"}, []string{"ci.yml"}},
	{hostRecoveryHealthLane, []string{hostRecoveryHealthLane, "Isolated host recovery and migration boundary contracts"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"spatial-tile-benchmarks", []string{"Spatial tile benchmarks"}, []string{"ci.yml"}},
	{"warehouse-validation", []string{"Warehouse physical contract"}, []string{"ci.yml"}},
	{"full-validation", []string{"Full merge validation", "Full nightly validation"}, []string{"merge-validation.yml", "nightly.yml"}},
	{"security-validation", []string{"Nightly dependency security"}, []string{"nightly.yml"}},
	{"dependency-evidence-refresh", []string{"JavaScript dependency evidence refresh"}, []string{"nightly.yml"}},
	{"ci-gate", []string{"CI gate"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"agent-tool-evaluation", []string{"Agent all-tools evaluation (diagnostic)"}, nil},
	{"legacy-pr-validation", []string{"GitHub-hosted PR validation"}, nil},
	{"prepare", []string{"Prepare generated assets"}, nil},
	{"frontend-prepare", []string{"Prepare frontend assets"}, nil},
	{"docs", []string{"Documentation and public site"}, nil},
	{"go-analysis", []string{"Go static and race analysis"}, nil},
	{"ui-route-qa", []string{"UI route QA"}, nil},
	{"node-audit", []string{"JavaScript dependency audit"}, nil},
	{"go-vuln", []string{"Go vulnerability scan"}, nil},
	{"production-image", []string{"Production image", "Production image (external pull request)"}, nil},
	{"site-image", []string{"Public site image", "Public site image (external pull request)"}, nil},
	{"deployment-contracts", []string{"Deployment contracts"}, nil},
}

// HealthJobName preserves matrix members; unknown display names stay visible.
func HealthJobName(name string) string {
	if lane, ok := reusableQualificationJob(name); ok {
		return lane
	}
	for _, lane := range healthLanes {
		for _, alias := range lane.names {
			if name == alias {
				return lane.id
			}
			for _, tier := range []string{"PR", "merge queue", "nightly"} {
				if name == alias+" ("+tier+")" {
					return lane.id
				}
			}
		}
	}
	for _, tier := range []string{"PR", "merge queue", "nightly"} {
		for _, shard := range frontendShards {
			if name == "Frontend tests ("+tier+", "+shard+")" {
				return "frontend-validation/" + shard
			}
		}
	}
	if strings.HasPrefix(name, "Go tests (") && strings.HasSuffix(name, ")") {
		return "go-tests/" + strings.TrimSuffix(strings.TrimPrefix(name, "Go tests ("), ")")
	}
	if strings.HasPrefix(name, "Frontend tests (") && strings.HasSuffix(name, ")") {
		return "frontend-tests/" + strings.TrimSuffix(strings.TrimPrefix(name, "Frontend tests ("), ")")
	}
	return "unknown/" + name
}

// Reusable workflows expose child jobs with a caller/workflow prefix. The
// recovery and historical-transition children jointly implement the single
// mandatory host-recovery contract in their caller workflow. Keep their IDs
// distinct so the health aggregator can report an incomplete run when either
// child is absent, then separately synthesize the parent result from both.
func reusableQualificationJob(name string) (string, bool) {
	parts := strings.Split(name, " / ")
	if len(parts) < 2 {
		return "", false
	}
	for index := 1; index < len(parts); index++ {
		parent := strings.Join(parts[:index], " / ")
		switch parent {
		case "host-recovery-validation",
			"Isolated host recovery and migration boundary contracts",
			"Qualification / Host recovery and migration",
			"qualify-historical-transition",
			"Qualify schema-32 predecessor transition against exact image":
		default:
			continue
		}
		child := strings.Join(parts[index:], " / ")
		switch child {
		case "recovery", "Isolated host recovery and migration boundary contracts":
			return hostRecoveryRecoveryJob, true
		case "historical-transition", "Schema-32 legacy access transition":
			return hostRecoveryHistoricalJob, true
		}
		if lane, ok := reusableQualificationJob(child); ok {
			return lane, true
		}
	}
	return "", false
}

func ExpectedHealthJobs(workflow string) []string {
	return expectedHealthJobs(workflow, true)
}

// HistoricalExpectedHealthJobs returns the workflow inventory before the
// standalone quality and isolated host recovery lanes. It recognizes older
// runs that predate either required contract.
func HistoricalExpectedHealthJobs(workflow string) []string {
	return expectedHealthJobs(workflow, false)
}

func expectedHealthJobs(workflow string, current bool) []string {
	var result []string
	for _, lane := range healthLanes {
		if !current && (lane.id == "quality-validation" || lane.id == "host-recovery-validation") {
			continue
		}
		for _, owner := range lane.workflows {
			if owner == workflow {
				result = append(result, expectedHealthLaneJobs(lane.id)...)
			}
		}
	}
	if len(result) > 0 {
		for _, shard := range frontendShards {
			result = append(result, "frontend-validation/"+shard)
		}
	}
	return result
}

func expectedHealthLaneJobs(laneID string) []string {
	if laneID == hostRecoveryHealthLane {
		return []string{hostRecoveryHealthLane, hostRecoveryRecoveryJob, hostRecoveryHistoricalJob}
	}
	return []string{laneID}
}
