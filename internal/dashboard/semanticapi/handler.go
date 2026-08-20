package http

import (
	"context"
	"errors"
	"fmt"
	nethttp "net/http"

	"github.com/flidai/leapview/internal/access"
	"github.com/flidai/leapview/internal/analytics/dataquery"
	semanticmodel "github.com/flidai/leapview/internal/analytics/model"
	"github.com/flidai/leapview/internal/dashboard"
	"github.com/flidai/leapview/internal/dashboard/api"
	"github.com/flidai/leapview/internal/dashboard/consumer"
	dashboardresolver "github.com/flidai/leapview/internal/dashboard/resolver"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	"github.com/go-chi/chi/v5"
)

type Metrics interface {
	Catalog() dashboard.Catalog
	ExecuteDataQuery(ctx context.Context, request dataquery.Query) (dataquery.Result, error)
	Pages(dashboardID string) []dashboard.Page
	Resolver() dashboardresolver.Resolver
	SemanticModel(modelID string) (*semanticmodel.Model, bool)
}

type consumerPlannerProvider interface {
	Planner(modelID string) (consumer.Planner, bool)
}

type Handler struct {
	Metrics               Metrics
	ResolveProjectID      func(context.Context) (projectgraph.ResourceID, error)
	CurrentPrincipalID    func(r *nethttp.Request) string
	AuthorizeListResource func(ctx context.Context, principalID string, projectID projectgraph.ResourceID, resource access.ResourceRef, capability access.Capability) (bool, error)
	QueryFreshness        func(ctx context.Context, projectID, modelID, servingSnapshot string) (api.QueryFreshness, bool)
}

var errSemanticAuthorizationUnavailable = errors.New("semantic model authorization is unavailable")
var errSemanticModelActivationUnavailable = errors.New("active semantic model planner is unavailable")

func (h Handler) authorizeSemanticModel(r *nethttp.Request, modelID string) (bool, error) {
	if h.AuthorizeListResource == nil {
		return false, errSemanticAuthorizationUnavailable
	}
	projectID, err := h.projectIDForRequest(r.Context())
	if err != nil {
		return false, err
	}
	resourceID, err := projectgraph.NewResourceID(modelID)
	if err != nil {
		return false, err
	}
	resource, err := access.NewResourceRef(resourceID, projectgraph.KindSemanticModel)
	if err != nil {
		return false, err
	}
	principalID := ""
	if h.CurrentPrincipalID != nil {
		principalID = h.CurrentPrincipalID(r)
	}
	return h.AuthorizeListResource(r.Context(), principalID, projectID, resource, access.CapabilityResourceRead)
}

func (h Handler) biMetrics(w nethttp.ResponseWriter, r *nethttp.Request) (Metrics, bool) {
	metrics, ok := h.metricsForRequest(r)
	if !ok {
		writeJSONError(w, fmt.Errorf("project metrics are unavailable"), nethttp.StatusServiceUnavailable)
		return nil, false
	}
	return metrics, true
}

func (h Handler) projectIDForRequest(ctx context.Context) (projectgraph.ResourceID, error) {
	if h.ResolveProjectID == nil {
		return "", errors.New("active project resolver is unavailable")
	}
	projectID, err := h.ResolveProjectID(ctx)
	if err != nil {
		return "", err
	}
	if err := projectID.Validate(); err != nil {
		return "", err
	}
	return projectID, nil
}

func (h Handler) metricsForRequest(r *nethttp.Request) (Metrics, bool) {
	if h.Metrics == nil {
		return nil, false
	}
	if _, err := h.projectIDForRequest(r.Context()); err != nil {
		return nil, false
	}
	return h.Metrics, true
}

func (h Handler) semanticModelForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (*semanticmodel.Model, bool) {
	metrics, ok := h.biMetrics(w, r)
	if !ok {
		return nil, false
	}
	modelID := chi.URLParam(r, "model")
	model := semanticModelForID(metrics, modelID)
	if model == nil {
		writeJSONError(w, fmt.Errorf("model %q not found", modelID), nethttp.StatusNotFound)
		return nil, false
	}
	return model, true
}

func (h Handler) semanticDatasetForRequest(w nethttp.ResponseWriter, r *nethttp.Request) (*semanticmodel.Model, semanticmodel.Table, string, bool) {
	model, ok := h.semanticModelForRequest(w, r)
	if !ok {
		return nil, semanticmodel.Table{}, "", false
	}
	datasetID := chi.URLParam(r, "dataset")
	metrics, metricsOK := h.biMetrics(w, r)
	if !metricsOK {
		return nil, semanticmodel.Table{}, "", false
	}
	compiled := compiledSemanticModel(metrics, chi.URLParam(r, "model"))
	if compiled == nil {
		writeJSONError(w, fmt.Errorf("model %q semantic dataset bindings are unavailable", chi.URLParam(r, "model")), nethttp.StatusServiceUnavailable)
		return nil, semanticmodel.Table{}, "", false
	}
	dataset, exists := compiled.Dataset(datasetID)
	if !exists {
		writeJSONError(w, fmt.Errorf("dataset %q not found", datasetID), nethttp.StatusNotFound)
		return nil, semanticmodel.Table{}, "", false
	}
	return model, dataset.Table(), datasetID, true
}
