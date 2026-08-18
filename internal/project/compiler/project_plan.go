package compiler

import (
	"reflect"
	"sort"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	projectartifact "github.com/flidai/leapview/internal/project/artifact"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ProjectPlan is a project-wide, target-independent change plan. Every
// resource list contains canonical stable IDs; symbolic names and target
// scopes are intentionally absent.
type ProjectPlan struct {
	Project           string                        `json:"project"`
	Connections       []string                      `json:"connections,omitempty"`
	Sources           []string                      `json:"sources,omitempty"`
	Models            []string                      `json:"models,omitempty"`
	SemanticModels    []string                      `json:"semanticModels,omitempty"`
	Pipelines         []string                      `json:"pipelines,omitempty"`
	Dashboards        []string                      `json:"dashboards,omitempty"`
	Groups            []string                      `json:"groups,omitempty"`
	RoleBindings      []string                      `json:"roleBindings,omitempty"`
	Grants            []string                      `json:"grants,omitempty"`
	DataPolicies      []string                      `json:"dataPolicies,omitempty"`
	Changes           []ProjectPlanChange           `json:"changes,omitempty"`
	DependencyChanges []ProjectPlanDependencyChange `json:"dependencyChanges,omitempty"`
	// Deterministic is compiler-produced evidence that the project expressions
	// contain no known volatile SQL functions. Unknown/hand-built plans leave
	// this false, so reuse remains fail-closed.
	Deterministic bool               `json:"deterministic,omitempty"`
	Summary       ProjectPlanSummary `json:"summary,omitempty"`
}

type ProjectPlanSummary struct {
	Added                 int  `json:"added,omitempty"`
	Changed               int  `json:"changed,omitempty"`
	Removed               int  `json:"removed,omitempty"`
	DependencyChanges     int  `json:"dependencyChanges,omitempty"`
	Breaking              bool `json:"breaking,omitempty"`
	MaterializationImpact bool `json:"materializationImpact,omitempty"`
}

type ProjectPlanChange struct {
	Action                string `json:"action"`
	ID                    string `json:"id"`
	Type                  string `json:"type"`
	Key                   string `json:"key"`
	Reason                string `json:"reason,omitempty"`
	Breaking              bool   `json:"breaking,omitempty"`
	MaterializationImpact bool   `json:"materializationImpact,omitempty"`
}

type ProjectPlanDependencyChange struct {
	Action                string `json:"action"`
	From                  string `json:"from"`
	To                    string `json:"to"`
	Type                  string `json:"type"`
	MaterializationImpact bool   `json:"materializationImpact,omitempty"`
}

func PlanProject(projectPath string) (ProjectPlan, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return ProjectPlan{}, err
	}
	return planForProject(project), nil
}

// PlanProjectAgainstGraph compares authored project graph bytes with an
// active graph. The active graph is portable and contains no serving identity.
func PlanProjectAgainstGraph(projectPath string, active projectgraph.ProjectGraph) (ProjectPlan, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return ProjectPlan{}, err
	}
	plan := planForProject(project)
	changes, dependencyChanges, summary := diffProjectGraphs(project.Graph, active)
	plan.Changes, plan.DependencyChanges, plan.Summary = changes, dependencyChanges, summary
	return plan, nil
}

