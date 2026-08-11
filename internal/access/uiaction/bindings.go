package uiaction

import (
	accessgen "github.com/flidai/leapview/internal/access/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

var (
	CreateRoleBinding = uicommand.Must("workspace.access.role-binding.create", accessgen.GenCommandOperationCreateRoleBinding())
	UpdateRoleBinding = uicommand.Must("workspace.access.role-binding.update", accessgen.GenCommandOperationUpdateRoleBinding())
	DeleteRoleBinding = uicommand.Must("workspace.access.role-binding.delete", accessgen.GenCommandOperationDeleteRoleBinding())
	CreateGrant       = uicommand.Must("workspace.access.grant.create", accessgen.GenCommandOperationCreateGrant())
	DeleteGrant       = uicommand.Must("workspace.access.grant.delete", accessgen.GenCommandOperationDeleteGrant())
)

func Bindings() []uicommand.Binding {
	return []uicommand.Binding{CreateRoleBinding, UpdateRoleBinding, DeleteRoleBinding, CreateGrant, DeleteGrant}
}
