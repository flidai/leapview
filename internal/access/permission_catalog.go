package access

import (
	"fmt"
	"sort"
	"strings"

	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/flidai/leapview/pkg/permissions"
)

// PermissionCatalogProfile identifies the exact action meanings, prerequisite
// rules, and role expansions introduced by ADR-0025 and ADR-0026. Persisted
// credentials and assignments must pin this profile rather than interpreting
// an old action against whatever catalog happens to be current.
const PermissionCatalogProfile = "leapview.permissions/v1"

var (
	ErrInvalidPermissionCatalog = permissions.ErrInvalidCatalog
	ErrUnknownPermissionAction  = permissions.ErrUnknownAction
)

// Action is one typed operation in the resource authorization contract. It is
// deliberately separate from Capability, which remains the bounded legacy
// migration vocabulary until persisted grants and credentials have moved to
// action/resource pairs.
type Action = permissions.Action

const (
	ActionDashboardRead    Action = "dashboard.read"
	ActionDashboardCreate  Action = "dashboard.create"
	ActionDashboardUpdate  Action = "dashboard.update"
	ActionDashboardDelete  Action = "dashboard.delete"
	ActionDashboardPublish Action = "dashboard.publish"

	ActionSemanticRead    Action = "semantic.read"
	ActionSemanticQuery   Action = "semantic.query"
	ActionSemanticConsume Action = "semantic.consume"
	ActionSemanticCreate  Action = "semantic.create"
	ActionSemanticUpdate  Action = "semantic.update"
	ActionSemanticDelete  Action = "semantic.delete"

	ActionSourceRead   Action = "source.read"
	ActionSourceCreate Action = "source.create"
	ActionSourceUpdate Action = "source.update"
	ActionSourceDelete Action = "source.delete"

	ActionModelRead   Action = "model.read"
	ActionModelCreate Action = "model.create"
	ActionModelUpdate Action = "model.update"
	ActionModelDelete Action = "model.delete"

	ActionPipelineRead   Action = "pipeline.read"
	ActionPipelineCreate Action = "pipeline.create"
	ActionPipelineRun    Action = "pipeline.run"
	ActionPipelineUpdate Action = "pipeline.update"
	ActionPipelineDelete Action = "pipeline.delete"

	ActionConnectionRead   Action = "connection.read"
	ActionConnectionCreate Action = "connection.create"
	ActionConnectionUse    Action = "connection.use"
	ActionConnectionManage Action = "connection.manage"

	ActionResourceShare Action = "resource.share"

	ActionDeliveryRead     Action = "delivery.read"
	ActionDeliveryPlan     Action = "delivery.plan"
	ActionDeliveryBuild    Action = "delivery.build"
	ActionDeliveryPublish  Action = "delivery.publish"
	ActionDeliveryApprove  Action = "delivery.approve"
	ActionDeliveryActivate Action = "delivery.activate"
	ActionDeliveryRollback Action = "delivery.rollback"

	ActionProjectSettingsRead   Action = "project.settings.read"
	ActionProjectSettingsUpdate Action = "project.settings.update"
	ActionProjectAccessRead     Action = "project.access.read"
	ActionProjectAccessManage   Action = "project.access.manage"
	ActionProjectAccessDelegate Action = "project.access.delegate"
	ActionAuditRead             Action = "audit.read"

	ActionWorkloadDelegate Action = "workload.delegate"

	ActionPlatformSettingsRead   Action = "platform.settings.read"
	ActionPlatformSettingsUpdate Action = "platform.settings.update"
	ActionPlatformAccessRead     Action = "platform.access.read"
	ActionPlatformAccessManage   Action = "platform.access.manage"
	ActionPlatformAuditRead      Action = "platform.audit.read"
)

type PermissionScope = permissions.Scope

const (
	PermissionScopeInstance PermissionScope = "instance"
	PermissionScopeProject  PermissionScope = "project"
	PermissionScopeResource PermissionScope = "resource"
)

