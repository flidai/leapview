package compiler

import (
	"fmt"
	"reflect"
	"sort"

	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectcontracts "github.com/flidai/leapview/internal/project/contracts"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// BundlePlan is a bundle-wide, target-independent change plan. Every
// resource list contains canonical stable IDs; symbolic names and target
// scopes are intentionally absent.
type BundlePlan struct {
	Connections       []string                     `json:"connections,omitempty"`
	Sources           []string                     `json:"sources,omitempty"`
	Models            []string                     `json:"models,omitempty"`
	SemanticModels    []string                     `json:"semanticModels,omitempty"`
	Pipelines         []string                     `json:"pipelines,omitempty"`
	Dashboards        []string                     `json:"dashboards,omitempty"`
	Changes           []BundlePlanChange           `json:"changes,omitempty"`
	DependencyChanges []BundlePlanDependencyChange `json:"dependencyChanges,omitempty"`
	// Deterministic is compiler-produced evidence that the source expressions
	// contain no known volatile SQL functions. Unknown/hand-built plans leave
	// this false, so reuse remains fail-closed.
	Deterministic bool              `json:"deterministic,omitempty"`
	Summary       BundlePlanSummary `json:"summary,omitempty"`
}

type BundlePlanSummary struct {
	Added                 int  `json:"added,omitempty"`
	Changed               int  `json:"changed,omitempty"`
	Removed               int  `json:"removed,omitempty"`
	DependencyChanges     int  `json:"dependencyChanges,omitempty"`
	Breaking              bool `json:"breaking,omitempty"`
	MaterializationImpact bool `json:"materializationImpact,omitempty"`
}

type BundlePlanChange struct {
	Action                string `json:"action"`
	ID                    string `json:"id"`
	Type                  string `json:"type"`
	Key                   string `json:"key"`
	Reason                string `json:"reason,omitempty"`
	Breaking              bool   `json:"breaking,omitempty"`
	MaterializationImpact bool   `json:"materializationImpact,omitempty"`
}

type BundlePlanDependencyChange struct {
	Action                string `json:"action"`
	From                  string `json:"from"`
	To                    string `json:"to"`
	Type                  string `json:"type"`
	ResourceKind          string `json:"resourceKind"`
	MaterializationImpact bool   `json:"materializationImpact,omitempty"`
}

func PlanSourceRoot(sourceRoot string) (BundlePlan, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return BundlePlan{}, err
	}
	return planForSourceAssembly(project), nil
}

// PlanSourceRootAgainstGraph compares authored resource graph bytes with an
// active graph. The active graph is portable and contains no serving identity.
func PlanSourceRootAgainstGraph(sourceRoot string, active projectgraph.ProjectGraph) (BundlePlan, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return BundlePlan{}, err
	}
	plan := planForSourceAssembly(project)
	changes, dependencyChanges, summary := diffResourceGraphs(project.Graph, active)
	plan.Changes, plan.DependencyChanges, plan.Summary = changes, dependencyChanges, summary
	return plan, nil
}

