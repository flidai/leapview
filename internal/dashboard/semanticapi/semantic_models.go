package http

import (
	"fmt"
	nethttp "net/http"
	"sort"

	semanticquery "github.com/flidai/leapview/internal/analytics/query"
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
		model := semanticModelForID(metrics, row.ID)
		planner, plannerOK := semanticPlanner(metrics, row.ID)
		if model == nil || !plannerOK || planner == nil || planner.CompiledModel() == nil || !planner.CompiledModel().MatchesModel(model) {
			// Catalog entries without a current, lineage-matching activation
			// snapshot are not authoritative metadata and must not be listed.
			continue
		}
		allowed, err := h.authorizeSemanticModelResource(r, row.ID)
		if err != nil {
			writeJSONError(w, err, nethttp.StatusServiceUnavailable)
			return
		}
		if allowed {
			scoped := r.Context()
			consumer, consumerOK := (*semanticquery.SemanticAccessConsumer)(nil), false
			if _, hasProvider := any(metrics).(semanticContextConsumerProvider); hasProvider {
				var consumerErr error
				scoped, consumerErr = semanticConsumerForRequest(scoped, metrics, row.ID)
				if consumerErr != nil {
					writeJSONError(w, consumerErr, nethttp.StatusServiceUnavailable)
					return
				}
				consumer, consumerOK = semanticConsumerFromContext(scoped, row.ID)
				if !consumerOK {
					writeJSONError(w, errSemanticConsumerUnavailable, nethttp.StatusServiceUnavailable)
					return
				}
			}
			if protectedSemanticModel(metrics, row.ID) {
				if !consumerOK {
					writeJSONError(w, errSemanticConsumerUnavailable, nethttp.StatusServiceUnavailable)
					return
				}
				assets, assetsErr := consumer.Assets()
				if assetsErr != nil {
					writeJSONError(w, assetsErr, nethttp.StatusServiceUnavailable)
					return
				}
				hasDataset := false
				for _, asset := range assets {
					if asset.Kind == "dataset" {
						hasDataset = true
						break
					}
				}
				if !hasDataset {
					continue
				}
			}
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
	if allowed, err := h.authorizeSemanticModel(r, modelID); err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	} else if !allowed {
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
	modelID := chi.URLParam(r, "model")
	ctx, err := semanticConsumerForRequest(r.Context(), h.Metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	if !protectedSemanticModel(h.Metrics, modelID) {
		items, nextCursor, ok := pageSliceForRequest(w, r, fields)
		if !ok {
			return
		}
		writeJSON(w, nethttp.StatusOK, api.SemanticFieldListResponse{Items: items, Page: api.PageInfo{NextCursor: nextCursor}})
		return
	}
	authorized := fields[:0]
	for _, field := range fields {
		allowed := false
		if field.Kind == "metric" {
			if err := authorizeSemanticTarget(ctx, h.Metrics, modelID, semanticquery.SemanticAccessTarget{Metric: field.Name, Dataset: field.Dataset}); err != nil {
				if semanticAuthorizationUnavailable(err) {
					writeJSONError(w, err, nethttp.StatusServiceUnavailable)
					return
				}
				if !semanticMemberDenied(err) {
					writeJSONError(w, err, nethttp.StatusServiceUnavailable)
					return
				}
			} else {
				allowed = true
			}
		} else if dimension, exists := model.Dimensions[field.Name]; exists {
			datasets := sortedMapKeys(dimension.Bindings)
			for _, dataset := range datasets {
				if err := authorizeSemanticField(ctx, h.Metrics, modelID, dataset, field.Name); err != nil {
					if semanticAuthorizationUnavailable(err) {
						writeJSONError(w, err, nethttp.StatusServiceUnavailable)
						return
					}
					if !semanticMemberDenied(err) {
						writeJSONError(w, err, nethttp.StatusServiceUnavailable)
						return
					}
					continue
				}
				allowed = true
				break
			}
		}
		if allowed {
			authorized = append(authorized, field)
		}
	}
	fields = authorized
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
	modelID := chi.URLParam(r, "model")
	ctx, err := semanticConsumerForRequest(r.Context(), h.Metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	items := make([]api.SemanticRelationshipResponse, 0, len(model.Relationships))
	for _, relationship := range model.Relationships {
		if err := authorizeSemanticRelationship(ctx, h.Metrics, modelID, relationship); err != nil {
			if semanticAuthorizationUnavailable(err) {
				writeJSONError(w, err, nethttp.StatusServiceUnavailable)
				return
			}
			continue
		}
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
	modelID := chi.URLParam(r, "model")
	ctx, err := semanticConsumerForRequest(r.Context(), h.Metrics, modelID)
	if err != nil {
		writeJSONError(w, err, nethttp.StatusServiceUnavailable)
		return
	}
	if err := authorizeSemanticModelProjection(ctx, h.Metrics, modelID); err != nil {
		writeJSONError(w, err, semanticAuthorizationStatus(err))
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
