package module

import (
	analyticsgen "github.com/flidai/leapview/internal/analytics/api/gen"
	"github.com/flidai/leapview/internal/platform/web/uicommand"
)

// ConnectionUICommandBindings is the analytics-owned identity surface used by
// the project UI adapter. The generated bindings keep browser mutations
// tied to their audited API operation contracts.
type ConnectionUICommandBindings struct {
	Create  uicommand.Binding
	Update  uicommand.Binding
	Test    uicommand.Binding
	Refresh uicommand.Binding
	Enable  uicommand.Binding
	Disable uicommand.Binding
}

func (*Module) ConnectionUICommandBindings() ConnectionUICommandBindings {
	return ConnectionUICommandBindings{
		Create:  analyticsgen.GenUIActionCreateTargetConnectionBinding(),
		Update:  analyticsgen.GenUIActionUpdateTargetConnectionBinding(),
		Test:    analyticsgen.GenUIActionTestTargetConnectionBinding(),
		Refresh: analyticsgen.GenUIActionRefreshTargetConnectionBinding(),
		Enable:  analyticsgen.GenUIActionEnableTargetConnectionBinding(),
		Disable: analyticsgen.GenUIActionDisableTargetConnectionBinding(),
	}
}
