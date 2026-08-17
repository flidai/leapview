package compiler

import (
	"reflect"
	"sort"

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
	Summary           ProjectPlanSummary            `json:"summary,omitempty"`
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

func planForProject(project Project) ProjectPlan {
	plan := ProjectPlan{Project: string(project.ID)}
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
