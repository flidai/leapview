package http

import (
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	"github.com/flidai/leapview/pkg/pagestream"
)

// Publish navigation and pane chrome before the page's analytical queries.
// Editing remains disabled until the exact revision's preview is ready, so a
// late bootstrap cannot overwrite an intervening authoring command.
func builderLoadingPatch(builder uisignals.DashboardBuilderSignal) pagestream.SignalPatch {
	builder.Capabilities = uisignals.DashboardBuilderCapabilitiesSignal{}
	builder.Preview.Loading = true
	builder.Preview.Active = false
	return pagestream.SignalPatch{
		"builder":        builder,
		"builderVisuals": nil,
		"status":         uisignals.DashboardStatus{Loading: true},
	}
}
