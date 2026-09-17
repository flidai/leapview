package access

import (
	"fmt"
	"strings"
)

// PermissionRole is a versioned presentation preset over typed actions. It is
// not an authorization bypass: assignments persist the profile and exact
// expansion, and runtime evaluation checks the resulting action/resource
// pairs. These names intentionally do not preserve every legacy ProjectRole.
type PermissionRole string

const (
	PermissionRoleViewer          PermissionRole = "viewer"
	PermissionRoleExplorer        PermissionRole = "explorer"
	PermissionRoleEditor          PermissionRole = "editor"
	PermissionRoleProjectAdmin    PermissionRole = "project_admin"
	PermissionRolePublisher       PermissionRole = "publisher"
	PermissionRoleReleaseApprover PermissionRole = "release_approver"
	PermissionRoleReleaseOperator PermissionRole = "release_operator"
	PermissionRoleAuditor         PermissionRole = "auditor"
)

type PermissionRolePreset struct {
	Role        PermissionRole `json:"role"`
	Profile     string         `json:"profile"`
	Description string         `json:"description"`
	Actions     []Action       `json:"actions"`
}

var permissionRolePresets = []PermissionRolePreset{
	{
		Role: PermissionRoleViewer, Profile: PermissionCatalogProfile,
		Description: "View approved dashboards and consume explicitly scoped semantic data.",
		Actions:     []Action{ActionDashboardRead, ActionSemanticConsume},
	},
	{
		Role: PermissionRoleExplorer, Profile: PermissionCatalogProfile,
		Description: "View approved dashboards and discover and query explicitly scoped semantic data.",
		Actions:     []Action{ActionDashboardRead, ActionSemanticRead, ActionSemanticConsume, ActionSemanticQuery},
	},
	{
		Role: PermissionRoleEditor, Profile: PermissionCatalogProfile,
		Description: "Explore governed data and create or update authored resources without publish, delete, share, run, or administration authority.",
		Actions: []Action{
			ActionDashboardRead, ActionDashboardCreate, ActionDashboardUpdate,
			ActionSemanticRead, ActionSemanticConsume, ActionSemanticQuery, ActionSemanticCreate, ActionSemanticUpdate,
			ActionSourceRead, ActionSourceCreate, ActionSourceUpdate,
			ActionModelRead, ActionModelCreate, ActionModelUpdate,
			ActionPipelineRead, ActionPipelineCreate, ActionPipelineUpdate,
			ActionConnectionRead, ActionConnectionCreate, ActionConnectionUse,
		},
	},
	{
		Role: PermissionRoleProjectAdmin, Profile: PermissionCatalogProfile,
		Description: "Manage Project settings and access without acquiring semantic data or platform authority.",
		Actions: []Action{
			ActionProjectSettingsRead, ActionProjectSettingsUpdate,
			ActionProjectAccessRead, ActionProjectAccessManage, ActionProjectAccessDelegate,
			ActionAuditRead,
		},
	},
	{
		Role: PermissionRolePublisher, Profile: PermissionCatalogProfile,
		Description: "Publish explicitly scoped dashboards without editing, deletion, sharing, or release authority.",
		Actions:     []Action{ActionDashboardRead, ActionDashboardPublish},
	},
	{
		Role: PermissionRoleReleaseApprover, Profile: PermissionCatalogProfile,
		Description: "Inspect and approve protected delivery candidates.",
		Actions:     []Action{ActionDeliveryRead, ActionDeliveryApprove},
	},
	{
		Role: PermissionRoleReleaseOperator, Profile: PermissionCatalogProfile,
		Description: "Plan, build, publish, activate, and roll back qualified releases without approval authority.",
		Actions: []Action{
			ActionDeliveryRead, ActionDeliveryPlan, ActionDeliveryBuild,
			ActionDeliveryPublish, ActionDeliveryActivate, ActionDeliveryRollback,
		},
	},
	{
		Role: PermissionRoleAuditor, Profile: PermissionCatalogProfile,
		Description: "Inspect Project access, delivery evidence, and audit events without mutation authority.",
		Actions:     []Action{ActionProjectAccessRead, ActionDeliveryRead, ActionAuditRead},
	},
}

func PermissionRolePresets() []PermissionRolePreset {
	result := make([]PermissionRolePreset, len(permissionRolePresets))
	for index, preset := range permissionRolePresets {
		preset.Actions = append([]Action(nil), preset.Actions...)
		result[index] = preset
	}
	return result
}

func PermissionRoleActions(role PermissionRole) ([]Action, bool) {
	for _, preset := range permissionRolePresets {
		if preset.Role == role {
			return append([]Action(nil), preset.Actions...), true
		}
	}
	return nil, false
}

func ValidatePermissionRolePresets(presets []PermissionRolePreset, definitions []PermissionDefinition) error {
	knownActions := make(map[Action]struct{}, len(definitions))
	for _, definition := range definitions {
		knownActions[definition.Action] = struct{}{}
	}
	seenRoles := make(map[PermissionRole]struct{}, len(presets))
	for index, preset := range presets {
		if preset.Role == "" || string(preset.Role) != strings.TrimSpace(string(preset.Role)) || strings.TrimSpace(preset.Description) == "" {
			return fmt.Errorf("%w: role preset %d is malformed", ErrInvalidPermissionCatalog, index)
		}
		if preset.Profile != PermissionCatalogProfile {
			return fmt.Errorf("%w: role %q uses unsupported profile %q", ErrInvalidPermissionCatalog, preset.Role, preset.Profile)
		}
		if _, duplicate := seenRoles[preset.Role]; duplicate {
			return fmt.Errorf("%w: duplicate role preset %q", ErrInvalidPermissionCatalog, preset.Role)
		}
		seenRoles[preset.Role] = struct{}{}
		seenActions := make(map[Action]struct{}, len(preset.Actions))
		for _, action := range preset.Actions {
			if _, ok := knownActions[action]; !ok {
				return fmt.Errorf("%w: role %q contains unknown action %q", ErrInvalidPermissionCatalog, preset.Role, action)
			}
			if _, duplicate := seenActions[action]; duplicate {
				return fmt.Errorf("%w: role %q repeats action %q", ErrInvalidPermissionCatalog, preset.Role, action)
			}
			seenActions[action] = struct{}{}
		}
	}
	return nil
}
