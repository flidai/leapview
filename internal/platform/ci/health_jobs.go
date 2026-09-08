package ci

import "strings"

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
	{"apigen-validation", []string{"APIGen tests"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"go-packages-validation", []string{"Go package tests"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"go-application-validation", []string{"Go application tests"}, []string{"ci.yml", "merge-validation.yml", "nightly.yml"}},
	{"postgres-isolation-validation", []string{"PostgreSQL topology isolation"}, []string{"ci.yml"}},
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

func ExpectedHealthJobs(workflow string) []string {
	var result []string
	for _, lane := range healthLanes {
		for _, owner := range lane.workflows {
			if owner == workflow {
				result = append(result, lane.id)
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
