package app

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"

	dashboardmodule "github.com/flidai/leapview/internal/dashboard/module"
	deploymentmodule "github.com/flidai/leapview/internal/deployment/module"
	"github.com/flidai/leapview/internal/project/developmentsession"
	developmenthttp "github.com/flidai/leapview/internal/project/developmentsession/http"
	"github.com/go-chi/chi/v5"
)

// mountDevelopmentSessionRoutes binds the existing candidate dashboard
// adapter to the durable session pointer. Every request resolves the current
// candidate through the session handler and then reuses the candidate route
// implementation with a stable RouteBasePath. The generated dashboard does
// not need to know the candidate ID and its document/page/command/update URLs
// therefore remain stable across pointer advances.
func mountDevelopmentSessionRoutes(r chi.Router, session *developmenthttp.Handler, candidates candidateRouteDependencies, updates func(http.Handler) http.Handler, guard func(http.HandlerFunc) http.HandlerFunc) {
	if session == nil {
		return
	}
	if guard == nil {
		guard = func(next http.HandlerFunc) http.HandlerFunc { return next }
	}
	sessionBase := "/api/v1/projects/{project}/targets/{target}/development-session"
	r.Get(sessionBase+"/events", guard(session.Events))
	base := sessionBase + "/candidate/preview"
	r.Get(base, guard(func(w http.ResponseWriter, request *http.Request) {
		stableCandidatePreview(session, candidates, w, request)
	}))
	r.Get(base+"/dashboards/{dashboard}", guard(func(w http.ResponseWriter, request *http.Request) {
		stableCandidateDocument(session, candidates, w, request)
	}))
	r.Get(base+"/dashboards/{dashboard}/pages/{page}", guard(func(w http.ResponseWriter, request *http.Request) {
		stableCandidateDocument(session, candidates, w, request)
	}))
	if updates == nil {
		updates = func(next http.Handler) http.Handler { return next }
	}
	r.With(updates).Get(base+"/updates", guard(func(w http.ResponseWriter, request *http.Request) {
		stableCandidateUpdates(session, candidates, w, request)
	}))
	r.Post(base+"/dashboards/{dashboard}/commands/{command}", guard(func(w http.ResponseWriter, request *http.Request) {
		stableCandidateCommand(session, candidates, w, request)
	}))
}

func stableCandidatePreview(session *developmenthttp.Handler, candidates candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	proof, ok := resolveStableSessionCandidate(session, w, r)
	if !ok {
		return
	}
	request, candidate, _, metrics, ok := stableCandidateContext(candidates, w, r, proof.Identity.CandidateID)
	if !ok {
		return
	}
	dashboardID := strings.TrimSpace(metrics.DefaultDashboardID())
	if dashboardID == "" {
		http.NotFound(w, r)
		return
	}
	request = withStableURLParam(request, "dashboard", dashboardID)
	candidateDashboardAtRoute(candidates, w, request, candidate.ID, stableCandidateRouteBase(r), func(handler dashboardmodule.HTTP) {
		handler.Dashboard(w, request)
	})
}

func stableCandidateDocument(session *developmenthttp.Handler, candidates candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	proof, ok := resolveStableSessionCandidate(session, w, r)
	if !ok {
		return
	}
	request, candidate, _, _, ok := stableCandidateContext(candidates, w, r, proof.Identity.CandidateID)
	if !ok {
		return
	}
	candidateDashboardAtRoute(candidates, w, request, candidate.ID, stableCandidateRouteBase(r), func(handler dashboardmodule.HTTP) {
		if strings.TrimSpace(chi.URLParam(request, "page")) == "" {
			handler.Dashboard(w, request)
			return
		}
		handler.Page(w, request)
	})
}

func stableCandidateUpdates(session *developmenthttp.Handler, candidates candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	proof, ok := resolveStableSessionCandidate(session, w, r)
	if !ok {
		return
	}
	request, candidate, _, _, ok := stableCandidateContext(candidates, w, r, proof.Identity.CandidateID)
	if !ok {
		return
	}
	candidateDashboardAtRoute(candidates, w, request, candidate.ID, stableCandidateRouteBase(r), func(handler dashboardmodule.HTTP) {
		handler.Updates(w, request)
	})
}

