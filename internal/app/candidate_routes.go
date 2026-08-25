package app

import (
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/flidai/leapview/internal/access"
	accessmodule "github.com/flidai/leapview/internal/access/module"
	adminmodule "github.com/flidai/leapview/internal/admin/module"
	agentmodule "github.com/flidai/leapview/internal/agent/module"
	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	webpage "github.com/flidai/leapview/internal/platform/web/page"
	"github.com/flidai/leapview/internal/platform/web/staticasset"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
	runtimehostmodule "github.com/flidai/leapview/internal/runtimehost/module"
	"github.com/go-chi/chi/v5"
)

type candidatePreviewHandler interface {
	ServeCandidatePreview(http.ResponseWriter, *http.Request, string, string, webpage.Provider)
}

type candidateReviewHandler interface {
	ServeCandidateReview(http.ResponseWriter, *http.Request, string, projectgraph.ResourceID, webpage.Provider)
}

type candidateRouteDependencies struct {
	access           *accessmodule.Module
	product          *adminmodule.ProductService
	agent            *agentmodule.Module
	assets           staticasset.Resolver
	dashboards       *dashboardmodule.Module
	deployments      *deploymentmodule.Module
	runtimeHost      *runtimehostmodule.Module
	candidateMetrics func(runtimehostmodule.Provider, projectgraph.ResourceID) QueryMetrics
}

func candidatePreview(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	candidate, principalID, ok := resolveOwnedCandidate(deps, w, r)
	if !ok {
		return
	}
	if candidate.Status != deploymentmodule.CandidateReady {
		serveCandidatePreview(
			deps.deployments, candidate.ID, principalID,
			applicationLayout(deps.access, deps.agent, deps.product, deps.assets, r), w, r,
		)
		return
	}
	if deps.runtimeHost == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return
	}
	view, err := deps.runtimeHost.ResolveOwnedCandidate(candidate.ID, principalID)
	if err != nil || view.Provider == nil || deps.candidateMetrics == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return
	}
	projectID := view.ProjectID
	metrics := deps.candidateMetrics(view.Provider, projectID)
	if metrics == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return
	}
	dashboardID := strings.TrimSpace(metrics.DefaultDashboardID())
	if dashboardID == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, candidateRouteBase(candidate.ID)+"/dashboards/"+url.PathEscape(dashboardID), http.StatusFound)
}

// candidateReview renders the bounded reviewer handoff. It never resolves a
// runtime provider: candidate dashboard data remains owner-only and is served
// exclusively through candidatePreview/candidateDashboard.
func candidateReview(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	if deps.access == nil || deps.deployments == nil || deps.runtimeHost == nil {
		http.Error(w, "Candidate review is unavailable", http.StatusServiceUnavailable)
		return
	}
	if _, ok := deps.access.CurrentPrincipal(r); !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return
	}
	projectID := deps.runtimeHost.ProjectID()
	if err := projectID.Validate(); err != nil {
		http.Error(w, "Candidate review is unavailable", http.StatusServiceUnavailable)
		return
	}
	serveCandidateReview(
		deps.deployments, strings.TrimSpace(chi.URLParam(r, "candidate")), projectID,
		applicationLayout(deps.access, deps.agent, deps.product, deps.assets, r), w, r,
	)
}

func candidateDashboard(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request, action func(dashboardmodule.HTTP)) {
	handler, ok := resolveCandidateDashboardHTTP(deps, w, r)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	action(handler)
}

func candidateDashboardDocument(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	candidateDashboard(deps, w, r, func(handler dashboardmodule.HTTP) {
		if strings.TrimSpace(chi.URLParam(r, "page")) == "" {
			handler.Dashboard(w, r)
			return
		}
		handler.Page(w, r)
	})
}

func candidateDashboardUpdates(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	candidateDashboard(deps, w, r, func(handler dashboardmodule.HTTP) {
		handler.Updates(w, r)
	})
}

func candidateDashboardCommand(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	candidateDashboard(deps, w, r, func(handler dashboardmodule.HTTP) {
		switch strings.TrimSpace(chi.URLParam(r, "command")) {
		case "filter":
			handler.FilterCommand(w, r)
		case "filter-options":
			handler.FilterOptions(w, r)
		case "navigate":
			handler.Navigate(w, r)
		case "select":
			handler.Select(w, r)
		case "spatial-select":
			handler.SpatialSelect(w, r)
		case "clear-selection":
			handler.ClearSelection(w, r)
		case "visual-window":
			handler.VisualWindow(w, r)
		default:
			http.NotFound(w, r)
		}
	})
}

