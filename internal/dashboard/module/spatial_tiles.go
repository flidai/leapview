package module

import (
	"compress/gzip"
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	dashboardruntime "github.com/flidai/leapview/internal/dashboard/runtime"
	"github.com/go-chi/chi/v5"
)

type visualizationTileMetrics interface {
	QueryVisualizationTile(context.Context, string, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
}

type publicVisualizationTileMetrics interface {
	QueryPublicVisualizationTile(context.Context, string, string, string, string, int, int, int) (dashboardruntime.SpatialTileResult, error)
}

func (m *Module) VisualizationTile(w http.ResponseWriter, r *http.Request) {
	workspaceID, dashboardID := chi.URLParam(r, "workspace"), chi.URLParam(r, "dashboard")
	visualID, revision := chi.URLParam(r, "visual"), chi.URLParam(r, "revision")
	zoom, zoomErr := strconv.Atoi(chi.URLParam(r, "z"))
	x, xErr := strconv.Atoi(chi.URLParam(r, "x"))
	y, yErr := strconv.Atoi(chi.URLParam(r, "y"))
	if zoomErr != nil || xErr != nil || yErr != nil || zoom < 0 || zoom > 18 {
		spatialTileFailure(w, http.StatusBadRequest, "invalid tile coordinates")
		return
	}
	world := 1 << zoom
	if x < 0 || y < 0 || x >= world || y >= world || workspaceID == "" || dashboardID == "" || visualID == "" || revision == "" {
		spatialTileFailure(w, http.StatusBadRequest, "invalid tile coordinates")
		return
	}
	metrics, ok := m.runtimeMetrics.(visualizationTileMetrics)
	if !ok {
		spatialTileFailure(w, http.StatusServiceUnavailable, "spatial tile runtime unavailable")
		return
	}
	ctx := r.Context()
	if m.handler.AnalyticalContext != nil {
		ctx = m.handler.AnalyticalContext(ctx)
	}
	result, err := metrics.QueryVisualizationTile(ctx, workspaceID, dashboardID, visualID, revision, zoom, x, y)
	if err != nil {
		m.observeSpatialTile("error", dashboardruntime.SpatialTileResult{})
		spatialTileFailure(w, http.StatusNotFound, "tile revision unavailable")
		return
	}
	m.observeSpatialTile("success", result)
	writeSpatialTile(w, r, result, "private, immutable")
}

func (m *Module) PublicVisualizationTile(w http.ResponseWriter, r *http.Request) {
	publicID, visualID, revision := chi.URLParam(r, "publicId"), chi.URLParam(r, "visual"), chi.URLParam(r, "revision")
	zoom, zoomErr := strconv.Atoi(chi.URLParam(r, "z"))
	x, xErr := strconv.Atoi(chi.URLParam(r, "x"))
	y, yErr := strconv.Atoi(chi.URLParam(r, "y"))
	if zoomErr != nil || xErr != nil || yErr != nil || zoom < 0 || zoom > 18 || publicID == "" || visualID == "" || revision == "" {
		spatialTileFailure(w, http.StatusBadRequest, "invalid tile coordinates")
		return
	}
	world := 1 << zoom
	if x < 0 || y < 0 || x >= world || y >= world {
		spatialTileFailure(w, http.StatusBadRequest, "invalid tile coordinates")
		return
	}
	resolved, err := m.ResolvePublicDashboard(r.Context(), publicID)
	if err != nil {
		spatialTileFailure(w, http.StatusNotFound, "tile revision unavailable")
		return
	}
	metrics, ok := resolved.Metrics.(publicVisualizationTileMetrics)
	if !ok {
		spatialTileFailure(w, http.StatusServiceUnavailable, "spatial tile runtime unavailable")
		return
	}
	ctx := PublicationExecutionContext(r.Context(), resolved.Publication, resolved.ModelID)
	if m.handler.AnalyticalContext != nil {
		ctx = m.handler.AnalyticalContext(ctx)
	}
	result, err := metrics.QueryPublicVisualizationTile(ctx, publicID, resolved.Publication.Dashboard, visualID, revision, zoom, x, y)
	if err != nil {
		m.observeSpatialTile("error", dashboardruntime.SpatialTileResult{})
		spatialTileFailure(w, http.StatusNotFound, "tile revision unavailable")
		return
	}
	m.observeSpatialTile("success", result)
	writeSpatialTile(w, r, result, "public, immutable")
}

func (m *Module) observeSpatialTile(outcome string, result dashboardruntime.SpatialTileResult) {
	if m != nil && m.dashboardTelemetry != nil {
		m.dashboardTelemetry.SpatialTileObserved(outcome, result.CacheOutcome, result.Precision, result.QueryMS, result.EncodingMS, len(result.Bytes), result.Features, result.Fallback)
	}
}

func writeSpatialTile(w http.ResponseWriter, r *http.Request, result dashboardruntime.SpatialTileResult, cacheControl string) {
	w.Header().Set("Content-Type", "application/vnd.mapbox-vector-tile")
	w.Header().Set("Cache-Control", cacheControl)
	w.Header().Set("Vary", "Accept-Encoding")
	w.Header().Set("X-LeapView-Tile-Precision", result.Precision)
	w.Header().Set("X-LeapView-Tile-Features", strconv.Itoa(result.Features))
	w.Header().Set("X-LeapView-Tile-Cache", result.CacheOutcome)
	if strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") && len(result.Bytes) > 0 {
		w.Header().Set("Content-Encoding", "gzip")
		writer := gzip.NewWriter(w)
		_, writeErr := writer.Write(result.Bytes)
		closeErr := writer.Close()
		if writeErr != nil || closeErr != nil {
			return
		}
		return
	}
	w.Header().Set("Content-Length", strconv.Itoa(len(result.Bytes)))
	_, _ = w.Write(result.Bytes)
}

func spatialTileFailure(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Cache-Control", "no-store")
	http.Error(w, fmt.Sprintf("%s\n", message), status)
}
