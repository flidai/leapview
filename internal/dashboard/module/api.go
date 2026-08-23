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

// ListDashboards resolves the active serving snapshot before dispatching the
// catalog list. Lists are paginated against an immutable snapshot just like
// dashboard query endpoints, so they must not bypass the module boundary.
func (m *Module) ListDashboards(w http.ResponseWriter, r *http.Request) {
	m.setServingSnapshot(r, "")
	m.handler.ListDashboards(w, r)
}

// ListSemanticModels resolves the active serving snapshot before dispatching
// the semantic-model catalog list. Keeping this on Module prevents callers
// from selecting an arbitrary snapshot through the transport header.
func (m *Module) ListSemanticModels(w http.ResponseWriter, r *http.Request) {
	m.setServingSnapshot(r, "")
	m.semantic.ListSemanticModels(w, r)
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
