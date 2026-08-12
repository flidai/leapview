package module

import (
	"fmt"

	"github.com/flidai/leapview/internal/access"
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// UICommandBindings is the access module's public command identity surface for
// composition into UI-owning capabilities.
type UICommandBindings struct {
	CreateRoleBinding uicommand.Binding
	UpdateRoleBinding uicommand.Binding
	DeleteRoleBinding uicommand.Binding
	CreateGrant       uicommand.Binding
	DeleteGrant       uicommand.Binding
}

func (*Module) WorkspaceCommandPrivileges() (access.WorkspaceCommandPrivileges, error) {
	createRoleBinding, err := generatedWorkspaceUIPrivilege(accessgen.GenCommandOperationCreateRoleBinding())
	if err != nil {
		return access.WorkspaceCommandPrivileges{}, err
	}
	updateRoleBinding, err := generatedWorkspaceUIPrivilege(accessgen.GenCommandOperationUpdateRoleBinding())
	if err != nil {
		return access.WorkspaceCommandPrivileges{}, err
	}
	deleteRoleBinding, err := generatedWorkspaceUIPrivilege(accessgen.GenCommandOperationDeleteRoleBinding())
	if err != nil {
		return access.WorkspaceCommandPrivileges{}, err
	}
	createGrant, err := generatedWorkspaceUIPrivilege(accessgen.GenCommandOperationCreateGrant())
	if err != nil {
		return access.WorkspaceCommandPrivileges{}, err
	}
	deleteGrant, err := generatedWorkspaceUIPrivilege(accessgen.GenCommandOperationDeleteGrant())
	if err != nil {
		return access.WorkspaceCommandPrivileges{}, err
	}
	if createRoleBinding != updateRoleBinding {
		return access.WorkspaceCommandPrivileges{}, fmt.Errorf("workspace role binding upsert operations require different privileges")
	}
	return access.WorkspaceCommandPrivileges{
		RoleBindingUpsert: createRoleBinding, RoleBindingDelete: deleteRoleBinding,
		GrantUpsert: createGrant, GrantDelete: deleteGrant,
	}, nil
}

func generatedWorkspaceUIPrivilege(operationID accessgen.GenCommandOperationID) (access.Privilege, error) {
	contract, ok := accessgen.GetAPIGenCommandRuntimeContract(operationID.APIGenOperationID())
	if !ok || contract.Target == nil || contract.Target.Type != string(access.SecurableWorkspace) || !contract.Exposes(access.OperationSurfaceUI) {
		return "", fmt.Errorf("workspace UI operation %q has an incompatible generated contract", operationID.APIGenOperationID())
	}
	privilege, ok := access.ParsePrivilege(contract.Privilege)
	if !ok {
		return "", fmt.Errorf("workspace UI operation %q has invalid privilege %q", operationID.APIGenOperationID(), contract.Privilege)
	}
	return privilege, nil
}

func (*Module) UICommandBindings() UICommandBindings {
	return UICommandBindings{
		CreateRoleBinding: accessgen.GenUIActionCreateRoleBinding(),
		UpdateRoleBinding: accessgen.GenUIActionUpdateRoleBinding(),
		DeleteRoleBinding: accessgen.GenUIActionDeleteRoleBinding(),
		CreateGrant:       accessgen.GenUIActionCreateGrant(),
		DeleteGrant:       accessgen.GenUIActionDeleteGrant(),
	}
}
