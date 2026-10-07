package ciadapter

import (
	"fmt"
	"path"
	"regexp"
	"slices"
	"sort"
	"strings"

	platformci "github.com/flidai/leapview/internal/platform/ci"
	"gopkg.in/yaml.v3"
)

var healthMatrixName = regexp.MustCompile(`\$\{\{\s*matrix\.([A-Za-z0-9_-]+)\s*\}\}`)

// HealthWorkflowContract is a parsed, revision-specific view of the jobs that
// a maintained workflow exposes to GitHub Actions. Names come from that
// workflow source, so historical display-name changes need no alias list.
type HealthWorkflowContract struct {
	RequiredJobs        []string
	PlanIndependentJobs []string
	WorkflowJobs        []string
	JobNames            map[string]string
	ReusableChildren    map[string][]string
}

type healthWorkflowJob struct {
	Name     string `yaml:"name"`
	Uses     string `yaml:"uses"`
	Needs    any    `yaml:"needs"`
	Strategy struct {
		Matrix yaml.Node `yaml:"matrix"`
	} `yaml:"strategy"`
}

type healthWorkflowDocument struct {
	Jobs map[string]healthWorkflowJob `yaml:"jobs"`
}

type compiledHealthJob struct {
	ids      []string
	mapNames map[string]string
}

// LocalHealthWorkflowCalls returns local reusable workflow paths referenced by
// jobs in a maintained health workflow. The caller retrieves these sources at
// the same immutable commit before building the contract.
func LocalHealthWorkflowCalls(contents []byte) ([]string, error) {
	workflow, err := parseHealthWorkflow(contents)
	if err != nil {
		return nil, err
	}
	var calls []string
	for id, job := range workflow.Jobs {
		if !strings.HasPrefix(job.Uses, "./.github/workflows/") {
			continue
		}
		if err := validateLocalWorkflowPath(job.Uses); err != nil {
			return nil, fmt.Errorf("job %s: %w", id, err)
		}
		calls = append(calls, job.Uses)
	}
	slices.Sort(calls)
	return slices.Compact(calls), nil
}

