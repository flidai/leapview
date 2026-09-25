package access

import (
	"fmt"
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
// deliberately separate from the historical Capability vocabulary.
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
	// ActionInstanceProjectClaim is a one-use bootstrap authority. It permits
	// establishing the instance's first Project claim, not general platform
	// access administration.
	ActionInstanceProjectClaim Action = "instance.project.claim"
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
	DisplayName   string              `json:"displayName"`
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
	resourcePermission(ActionDashboardRead, "View dashboard", "Dashboard", "View an approved dashboard definition and shell.", projectgraph.KindDashboard, true),
	createPermission(ActionDashboardCreate, "Create dashboards", "Dashboard", "Create a dashboard in the bound Project."),
	resourcePermission(ActionDashboardUpdate, "Edit dashboard", "Dashboard", "Edit an existing dashboard definition.", projectgraph.KindDashboard, true),
	resourcePermission(ActionDashboardDelete, "Delete dashboard", "Dashboard", "Delete or archive an existing dashboard.", projectgraph.KindDashboard, false),
	resourcePermission(ActionDashboardPublish, "Publish dashboard", "Dashboard", "Publish an approved dashboard revision.", projectgraph.KindDashboard, false),

	resourcePermission(ActionSemanticRead, "Discover metadata", "Semantic consumption", "Discover governed semantic metadata.", projectgraph.KindSemanticModel, true),
	withPrerequisites(resourcePermission(ActionSemanticQuery, "Build queries", "Semantic consumption", "Construct an arbitrary governed semantic query.", projectgraph.KindSemanticModel, true), ActionSemanticConsume),
	resourcePermission(ActionSemanticConsume, "Use governed data", "Semantic consumption", "Consume governed data from an exact SemanticModel.", projectgraph.KindSemanticModel, true),
	createPermissionFor(ActionSemanticCreate, "Create semantic models", "Development", "Create a SemanticModel definition in the bound Project.", projectgraph.KindSemanticModel),
	resourcePermission(ActionSemanticUpdate, "Edit semantic model", "Development", "Update a SemanticModel definition.", projectgraph.KindSemanticModel, true),
	resourcePermission(ActionSemanticDelete, "Delete semantic model", "Development", "Delete a SemanticModel definition.", projectgraph.KindSemanticModel, false),

	resourcePermission(ActionSourceRead, "View source", "Development", "Read a Source definition.", projectgraph.KindSource, true),
	createPermissionFor(ActionSourceCreate, "Create sources", "Development", "Create a Source definition in the bound Project.", projectgraph.KindSource),
	resourcePermission(ActionSourceUpdate, "Edit source", "Development", "Update a Source definition.", projectgraph.KindSource, true),
	resourcePermission(ActionSourceDelete, "Delete source", "Development", "Delete a Source definition.", projectgraph.KindSource, false),

	resourcePermission(ActionModelRead, "View model", "Development", "Read a Model definition.", projectgraph.KindModel, true),
	createPermissionFor(ActionModelCreate, "Create models", "Development", "Create a Model definition in the bound Project.", projectgraph.KindModel),
	resourcePermission(ActionModelUpdate, "Edit model", "Development", "Update a Model definition.", projectgraph.KindModel, true),
	resourcePermission(ActionModelDelete, "Delete model", "Development", "Delete a Model definition.", projectgraph.KindModel, false),

	resourcePermission(ActionPipelineRead, "View pipeline", "Pipeline", "Read a Pipeline definition and bounded operational status.", projectgraph.KindPipeline, true),
	createPermissionFor(ActionPipelineCreate, "Create pipelines", "Pipeline", "Create a Pipeline in the bound Project.", projectgraph.KindPipeline),
	resourcePermission(ActionPipelineRun, "Run pipeline", "Pipeline", "Trigger an approved Pipeline revision.", projectgraph.KindPipeline, true),
	resourcePermission(ActionPipelineUpdate, "Edit pipeline", "Pipeline", "Update a Pipeline definition.", projectgraph.KindPipeline, true),
	resourcePermission(ActionPipelineDelete, "Delete pipeline", "Pipeline", "Delete a Pipeline definition.", projectgraph.KindPipeline, false),

	resourcePermission(ActionConnectionRead, "View connection details", "Connection", "Read redacted Connection metadata.", projectgraph.KindConnection, true),
	createPermissionFor(ActionConnectionCreate, "Create connections", "Connection", "Create a Connection in the bound Project.", projectgraph.KindConnection),
	resourcePermission(ActionConnectionUse, "Use connection", "Connection", "Execute through an approved Connection binding without revealing credentials.", projectgraph.KindConnection, true),
	resourcePermission(ActionConnectionManage, "Manage connection", "Connection", "Update, rotate, test, or delete a Connection.", projectgraph.KindConnection, false),

	{
		Action: ActionResourceShare, DisplayName: "Share resource", Family: "Sharing", Description: "Issue a bounded independent grant on an exact supported resource.",
		Scope:         PermissionScopeResource,
		ResourceKinds: []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard},
		CheckKinds:    []projectgraph.Kind{projectgraph.KindConnection, projectgraph.KindSource, projectgraph.KindModel, projectgraph.KindSemanticModel, projectgraph.KindPipeline, projectgraph.KindDashboard},
		UISelectable:  true,
	},

	projectPermission(ActionDeliveryRead, "View releases", "Delivery", "Inspect delivery plans and retained evidence.", true),
	projectPermission(ActionDeliveryPlan, "Plan releases", "Delivery", "Persist an exact delivery plan.", true),
	projectPermission(ActionDeliveryBuild, "Build releases", "Delivery", "Build an approved delivery candidate.", true),
	projectPermission(ActionDeliveryPublish, "Publish releases", "Delivery", "Publish a built delivery candidate.", true),
	projectPermission(ActionDeliveryApprove, "Approve releases", "Delivery", "Approve a protected delivery candidate.", false),
	projectPermission(ActionDeliveryActivate, "Activate releases", "Delivery", "Activate an approved delivery publication.", false),
	projectPermission(ActionDeliveryRollback, "Roll back releases", "Delivery", "Rollback to eligible retained delivery evidence.", false),

	projectPermission(ActionProjectSettingsRead, "View project settings", "Project administration", "Read Project settings.", true),
	projectPermission(ActionProjectSettingsUpdate, "Update project settings", "Project administration", "Update Project settings.", false),
	projectPermission(ActionProjectAccessRead, "View project access", "Project administration", "Inspect Project access assignments.", true),
	projectPermission(ActionProjectAccessManage, "Manage project access", "Project administration", "Maintain Project access without unbounded privilege issuance.", false),
	projectPermission(ActionProjectAccessDelegate, "Delegate project access", "Project administration", "Issue authority within an explicit grant-administration envelope.", false),
	projectPermission(ActionAuditRead, "View audit log", "Project administration", "Read authorized Project audit evidence.", true),

	resourcePermission(ActionWorkloadDelegate, "Delegate workload", "Workload delegation", "Issue a bounded execution grant for an exact Pipeline and workload principal.", projectgraph.KindPipeline, false),

	instancePermission(ActionPlatformSettingsRead, "View platform settings", "Platform administration", "Read instance settings."),
	instancePermission(ActionPlatformSettingsUpdate, "Update platform settings", "Platform administration", "Update instance settings."),
	instancePermission(ActionPlatformAccessRead, "View platform access", "Platform administration", "Inspect instance access assignments."),
	instancePermission(ActionPlatformAccessManage, "Manage platform access", "Platform administration", "Manage instance access assignments."),
	instancePermission(ActionPlatformAuditRead, "View platform audit log", "Platform administration", "Read authorized instance audit evidence."),
	{Action: ActionInstanceProjectClaim, DisplayName: "Claim first project", Family: "Instance bootstrap", Description: "Establish the first Project claim for this instance.", Scope: PermissionScopeInstance},
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

