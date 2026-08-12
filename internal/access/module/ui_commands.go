package module

import (
	accessuiaction "github.com/flidai/leapview/internal/access/uiaction"
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

func (*Module) UICommandBindings() UICommandBindings {
	return UICommandBindings{
		CreateRoleBinding: accessuiaction.CreateRoleBinding,
		UpdateRoleBinding: accessuiaction.UpdateRoleBinding,
		DeleteRoleBinding: accessuiaction.DeleteRoleBinding,
		CreateGrant:       accessuiaction.CreateGrant,
		DeleteGrant:       accessuiaction.DeleteGrant,
	}
}