func stableCandidateCommand(session *developmenthttp.Handler, candidates candidateRouteDependencies, w http.ResponseWriter, r *http.Request) {
	proof, ok := resolveStableSessionCandidate(session, w, r)
	if !ok {
		return
	}
	request, candidate, _, _, ok := stableCandidateContext(candidates, w, r, proof.Identity.CandidateID)
	if !ok {
		return
	}
	candidateDashboardAtRoute(candidates, w, request, candidate.ID, stableCandidateRouteBase(r), func(handler dashboardmodule.HTTP) {
		switch strings.TrimSpace(chi.URLParam(request, "command")) {
		case "filter":
			handler.FilterCommand(w, request)
		case "filter-options":
			handler.FilterOptions(w, request)
		case "navigate":
			handler.Navigate(w, request)
		case "select":
			handler.Select(w, request)
		case "spatial-select":
			handler.SpatialSelect(w, request)
		case "clear-selection":
			handler.ClearSelection(w, request)
		case "visual-window":
			handler.VisualWindow(w, request)
		default:
			http.NotFound(w, request)
		}
	})
}

func resolveStableSessionCandidate(session *developmenthttp.Handler, w http.ResponseWriter, r *http.Request) (developmenthttp.CandidateValidation, bool) {
	proof, err := session.ResolveCandidate(r)
	if err != nil {
		switch {
		case errors.Is(err, developmentsession.ErrOwnerMismatch):
			http.Error(w, "The development session is not owned by the authenticated principal", http.StatusForbidden)
		case errors.Is(err, developmentsession.ErrNotFound):
			http.NotFound(w, r)
		default:
			http.Error(w, "The development session candidate is unavailable", http.StatusServiceUnavailable)
		}
		return developmenthttp.CandidateValidation{}, false
	}
	if proof.Expired {
		session.MarkCandidateExpired(r)
		http.Error(w, "The development session candidate has expired or been retired", http.StatusGone)
		return developmenthttp.CandidateValidation{}, false
	}
	return proof, true
}

func stableCandidateContext(deps candidateRouteDependencies, w http.ResponseWriter, r *http.Request, candidateID string) (*http.Request, deploymentmodule.Candidate, string, dashboardmodule.Metrics, bool) {
	request := withStableURLParam(r, "candidate", candidateID)
	candidate, principalID, ok := resolveOwnedCandidateID(deps, w, request, candidateID)
	if !ok {
		return nil, deploymentmodule.Candidate{}, "", nil, false
	}
	if candidate.Status != deploymentmodule.CandidateReady {
		status := http.StatusServiceUnavailable
		if candidate.Status == deploymentmodule.CandidateExpired || candidate.Status == deploymentmodule.CandidateCancelled {
			status = http.StatusGone
		}
		http.Error(w, http.StatusText(status), status)
		return nil, deploymentmodule.Candidate{}, "", nil, false
	}
	view, err := resolveCandidateRuntime(deps, request, candidate.ID, principalID)
	if err != nil || view.Provider == nil || deps.candidateMetrics == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return nil, deploymentmodule.Candidate{}, "", nil, false
	}
	metrics := deps.candidateMetrics(view.Provider, view.ProjectID)
	if metrics == nil {
		http.Error(w, "Candidate preview is unavailable", http.StatusServiceUnavailable)
		return nil, deploymentmodule.Candidate{}, "", nil, false
	}
	return request, candidate, principalID, metrics, true
}

func withStableURLParam(r *http.Request, name, value string) *http.Request {
	params := chi.NewRouteContext()
	for _, key := range []string{"project", "target", "candidate", "dashboard", "page", "command"} {
		if existing := chi.URLParam(r, key); existing != "" {
			params.URLParams.Add(key, existing)
		}
	}
	params.URLParams.Add(name, value)
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, params))
}

func stableCandidateRouteBase(r *http.Request) string {
	return "/api/v1/projects/" + url.PathEscape(chi.URLParam(r, "project")) + "/targets/" + url.PathEscape(chi.URLParam(r, "target")) + "/development-session/candidate/preview"
}