// BuildHealthWorkflow parses the three maintained health workflow contracts.
// It intentionally supports only static matrices and the CI planner's
// frontend shard matrix; other expressions remain unavailable evidence.
func BuildHealthWorkflow(filename string, contents []byte, reusable map[string][]byte, plan platformci.Plan) (HealthWorkflowContract, error) {
	workflow, err := parseHealthWorkflow(contents)
	if err != nil {
		return HealthWorkflowContract{}, err
	}
	gate, ok := workflow.Jobs["ci-gate"]
	if !ok {
		return HealthWorkflowContract{}, fmt.Errorf("%s has no ci-gate job", filename)
	}
	needs, err := healthWorkflowNeeds(gate.Needs)
	if err != nil {
		return HealthWorkflowContract{}, fmt.Errorf("%s ci-gate needs are unavailable: %w", filename, err)
	}
	if len(needs) == 0 {
		return HealthWorkflowContract{}, fmt.Errorf("%s ci-gate needs are empty", filename)
	}

	contract := HealthWorkflowContract{JobNames: map[string]string{}, ReusableChildren: map[string][]string{}}
	compiled := make(map[string]compiledHealthJob, len(workflow.Jobs))
	for id, job := range workflow.Jobs {
		name := job.Name
		if name == "" {
			name = id
		}
		current, err := compileHealthJob(filename, id, name, job.Strategy.Matrix, plan)
		if err != nil {
			return HealthWorkflowContract{}, fmt.Errorf("%s job %s: %w", filename, id, err)
		}
		if job.Uses != "" && !strings.HasPrefix(job.Uses, "./.github/workflows/") {
			return HealthWorkflowContract{}, fmt.Errorf("%s job %s uses unsupported external workflow %q", filename, id, job.Uses)
		}
		if strings.HasPrefix(job.Uses, "./.github/workflows/") {
			children, present := reusable[job.Uses]
			if !present {
				return HealthWorkflowContract{}, fmt.Errorf("%s job %s missing reusable workflow %s", filename, id, job.Uses)
			}
			if err := validateLocalWorkflowPath(job.Uses); err != nil {
				return HealthWorkflowContract{}, fmt.Errorf("%s job %s: %w", filename, id, err)
			}
			childWorkflow, err := parseHealthWorkflow(children)
			if err != nil {
				return HealthWorkflowContract{}, fmt.Errorf("%s reusable workflow %s: %w", filename, job.Uses, err)
			}
			for childID, child := range childWorkflow.Jobs {
				if child.Uses != "" {
					return HealthWorkflowContract{}, fmt.Errorf("%s reusable job %s uses nested workflow %q", filename, childID, child.Uses)
				}
				childName := child.Name
				if childName == "" {
					childName = childID
				}
				childCompiled, err := compileHealthJob(filename, childID, childName, child.Strategy.Matrix, plan)
				if err != nil {
					return HealthWorkflowContract{}, fmt.Errorf("%s reusable job %s: %w", filename, childID, err)
				}
				for _, childJobName := range mapKeys(childCompiled.mapNames) {
					childCanonical := childCompiled.mapNames[childJobName]
					canonical := InternalJobID(id) + "/" + childCanonical
					name := name + " / " + childJobName
					if err := addHealthJobName(contract.JobNames, name, canonical); err != nil {
						return HealthWorkflowContract{}, err
					}
					current.ids = append(current.ids, canonical)
					contract.ReusableChildren[InternalJobID(id)] = append(contract.ReusableChildren[InternalJobID(id)], canonical)
				}
				// The maintained caller-local qualification lane is independent of
				// PR planner output and is part of the selected run's required evidence.
				contract.PlanIndependentJobs = append(contract.PlanIndependentJobs, current.ids...)
			}
		}
		if len(current.ids) == 0 {
			return HealthWorkflowContract{}, fmt.Errorf("%s job %s has no observable job names", filename, id)
		}
		for display, canonical := range current.mapNames {
			if err := addHealthJobName(contract.JobNames, display, canonical); err != nil {
				return HealthWorkflowContract{}, err
			}
		}
		compiled[id] = current
		contract.WorkflowJobs = append(contract.WorkflowJobs, current.ids...)
	}
	if _, ok := compiled["ci-gate"]; !ok {
		return HealthWorkflowContract{}, fmt.Errorf("%s ci-gate is not observable", filename)
	}
	for _, id := range needs {
		job, ok := compiled[id]
		if !ok {
			return HealthWorkflowContract{}, fmt.Errorf("%s ci-gate needs missing job %s", filename, id)
		}
		contract.RequiredJobs = append(contract.RequiredJobs, job.ids...)
	}
	contract.RequiredJobs = append(contract.RequiredJobs, compiled["ci-gate"].ids...)
	contract.WorkflowJobs = uniqueSorted(contract.WorkflowJobs)
	contract.RequiredJobs = uniqueSorted(contract.RequiredJobs)
	contract.PlanIndependentJobs = uniqueSorted(contract.PlanIndependentJobs)
	return contract, nil
}

func parseHealthWorkflow(contents []byte) (healthWorkflowDocument, error) {
	var workflow healthWorkflowDocument
	if err := yaml.Unmarshal(contents, &workflow); err != nil {
		return healthWorkflowDocument{}, fmt.Errorf("parse workflow YAML: %w", err)
	}
	if len(workflow.Jobs) == 0 {
		return healthWorkflowDocument{}, fmt.Errorf("workflow YAML has no jobs")
	}
	return workflow, nil
}

func validateLocalWorkflowPath(value string) error {
	if !strings.HasPrefix(value, "./.github/workflows/") {
		return fmt.Errorf("unsupported local reusable workflow path %q", value)
	}
	clean := path.Clean(strings.TrimPrefix(value, "./"))
	if !strings.HasPrefix(clean, ".github/workflows/") || clean == ".github/workflows" || strings.Contains(clean, "..") || (!strings.HasSuffix(clean, ".yml") && !strings.HasSuffix(clean, ".yaml")) {
		return fmt.Errorf("invalid local reusable workflow path %q", value)
	}
	return nil
}

