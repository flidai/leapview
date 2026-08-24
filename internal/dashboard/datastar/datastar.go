package datastar

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/flidai/leapview/internal/dashboard"
	dashboardstream "github.com/flidai/leapview/internal/dashboard/stream"
	uisignals "github.com/flidai/leapview/internal/dashboard/ui/signals"
	visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"
	webtransport "github.com/flidai/leapview/internal/platform/web/transport"
	"github.com/flidai/leapview/pkg/pagestream"
)

func DashboardID(r *http.Request, signals dashboard.Signals, defaultID string) string {
	if id := r.URL.Query().Get("dashboard"); id != "" {
		return id
	}
	if signals.Runtime.DashboardID != "" {
		return signals.Runtime.DashboardID
	}
	return defaultID
}

func PageID(r *http.Request, signals dashboard.Signals) string {
	if id := r.URL.Query().Get("page"); id != "" {
		return id
	}
	if signals.Runtime.PageID != "" {
		return signals.Runtime.PageID
	}
	return ""
}

func ModelID(r *http.Request, signals dashboard.Signals, dashboardID string, defaultForDashboard func(string) string) string {
	if id := r.URL.Query().Get("model"); id != "" {
		return id
	}
	if signals.Runtime.ModelID != "" {
		return signals.Runtime.ModelID
	}
	return defaultForDashboard(dashboardID)
}

func ClientStreamID(r *http.Request, signals dashboard.Signals, dashboardID, pageID string) string {
	instanceID := signals.Runtime.StreamInstanceID
	if instanceID == "" {
		instanceID = r.URL.Query().Get("streamInstance")
	}
	return StreamID(webtransport.ClientIDFromRequest(r, signals.Runtime.ClientID), dashboardID, pageID, instanceID)
}

func StreamID(clientID, dashboardID, pageID string, streamInstanceID ...string) string {
	if len(streamInstanceID) > 0 && streamInstanceID[0] != "" {
		return clientID + ":" + dashboardID + ":view:" + streamInstanceID[0]
	}
	return clientID + ":" + dashboardID + ":" + pageID
}

func LoadingPatch() pagestream.SignalPatch {
	return pagestream.SignalPatch{
		"status": map[string]any{
			"loading":         true,
			"error":           "",
			"refreshId":       "",
			"generation":      int64(0),
			"lastUpdated":     "",
			"setupRequired":   false,
			"progressPercent": dashboard.NormalizeProgressPercent(nil, true),
		},
	}
}

func RefreshEventPatch(event dashboardstream.RefreshEvent) pagestream.SignalPatch {
	generation := int64(event.Generation)
	status := func(loading bool, err error) map[string]any {
		message := ""
		if err != nil {
			message = err.Error()
		}
		return map[string]any{
			"loading":         loading,
			"error":           message,
			"refreshId":       event.RefreshID,
			"generation":      generation,
			"setupRequired":   errorSetupRequired(err),
			"progressPercent": dashboard.NormalizeProgressPercent(event.ProgressPercent, loading),
		}
	}
	switch event.Type {
	case dashboardstream.RefreshEventStart:
		visuals := map[string]any{}
		for _, target := range event.Targets {
			if strings.HasPrefix(target, "visual:") {
				visuals[visualStatusKey(target)] = map[string]any{"status": map[string]any{"kind": "loading"}}
			}
		}
		return pagestream.SignalPatch{
			"interactionSelections": uisignals.DashboardInteractionSelectionsFromDashboard(event.Filters.Selections),
			"interactionRevision":   int64(event.Filters.InteractionRevision),
			"spatialSelections":     uisignals.DashboardSpatialSelectionsFromDashboard(event.Filters.SpatialSelections),
			"status":                status(true, nil),
			"visuals":               visuals,
		}
	case dashboardstream.RefreshEventProgress:
		return pagestream.SignalPatch{"status": status(true, nil)}
	case dashboardstream.RefreshEventVisual:
		envelope := visualizationEnvelopeSignal(event)
		return pagestream.SignalPatch{
			"visuals": map[string]VisualizationSignal{event.Target: envelope},
		}
	case dashboardstream.RefreshEventVisualMetadata:
		envelope := visualizationEnvelopeSignal(event)
		return pagestream.SignalPatch{"visuals": map[string]VisualizationSignal{event.Target: envelope}}
	case dashboardstream.RefreshEventTargetError:
		if event.Target == "refresh" {
			return pagestream.SignalPatch{"status": status(false, event.Err)}
		}
		message := ""
		if event.Err != nil {
			message = event.Err.Error()
		}
		return pagestream.SignalPatch{"visuals": map[string]any{visualStatusKey(event.Target): map[string]any{
			"status":      map[string]any{"kind": "error", "message": message},
			"diagnostics": []map[string]any{{"severity": "error", "code": "query_failed", "message": message}},
		}}}
	case dashboardstream.RefreshEventComplete:
		return pagestream.SignalPatch{"status": status(false, event.Err)}
	default:
		return pagestream.SignalPatch{}
	}
}

