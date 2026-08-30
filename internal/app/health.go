package app

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/flidai/leapview/internal/deployment"
	apitransport "github.com/flidai/leapview/internal/platform/http/transport"
	projectgraph "github.com/flidai/leapview/internal/project/graph"
)

var errNoActiveDeployment = errors.New("no active deployment")

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
		checks["platformStore"] = "failed"
		apitransport.WriteJSON(w, http.StatusServiceUnavailable, healthResponse{Status: "not_ready", Checks: checks})
		return
	}
	checks["platformStore"] = "ok"
	if h.config.Analytics != nil {
		if err := h.config.Analytics(); err != nil {
			checks["analytics"] = "failed"
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
	checkNames := make([]string, 0, len(h.config.Checks))
	for name := range h.config.Checks {
		checkNames = append(checkNames, name)
	}
	sort.Strings(checkNames)
	for _, name := range checkNames {
		check := h.config.Checks[name]
		if check == nil {
			continue
		}
		if err := check(ctx); err != nil {
			checks[name] = stableReadinessFailure(err)
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
		checks["runtime"] = "failed"
		return false
	}
	if err := projectID.Validate(); err != nil {
		checks["runtime"] = "no_active_deployments"
		return !h.config.RequireActiveDeployment
	}
	if err := h.config.RuntimeReady(ctx); err != nil {
		if errors.Is(err, errNoActiveDeployment) {
			checks["runtime"] = "no_active_deployments"
			return !h.config.RequireActiveDeployment
		}
		checks["runtime"] = "failed"
		return false
	}
	checks["runtime"] = "ok"
	return true
}

// stableReadinessFailure preserves only the reviewed delivery-startup codes
// that are part of the operator contract. Arbitrary dependency errors,
// including wrapped platform paths or provider messages, collapse to a fixed
// value before crossing the unauthenticated readiness boundary.
func stableReadinessFailure(err error) string {
	diagnostics := deployment.DeliveryStartupDiagnosticsOf(err)
	if len(diagnostics) == 0 {
		return "failed"
	}
	codes := make([]string, 0, len(diagnostics))
	seen := map[deployment.DeliveryStartupDiagnosticCode]struct{}{}
	for _, diagnostic := range diagnostics {
		if !stableDeliveryStartupDiagnostic(diagnostic.Code) {
			continue
		}
		if _, ok := seen[diagnostic.Code]; ok {
			continue
		}
		seen[diagnostic.Code] = struct{}{}
		codes = append(codes, string(diagnostic.Code))
	}
	if len(codes) == 0 {
		return "failed"
	}
	sort.Strings(codes)
	return strings.Join(codes, ",")
}

func stableDeliveryStartupDiagnostic(code deployment.DeliveryStartupDiagnosticCode) bool {
	switch code {
	case deployment.DeliveryStartupMissingPoolAdmission,
		deployment.DeliveryStartupUnadmittedPool,
		deployment.DeliveryStartupLegacyServingIdentity,
		deployment.DeliveryStartupMixedServingPaths,
		deployment.DeliveryStartupMissingTargetRevision,
		deployment.DeliveryStartupMissingServingGeneration,
		deployment.DeliveryStartupIndeterminatePublication,
		deployment.DeliveryStartupClaimTargetPartial,
		deployment.DeliveryStartupTargetIdentityMismatch,
		deployment.DeliveryStartupActivePointerMismatch,
		deployment.DeliveryStartupMissingPublication,
		deployment.DeliveryStartupMissingServingState,
		deployment.DeliveryStartupServingEvidenceMismatch,
		deployment.DeliveryStartupMissingSeal,
		deployment.DeliveryStartupSealEvidenceMismatch:
		return true
	default:
		return false
	}
}