// PlanProjectAgainstArtifact compares authored definitions with the exact
// compiled artifact retained by the active serving generation. Graph nodes
// intentionally carry only identity/metadata, so comparing the graph alone
// cannot detect SQL, source, or model-table changes at an unchanged ID/path.
func PlanProjectAgainstArtifact(projectPath string, active projectartifact.Project) (ProjectPlan, error) {
	project, err := LoadProject(projectPath)
	if err != nil {
		return ProjectPlan{}, err
	}
	plan := planForProject(project)
	changes, dependencyChanges, summary := diffProjectGraphs(project.Graph, active.Graph())
	for _, materialization := range diffCompiledMaterialization(project, active) {
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
	summary = ProjectPlanSummary{DependencyChanges: len(dependencyChanges)}
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
	plan.Changes, plan.DependencyChanges, plan.Summary = changes, dependencyChanges, summary
	return plan, nil
}

func diffCompiledMaterialization(project Project, active projectartifact.Project) []ProjectPlanChange {
	changes := make([]ProjectPlanChange, 0)
	activeTables := active.ModelTables()
	authoredTables := make(map[string]semanticmodel.Table, len(project.Models))
	for name, table := range project.Models {
		if id := project.ModelIDs[name]; id != "" {
			authoredTables[id] = table
		}
	}
	seen := make(map[string]struct{}, len(activeTables)+len(authoredTables))
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
		resource, ok := project.Graph.Resource(projectgraph.ResourceID(id))
		if !ok {
			resource, _ = active.Graph().Resource(projectgraph.ResourceID(id))
		}
		action, reason := "change", "compiled model definition changed"
		if !authoredOK {
			action, reason = "remove", "model definition removed from authored artifact"
		} else if !retainedOK {
			action, reason = "add", "model definition added to authored artifact"
		}
		breaking, _ := projectResourceImpact(projectgraph.KindModel, projectgraph.KindModel, action)
		changes = append(changes, ProjectPlanChange{Action: action, ID: id, Type: string(projectgraph.KindModel), Key: resource.Name, Reason: reason, Breaking: breaking, MaterializationImpact: true})
	}
	activeSources := active.Manifest().Sources
	authoredSources := make(map[string]semanticmodel.Source, len(project.Sources))
	for name, source := range project.Sources {
		if id := project.SourceIDs[name]; id != "" {
			authoredSources[id] = source
		}
	}
	seen = make(map[string]struct{}, len(activeSources)+len(authoredSources))
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
		resource, ok := project.Graph.Resource(projectgraph.ResourceID(id))
		if !ok {
			resource, _ = active.Graph().Resource(projectgraph.ResourceID(id))
		}
		action := "change"
		if _, authored := authoredSources[id]; !authored {
			action = "remove"
		} else if _, retained := activeSources[id]; !retained {
			action = "add"
		}
		changes = append(changes, ProjectPlanChange{Action: action, ID: id, Type: string(projectgraph.KindSource), Key: resource.Name, Reason: "compiled source definition changed", MaterializationImpact: true})
	}
	return changes
}

func planForProject(project Project) ProjectPlan {
	plan := ProjectPlan{Project: string(project.ID), Deterministic: projectDeterministic(project)}
	plan.Connections = sortedIDValues(project.ConnectionIDs)
	plan.Sources = sortedIDValues(project.SourceIDs)
	plan.Models = sortedIDValues(project.ModelIDs)
	plan.SemanticModels = sortedIDValues(project.SemanticModelIDs)
	plan.Pipelines = sortedIDValues(project.PipelineIDs)
	plan.Dashboards = sortedIDValues(project.DashboardIDs)
	for name := range project.Access.Groups {
		if id := project.ResourceIDs["group:"+name]; id != "" {
			plan.Groups = append(plan.Groups, id)
		}
	}
	for name := range project.Access.RoleBindings {
		if id := project.ResourceIDs["rolebinding:"+name]; id != "" {
			plan.RoleBindings = append(plan.RoleBindings, id)
		}
	}
	for name := range project.Access.Grants {
		if id := project.ResourceIDs["grant:"+name]; id != "" {
			plan.Grants = append(plan.Grants, id)
		}
	}
	for name := range project.Access.DataPolicies {
		if id := project.ResourceIDs["datapolicy:"+name]; id != "" {
			plan.DataPolicies = append(plan.DataPolicies, id)
		}
	}
	sort.Strings(plan.Groups)
	sort.Strings(plan.RoleBindings)
	sort.Strings(plan.Grants)
	sort.Strings(plan.DataPolicies)
	return plan
}

