package uiaction

import (
	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

var (
	SuspendPublication = uicommand.Must("dashboard.publication.suspend", dashboardgen.GenCommandOperationSuspendDashboardPublication())
	ResumePublication  = uicommand.Must("dashboard.publication.resume", dashboardgen.GenCommandOperationResumeDashboardPublication())
	RotatePublication  = uicommand.Must("dashboard.publication.rotate", dashboardgen.GenCommandOperationRotateDashboardPublication())
)

func PublicationActions() map[string]uicommand.Binding {
	return map[string]uicommand.Binding{
		"suspend": SuspendPublication,
		"resume":  ResumePublication,
		"rotate":  RotatePublication,
	}
}

func Bindings() []uicommand.Binding {
	return []uicommand.Binding{SuspendPublication, ResumePublication, RotatePublication}
}