func visualizationEnvelopeSignal(event dashboardstream.RefreshEvent) VisualizationSignal {
	envelope, ok := event.Value.(visualizationir.VisualizationEnvelope)
	if !ok {
		panic(fmt.Sprintf("dashboard visualization %q has invalid envelope value %T", event.Target, event.Value))
	}
	transport, err := visualizationir.EncodeDataStateTransport(envelope.DataState)
	if err != nil {
		panic(fmt.Sprintf("encode dashboard visualization data-state transport: %v", err))
	}
	return VisualizationSignal{
		SchemaVersion: envelope.SchemaVersion, VisualID: envelope.VisualID, RendererID: envelope.RendererID,
		SpecRevision: envelope.SpecRevision, Spec: envelope.Spec, DataRevision: envelope.DataRevision,
		DataState: visualizationir.VisualizationDataStateTransport{
			SchemaVersion: transport.SchemaVersion, Encoding: visualizationir.VisualizationDataStateTransportEncoding(transport.Encoding),
			Kind: visualizationir.VisualizationDataStateKind(transport.Kind), SpecRevision: transport.SpecRevision,
			DataRevision: transport.DataRevision, Generation: transport.Generation, Payload: transport.Payload,
		},
		Selection: envelope.Selection, Highlights: envelope.Highlights, SpatialSelection: envelope.SpatialSelection,
		Status: envelope.Status, Diagnostics: envelope.Diagnostics,
		StreamGeneration: int64(event.Generation), FilterRevision: event.FilterRevision,
		InteractionRevision: int64(event.Filters.InteractionRevision),
		ServingStateID:      event.ServingStateID, ConsumerIdentity: event.Target,
	}
}

// RefreshEventEnvelope keeps refresh ordering and mailbox behavior outside the
// signal payload. The browser receives Signals only; pagestream consumes the
// explicit delivery metadata.
func RefreshEventEnvelope(event dashboardstream.RefreshEvent) dashboardstream.Envelope {
	generation := uint64(0)
	if event.Generation > 0 {
		generation = uint64(event.Generation)
	}
	delivery := dashboardstream.DeliveryMetadata{Generation: generation}
	switch event.Type {
	case dashboardstream.RefreshEventStart, dashboardstream.RefreshEventProgress, dashboardstream.RefreshEventComplete:
		delivery.Boundary = true
	case dashboardstream.RefreshEventTargetError:
		if event.Target == "refresh" {
			delivery.Boundary = true
		} else {
			delivery.CoalesceGroup = "dashboard-results"
			delivery.MergeRoots = dashboardMergeRoots()
		}
	default:
		delivery.CoalesceGroup = "dashboard-results"
		delivery.MergeRoots = dashboardMergeRoots()
	}
	return dashboardstream.Envelope{
		Signals:  RefreshEventPatch(event),
		Delivery: delivery,
	}
}

func dashboardMergeRoots() []string {
	return []string{"visuals"}
}

func visualStatusKey(target string) string {
	return strings.TrimPrefix(target, "visual:")
}

func errorSetupRequired(err error) bool {
	var setup interface{ SetupRequired() bool }
	return errors.As(err, &setup) && setup.SetupRequired()
}