// PermissionDefinition is the single machine-readable source for an action's
// identity and structural constraints. ResourceKinds describe the logical
// object family. CheckKinds describe the resource on which authorization is
// evaluated; create actions, for example, concern a Dashboard but are checked
// on its containing Project because the Dashboard does not exist yet.
//
// Prerequisites are additional checks, never implied grants. Delegable marks
// actions eligible for an explicitly bounded delegation envelope; it does not
// make possession of the action sufficient to issue a grant.
type PermissionDefinition struct {
	Action        Action              `json:"action"`
	Family        string              `json:"family"`
	Description   string              `json:"description"`
	Scope         PermissionScope     `json:"scope"`
	ResourceKinds []projectgraph.Kind `json:"resourceKinds,omitempty"`
	CheckKinds    []projectgraph.Kind `json:"checkKinds,omitempty"`
	Prerequisites []Action            `json:"prerequisites,omitempty"`
	Delegable     bool                `json:"delegable"`
	UISelectable  bool                `json:"uiSelectable"`
}

var permissionCatalog = []PermissionDefinition{
	resourcePermission(ActionDashboardRead, "Dashboard", "View an approved dashboard definition and shell.", projectgraph.KindDashboard, true),
	createPermission(ActionDashboardCreate, "Dashboard", "Create a dashboard in the bound Project."),
	resourcePermission(ActionDashboardUpdate, "Dashboard", "Edit an existing dashboard definition.", projectgraph.KindDashboard, true),
	resourcePermission(ActionDashboardDelete, "Dashboard", "Delete or archive an existing dashboard.", projectgraph.KindDashboard, false),
	resourcePermission(ActionDashboardPublish, "Dashboard", "Publish an approved dashboard revision.", projectgraph.KindDashboard, false),

	resourcePermission(ActionSemanticRead, "Semantic consumption", "Discover governed semantic metadata.", projectgraph.KindSemanticModel, true),
	withPrerequisites(resourcePermission(ActionSemanticQuery, "Semantic consumption", "Construct an arbitrary governed semantic query.", projectgraph.KindSemanticModel, true), ActionSemanticConsume),
	resourcePermission(ActionSemanticConsume, "Semantic consumption", "Consume governed data from an exact SemanticModel.", projectgraph.KindSemanticModel, true),
	createPermissionFor(ActionSemanticCreate, "Development", "Create a SemanticModel definition in the bound Project.", projectgraph.KindSemanticModel),
	resourcePermission(ActionSemanticUpdate, "Development", "Update a SemanticModel definition.", projectgraph.KindSemanticModel, true),
	resourcePermission(ActionSemanticDelete, "Development", "Delete a SemanticModel definition.", projectgraph.KindSemanticModel, false),

	resourcePermission(ActionSourceRead, "Development", "Read a Source definition.", projectgraph.KindSource, true),
	createPermissionFor(ActionSourceCreate, "Development", "Create a Source definition in the bound Project.", projectgraph.KindSource),
	resourcePermission(ActionSourceUpdate, "Development", "Update a Source definition.", projectgraph.KindSource, true),
	resourcePermission(ActionSourceDelete, "Development", "Delete a Source definition.", projectgraph.KindSource, false),

	resourcePermission(ActionModelRead, "Development", "Read a Model definition.", projectgraph.KindModel, true),
	createPermissionFor(ActionModelCreate, "Development", "Create a Model definition in the bound Project.", projectgraph.KindModel),
	resourcePermission(ActionModelUpdate, "Development", "Update a Model definition.", projectgraph.KindModel, true),
	resourcePermission(ActionModelDelete, "Development", "Delete a Model definition.", projectgraph.KindModel, false),

	resourcePermission(ActionPipelineRead, "Pipeline", "Read a Pipeline definition and bounded operational status.", projectgraph.KindPipeline, true),
	createPermissionFor(ActionPipelineCreate, "Pipeline", "Create a Pipeline in the bound Project.", projectgraph.KindPipeline),
	resourcePermission(ActionPipelineRun, "Pipeline", "Trigger an approved Pipeline revision.", projectgraph.KindPipeline, true),
	resourcePermission(ActionPipelineUpdate, "Pipeline", "Update a Pipeline definition.", projectgraph.KindPipeline, true),
	resourcePermission(ActionPipelineDelete, "Pipeline", "Delete a Pipeline definition.", projectgraph.KindPipeline, false),

	resourcePermission(ActionConnectionRead, "Connection", "Read redacted Connection metadata.", projectgraph.KindConnection, true),
	createPermissionFor(ActionConnectionCreate, "Connection", "Create a Connection in the bound Project.", projectgraph.KindConnection),
	resourcePermission(ActionConnectionUse, "Connection", "Execute through an approved Connection binding without revealing credentials.", projectgraph.KindConnection, true),
	resourcePermission(ActionConnectionManage, "Connection", "Update, rotate, test, or delete a Connection.", projectgraph.KindConnection, false),

	{
		Action: ActionResourceShare, Family: "Sharing", Description: "Issue a bounded independent grant on an exact supported resource.",
		Scope:         PermissionScopeResource,
		ResourceKinds: []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard},
		CheckKinds:    []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard},
		UISelectable:  true,
	},

	projectPermission(ActionDeliveryRead, "Delivery", "Inspect delivery plans and retained evidence.", true),
	projectPermission(ActionDeliveryPlan, "Delivery", "Persist an exact delivery plan.", true),
	projectPermission(ActionDeliveryBuild, "Delivery", "Build an approved delivery candidate.", true),
	projectPermission(ActionDeliveryPublish, "Delivery", "Publish a built delivery candidate.", true),
	projectPermission(ActionDeliveryApprove, "Delivery", "Approve a protected delivery candidate.", false),
	projectPermission(ActionDeliveryActivate, "Delivery", "Activate an approved delivery publication.", false),
	projectPermission(ActionDeliveryRollback, "Delivery", "Rollback to eligible retained delivery evidence.", false),

	projectPermission(ActionProjectSettingsRead, "Project administration", "Read Project settings.", true),
	projectPermission(ActionProjectSettingsUpdate, "Project administration", "Update Project settings.", false),
	projectPermission(ActionProjectAccessRead, "Project administration", "Inspect Project access assignments.", true),
	projectPermission(ActionProjectAccessManage, "Project administration", "Maintain Project access without unbounded privilege issuance.", false),
	projectPermission(ActionProjectAccessDelegate, "Project administration", "Issue authority within an explicit grant-administration envelope.", false),
	projectPermission(ActionAuditRead, "Project administration", "Read authorized Project audit evidence.", true),

	resourcePermission(ActionWorkloadDelegate, "Workload delegation", "Issue a bounded execution grant for an exact Pipeline and workload principal.", projectgraph.KindPipeline, false),

	instancePermission(ActionPlatformSettingsRead, "Platform administration", "Read instance settings."),
	instancePermission(ActionPlatformSettingsUpdate, "Platform administration", "Update instance settings."),
	instancePermission(ActionPlatformAccessRead, "Platform administration", "Inspect instance access assignments."),
	instancePermission(ActionPlatformAccessManage, "Platform administration", "Manage instance access assignments."),
	instancePermission(ActionPlatformAuditRead, "Platform administration", "Read authorized instance audit evidence."),
}

