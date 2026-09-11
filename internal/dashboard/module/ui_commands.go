package module

import (
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

func (*Module) PublicationCommandBindings() map[string]uicommand.Binding {
	return map[string]uicommand.Binding{
		"suspend": dashboardgen.GenUIActionSuspendDashboardPublication(),
		"resume":  dashboardgen.GenUIActionResumeDashboardPublication(),
		"rotate":  dashboardgen.GenUIActionRotateDashboardPublication(),
	}
}
