package ci

import (
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"sort"
	"strings"
)

const PlanVersion = 1

type Change struct {
	Status string   `json:"status"`
	Paths  []string `json:"paths"`
}

type Input struct {
	Event             string
	PullRequestNumber int
	Labels            []string
}

type GoShard struct {
	Name     string `json:"name"`
	AppShard string `json:"app_shard"`
}

type Jobs struct {
	Prepare             bool      `json:"prepare"`
	FrontendPrepare     bool      `json:"frontend_prepare"`
	Docs                bool      `json:"docs"`
	GoMatrix            []GoShard `json:"go_matrix"`
	Frontend            []string  `json:"frontend"`
	GoAnalysis          bool      `json:"go_analysis"`
	UIRouteQA           bool      `json:"ui_route_qa"`
	NodeAudit           bool      `json:"node_audit"`
	GoVuln              bool      `json:"go_vuln"`
	SiteImage           bool      `json:"site_image"`
	ProductionImage     bool      `json:"production_image"`
	DeploymentContracts bool      `json:"deployment_contracts"`
}

type Plan struct {
	Version   int      `json:"version"`
	Reason    string   `json:"reason"`
	Audit     bool     `json:"audit"`
	Changes   []Change `json:"changes"`
	Nominal   Jobs     `json:"nominal"`
	Effective Jobs     `json:"effective"`
}

func PlanChanges(input Input, changes []Change) Plan {
	plan := Plan{
		Version: PlanVersion,
		Changes: changes,
	}
	if input.Event != "pull_request" {
		plan.Nominal = FullJobs()
		plan.Effective = FullJobs()
		plan.Reason = input.Event + " event"
		return plan
	}
	if slices.Contains(input.Labels, "ci:full") {
		plan.Nominal = FullJobs()
		plan.Effective = FullJobs()
		plan.Reason = "ci:full label"
		return plan
	}

	jobs, reasons, forceReason := classify(changes)
	if forceReason != "" {
		plan.Nominal = FullJobs()
		plan.Effective = FullJobs()
		plan.Reason = forceReason
		return plan
	}
	normalizeJobs(&jobs)
	plan.Nominal = jobs
	plan.Effective = jobs
	plan.Reason = strings.Join(sortedKeys(reasons), ", ")

	if input.PullRequestNumber > 0 && input.PullRequestNumber%5 == 0 {
		plan.Audit = true
		plan.Effective = FullJobs()
		plan.Reason = "20% deterministic PR audit"
	}
	return plan
}

func FullJobs() Jobs {
	return Jobs{
		Prepare:             true,
		Docs:                true,
		GoMatrix:            allGoShards(),
		Frontend:            []string{"core", "reports", "chat", "data", "site"},
		GoAnalysis:          true,
		UIRouteQA:           true,
		NodeAudit:           true,
		GoVuln:              true,
		SiteImage:           true,
		ProductionImage:     true,
		DeploymentContracts: true,
	}
}

func fullGoJobs() Jobs {
	return Jobs{
		Prepare:         true,
		GoMatrix:        allGoShards(),
		GoAnalysis:      true,
		GoVuln:          true,
		ProductionImage: true,
	}
}

func ParseNameStatusZ(data []byte) ([]Change, error) {
	if len(data) == 0 {
		return nil, nil
	}
	fields := strings.Split(string(data), "\x00")
	if fields[len(fields)-1] != "" {
		return nil, errors.New("git diff name-status output is not NUL terminated")
	}
	fields = fields[:len(fields)-1]

	var changes []Change
	for index := 0; index < len(fields); {
		status := fields[index]
		index++
		pathCount := 1
		if strings.HasPrefix(status, "R") || strings.HasPrefix(status, "C") {
			pathCount = 2
		}
		if status == "" || index+pathCount > len(fields) {
			return nil, fmt.Errorf("malformed git diff name-status record %q", status)
		}
		paths := append([]string(nil), fields[index:index+pathCount]...)
		index += pathCount
		for _, changedPath := range paths {
			if changedPath == "" {
				return nil, fmt.Errorf("empty path in git diff record %q", status)
			}
		}
		changes = append(changes, Change{Status: status, Paths: paths})
	}
	return changes, nil
}