var permissionMechanicsCatalog = mustCompilePermissionMechanicsCatalog(permissionCatalog)

func mustCompilePermissionMechanicsCatalog(definitions []PermissionDefinition) *permissions.CompiledCatalog {
	compiled, err := permissions.CompileCatalog(PermissionCatalogProfile, permissionMechanicsDefinitions(definitions))
	if err != nil {
		panic(fmt.Sprintf("compile permission mechanics catalog: %v", err))
	}
	return compiled
}

func permissionMechanicsDefinitions(definitions []PermissionDefinition) []permissions.Definition {
	result := make([]permissions.Definition, len(definitions))
	for index, definition := range definitions {
		result[index] = permissions.Definition{
			Action:        definition.Action,
			Scope:         definition.Scope,
			ResourceKinds: permissionMechanicsKinds(definition.ResourceKinds),
			CheckKinds:    permissionMechanicsKinds(definition.CheckKinds),
			Prerequisites: append([]permissions.Action(nil), definition.Prerequisites...),
		}
	}
	return result
}

func permissionMechanicsKinds(kinds []projectgraph.Kind) []permissions.Kind {
	result := make([]permissions.Kind, len(kinds))
	for index, kind := range kinds {
		result[index] = permissions.Kind(kind)
	}
	return result
}

func resourcePermission(action Action, family, description string, kind projectgraph.Kind, delegable bool) PermissionDefinition {
	return PermissionDefinition{
		Action: action, Family: family, Description: description, Scope: PermissionScopeResource,
		ResourceKinds: []projectgraph.Kind{kind}, CheckKinds: []projectgraph.Kind{kind},
		Delegable: delegable, UISelectable: true,
	}
}

