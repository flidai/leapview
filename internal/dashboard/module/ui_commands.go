package module

import (
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// DashboardAuthoringCommandBinding exposes the generated UI operation through
// the dashboard module boundary. Consumers outside the dashboard capability
// must not import the generated API package to obtain this binding.
func DashboardAuthoringCommandBinding() uicommand.Binding {
	return dashboardgen.GenUIActionExecuteDashboardAuthoringCommand()
}

func (*Module) PublicationCommandBindings() map[string]uicommand.Binding {
	return map[string]uicommand.Binding{
		"suspend": dashboardgen.GenUIActionSuspendDashboardPublication(),
		"resume":  dashboardgen.GenUIActionResumeDashboardPublication(),
		"rotate":  dashboardgen.GenUIActionRotateDashboardPublication(),
	}
}
