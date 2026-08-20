package http

import (
	"fmt"
	nethttp "net/http"
	"sort"

	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/go-chi/chi/v5"
)

func (h Handler) ListSemanticModels(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	catalog := metrics.Catalog()
	out := make([]api.SemanticModelSummary, 0, len(catalog.Models))
	for _, row := range catalog.Models {
		out = append(out, semanticModelSummaryDTO(row))
	}
	filtered := make([]api.SemanticModelSummary, 0, len(out))
	for _, row := range out {
		allowed, err := h.authorizeSemanticModel(r, row.ID)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusServiceUnavailable)
			return
		}
		if allowed {
			filtered = append(filtered, row)
		}
	}
	out = filtered
	page, nextCursor, ok := pageSliceForRequest(w, r, out)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticModelListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) GetSemanticModel(w nethttp.ResponseWriter, r *nethttp.Request) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return
	}
	modelID := chi.URLParam(r, "model")
	model, ok := SemanticModelProjection(metrics, modelID)
	if !ok {
		if semanticModelActivationUnavailable(metrics, modelID) {
			writeJSONError(w, errSemanticModelActivationUnavailable, nethttp.StatusServiceUnavailable)
			return
		}
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return
	}
	writeJSON(w, nethttp.StatusOK, model)
}

func (h Handler) ListSemanticModelFields(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	fields := SemanticModelFieldsProjection(model)
	items, nextCursor, ok := pageSliceForRequest(w, r, fields)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticFieldListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) ListSemanticRelationships(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	items := make([]api.SemanticRelationshipResponse, 0, len(model.Relationships))
	for _, relationship := range model.Relationships {
		item, err := semanticRelationshipDTO(relationship)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusInternalServerError)
			return
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticRelationshipListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}

func (h Handler) ListSemanticSources(w nethttp.ResponseWriter, r *nethttp.Request) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return
	}
	names := make([]string, 0, len(model.Sources))
	for name := range model.Sources {
		names = append(names, name)
	}
	sort.Strings(names)
	items := make([]api.SemanticSourceResponse, 0, len(names))
	for _, name := range names {
		source := model.Sources[name]
		items = append(items, api.SemanticSourceResponse{
			ID: name, Kind: source.Format, Connection: source.Connection,
			Table: source.Object, Description: source.Description,
		})
	}
	page, nextCursor, ok := pageSliceForRequest(w, r, items)
	if !ok {
		return
	}
	writeJSON(w, nethttp.StatusOK, api.SemanticSourceListResponse{Items: page, Page: api.PageInfo{NextCursor: nextCursor}})
}