func createPermission(action Action, family, description string) PermissionDefinition {
	return createPermissionFor(action, family, description, projectgraph.KindDashboard)
}

func createPermissionFor(action Action, family, description string, kind projectgraph.Kind) PermissionDefinition {
	return PermissionDefinition{
		Action: action, Family: family, Description: description, Scope: PermissionScopeProject,
		ResourceKinds: []projectgraph.Kind{kind}, CheckKinds: []projectgraph.Kind{projectgraph.KindProjectNamespace},
		Delegable: true, UISelectable: true,
	}
}

func projectPermission(action Action, family, description string, delegable bool) PermissionDefinition {
	return PermissionDefinition{
		Action: action, Family: family, Description: description, Scope: PermissionScopeProject,
		ResourceKinds: []projectgraph.Kind{projectgraph.KindProjectNamespace}, CheckKinds: []projectgraph.Kind{projectgraph.KindProjectNamespace},
		Delegable: delegable, UISelectable: true,
	}
}

func instancePermission(action Action, family, description string) PermissionDefinition {
	return PermissionDefinition{Action: action, Family: family, Description: description, Scope: PermissionScopeInstance, UISelectable: true}
}

func withPrerequisites(definition PermissionDefinition, prerequisites ...Action) PermissionDefinition {
	definition.Prerequisites = append([]Action(nil), prerequisites...)
	return definition
}

// PermissionCatalog returns a defensive deep copy in stable contract order.
func PermissionCatalog() []PermissionDefinition {
	result := make([]PermissionDefinition, len(permissionCatalog))
	for index, definition := range permissionCatalog {
		result[index] = clonePermissionDefinition(definition)
	}
	return result
}

// Permission looks up one action without allowing callers to mutate the
// package-owned catalog.
func Permission(action Action) (PermissionDefinition, bool) {
	for _, definition := range permissionCatalog {
		if definition.Action == action {
			return clonePermissionDefinition(definition), true
		}
	}
	return PermissionDefinition{}, false
}

func clonePermissionDefinition(definition PermissionDefinition) PermissionDefinition {
	definition.ResourceKinds = append([]projectgraph.Kind(nil), definition.ResourceKinds...)
	definition.CheckKinds = append([]projectgraph.Kind(nil), definition.CheckKinds...)
	definition.Prerequisites = append([]Action(nil), definition.Prerequisites...)
	return definition
}

// ValidateActionForKind rejects unknown actions and invalid action/resource
// combinations. The kind is the resource on which the authorization check is
// performed, so create actions accept only the Project namespace.
func ValidateActionForKind(action Action, kind projectgraph.Kind) error {
	definition, ok := Permission(action)
	if !ok {
		return fmt.Errorf("%w %q", ErrUnknownPermissionAction, action)
	}
	if definition.Scope == PermissionScopeInstance {
		return fmt.Errorf("%w: instance action %q has no graph resource kind", ErrInvalidPermissionCatalog, action)
	}
	for _, allowed := range definition.CheckKinds {
		if allowed == kind {
			return nil
		}
	}
	return fmt.Errorf("%w: action %q is not valid for check kind %q", ErrInvalidPermissionCatalog, action, kind)
}