func (p Plan) JSON() ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

func (j Jobs) Selected() map[string]bool {
	return map[string]bool{
		"prepare":              j.Prepare,
		"frontend-prepare":     j.FrontendPrepare,
		"docs":                 j.Docs,
		"go-tests":             len(j.GoMatrix) > 0,
		"frontend-tests":       len(j.Frontend) > 0,
		"go-analysis":          j.GoAnalysis,
		"ui-route-qa":          j.UIRouteQA,
		"node-audit":           j.NodeAudit,
		"go-vuln":              j.GoVuln,
		"site-image":           j.SiteImage,
		"production-image":     j.ProductionImage,
		"deployment-contracts": j.DeploymentContracts,
	}
}

func classify(changes []Change) (Jobs, map[string]struct{}, string) {
	if len(changes) == 0 {
		return Jobs{}, nil, "no changed paths"
	}
	var jobs Jobs
	reasons := map[string]struct{}{}
	for _, change := range changes {
		for _, changedPath := range change.Paths {
			if reason := classifyPath(changedPath, &jobs, reasons); reason != "" {
				return Jobs{}, nil, reason
			}
		}
	}
	return jobs, reasons, ""
}

func classifyPath(changedPath string, jobs *Jobs, reasons map[string]struct{}) string {
	changedPath = strings.TrimPrefix(path.Clean(changedPath), "./")
	if isCrossCutting(changedPath) {
		return "cross-cutting build or contract input"
	}
	if isRootDocumentation(changedPath) {
		jobs.Docs = true
		reasons["repository documentation"] = struct{}{}
		return ""
	}
	if strings.HasPrefix(changedPath, "docs/") || strings.HasPrefix(changedPath, "site/") {
		jobs.Docs = true
		jobs.SiteImage = true
		reasons["published documentation or site"] = struct{}{}
		return ""
	}
	if changedPath == "Dockerfile" {
		jobs.ProductionImage = true
		jobs.DeploymentContracts = true
		reasons["production image"] = struct{}{}
		return ""
	}
	if changedPath == "Dockerfile.site" {
		jobs.SiteImage = true
		reasons["published documentation or site"] = struct{}{}
		return ""
	}
	if strings.HasPrefix(changedPath, "deploy/") {
		if strings.HasSuffix(strings.ToLower(changedPath), ".md") {
			jobs.Docs = true
			reasons["repository documentation"] = struct{}{}
			return ""
		}
		jobs.DeploymentContracts = true
		if strings.HasPrefix(changedPath, "deploy/compose/") || strings.HasPrefix(changedPath, "deploy/host/") {
			jobs.ProductionImage = true
			reasons["production deployment"] = struct{}{}
			return ""
		}
		reasons["deployment"] = struct{}{}
		return ""
	}
	if strings.HasPrefix(changedPath, "dashboards/") ||
		strings.HasPrefix(changedPath, "evaluation/") ||
		strings.HasPrefix(changedPath, "schemas/") {
		jobs.Prepare = true
		jobs.GoMatrix = unionGoShards(jobs.GoMatrix, allGoShards())
		jobs.UIRouteQA = true
		jobs.ProductionImage = true
		reasons["runtime project"] = struct{}{}
		return ""
	}
	if strings.HasPrefix(changedPath, "web/") {
		classifyFrontend(changedPath, jobs)
		if isFrontendTest(changedPath) {
			reasons["frontend tests"] = struct{}{}
		} else {
			jobs.ProductionImage = true
			reasons["frontend"] = struct{}{}
		}
		return ""
	}
	if strings.HasPrefix(changedPath, "static/") {
		classifySharedFrontend(jobs)
		jobs.ProductionImage = true
		reasons["frontend"] = struct{}{}
		return ""
	}
	if strings.HasPrefix(changedPath, "scripts/") {
		if isFrontendTest(changedPath) {
			jobs.FrontendPrepare = true
			jobs.Frontend = unionStrings(jobs.Frontend, []string{"core"})
			reasons["frontend tests"] = struct{}{}
			return ""
		}
		return "cross-cutting build or contract input"
	}
	if isGoPath(changedPath) {
		if strings.HasSuffix(changedPath, "_test.go") {
			jobs.Prepare = true
			if path.Dir(changedPath) == "internal/app" {
				jobs.GoMatrix = unionGoShards(jobs.GoMatrix, appGoShards())
			} else {
				jobs.GoMatrix = unionGoShards(jobs.GoMatrix, []GoShard{{Name: "packages"}})
			}
			reasons["Go tests"] = struct{}{}
		} else {
			mergeJobs(jobs, fullGoJobs())
			reasons["Go/backend"] = struct{}{}
		}
		if strings.HasPrefix(changedPath, "cmd/leapview-site/") ||
			strings.HasPrefix(changedPath, "internal/app/site/") ||
			strings.HasPrefix(changedPath, "internal/app/tools/docsitegen/") {
			jobs.Docs = true
			jobs.SiteImage = true
		}
		return ""
	}
	return "unknown path: " + changedPath
}

