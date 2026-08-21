// Package plan turns compiled refresh definitions into deterministic execution plans.
package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	semanticquery "github.com/flidai/leapview/internal/analytics/query"
	projectpipelineplan "github.com/flidai/leapview/internal/project/contracts/pipelineplan"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	refreshartifact "github.com/flidai/leapview/internal/refresh/artifact"
	refreshschedule "github.com/flidai/leapview/internal/refresh/schedule"
)

type Plan struct {
	ProjectID        projectgraph.ResourceID
	Environment      string
	TargetType       string
	TargetID         projectgraph.ResourceID
	SemanticModelID  projectgraph.ResourceID
	Tables           []string
	DependencyTables []string
	// MaterializationScope is the exact ordered model-table closure selected
	// by the pipeline. It is intentionally distinct from downstream/dashboard
	// scope and is preserved through delivery planning.
	MaterializationScope []string
	SourceInputs         []string
	ServingGenerationID  string
	ArtifactDigest       string
	SelectionDigest      string
	Digest               string
}

// InvocationPolicy is the effective authored trigger and overlap policy bound
// when a compiled selection is admitted as a run.
type InvocationPolicy struct {
	InvocationSource        string
	MatchingScheduleIDs     []string
	StartingDeadlineSeconds int64
	ConcurrencyPolicy       string
}

// DeliveryPipelinePlan lowers a generation-bound refresh selection into the
// target delivery contract. Callers must bind the refresh plan first; an
// unbound plan is not safe to dispatch.
func (p Plan) DeliveryPipelinePlan(policy ...InvocationPolicy) (projectpipelineplan.Plan, error) {
	if p.Digest == "" || p.ServingGenerationID == "" || p.ArtifactDigest == "" {
		return projectpipelineplan.Plan{}, fmt.Errorf("pipeline plan is not generation-bound")
	}
	effective := InvocationPolicy{}
	if len(policy) > 1 {
		return projectpipelineplan.Plan{}, fmt.Errorf("pipeline plan accepts one effective invocation policy")
	}
	if len(policy) == 1 {
		effective = policy[0]
	}
	if err := refreshschedule.ValidateArtifactDigest(p.SelectionDigest); err != nil {
		return projectpipelineplan.Plan{}, fmt.Errorf("pipeline authored selection digest: %w", err)
	}
	return projectpipelineplan.New(projectpipelineplan.Plan{
		ID: "pipeline-plan-" + strings.TrimPrefix(p.Digest, "sha256:"), PipelineID: p.TargetID.String(), ProjectID: p.ProjectID.String(), Environment: p.Environment,
		SemanticModelID: p.SemanticModelID.String(), SelectedResourceType: "semanticModel", SelectedResourceID: p.SemanticModelID.String(), ServingGenerationID: p.ServingGenerationID,
		ArtifactDigest: p.ArtifactDigest, SelectionDigest: p.SelectionDigest, MaterializationScope: append([]string(nil), p.MaterializationScope...),
		ModelExecutionOrder: append([]string(nil), p.MaterializationScope...), SourceInputs: append([]string(nil), p.SourceInputs...),
		InvocationSource: effective.InvocationSource, MatchingScheduleIDs: append([]string(nil), effective.MatchingScheduleIDs...), StartingDeadlineSeconds: effective.StartingDeadlineSeconds, ConcurrencyPolicy: effective.ConcurrencyPolicy,
	})
}

func ForPipeline(definition *refreshartifact.Definition, projectID, pipelineID projectgraph.ResourceID) (Plan, error) {
	if definition == nil {
		return Plan{}, fmt.Errorf("project definition is required")
	}
	if err := projectID.Validate(); err != nil {
		return Plan{}, err
	}
	if err := pipelineID.Validate(); err != nil {
		return Plan{}, err
	}
	pipeline, ok := definition.Pipelines[pipelineID.String()]
	if !ok {
		return Plan{}, fmt.Errorf("unknown refresh pipeline %q", pipelineID)
	}
	model, ok := definition.Models[pipeline.SemanticModelID.String()]
	if !ok {
		return Plan{}, fmt.Errorf("refresh pipeline %q references unknown semantic model %q", pipelineID, pipeline.SemanticModelID)
	}
	order, err := modelTableOrder(model, definition.ModelTables)
	if err != nil {
		return Plan{}, err
	}
	result := Plan{
		ProjectID:        projectID,
		TargetType:       "refresh_pipeline",
		TargetID:         pipelineID,
		SemanticModelID:  pipeline.SemanticModelID,
		Tables:           order,
		DependencyTables: append([]string(nil), order...), MaterializationScope: append([]string(nil), order...),
		SourceInputs: modelSourceInputs(definition.ModelTables, order),
	}
	if err := refreshschedule.ValidateArtifactDigest(pipeline.SelectionDigest); err != nil {
		return Plan{}, fmt.Errorf("refresh pipeline %q authored selection digest: %w", pipelineID, err)
	}
	result.SelectionDigest = pipeline.SelectionDigest
	return result, nil
}