func healthWorkflowNeeds(value any) ([]string, error) {
	switch typed := value.(type) {
	case string:
		if typed == "" {
			return nil, nil
		}
		return []string{typed}, nil
	case []any:
		result := make([]string, 0, len(typed))
		for _, entry := range typed {
			name, ok := entry.(string)
			if !ok || name == "" {
				return nil, fmt.Errorf("invalid ci-gate needs entry %v", entry)
			}
			result = append(result, name)
		}
		return result, nil
	default:
		if value == nil {
			return nil, nil
		}
		return nil, fmt.Errorf("unsupported ci-gate needs type %T", value)
	}
}

func compileHealthJob(workflow, id, name string, matrix yaml.Node, plan platformci.Plan) (compiledHealthJob, error) {
	result := compiledHealthJob{mapNames: map[string]string{}}
	matches := healthMatrixName.FindAllStringSubmatch(name, -1)
	if len(matches) == 0 {
		if strings.Contains(name, "${{") {
			return result, fmt.Errorf("unsupported dynamic job name %q", name)
		}
		if matrix.Kind != 0 {
			return result, fmt.Errorf("job %s has an unsupported matrix without a shard name", id)
		}
		canonical := InternalJobID(id)
		result.ids = []string{canonical}
		result.mapNames[name] = canonical
		return result, nil
	}
	if len(matches) != 1 {
		return result, fmt.Errorf("job %s has multiple matrix name placeholders in %q", id, name)
	}
	match := matches[0]
	axis := match[1]
	if axis != "shard" {
		return result, fmt.Errorf("unsupported dynamic matrix name axis %q in %q", axis, name)
	}
	values, err := healthMatrixValues(workflow, id, axis, matrix, plan)
	if err != nil {
		return result, err
	}
	for _, value := range values {
		display := healthMatrixName.ReplaceAllString(name, value)
		canonical := InternalJobID(id)
		if value != "not-selected" {
			canonical += "/" + value
		}
		if err := addHealthJobName(result.mapNames, display, canonical); err != nil {
			return result, err
		}
		result.ids = append(result.ids, canonical)
	}
	return result, nil
}

func healthMatrixValues(workflow, id, axis string, matrix yaml.Node, plan platformci.Plan) ([]string, error) {
	switch matrix.Kind {
	case yaml.MappingNode:
		var values map[string][]string
		if err := matrix.Decode(&values); err != nil {
			return nil, fmt.Errorf("parse static matrix: %w", err)
		}
		shards, ok := values[axis]
		if !ok || len(shards) == 0 {
			return nil, fmt.Errorf("static matrix has no %s values", axis)
		}
		if len(values) != 1 {
			return nil, fmt.Errorf("unsupported static matrix axes: %v", mapKeysOfSliceMap(values))
		}
		return shards, nil
	case yaml.ScalarNode:
		if workflow != "ci.yml" || id != "frontend-validation" || axis != "shard" || strings.TrimSpace(matrix.Value) != "${{ fromJSON(needs.prepare.outputs.frontend_matrix) }}" {
			return nil, fmt.Errorf("unsupported dynamic matrix %q", matrix.Value)
		}
		var shards []string
		if plan.PR != nil {
			shards = append(shards, plan.PR.Effective.Frontend...)
		} else if plan.Version == platformci.PlanVersion {
			shards = append(shards, plan.Effective.Frontend...)
		}
		if len(shards) == 0 {
			shards = []string{"not-selected"}
		}
		return shards, nil
	default:
		return nil, fmt.Errorf("matrix %s is missing or has unsupported YAML shape", axis)
	}
}

func mapKeysOfSliceMap(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func addHealthJobName(names map[string]string, name, canonical string) error {
	if prior, exists := names[name]; exists && prior != canonical {
		return fmt.Errorf("workflow job name %q collides between %s and %s", name, prior, canonical)
	}
	names[name] = canonical
	return nil
}

func mapKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func uniqueSorted(values []string) []string {
	sort.Strings(values)
	return slices.Compact(values)
}
