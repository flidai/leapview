package module

import (
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

func (*Module) UICommandBindings() UICommandBindings {
	return UICommandBindings{
		CreateRoleBinding: accessgen.GenUIActionCreateRoleBinding(),
		UpdateRoleBinding: accessgen.GenUIActionUpdateRoleBinding(),
		DeleteRoleBinding: accessgen.GenUIActionDeleteRoleBinding(),
		CreateGrant:       accessgen.GenUIActionCreateGrant(),
		DeleteGrant:       accessgen.GenUIActionDeleteGrant(),
	}
}
