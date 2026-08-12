package module

import (
	dashboarduiaction "github.com/flidai/leapview/internal/dashboard/uiaction"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func (*Module) PublicationCommandBindings() map[string]uicommand.Binding {
	return dashboarduiaction.PublicationActions()
}