func isCrossCutting(changedPath string) bool {
	switch changedPath {
	case "Taskfile.yml", "go.mod", "go.sum", "package.json", "bun.lock", "sqlc.yaml",
		".dockerignore", ".gitignore", ".env.example", "VERSION",
		"tsconfig.json", "tsconfig.app.json", "tsconfig.contracts.json":
		return true
	}
	return strings.HasPrefix(changedPath, ".github/workflows/") ||
		strings.HasPrefix(changedPath, "api/") ||
		strings.HasPrefix(changedPath, "internal/agent/contracts/generate/") ||
		strings.HasPrefix(changedPath, "internal/app/tools/") ||
		strings.HasPrefix(changedPath, "internal/platform/ci/")
}

func isRootDocumentation(changedPath string) bool {
	if strings.Contains(changedPath, "/") {
		return strings.HasPrefix(changedPath, ".github/") &&
			strings.HasSuffix(strings.ToLower(changedPath), ".md")
	}
	lower := strings.ToLower(changedPath)
	return strings.HasSuffix(lower, ".md") || changedPath == "LICENSE" || changedPath == "AGENTS.md"
}

func isGoPath(changedPath string) bool {
	if !strings.HasSuffix(changedPath, ".go") {
		return false
	}
	return strings.HasPrefix(changedPath, "cmd/") ||
		strings.HasPrefix(changedPath, "internal/") ||
		strings.HasPrefix(changedPath, "pkg/")
}

func isFrontendTest(changedPath string) bool {
	return strings.HasSuffix(changedPath, ".test.ts") ||
		strings.HasSuffix(changedPath, ".test.tsx") ||
		strings.HasSuffix(changedPath, ".spec.ts")
}

func classifyFrontend(changedPath string, jobs *Jobs) {
	jobs.FrontendPrepare = true
	jobs.Frontend = unionStrings(jobs.Frontend, []string{"core"})
	switch {
	case strings.HasPrefix(changedPath, "web/components/shared/"),
		strings.Contains(changedPath, "/visualization/"),
		strings.Contains(changedPath, "datastar"):
		classifySharedFrontend(jobs)
	case strings.HasPrefix(changedPath, "web/components/dashboard/"):
		jobs.Frontend = unionStrings(jobs.Frontend, []string{"reports"})
	case strings.HasPrefix(changedPath, "web/components/chat/"):
		jobs.Frontend = unionStrings(jobs.Frontend, []string{"chat"})
	case strings.HasPrefix(changedPath, "web/components/data/"),
		strings.HasPrefix(changedPath, "web/components/admin/"),
		strings.HasPrefix(changedPath, "web/components/login/"),
		strings.HasPrefix(changedPath, "web/components/inspector/"):
		jobs.Frontend = unionStrings(jobs.Frontend, []string{"data"})
	default:
		classifySharedFrontend(jobs)
	}
}

