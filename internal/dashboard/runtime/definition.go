package runtime

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	dashboarddefinition "github.com/flidai/leapview/internal/dashboard/definition"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

// ProjectDefinition is the immutable dashboard projection owned by one
// project artifact. It deliberately has no legacy container identity;
// every resource map is keyed by its canonical graph ResourceID.
type ProjectDefinition struct {
	projectID   projectgraph.ResourceID
	title       string
	description string
	models      map[projectgraph.ResourceID]*semanticmodel.Model
	dashboards  map[projectgraph.ResourceID]dashboarddefinition.Definition
}

// NewProjectDefinition validates and defensively copies a project dashboard
// projection. The maps and model pointers supplied by callers are never
// retained, so a generation cannot change after activation.
func NewProjectDefinition(projectID projectgraph.ResourceID, title, description string, models map[projectgraph.ResourceID]*semanticmodel.Model, dashboards map[projectgraph.ResourceID]dashboarddefinition.Definition) (*ProjectDefinition, error) {
	return newProjectDefinition(projectID, title, description, models, dashboards, false)
}

// NewTargetBoundProjectDefinition validates and defensively copies a runtime
// projection after trusted target-owned managed-data roots have been bound.
// All other target state is redacted exactly as it is by NewProjectDefinition.
func NewTargetBoundProjectDefinition(projectID projectgraph.ResourceID, title, description string, models map[projectgraph.ResourceID]*semanticmodel.Model, dashboards map[projectgraph.ResourceID]dashboarddefinition.Definition) (*ProjectDefinition, error) {
	return newProjectDefinition(projectID, title, description, models, dashboards, true)
}

func newProjectDefinition(projectID projectgraph.ResourceID, title, description string, models map[projectgraph.ResourceID]*semanticmodel.Model, dashboards map[projectgraph.ResourceID]dashboarddefinition.Definition, retainManagedRoots bool) (*ProjectDefinition, error) {
	if err := projectID.Validate(); err != nil {
		return nil, fmt.Errorf("project id: %w", err)
	}
	copyModels := make(map[projectgraph.ResourceID]*semanticmodel.Model, len(models))
	for id, model := range models {
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("semantic model id: %w", err)
		}
		if model == nil {
			return nil, fmt.Errorf("semantic model %q is nil", id)
		}
		clone, err := cloneModel(model, retainManagedRoots)
		if err != nil {
			return nil, fmt.Errorf("semantic model %q: %w", id, err)
		}
		copyModels[id] = clone
	}
	copyDashboards := make(map[projectgraph.ResourceID]dashboarddefinition.Definition, len(dashboards))
	for id, dashboard := range dashboards {
		if err := id.Validate(); err != nil {
			return nil, fmt.Errorf("dashboard id: %w", err)
		}
		if dashboard.ID != id.String() {
			return nil, fmt.Errorf("dashboard %q has mismatched definition id %q", id, dashboard.ID)
		}
		modelID, err := projectgraph.NewResourceID(dashboard.SemanticModel)
		if err != nil {
			return nil, fmt.Errorf("dashboard %q semantic model: %w", id, err)
		}
		if _, ok := copyModels[modelID]; !ok {
			return nil, fmt.Errorf("dashboard %q references unknown semantic model %q", id, modelID)
		}
		clone, err := cloneDashboard(dashboard)
		if err != nil {
			return nil, fmt.Errorf("dashboard %q: %w", id, err)
		}
		copyDashboards[id] = clone
	}
	return &ProjectDefinition{projectID: projectID, title: title, description: description, models: copyModels, dashboards: copyDashboards}, nil
}

func cloneModel(model *semanticmodel.Model, retainManagedRoots bool) (*semanticmodel.Model, error) {
	clone, err := model.RuntimeSnapshot()
	if err != nil || clone == nil || !retainManagedRoots {
		return clone, err
	}
	for name, source := range model.Connections {
		root := strings.TrimSpace(source.Root)
		if source.Kind != "managed" || root == "" {
			continue
		}
		connection := clone.Connections[name]
		connection.Root = root
		clone.Connections[name] = connection
	}
	return clone, nil
}

func cloneDashboard(dashboard dashboarddefinition.Definition) (dashboarddefinition.Definition, error) {
	encoded, err := json.Marshal(dashboard)
	if err != nil {
		return dashboarddefinition.Definition{}, err
	}
	var clone dashboarddefinition.Definition
	if err := json.Unmarshal(encoded, &clone); err != nil {
		return dashboarddefinition.Definition{}, err
	}
	return clone, nil
}

func (d *ProjectDefinition) Validate() error {
	if d == nil {
		return fmt.Errorf("project dashboard definition is required")
	}
	if err := d.projectID.Validate(); err != nil {
		return fmt.Errorf("project id: %w", err)
	}
	return nil
}

func (d *ProjectDefinition) ProjectID() projectgraph.ResourceID {
	if d == nil {
		return ""
	}
	return d.projectID
}
func (d *ProjectDefinition) Title() string {
	if d == nil {
		return ""
	}
	return d.title
}
func (d *ProjectDefinition) Description() string {
	if d == nil {
		return ""
	}
	return d.description
}

// Models returns a detached map keyed by canonical model ResourceID.
func (d *ProjectDefinition) Models() map[projectgraph.ResourceID]*semanticmodel.Model {
	if d == nil {
		return nil
	}
	result := make(map[projectgraph.ResourceID]*semanticmodel.Model, len(d.models))
	for id, model := range d.models {
		// The definition already contains only roots admitted through the
		// target-bound constructor, so detached accessors retain them.
		clone, _ := cloneModel(model, true)
		result[id] = clone
	}
	return result
}

// Dashboards returns a detached map keyed by canonical dashboard ResourceID.
func (d *ProjectDefinition) Dashboards() map[projectgraph.ResourceID]dashboarddefinition.Definition {
	if d == nil {
		return nil
	}
	result := make(map[projectgraph.ResourceID]dashboarddefinition.Definition, len(d.dashboards))
	for id, dashboard := range d.dashboards {
		clone, _ := cloneDashboard(dashboard)
		result[id] = clone
	}
	return result
}

func (d *ProjectDefinition) ModelIDs() []projectgraph.ResourceID {
	if d == nil {
		return nil
	}
	ids := make([]projectgraph.ResourceID, 0, len(d.models))
	for id := range d.models {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}

// DashboardIDs returns dashboard resource IDs in canonical order. Keeping
// ordering here gives all projections a deterministic traversal without
// exposing the mutable backing map.
func (d *ProjectDefinition) DashboardIDs() []projectgraph.ResourceID {
	if d == nil {
		return nil
	}
	ids := make([]projectgraph.ResourceID, 0, len(d.dashboards))
	for id := range d.dashboards {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids
}