// BindGeneration makes a compiled selection immutable for one serving
// generation and source artifact. The same selection against a different
// generation or artifact receives a different plan digest.
func (p Plan) BindGeneration(identity projectgraph.ServingIdentity, artifactDigest string) (Plan, error) {
	if err := identity.Validate(); err != nil {
		return Plan{}, err
	}
	if len(p.MaterializationScope) == 0 {
		return Plan{}, fmt.Errorf("pipeline materialization scope is empty")
	}
	if p.ProjectID != identity.ProjectID {
		return Plan{}, fmt.Errorf("pipeline plan project does not match serving identity")
	}
	p.Environment = identity.Environment
	p.ServingGenerationID = identity.GenerationID
	p.ArtifactDigest = artifactDigest
	p.Digest = digestPlan(struct {
		TargetID            string   `json:"targetId"`
		PipelineID          string   `json:"pipelineId"`
		SemanticModelID     string   `json:"semanticModelId"`
		ServingGenerationID string   `json:"servingGenerationId"`
		ArtifactDigest      string   `json:"artifactDigest"`
		SelectionDigest     string   `json:"selectionDigest"`
		Scope               []string `json:"materializationScope"`
		Sources             []string `json:"sourceInputs"`
	}{p.TargetID.String(), p.TargetID.String(), p.SemanticModelID.String(), identity.GenerationID, p.ArtifactDigest, p.SelectionDigest, p.MaterializationScope, p.SourceInputs})
	return p, nil
}

func digestPlan(value any) string {
	encoded, _ := json.Marshal(value)
	sum := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func modelSourceInputs(tables map[string]semanticmodel.Table, order []string) []string {
	seen := map[string]struct{}{}
	for _, name := range order {
		table, ok := tables[name]
		if !ok {
			continue
		}
		for _, source := range append([]string{table.Execution.Source}, table.SourceDependencies...) {
			if source != "" {
				seen[source] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for source := range seen {
		result = append(result, source)
	}
	sort.Strings(result)
	return result
}

func modelTableOrder(model *semanticmodel.Model, modelTables map[string]semanticmodel.Table) ([]string, error) {
	if model == nil {
		return nil, fmt.Errorf("semantic model is required")
	}
	if len(modelTables) == 0 {
		return nil, fmt.Errorf("project Model catalog is required")
	}
	compiled, err := semanticquery.CompileDatasetBindings(model)
	if err != nil {
		return nil, err
	}
	// Semantic dataset names are aliases. Refresh execution is project-scoped
	// and therefore orders the physical authored Model tables, deduplicating
	// aliases that point at the same Model.
	roots := map[string]struct{}{}
	for _, name := range compiled.DatasetNames() {
		dataset, _ := compiled.Dataset(name)
		physical := dataset.ModelName()
		if _, ok := modelTables[physical]; !ok {
			return nil, fmt.Errorf("semantic dataset %q references unknown project Model %q", name, physical)
		}
		roots[physical] = struct{}{}
	}
	temporary := map[string]bool{}
	permanent := map[string]bool{}
	order := make([]string, 0, len(modelTables))
	var visit func(string) error
	visit = func(name string) error {
		if permanent[name] {
			return nil
		}
		if temporary[name] {
			return fmt.Errorf("model table dependency cycle includes %q", name)
		}
		table, ok := modelTables[name]
		if !ok {
			return fmt.Errorf("unknown model dependency %q", name)
		}
		temporary[name] = true
		for _, dependency := range table.ModelDependencies {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		delete(temporary, name)
		permanent[name] = true
		order = append(order, name)
		return nil
	}
	names := make([]string, 0, len(roots))
	for name := range roots {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := visit(name); err != nil {
			return nil, err
		}
	}
	return order, nil
}