func resourcePermission(action Action, displayName, family, description string, kind projectgraph.Kind, delegable bool) PermissionDefinition {
	return PermissionDefinition{
		Action: action, DisplayName: displayName, Family: family, Description: description, Scope: PermissionScopeResource,
		ResourceKinds: []projectgraph.Kind{kind}, CheckKinds: []projectgraph.Kind{kind},
		Delegable: delegable, UISelectable: true,
	}
}

func createPermission(action Action, displayName, family, description string) PermissionDefinition {
	return createPermissionFor(action, displayName, family, description, projectgraph.KindDashboard)
}

func createPermissionFor(action Action, displayName, family, description string, kind projectgraph.Kind) PermissionDefinition {
	return PermissionDefinition{
		Action: action, DisplayName: displayName, Family: family, Description: description, Scope: PermissionScopeProject,
		ResourceKinds: []projectgraph.Kind{kind}, CheckKinds: []projectgraph.Kind{projectgraph.KindProjectNamespace},
		Delegable: true, UISelectable: true,
	}
}

func projectPermission(action Action, displayName, family, description string, delegable bool) PermissionDefinition {
	return PermissionDefinition{
		Action: action, DisplayName: displayName, Family: family, Description: description, Scope: PermissionScopeProject,
		ResourceKinds: []projectgraph.Kind{projectgraph.KindProjectNamespace}, CheckKinds: []projectgraph.Kind{projectgraph.KindProjectNamespace},
		Delegable: delegable, UISelectable: true,
	}
}

func instancePermission(action Action, displayName, family, description string) PermissionDefinition {
	return PermissionDefinition{Action: action, DisplayName: displayName, Family: family, Description: description, Scope: PermissionScopeInstance, UISelectable: true}
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
// future profile. Structural action, scope, prerequisite, and cycle checks
// belong to pkg/permissions. Access also checks its presentation metadata and
// the closed set of graph kinds before handing over the structural contract.
// Callers must still separately validate operation coverage and migration.
func ValidatePermissionCatalog(definitions []PermissionDefinition) error {
	for _, definition := range definitions {
		if strings.TrimSpace(definition.DisplayName) == "" || strings.TrimSpace(definition.Family) == "" || strings.TrimSpace(definition.Description) == "" {
			return fmt.Errorf("%w: action %q lacks presentation metadata", ErrInvalidPermissionCatalog, definition.Action)
		}
		for _, kind := range definition.ResourceKinds {
			if !kind.Valid() {
				return fmt.Errorf("%w: action %q has unknown resource kind %q", ErrInvalidPermissionCatalog, definition.Action, kind)
			}
		}
		for _, kind := range definition.CheckKinds {
			if !kind.Valid() {
				return fmt.Errorf("%w: action %q has unknown check kind %q", ErrInvalidPermissionCatalog, definition.Action, kind)
			}
		}
	}
	return permissions.ValidateCatalog(permissionMechanicsDefinitions(definitions))
}
