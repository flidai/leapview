package app

import (
	"context"
	"net/http"
	"time"

	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

type healthConfig struct {
	Platform          func(context.Context) error
	Analytics         func() error
	ActiveProjectID   func(context.Context) (projectgraph.ResourceID, error)
	RuntimeReady      func(context.Context) error
	RuntimeLeaseReady func(context.Context) error
	// RequireActiveDeployment makes readiness fail closed when the target
	// contract guarantees a bootstrapped serving state.
	RequireActiveDeployment bool
	Checks                  map[string]func(context.Context) error
}

type health struct {
	config healthConfig
}

type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

func newHealth(config healthConfig) *health {
	return &health{config: config}
}

func (h *health) Healthz(w http.ResponseWriter, _ *http.Request) {
	apitransport.WriteJSON(w, http.StatusOK, healthResponse{Status: "ok"})
}

func (h *health) Readyz(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	if h == nil || h.config.Platform == nil {
		checks["platformStore"] = "missing"
		apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := h.config.Platform(ctx); err != nil {
		checks["platformStore"] = err.Error()
		apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
		return
	}
	checks["platformStore"] = "ok"
	if h.config.Analytics != nil {
		if err := h.config.Analytics(); err != nil {
			checks["analytics"] = err.Error()
			apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
			return
		}
		checks["analytics"] = "ok"
	}
	if h.config.RuntimeLeaseReady != nil {
		if err := h.config.RuntimeLeaseReady(ctx); err != nil {
			// Lease/store diagnostics stay in runtime-host logs; readiness exposes
			// only a stable, non-sensitive check value.
			checks["runtimeLease"] = "failed"
			apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
			return
		}
		checks["runtimeLease"] = "ok"
	}
	for name, check := range h.config.Checks {
		if check == nil {
			continue
		}
		if err := check(ctx); err != nil {
			checks[name] = err.Error()
			apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
			return
		}
		checks[name] = "ok"
	}
	if !h.runtimeReady(ctx, checks) {
		apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
		return
	}
	apitransport.WriteJSON(w, http.StatusOK, healthResponse{Status: "ready", Checks: checks})
}

func (h *health) runtimeReady(ctx context.Context, checks map[string]string) bool {
	if h.config.ActiveProjectID == nil || h.config.RuntimeReady == nil {
		checks["runtime"] = "missing"
		return false
	}
	projectID, err := h.config.ActiveProjectID(ctx)
	if err != nil {
		checks["runtime"] = err.Error()
		return false
	}
	if err := projectID.Validate(); err != nil {
		checks["runtime"] = "no_active_deployments"
		return !h.config.RequireActiveDeployment
	}
	if err := h.config.RuntimeReady(ctx); err != nil {
		checks["projectRuntime:"+projectID.String()] = err.Error()
		return false
	}
	checks["projectRuntime:"+projectID.String()] = "ok"
	return true
}
