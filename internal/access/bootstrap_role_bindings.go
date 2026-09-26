package access

import projectgraph "github.com/flidai/leapview/internal/project/graph"

// These are the only role bindings the project-claim bootstrap may issue
// without a grant-administration envelope. Their subject is always the
// principal who made the durable project claim.
const (
	BootstrapOwnerBindingID             = "project-bootstrap-owner"
	BootstrapOwnerBindingName           = "Project bootstrap owner"
	BootstrapEditorBindingID            = "project-bootstrap-editor"
	BootstrapEditorBindingName          = "Project bootstrap editor"
	BootstrapReleaseOperatorBindingID   = "project-bootstrap-release-operator"
	BootstrapReleaseOperatorBindingName = "Project bootstrap release operator"
)

// IsProjectClaimBootstrapBinding deliberately compares every authority-bearing
// field, including the versioned role expansion, against the canonical role.
func IsProjectClaimBootstrapBinding(binding RoleBinding, projectID projectgraph.ResourceID, claimingPrincipalID string) bool {
	if binding.Subject != (SubjectRef{Kind: SubjectKindPrincipal, ID: claimingPrincipalID}) || binding.Role != "" || binding.Capabilities != nil || binding.PermissionProfile != PermissionCatalogProfile {
		return false
	}
	var name string
	var role PermissionRole
	switch binding.ID {
	case BootstrapOwnerBindingID:
		name, role = BootstrapOwnerBindingName, PermissionRoleProjectAdmin
	case BootstrapEditorBindingID:
		name, role = BootstrapEditorBindingName, PermissionRoleEditor
	case BootstrapReleaseOperatorBindingID:
		name, role = BootstrapReleaseOperatorBindingName, PermissionRoleReleaseOperator
	default:
		return false
	}
	if binding.Name != name || binding.PermissionRole != role {
		return false
	}
	expected, err := ExpandPermissionRole(role, projectID)
	if err != nil || len(expected) != len(binding.Permissions) {
		return false
	}
	for index := range expected {
		if binding.Permissions[index].Key() != expected[index].Key() {
			return false
		}
	}
	return true
}
