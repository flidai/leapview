package module

import (
	"net/http"
)

func (m *Module) QuerySemanticModel(w http.ResponseWriter, r *http.Request, workspaceID string) {
	m.setServingSnapshot(r, workspaceID)
	m.semantic.QuerySemanticModel(w, r)
}

func (m *Module) PreviewSemanticDataset(w http.ResponseWriter, r *http.Request, workspaceID string) {
	m.setServingSnapshot(r, workspaceID)
	m.semantic.PreviewSemanticDataset(w, r)
}

func (m *Module) QueryDashboardPage(w http.ResponseWriter, r *http.Request, workspaceID string) {
	m.setServingSnapshot(r, workspaceID)
	m.handler.QueryDashboardPage(w, r)
}

func (m *Module) QueryDashboardVisualData(w http.ResponseWriter, r *http.Request, workspaceID string) {
	m.setServingSnapshot(r, workspaceID)
	m.handler.QueryDashboardVisualData(w, r)
}

func (m *Module) ListDashboardFilterValues(w http.ResponseWriter, r *http.Request, workspaceID string) {
	m.setServingSnapshot(r, workspaceID)
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