func projectDeterministic(project Project) bool {
	// SQL volatility cannot be established safely with a substring denylist:
	// DuckDB exposes a large and evolving function registry, and a new
	// context-dependent function would otherwise silently become reusable. The
	// compiler therefore emits positive evidence only for the narrow static
	// subset whose execution contains no authored SQL or expressions: direct
	// source-backed tables and non-expression semantic metrics. Unknown or
	// hand-built plans remain false and force a refresh.
	for _, table := range project.Models {
		if table.Transform.SQL != "" || table.Source == "" {
			return false
		}
		source, ok := project.Sources[table.Source]
		connection, connected := project.Connections[source.Connection]
		// Only managed revisions are target-pinned at this phase. Authored
		// connector bindings are observed/unbounded here and must not be reused
		// without a target-issued equivalence token.
		if !ok || !connected || connection.Kind != "managed" {
			return false
		}
	}
	for _, semantic := range project.SemanticModels {
		for _, metric := range semantic.Metrics {
			if metric.Expression != "" {
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

func diffProjectGraphs(authored, active projectgraph.ProjectGraph) ([]ProjectPlanChange, []ProjectPlanDependencyChange, ProjectPlanSummary) {
	authoredResources := map[projectgraph.ResourceID]projectgraph.Resource{}
	activeResources := map[projectgraph.ResourceID]projectgraph.Resource{}
	for _, resource := range authored.Resources() {
		authoredResources[resource.ID] = resource
	}
	for _, resource := range active.Resources() {
		activeResources[resource.ID] = resource
	}
	changes := make([]ProjectPlanChange, 0)
	for id, resource := range authoredResources {
		other, exists := activeResources[id]
		if !exists {
			change := ProjectPlanChange{Action: "add", ID: string(id), Type: string(resource.Kind), Key: resource.Name, Reason: "not in active graph"}
			change.Breaking, change.MaterializationImpact = projectResourceImpact(resource.Kind, resource.Kind, change.Action)
			changes = append(changes, change)
			continue
		}
		if !reflect.DeepEqual(resource, other) {
			change := ProjectPlanChange{Action: "change", ID: string(id), Type: string(resource.Kind), Key: resource.Name, Reason: "resource descriptor changed"}
			change.Breaking, change.MaterializationImpact = projectResourceImpact(resource.Kind, other.Kind, change.Action)
			changes = append(changes, change)
		}
	}
	for id, resource := range activeResources {
		if _, exists := authoredResources[id]; !exists {
			change := ProjectPlanChange{Action: "remove", ID: string(id), Type: string(resource.Kind), Key: resource.Name, Reason: "not in authored graph"}
			change.Breaking, change.MaterializationImpact = projectResourceImpact(resource.Kind, resource.Kind, change.Action)
			changes = append(changes, change)
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].ID < changes[j].ID })
	dependencyChanges := diffProjectEdges(authored.Edges(), active.Edges())
	summary := ProjectPlanSummary{DependencyChanges: len(dependencyChanges)}
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

func projectResourceImpact(kind, otherKind projectgraph.Kind, action string) (breaking, materialization bool) {
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

func diffProjectEdges(authored, active []projectgraph.Edge) []ProjectPlanDependencyChange {
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
	result := make([]ProjectPlanDependencyChange, 0)
	for value, edge := range authoredSet {
		if _, ok := activeSet[value]; !ok {
			result = append(result, projectDependencyChange("add", edge))
		}
	}
	for value, edge := range activeSet {
		if _, ok := authoredSet[value]; !ok {
			result = append(result, projectDependencyChange("remove", edge))
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

func projectDependencyChange(action string, edge projectgraph.Edge) ProjectPlanDependencyChange {
	return ProjectPlanDependencyChange{
		Action: action, From: string(edge.From), To: string(edge.To), Type: edge.Relation,
		MaterializationImpact: edge.Relation == "reads_source" || edge.Relation == "uses_model" || edge.Relation == "refreshes",
	}
}