// PlanSourceRootAgainstBundle compares authored definitions with the exact
// compiled artifact retained by the active serving generation. Graph nodes
// intentionally carry only identity/metadata, so comparing the graph alone
// cannot detect SQL, source, or Model materialization changes at an unchanged ID/path.
func PlanSourceRootAgainstBundle(sourceRoot string, active projectartifact.SourceBundle) (BundlePlan, error) {
	project, err := LoadSourceRoot(sourceRoot)
	if err != nil {
		return BundlePlan{}, err
	}
	candidate, err := projectartifact.NewSourceBundle(project.Graph, project.Manifest)
	if err != nil {
		return BundlePlan{}, err
	}
	plan := planForSourceAssembly(project)
	changes, dependencyChanges, summary := diffResourceGraphs(project.Graph, active.Graph())
	materializationChanges, err := diffCompiledMaterialization(candidate, active)
	if err != nil {
		return BundlePlan{}, err
	}
	for _, materialization := range materializationChanges {
		merged := false
		for i := range changes {
			if changes[i].ID == materialization.ID && changes[i].Action == materialization.Action {
				changes[i].MaterializationImpact = true
				changes[i].Reason = materialization.Reason
				merged = true
				break
			}
		}
		if !merged {
			changes = append(changes, materialization)
		}
	}
	sort.Slice(changes, func(i, j int) bool {
		if changes[i].ID != changes[j].ID {
			return changes[i].ID < changes[j].ID
		}
		return changes[i].Action < changes[j].Action
	})
	summary = BundlePlanSummary{DependencyChanges: len(dependencyChanges)}
	for _, change := range changes {
		if change.Action == "add" {
			summary.Added++
		} else if change.Action == "remove" {
			summary.Removed++
		} else {
			summary.Changed++
		}
		summary.Breaking = summary.Breaking || change.Breaking
		summary.MaterializationImpact = summary.MaterializationImpact || change.MaterializationImpact
	}
	for _, change := range dependencyChanges {
		summary.MaterializationImpact = summary.MaterializationImpact || change.MaterializationImpact
	}
	if len(changes) == 0 && len(dependencyChanges) == 0 {
		changes = nil
		dependencyChanges = nil
		summary = BundlePlanSummary{}
	}
	plan.Changes, plan.DependencyChanges, plan.Summary = changes, dependencyChanges, summary
	return plan, nil
}

func diffCompiledMaterialization(candidate, active projectartifact.SourceBundle) ([]BundlePlanChange, error) {
	changes := make([]BundlePlanChange, 0)
	candidateGraph := candidate.Graph()
	activeManifest := active.Manifest()
	candidateManifest := candidate.Manifest()
	activeTables := activeManifest.Models
	// Compare the same artifact projection on both sides. NewSourceBundle closes
	// authored symbolic references to stable IDs and applies runtime aliases;
	// comparing a pre-artifact manifest against that projection reports false
	// changes for an otherwise identical source root.
	authoredTables := candidateManifest.Models
	capacity, err := checkedCapacitySum(len(activeTables), len(authoredTables))
	if err != nil {
		return nil, fmt.Errorf("compiled materialization table set: %w", err)
	}
	seen := make(map[string]struct{}, capacity)
	for id := range authoredTables {
		seen[id] = struct{}{}
	}
	for id := range activeTables {
		seen[id] = struct{}{}
	}
	for id := range seen {
		authored, authoredOK := authoredTables[id]
		retained, retainedOK := activeTables[id]
		if authoredOK && retainedOK && reflect.DeepEqual(authored, retained) {
			continue
		}
		resource, ok := candidateGraph.Resource(projectgraph.ResourceID(id))
		if !ok {
			resource, _ = active.Graph().Resource(projectgraph.ResourceID(id))
		}
		action, reason := "change", "compiled model definition changed"
		if !authoredOK {
			action, reason = "remove", "model definition removed from authored artifact"
		} else if !retainedOK {
			action, reason = "add", "model definition added to authored artifact"
		}
		breaking, _ := resourceImpact(projectgraph.KindModel, projectgraph.KindModel, action)
		changes = append(changes, BundlePlanChange{Action: action, ID: id, Type: string(projectgraph.KindModel), Key: resource.Name, Reason: reason, Breaking: breaking, MaterializationImpact: true})
	}
	activeSources := activeManifest.Sources
	authoredSources := candidateManifest.Sources
	capacity, err = checkedCapacitySum(len(activeSources), len(authoredSources))
	if err != nil {
		return nil, fmt.Errorf("compiled materialization source set: %w", err)
	}
	seen = make(map[string]struct{}, capacity)
	for id := range authoredSources {
		seen[id] = struct{}{}
	}
	for id := range activeSources {
		seen[id] = struct{}{}
	}
	for id := range seen {
		if reflect.DeepEqual(authoredSources[id], activeSources[id]) {
			continue
		}
		resource, ok := candidateGraph.Resource(projectgraph.ResourceID(id))
		if !ok {
			resource, _ = active.Graph().Resource(projectgraph.ResourceID(id))
		}
		action := "change"
		if _, authored := authoredSources[id]; !authored {
			action = "remove"
		} else if _, retained := activeSources[id]; !retained {
			action = "add"
		}
		changes = append(changes, BundlePlanChange{Action: action, ID: id, Type: string(projectgraph.KindSource), Key: resource.Name, Reason: "compiled source definition changed", MaterializationImpact: true})
	}
	return changes, nil
}

