package module

import (
	"net/http"

	dashboardgen "github.com/flidai/leapview/internal/dashboard/api/gen"
)

func (m *Module) QuerySemanticModel(w http.ResponseWriter, r *http.Request, modelID string) {
	m.setServingSnapshot(r, modelID)
	m.semantic.QuerySemanticModel(w, r)
}

func (m *Module) PreviewSemanticDataset(w http.ResponseWriter, r *http.Request, modelID, datasetID string) {
	m.setServingSnapshot(r, modelID)
	m.semantic.PreviewSemanticDataset(w, r)
}

func (m *Module) QueryDashboardPage(w http.ResponseWriter, r *http.Request, dashboardID, pageID string) {
	m.setServingSnapshot(r, dashboardID)
	m.handler.QueryDashboardPage(w, r)
}

func (m *Module) QueryDashboardVisualData(w http.ResponseWriter, r *http.Request, dashboardID, pageID, visualID string) {
	m.setServingSnapshot(r, dashboardID)
	m.handler.QueryDashboardVisualData(w, r)
}

func (m *Module) ListDashboardFilterValues(w http.ResponseWriter, r *http.Request, dashboardID, pageID, filterID string, _ dashboardgen.GenListDashboardFilterValuesParams) {
	m.setServingSnapshot(r, dashboardID)
	m.handler.ListDashboardFilterOptions(w, r)
}

func (m *Module) setServingSnapshot(r *http.Request, _ string) {
	r.Header.Del("X-Serving-Snapshot")
	if m == nil || m.snapshot == nil {
		return
	}
	if snapshot, err := m.snapshot(r.Context()); err == nil && snapshot != "" {
		r.Header.Set("X-Serving-Snapshot", snapshot)
	}
}
