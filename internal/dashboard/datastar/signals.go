package datastar

import visualizationir "github.com/flidai/leapview/internal/dashboard/visualization/ir"

// VisualizationSignal is the dashboard-owned browser transport projection for
// a visualization envelope.
type VisualizationSignal struct {
	SchemaVersion       int32                                               `json:"schemaVersion"`
	ExploreHref         string                                              `json:"exploreHref"`
	VisualID            string                                              `json:"visualID"`
	RendererID          string                                              `json:"rendererID"`
	SpecRevision        string                                              `json:"specRevision"`
	Spec                visualizationir.VisualizationSpec                   `json:"spec"`
	DataRevision        int64                                               `json:"dataRevision"`
	DataState           visualizationir.VisualizationDataStateTransport     `json:"dataState"`
	Selection           []visualizationir.VisualizationSelectionEntry       `json:"selection"`
	Highlights          []visualizationir.VisualizationHighlightState       `json:"highlights"`
	SpatialSelection    *visualizationir.VisualizationSpatialSelectionState `json:"spatialSelection,omitempty"`
	Status              visualizationir.VisualizationStatus                 `json:"status"`
	Diagnostics         []visualizationir.VisualizationDiagnostic           `json:"diagnostics"`
	ServingStateID      string                                              `json:"servingStateID"`
	StreamGeneration    int64                                               `json:"streamGeneration"`
	FilterRevision      int64                                               `json:"filterRevision"`
	InteractionRevision int64                                               `json:"interactionRevision"`
	ConsumerIdentity    string                                              `json:"consumerIdentity"`
}