func checkedCapacitySum(left, right int) (int, error) {
	if left < 0 || right < 0 {
		return 0, fmt.Errorf("capacity cannot be negative")
	}
	maximumInt := int(^uint(0) >> 1)
	if left > maximumInt-right {
		return 0, fmt.Errorf("capacity overflows platform int")
	}
	return left + right, nil
}

func planForSourceAssembly(project sourceAssembly) BundlePlan {
	plan := BundlePlan{Deterministic: sourceAssemblyDeterministic(project)}
	plan.Connections = sortedIDValues(project.ConnectionIDs)
	plan.Sources = sortedIDValues(project.SourceIDs)
	plan.Models = sortedIDValues(project.ModelIDs)
	plan.SemanticModels = sortedIDValues(project.SemanticModelIDs)
	plan.Pipelines = sortedIDValues(project.PipelineIDs)
	plan.Dashboards = sortedIDValues(project.DashboardIDs)
	return plan
}

func sourceAssemblyDeterministic(project sourceAssembly) bool {
	// SQL volatility cannot be established safely with a substring denylist:
	// DuckDB exposes a large and evolving function registry, and a new
	// context-dependent function would otherwise silently become reusable. The
	// compiler therefore emits positive evidence only for the narrow static
	// subset whose execution contains no authored SQL or expressions: direct
	// source-backed tables and non-expression semantic metrics. Unknown or
	// hand-built plans remain false and force a refresh.
	for _, table := range project.Models {
		if table.Execution.SQL != "" || table.Execution.Source == "" {
			return false
		}
		source, ok := project.Sources[table.Execution.Source]
		connection, connected := project.Connections[source.Connection]
		// Only managed revisions are target-pinned at this phase. Authored
		// connector bindings are observed/unbounded here and must not be reused
		// without a target-issued equivalence token.
		if !ok || !connected || connection.Kind != "managed" {
			return false
		}
	}
	for _, semantic := range project.SemanticModels {
		if semantic.Metrics == nil {
			continue
		}
		for _, metric := range *semantic.Metrics {
			if derived, ok := metric.Value.(*projectcontracts.SemanticMetricDerivedVariant); ok && derived.Expression != "" {
				return false
			}
		}
	}
	return true
}

func sortedIDValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, id := range values {
		if id != "" {
			result = append(result, id)
		}
	}
	sort.Strings(result)
	return result
}