func classifySharedFrontend(jobs *Jobs) {
	jobs.Prepare = true
	jobs.FrontendPrepare = true
	jobs.Frontend = unionStrings(jobs.Frontend, []string{"core", "reports", "chat", "data"})
	jobs.UIRouteQA = true
	jobs.Docs = true
	jobs.SiteImage = true
}

func mergeJobs(target *Jobs, source Jobs) {
	target.Prepare = target.Prepare || source.Prepare
	target.FrontendPrepare = target.FrontendPrepare || source.FrontendPrepare
	target.Docs = target.Docs || source.Docs
	target.GoMatrix = unionGoShards(target.GoMatrix, source.GoMatrix)
	target.Frontend = unionStrings(target.Frontend, source.Frontend)
	target.GoAnalysis = target.GoAnalysis || source.GoAnalysis
	target.UIRouteQA = target.UIRouteQA || source.UIRouteQA
	target.NodeAudit = target.NodeAudit || source.NodeAudit
	target.GoVuln = target.GoVuln || source.GoVuln
	target.SiteImage = target.SiteImage || source.SiteImage
	target.ProductionImage = target.ProductionImage || source.ProductionImage
	target.DeploymentContracts = target.DeploymentContracts || source.DeploymentContracts
}

func normalizeJobs(jobs *Jobs) {
	if jobs.Prepare {
		jobs.FrontendPrepare = false
	}
	jobs.Frontend = orderedStrings(jobs.Frontend, []string{"core", "reports", "chat", "data", "site"})
	jobs.GoMatrix = orderedGoShards(jobs.GoMatrix)
}

func allGoShards() []GoShard {
	return append([]GoShard{{Name: "packages"}}, appGoShards()...)
}

func appGoShards() []GoShard {
	return []GoShard{
		{Name: "app 1/4", AppShard: "0"},
		{Name: "app 2/4", AppShard: "1"},
		{Name: "app 3/4", AppShard: "2"},
		{Name: "app 4/4", AppShard: "3"},
	}
}

func unionStrings(current, additions []string) []string {
	seen := make(map[string]struct{}, len(current)+len(additions))
	for _, value := range append(append([]string(nil), current...), additions...) {
		seen[value] = struct{}{}
	}
	values := make([]string, 0, len(seen))
	for value := range seen {
		values = append(values, value)
	}
	return values
}

func orderedStrings(values, order []string) []string {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		seen[value] = struct{}{}
	}
	var result []string
	for _, value := range order {
		if _, ok := seen[value]; ok {
			result = append(result, value)
			delete(seen, value)
		}
	}
	extra := sortedKeys(seen)
	return append(result, extra...)
}

func unionGoShards(current, additions []GoShard) []GoShard {
	seen := map[string]GoShard{}
	for _, shard := range append(append([]GoShard(nil), current...), additions...) {
		seen[shard.Name] = shard
	}
	result := make([]GoShard, 0, len(seen))
	for _, shard := range seen {
		result = append(result, shard)
	}
	return result
}

func orderedGoShards(values []GoShard) []GoShard {
	seen := map[string]GoShard{}
	for _, value := range values {
		seen[value.Name] = value
	}
	var result []GoShard
	for _, candidate := range allGoShards() {
		if value, ok := seen[candidate.Name]; ok {
			result = append(result, value)
			delete(seen, candidate.Name)
		}
	}
	var names []string
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		result = append(result, seen[name])
	}
	return result
}

func sortedKeys[T any](values map[string]T) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