// ValidatePermissionCatalog validates the package catalog or a candidate
// future profile. It proves action identity, kinds, prerequisites, and cycles;
// callers must still separately validate operation coverage and migration.
func ValidatePermissionCatalog(definitions []PermissionDefinition) error {
	if len(definitions) == 0 {
		return fmt.Errorf("%w: catalog is empty", ErrInvalidPermissionCatalog)
	}
	byAction := make(map[Action]PermissionDefinition, len(definitions))
	for index, definition := range definitions {
		if !validActionName(definition.Action) {
			return fmt.Errorf("%w: definition %d has invalid action %q", ErrInvalidPermissionCatalog, index, definition.Action)
		}
		if _, exists := byAction[definition.Action]; exists {
			return fmt.Errorf("%w: duplicate action %q", ErrInvalidPermissionCatalog, definition.Action)
		}
		if strings.TrimSpace(definition.Family) == "" || strings.TrimSpace(definition.Description) == "" {
			return fmt.Errorf("%w: action %q lacks presentation metadata", ErrInvalidPermissionCatalog, definition.Action)
		}
		switch definition.Scope {
		case PermissionScopeInstance:
			if len(definition.ResourceKinds) != 0 || len(definition.CheckKinds) != 0 {
				return fmt.Errorf("%w: instance action %q has graph kinds", ErrInvalidPermissionCatalog, definition.Action)
			}
		case PermissionScopeProject, PermissionScopeResource:
			if len(definition.ResourceKinds) == 0 || len(definition.CheckKinds) == 0 {
				return fmt.Errorf("%w: action %q lacks resource/check kinds", ErrInvalidPermissionCatalog, definition.Action)
			}
		default:
			return fmt.Errorf("%w: action %q has scope %q", ErrInvalidPermissionCatalog, definition.Action, definition.Scope)
		}
		if err := validateKinds(definition.Action, "resource", definition.ResourceKinds); err != nil {
			return err
		}
		if err := validateKinds(definition.Action, "check", definition.CheckKinds); err != nil {
			return err
		}
		byAction[definition.Action] = definition
	}
	for _, definition := range definitions {
		seen := make(map[Action]struct{}, len(definition.Prerequisites))
		for _, prerequisite := range definition.Prerequisites {
			if prerequisite == definition.Action {
				return fmt.Errorf("%w: action %q requires itself", ErrInvalidPermissionCatalog, definition.Action)
			}
			if _, duplicate := seen[prerequisite]; duplicate {
				return fmt.Errorf("%w: action %q repeats prerequisite %q", ErrInvalidPermissionCatalog, definition.Action, prerequisite)
			}
			seen[prerequisite] = struct{}{}
			if _, exists := byAction[prerequisite]; !exists {
				return fmt.Errorf("%w: action %q has unknown prerequisite %q", ErrInvalidPermissionCatalog, definition.Action, prerequisite)
			}
		}
	}
	if cycle := permissionCycle(byAction); len(cycle) > 0 {
		return fmt.Errorf("%w: prerequisite cycle %s", ErrInvalidPermissionCatalog, strings.Join(cycle, " -> "))
	}
	return permissions.ValidateCatalog(permissionMechanicsDefinitions(definitions))
}

func validActionName(action Action) bool {
	value := string(action)
	if value == "" || value != strings.ToLower(value) || strings.HasPrefix(value, ".") || strings.HasSuffix(value, ".") || !strings.Contains(value, ".") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '.' || char == '_' {
			continue
		}
		return false
	}
	return !strings.Contains(value, "..")
}

func validateKinds(action Action, label string, kinds []projectgraph.Kind) error {
	seen := make(map[projectgraph.Kind]struct{}, len(kinds))
	for _, kind := range kinds {
		if !kind.Valid() {
			return fmt.Errorf("%w: action %q has invalid %s kind %q", ErrInvalidPermissionCatalog, action, label, kind)
		}
		if _, duplicate := seen[kind]; duplicate {
			return fmt.Errorf("%w: action %q repeats %s kind %q", ErrInvalidPermissionCatalog, action, label, kind)
		}
		seen[kind] = struct{}{}
	}
	return nil
}

func permissionCycle(definitions map[Action]PermissionDefinition) []string {
	const (
		unvisited = iota
		visiting
		visited
	)
	state := make(map[Action]int, len(definitions))
	stack := make([]Action, 0, len(definitions))
	actions := make([]string, 0, len(definitions))
	for action := range definitions {
		actions = append(actions, string(action))
	}
	sort.Strings(actions)
	var visit func(Action) []string
	visit = func(action Action) []string {
		if state[action] == visiting {
			start := 0
			for index, candidate := range stack {
				if candidate == action {
					start = index
					break
				}
			}
			cycle := make([]string, 0, len(stack)-start+1)
			for _, candidate := range stack[start:] {
				cycle = append(cycle, string(candidate))
			}
			return append(cycle, string(action))
		}
		if state[action] == visited {
			return nil
		}
		state[action] = visiting
		stack = append(stack, action)
		for _, prerequisite := range definitions[action].Prerequisites {
			if cycle := visit(prerequisite); len(cycle) > 0 {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[action] = visited
		return nil
	}
	for _, raw := range actions {
		if cycle := visit(Action(raw)); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}