func diffResourceGraphs(authored, active projectgraph.ProjectGraph) ([]BundlePlanChange, []BundlePlanDependencyChange, BundlePlanSummary) {
	authoredResources := map[projectgraph.ResourceID]projectgraph.Resource{}
	activeResources := map[projectgraph.ResourceID]projectgraph.Resource{}
	for _, resource := range authored.Resources() {
		authoredResources[resource.ID] = resource
	}
	for _, resource := range active.Resources() {
		activeResources[resource.ID] = resource
	}
	changes := make([]BundlePlanChange, 0)
	for id, resource := range authoredResources {
		other, exists := activeResources[id]
		if !exists {
			change := BundlePlanChange{Action: "add", ID: string(id), Type: string(resource.Kind), Key: resource.Name, Reason: "not in active graph"}
			change.Breaking, change.MaterializationImpact = resourceImpact(resource.Kind, resource.Kind, change.Action)
			changes = append(changes, change)
			continue
		}
		if !reflect.DeepEqual(resource, other) {
			change := BundlePlanChange{Action: "change", ID: string(id), Type: string(resource.Kind), Key: resource.Name, Reason: "resource descriptor changed"}
			change.Breaking, change.MaterializationImpact = resourceImpact(resource.Kind, other.Kind, change.Action)
			changes = append(changes, change)
		}
	}
	for id, resource := range activeResources {
		if _, exists := authoredResources[id]; !exists {
			change := BundlePlanChange{Action: "remove", ID: string(id), Type: string(resource.Kind), Key: resource.Name, Reason: "not in authored graph"}
			change.Breaking, change.MaterializationImpact = resourceImpact(resource.Kind, resource.Kind, change.Action)
			changes = append(changes, change)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	dependencyChanges := diffProjectEdges(authored.Edges(), active.Edges(), authoredResources, activeResources)
	summary := BundlePlanSummary{DependencyChanges: len(dependencyChanges)}
	for _, change := range changes {
		switch change.Action {
		case "add":
			summary.Added++
		case "change":
			summary.Changed++
		case "remove":
			summary.Removed++
		}
		summary.Breaking = summary.Breaking || change.Breaking
		summary.MaterializationImpact = summary.MaterializationImpact || change.MaterializationImpact
	}
	for _, change := range dependencyChanges {
		summary.MaterializationImpact = summary.MaterializationImpact || change.MaterializationImpact
	}
	return changes, dependencyChanges, summary
}

func resourceImpact(kind, otherKind projectgraph.Kind, action string) (breaking, materialization bool) {
	// Metadata/provenance movement is intentionally non-breaking: graph
	// identity is the stable resource ID. Kind changes and removals are
	// breaking; removing executable resources also invalidates materialization.
	switch action {
	case "remove":
		breaking = true
		materialization = kind == projectgraph.KindSource || kind == projectgraph.KindModel || kind == projectgraph.KindSemanticModel
	case "change":
		breaking = kind != otherKind
	}
	return breaking, materialization
}

func diffProjectEdges(
	authored,
	active []projectgraph.Edge,
	authoredResources,
	activeResources map[projectgraph.ResourceID]projectgraph.Resource,
) []BundlePlanDependencyChange {
	key := func(edge projectgraph.Edge) string {
		return string(edge.From) + "|" + string(edge.To) + "|" + edge.Relation
	}
	authoredSet, activeSet := map[string]projectgraph.Edge{}, map[string]projectgraph.Edge{}
	for _, edge := range authored {
		authoredSet[key(edge)] = edge
	}
	for _, edge := range active {
		activeSet[key(edge)] = edge
	}
	result := make([]BundlePlanDependencyChange, 0)
	for value, edge := range authoredSet {
		if _, ok := activeSet[value]; !ok {
			result = append(result, projectDependencyChange("add", edge, authoredResources[edge.To].Kind))
		}
	}
	for value, edge := range activeSet {
		if _, ok := authoredSet[value]; !ok {
			result = append(result, projectDependencyChange("remove", edge, activeResources[edge.To].Kind))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].From != result[j].From {
			return result[i].From < result[j].From
		}
		if result[i].To != result[j].To {
			return result[i].To < result[j].To
		}
		return result[i].Type < result[j].Type
	})
	return result
}

func projectDependencyChange(action string, edge projectgraph.Edge, resourceKind projectgraph.Kind) BundlePlanDependencyChange {
	return BundlePlanDependencyChange{
		Action: action, From: string(edge.From), To: string(edge.To), Type: edge.Relation, ResourceKind: string(resourceKind),
		MaterializationImpact: edge.Relation == "reads_source" || edge.Relation == "uses_model" || edge.Relation == "refreshes",
	}
}