func resolveCandidateDashboardHTTP(
	deps candidateRouteDependencies,
	w http.ResponseWriter,
	r *http.Request,
) (dashboardmodule.HTTP, bool) {
	candidate, principalID, ok := resolveOwnedCandidate(deps, w, r)
	if !ok {
		return dashboardmodule.HTTP{}, false
	}
	if candidate.Status != deploymentmodule.CandidateReady {
		http.Redirect(w, r, "/candidates/"+url.PathEscape(candidate.ID), http.StatusSeeOther)
		return dashboardmodule.HTTP{}, false
	}
	if deps.runtimeHost == nil || deps.candidateMetrics == nil || deps.dashboards == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return dashboardmodule.HTTP{}, false
	}
	view, err := deps.runtimeHost.ResolveOwnedCandidate(candidate.ID, principalID)
	if err != nil {
		status := http.StatusServiceUnavailable
		if errors.Is(err, runtimehostmodule.ErrCandidateRuntimeNotFound) ||
			errors.Is(err, runtimehostmodule.ErrCandidateRuntimeExpired) {
			status = http.StatusNotFound
		}
		http.Error(w, http.StatusText(status), status)
		return dashboardmodule.HTTP{}, false
	}
	if view.Provider == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return dashboardmodule.HTTP{}, false
	}
	projectID := view.ProjectID
	metrics := deps.candidateMetrics(view.Provider, projectID)
	restrictions := make([]dashboardmodule.CandidateRestriction, len(view.Restrictions))
	for index, restriction := range view.Restrictions {
		resource, err := access.NewResourceRef(restriction.ObjectID, restriction.ObjectKind)
		if err != nil {
			http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
			return dashboardmodule.HTTP{}, false
		}
		restrictions[index] = dashboardmodule.CandidateRestriction{
			ID: restriction.ID, Resource: resource, Subject: restriction.Subject, PolicyType: restriction.PolicyType,
			ExpressionJSON: restriction.ExpressionJSON,
		}
	}
	handler, err := deps.dashboards.CandidateHTTP(dashboardmodule.CandidateHTTPConfig{
		Metrics: metrics, CandidateID: candidate.ID, OwnerPrincipalID: principalID,
		ProjectID: projectID, ArtifactDigest: candidate.ArtifactDigest,
		AuthorizationFingerprint: view.AuthorizationFingerprint,
		RouteBasePath:            candidateRouteBase(candidate.ID),
		Restrictions:             restrictions,
	})
	if err != nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return dashboardmodule.HTTP{}, false
	}
	return handler, true
}

func resolveOwnedCandidate(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request) (deploymentmodule.Candidate, string, bool) {
	if deps.access == nil || deps.deployments == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return deploymentmodule.Candidate{}, "", false
	}
	principal, ok := deps.access.CurrentPrincipal(r)
	if !ok {
		http.Error(w, "Authentication required", http.StatusUnauthorized)
		return deploymentmodule.Candidate{}, "", false
	}
	candidate, err := deps.deployments.ResolveOwnedCandidate(
		r.Context(),
		strings.TrimSpace(chi.URLParam(r, "candidate")),
		principal.ID,
	)
	if err != nil {
		if errors.Is(err, deploymentmodule.ErrCandidateNotFound) {
			http.NotFound(w, r)
			return deploymentmodule.Candidate{}, "", false
		}
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return deploymentmodule.Candidate{}, "", false
	}
	return candidate, principal.ID, true
}

func candidateRouteBase(candidateID string) string {
	return "/candidates/" + url.PathEscape(strings.TrimSpace(candidateID))
}

func serveCandidatePreview(
	handler candidatePreviewHandler,
	candidateID, principalID string,
	layout webpage.Provider,
	w http.ResponseWriter,
	r *http.Request,
) {
	if handler == nil || candidateID == "" || principalID == "" {
		http.NotFound(w, r)
		return
	}
	handler.ServeCandidatePreview(w, r, candidateID, principalID, layout)
}

func serveCandidateReview(
	handler candidateReviewHandler,
	candidateID string,
	projectID projectgraph.ResourceID,
	layout webpage.Provider,
	w http.ResponseWriter,
	r *http.Request,
) {
	if handler == nil || strings.TrimSpace(candidateID) == "" || projectID.Validate() != nil {
		http.NotFound(w, r)
		return
	}
	handler.ServeCandidateReview(w, r, strings.TrimSpace(candidateID), projectID, layout)
}

var _ candidatePreviewHandler = (*deploymentmodule.Module)(nil)
var _ candidateReviewHandler = (*deploymentmodule.Module)(nil)
